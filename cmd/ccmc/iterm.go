package main

// iterm.go wires the "ccmc iterm-install" subcommand.
//
// Seam strategy: two package-level function variables (itermInstallFn and
// itermGetInstallDirFn) let tests inject stubs without touching the filesystem
// or the internal/iterm package's own installDirFn. This mirrors the pattern
// used by integrator.go (ghFetchFunc, evalFunc, installFunc).

import (
	"context"
	"fmt"
	"io"

	"ccmc/internal/iterm"
)

// itermInstallFn is the function called to perform the actual iTerm2 script
// installation. Tests replace this to inject success or error without touching
// the filesystem.
var itermInstallFn = func(ctx context.Context) error {
	return iterm.Install(ctx)
}

// itermGetInstallDirFn is the function called to retrieve the install path for
// the post-install instructions. Tests replace this to return a deterministic
// path without involving os.UserHomeDir.
var itermGetInstallDirFn = func() (string, error) {
	return iterm.GetStatusbarInstallDir()
}

// runItermInstall handles "ccmc iterm-install".
//
// On success it prints the installed path and the five-step iTerm2 activation
// instructions to stdout and returns 0. On error it prints a prefixed message
// to stderr and returns 1.
func runItermInstall(ctx context.Context, stdout, stderr io.Writer) int {
	if err := itermInstallFn(ctx); err != nil {
		fmt.Fprintf(stderr, "ccmc iterm-install: %v\n", err)
		return 1
	}

	installDir, err := itermGetInstallDirFn()
	if err != nil {
		// Install succeeded but we can't resolve the path — still a success from
		// the user's perspective; just omit the path from the message.
		fmt.Fprintf(stdout, "Installed CCMC statusbar to (path unavailable)\n")
	} else {
		fmt.Fprintf(stdout, "Installed CCMC statusbar to %s\n", installDir)
	}

	fmt.Fprintln(stdout, "Next steps:")
	fmt.Fprintln(stdout, " 1. Open iTerm2")
	fmt.Fprintln(stdout, " 2. iTerm2 → Preferences → Profiles → Session")
	fmt.Fprintln(stdout, " 3. Click 'Configure Status Bar'")
	fmt.Fprintln(stdout, " 4. Drag the 'CCMC' component into your Active Components")
	fmt.Fprintln(stdout, " 5. Restart iTerm2 (or open a new tab)")

	return 0
}
