//go:build !unix

package agentiscode

import "os/exec"

func resolveExecutable(command string, c Config) (string, error) { return exec.LookPath(command) }

func configureProcess(cmd *exec.Cmd) {}
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
