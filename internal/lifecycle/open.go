package lifecycle

import (
	"fmt"
	"os/exec"
)

// openCmd is the seam used to invoke osascript for OpenInITerm. Tests replace
// this with a stub so the AppleScript path can be exercised without launching iTerm.
var openCmd = func(script string) error {
	return exec.Command("osascript", "-e", script).Run()
}

// OpenInITerm opens a new iTerm window with the working directory set to dir.
// It uses the same AppleScript approach as Launch but only issues a "cd <dir>"
// command — it does NOT run "claude". The caller controls what happens in the tab.
//
// Shell quoting: dir is wrapped in single quotes using the '\'' idiom (same as
// singleQuoteShell in launch.go) so that paths containing spaces or shell
// metacharacters are safe to embed in the AppleScript "write text" argument.
func OpenInITerm(dir string) error {
	quotedDir := singleQuoteShell(dir)
	script := fmt.Sprintf(
		`tell application "iTerm"
  create window with default profile
  tell current session of current window
    write text "cd %s"
  end tell
end tell`,
		quotedDir,
	)
	if err := openCmd(script); err != nil {
		return fmt.Errorf("open in iTerm: osascript: %w", err)
	}
	return nil
}
