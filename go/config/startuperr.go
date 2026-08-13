package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"vol20toglm/version"
)

// startupErrorFileName is written next to the binary whenever startup fails
// hard. On headless VMs the console window is minimized (and the rotating log
// may not be open yet), so a fail-fast exit would otherwise leave no visible
// trace of why the agent never came up.
const startupErrorFileName = "vol20toglm-startup-error.log"

// WriteStartupError records a fatal startup failure to startupErrorFileName in
// the binary's directory. The file is truncated on every write, so it always
// holds exactly the most recent failure. Write errors are ignored — the caller
// is already on its way out and has nowhere better to report them.
func WriteStartupError(err error) {
	path := startupErrorFileName
	if exe, exeErr := os.Executable(); exeErr == nil {
		path = filepath.Join(filepath.Dir(exe), startupErrorFileName)
	}

	f, createErr := os.Create(path)
	if createErr != nil {
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "%s vol20toglm %s: %v\n", time.Now().Format(time.RFC3339), version.Version, err)
}

// failStartup reports a fatal configuration error to stderr and to the
// startup-error file, then exits non-zero.
func failStartup(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	WriteStartupError(err)
	os.Exit(1)
}
