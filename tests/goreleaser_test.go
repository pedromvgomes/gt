package tests

import (
	"os"
	"strings"
	"testing"
	"text/template"

	"github.com/pedromvgomes/gt/internal/selfupdate"
	"go.vipengele.org/update"
	"gopkg.in/yaml.v3"
)

// goreleaserConfig is the part of .goreleaser.yaml that names the release
// assets gt update looks for.
type goreleaserConfig struct {
	ProjectName string `yaml:"project_name"`
	Builds      []struct {
		Binary string   `yaml:"binary"`
		GOOS   []string `yaml:"goos"`
		GOARCH []string `yaml:"goarch"`
	} `yaml:"builds"`
	Archives []struct {
		Formats      []string `yaml:"formats"`
		NameTemplate string   `yaml:"name_template"`
	} `yaml:"archives"`
	Checksum struct {
		NameTemplate string `yaml:"name_template"`
	} `yaml:"checksum"`
	Signs []struct {
		Artifacts string `yaml:"artifacts"`
		Signature string `yaml:"signature"`
	} `yaml:"signs"`
}

// renderGoReleaser renders a GoReleaser name template for one platform. Both
// use Go's text/template, so the whitespace trimming of {{- is the same.
func renderGoReleaser(t *testing.T, tmpl, project, goos, goarch string) string {
	t.Helper()
	tp, err := template.New("name").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		t.Fatalf("parsing %q: %v", tmpl, err)
	}
	var b strings.Builder
	data := map[string]string{"ProjectName": project, "Os": goos, "Arch": goarch, "Version": "3.0.0", "Tag": "v3.0.0"}
	if err := tp.Execute(&b, data); err != nil {
		t.Fatalf("rendering %q: %v", tmpl, err)
	}
	return b.String()
}

// Every archive, the checksums file and its signature that GoReleaser
// publishes are the assets selfupdate.Base selects on each platform gt is
// built for. A name that drifts on either side leaves every installed gt
// unable to find its update.
func TestGoReleaserAssetsAreTheOnesBaseSelects(t *testing.T) {
	raw, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var gr goreleaserConfig
	if err := yaml.Unmarshal(raw, &gr); err != nil {
		t.Fatalf(".goreleaser.yaml: %v", err)
	}
	if len(gr.Builds) != 1 || len(gr.Archives) != 1 || len(gr.Signs) != 1 {
		t.Fatalf("want one build, one archive and one signs entry; got %d, %d, %d", len(gr.Builds), len(gr.Archives), len(gr.Signs))
	}
	build, archive, sign := gr.Builds[0], gr.Archives[0], gr.Signs[0]
	if len(archive.Formats) != 1 {
		t.Fatalf("archive formats = %q, want exactly one", archive.Formats)
	}
	if sign.Artifacts != "checksum" {
		t.Fatalf("signs artifacts = %q, want checksum", sign.Artifacts)
	}

	checksums := renderGoReleaser(t, gr.Checksum.NameTemplate, gr.ProjectName, "", "")
	signature := strings.ReplaceAll(sign.Signature, "${artifact}", checksums)

	type platform struct{ goos, goarch string }
	archives := map[platform]string{}
	rel := update.Release{Tag: "v3.0.0"}
	for _, goos := range build.GOOS {
		for _, goarch := range build.GOARCH {
			name := renderGoReleaser(t, archive.NameTemplate, gr.ProjectName, goos, goarch) + "." + archive.Formats[0]
			archives[platform{goos, goarch}] = name
			rel.Assets = append(rel.Assets, update.Asset{Name: name, URL: "https://example.invalid/" + name})
		}
	}
	rel.Assets = append(rel.Assets,
		update.Asset{Name: checksums, URL: "https://example.invalid/" + checksums},
		update.Asset{Name: signature, URL: "https://example.invalid/" + signature},
	)

	want := map[platform]string{
		{"darwin", "amd64"}: "gt_darwin_amd64.tar.gz",
		{"darwin", "arm64"}: "gt_darwin_arm64.tar.gz",
		{"linux", "amd64"}:  "gt_linux_amd64.tar.gz",
		{"linux", "arm64"}:  "gt_linux_arm64.tar.gz",
	}
	if len(archives) != len(want) {
		t.Fatalf("GoReleaser builds %d platforms, want %d: %v", len(archives), len(want), archives)
	}
	for p, name := range want {
		if archives[p] != name {
			t.Errorf("GoReleaser names the %s/%s archive %q, want %q", p.goos, p.goarch, archives[p], name)
		}

		cfg := selfupdate.Base("3.0.0")
		cfg.GOOS, cfg.GOARCH = p.goos, p.goarch
		cfg.StateDir = t.TempDir()
		sel, err := cfg.Select(rel)
		if err != nil {
			t.Errorf("%s/%s: Select() error = %v", p.goos, p.goarch, err)
			continue
		}
		if sel.Archive.Name != name {
			t.Errorf("%s/%s: selects archive %q, want %q", p.goos, p.goarch, sel.Archive.Name, name)
		}
		if sel.Checksums.Name != "checksums.txt" || sel.Signature.Name != "checksums.txt.minisig" {
			t.Errorf("%s/%s: selects %q and %q, want checksums.txt and checksums.txt.minisig", p.goos, p.goarch, sel.Checksums.Name, sel.Signature.Name)
		}
	}
}
