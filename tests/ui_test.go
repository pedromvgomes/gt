package tests

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/ui"
)

func TestUILinesRespectQuietAndErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	printer := &ui.UI{Out: &out, Err: &errOut, Color: false, Quiet: true}

	printer.Info("hidden")
	printer.Success("hidden")
	printer.Warn("careful")
	printer.Error("failed")

	if out.String() != "" {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if got := errOut.String(); !strings.Contains(got, "WARN: careful") || !strings.Contains(got, "ERROR: failed") {
		t.Fatalf("stderr = %q, want warning and error", got)
	}
}

func TestPromptNonInteractiveUsesOptionalDefault(t *testing.T) {
	printer := &ui.UI{In: strings.NewReader(""), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: false}

	got, err := printer.Prompt("Use SSH instead? [Y/n]", "Y", false, "")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got != "Y" {
		t.Fatalf("Prompt() = %q, want Y", got)
	}
}

func TestPromptNonInteractiveRequiredErrors(t *testing.T) {
	printer := &ui.UI{In: strings.NewReader(""), Out: ioDiscard{}, Err: ioDiscard{}, Interactive: false}

	err := errorOnly(printer.Prompt("Branch", "", true, "set --from to skip the prompt"))
	if err == nil {
		t.Fatal("Prompt() succeeded, want error")
	}
	if ui.ExitCode(err) != ui.ExitUser {
		t.Fatalf("ExitCode() = %d, want %d", ui.ExitCode(err), ui.ExitUser)
	}
}

func TestPromptInteractiveReadsAnswer(t *testing.T) {
	var out bytes.Buffer
	printer := &ui.UI{In: strings.NewReader("feature\n"), Out: &out, Err: ioDiscard{}, Interactive: true}

	got, err := printer.Prompt("Branch", "main", true, "")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got != "feature" {
		t.Fatalf("Prompt() = %q, want feature", got)
	}
	if !strings.Contains(out.String(), "Branch [main]:") {
		t.Fatalf("prompt output = %q", out.String())
	}
}

func TestExitCodeFallsBackToGeneral(t *testing.T) {
	if got := ui.ExitCode(errors.New("boom")); got != ui.ExitGeneral {
		t.Fatalf("ExitCode() = %d, want %d", got, ui.ExitGeneral)
	}
}

func errorOnly(_ string, err error) error {
	return err
}
