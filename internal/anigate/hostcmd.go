package anigate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitNetworkTimeout bounds subprocesses that contact a remote (clone, fetch,
// push, gh); gitToolTimeout stays the bound for local-only invocations.
const gitNetworkTimeout = 120 * time.Second

// hostCmdOpts configures runHostCommand. The zero value is the safe default:
// local timeout, combined output, no stdin, no extra env, sanitized errors.
type hostCmdOpts struct {
	Timeout    time.Duration // <=0 means gitToolTimeout
	Stdin      string
	ExtraEnv   []string // appended to the PATH-only base env
	StdoutOnly bool     // capture stdout only, so stderr noise cannot pollute parsed output
}

// runHostCommand is the single execution point for git/gh style helper
// subprocesses (jobs and agents go through JobManager.run instead). It always
// builds the environment from scratch and always sanitizes argv and output
// previews in error messages so credentialed URLs cannot leak.
func runHostCommand(cwd string, opts hostCmdOpts, name string, args ...string) (string, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = gitToolTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Env = append([]string{"PATH=" + pathEnv()}, opts.ExtraEnv...)
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	var out []byte
	var err error
	if opts.StdoutOnly {
		out, err = cmd.Output()
	} else {
		out, err = cmd.CombinedOutput()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s command timed out", name)
	}
	if err != nil {
		preview := out
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			preview = ee.Stderr
		}
		return "", fmt.Errorf("%s %s failed: %s", name, sanitizeCommandText(strings.Join(args, " ")), sanitizeCommandText(trimPreview(string(preview), 500)))
	}
	return string(out), nil
}
