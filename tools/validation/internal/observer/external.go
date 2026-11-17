package observer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// External calls the selected observer with only the environment needed for
// host configuration and local SSH authentication.
func External(ctx context.Context, executable, target, expected string) Outcome {
	// SSH and other remote observers need time beyond the HTTP request itself.
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, executable, target, expected) // #nosec G204 -- explicitly configured observer; URL and marker are passed as separate arguments.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "SSH_AUTH_SOCK=" + os.Getenv("SSH_AUTH_SOCK")}
	err := cmd.Run()
	if callCtx.Err() != nil {
		return Inconclusive
	}
	if err == nil {
		return Present
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == int(Absent) {
		return Absent
	}
	return Inconclusive
}
