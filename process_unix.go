//go:build unix

package agentiscode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Resolve relative PATH entries against the runtime cwd, including Config.Env
// overrides. exec.Command alone looks up commands in the wrapper's environment.
func resolveExecutable(command string, c Config) (string, error) {
	if strings.ContainsRune(command, '/') {
		return command, nil
	}
	path := os.Getenv("PATH")
	if p, ok := c.Env["PATH"]; ok {
		path = p
	}
	base := c.Cwd
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		candidate := filepath.Join(dir, command)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: executable file not found in PATH", command)
}

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
