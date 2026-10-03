//go:build linux

package agentiscode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}
func assertProcessStopped(t *testing.T, pid int) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			fields := strings.Fields(string(b))
			if len(fields) > 2 && fields[2] == "Z" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant %d is still running", pid)
}
func TestTimeoutKillsDescendants(t *testing.T) {
	c := fakeRuntime(t, OpenCode, "descendant")
	c.Timeout = 500 * time.Millisecond
	pidPath := filepath.Join(c.Cwd, "child")
	c.Env["GO_AGENT_CHILD"] = pidPath
	e, err := collectEvents(t, c, "x")
	if err != nil {
		t.Fatal(err)
	}
	if e[len(e)-1].Data["is_error"] != true {
		t.Fatal(e)
	}
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	assertProcessStopped(t, pid)
}
func TestCLISignalsPreserveOutputAndKillDescendants(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "agentiscode")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agentiscode")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			c := fakeRuntime(t, OpenCode, "descendant")
			pidPath := filepath.Join(c.Cwd, "child")
			final := filepath.Join(c.Cwd, "final")
			if err := os.WriteFile(final, []byte("previous output"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-a", "oc", "--final-output", final, "prompt")
			cmd.Env = append(os.Environ(), "PATH="+c.Cwd+":"+os.Getenv("PATH"), "GO_AGENT_HELPER=1", "GO_AGENT_MODE=descendant", "GO_AGENT_CHILD="+pidPath)
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			pid, err := strconv.Atoi(string(waitForFile(t, pidPath)))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			exit, ok := err.(*exec.ExitError)
			want := 128 + int(sig)
			if !ok || exit.ExitCode() != want {
				t.Fatal(err, stderr.String())
			}
			b, err := os.ReadFile(final)
			if err != nil || string(b) != "previous output" {
				t.Fatal("final output overwritten on interruption", string(b), err)
			}
			assertProcessStopped(t, pid)
		})
	}
}
