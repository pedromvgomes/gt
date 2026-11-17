// Command gt is the shell around internal/app: it carries the build stamp and
// exits with the App's exit code.
package main

import (
	"os"

	"github.com/pedromvgomes/gt/internal/app"
)

// The release build sets these with -ldflags "-X main.version=… -X
// main.commit=… -X main.date=…". version is stamped without a leading v.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	os.Exit(app.New(app.BuildInfo{Version: version, Commit: commit, Date: date}).Execute())
}
