package agentiscode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test executable doubles as a deterministic agent runtime in subprocesses.
func TestRuntimeHelper(t *testing.T) {
	if os.Getenv("GO_AGENT_HELPER") != "1" {
		return
	}
	mode := os.Getenv("GO_AGENT_MODE")
	if mode == "unread" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if capture := os.Getenv("GO_AGENT_CAPTURE"); capture != "" {
		cwd, _ := os.Getwd()
		data, _ := json.Marshal(Object{"args": os.Args, "prompt": string(prompt), "cwd": cwd, "sandbox": os.Getenv("IS_SANDBOX")})
		_ = os.WriteFile(capture, data, 0600)
	}
	printJSON := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	switch mode {
	case "replay":
		data, err := os.ReadFile(os.Getenv("GO_AGENT_FIXTURE"))
		if err != nil {
			os.Exit(8)
		}
		_, _ = os.Stdout.Write(data)
	case "fail":
		fmt.Fprintln(os.Stdout, "unparsed output")
		fmt.Fprintln(os.Stderr, "--- Start ---\ncontext error\n--- End ---")
		os.Exit(7)
	case "timeout":
		time.Sleep(time.Minute)
	case "eof-hang":
		os.Stdout.Close()
		time.Sleep(time.Minute)
	case "big":
		printJSON(Object{"type": "text", "sessionID": "s", "part": Object{"id": "p", "type": "text", "text": strings.Repeat("ž🐈", 100000)}})
	case "claude", "result-hang":
		printJSON(Object{"type": "system", "subtype": "init", "session_id": "s", "model": "claude-x"})
		printJSON(Object{"type": "assistant", "message": Object{"content": []any{Object{"type": "text", "text": "Done"}}}})
		printJSON(Object{"type": "result", "session_id": "s", "usage": Object{"input_tokens": 3}, "total_cost_usd": .01})
		if mode == "result-hang" {
			time.Sleep(time.Minute)
		}
	case "claude-p":
		printJSON(Object{"type": "mode", "sessionId": "s"})
		printJSON(Object{"type": "assistant", "message": Object{"content": []any{Object{"type": "text", "text": "Done"}}}})
		printJSON(Object{"type": "result", "session_id": "s"})
	case "stderr-live":
		fmt.Fprintln(os.Stderr, "progress")
		time.Sleep(time.Minute)
	case "native-error":
		printJSON(Object{"type": "error", "sessionID": "s", "error": Object{"data": Object{"message": "provider failed"}}})
		os.Exit(1)
	case "descendant":
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		if p := os.Getenv("GO_AGENT_CHILD"); p != "" {
			_ = os.WriteFile(p, []byte(fmt.Sprint(child.Process.Pid)), 0600)
		}
		time.Sleep(time.Minute)
	default:
		printJSON(Object{"type": "text", "sessionID": "s", "part": Object{"id": "p", "type": "text", "text": "Done"}})
		// Final unterminated line must still be parsed.
		fmt.Fprint(os.Stdout, `{"type":"step_finish","sessionID":"s","part":{"type":"step-finish","tokens":{"input":10,"output":5},"cost":0.03}}`)
	}
	os.Exit(0)
}

func fakeRuntime(t *testing.T, adapter, mode string) Config {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX runtime fixture")
	}
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, adapter)
	script := "#!/bin/sh\nexec " + shellJoin([]string{binary, "-test.run=^TestRuntimeHelper$", "--"}) + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return Config{Adapter: adapter, Command: path, Cwd: dir, Timeout: 5 * time.Second, ExitGrace: 50 * time.Millisecond, Env: map[string]string{"GO_AGENT_HELPER": "1", "GO_AGENT_MODE": mode}}
}
func collectEvents(t *testing.T, c Config, prompt string) ([]Event, error) {
	t.Helper()
	w, err := NewWrapper(c)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	err = w.Stream(context.Background(), prompt, func(e Event) error { events = append(events, e); return nil })
	return events, err
}

func TestRealSubprocessAdaptersAndPrompt(t *testing.T) {
	for _, adapter := range []string{OpenCode, Claude, ClaudeP} {
		t.Run(adapter, func(t *testing.T) {
			c := fakeRuntime(t, adapter, adapter)
			c.Model = "model"
			c.Effort = "high"
			capture := filepath.Join(c.Cwd, "capture.json")
			c.Env["GO_AGENT_CAPTURE"] = capture
			prompt := strings.Repeat("long prompt $(do-not-execute) ' \" ž\n", 10000)
			e, err := collectEvents(t, c, prompt)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"session", "text", "result"}
			if adapter == OpenCode {
				want = []string{"session", "text", "step", "result"}
			}
			equalJSON(t, eventTypes(e), want)
			if e[len(e)-1].Data["is_error"] != false {
				t.Fatal(e)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			p := decode(t, string(data))
			if p["prompt"] != prompt || p["cwd"] != c.Cwd || p["sandbox"] != "1" {
				t.Fatal("stdin/cwd/env mismatch")
			}
			for _, arg := range list(p["args"]) {
				if arg == prompt {
					t.Fatal("prompt leaked into argv")
				}
			}
		})
	}
}
func TestRuntimeLongLineAndNativeError(t *testing.T) {
	c := fakeRuntime(t, OpenCode, "big")
	e, err := collectEvents(t, c, "x")
	if err != nil {
		t.Fatal(err)
	}
	if e[1].Data["text"] != strings.Repeat("ž🐈", 100000) {
		t.Fatal("large line truncated")
	}
	c.Env["GO_AGENT_MODE"] = "native-error"
	e, err = collectEvents(t, c, "x")
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, eventTypes(e), []string{"session", "error", "result"})
	if e[1].Data["message"] != "provider failed" || e[2].Data["is_error"] != true {
		t.Fatal(e)
	}
}

func TestRuntimeEnvironmentPATHOverride(t *testing.T) {
	c := fakeRuntime(t, OpenCode, OpenCode)
	c.Command = OpenCode
	c.Env["PATH"] = c.Cwd
	e, err := collectEvents(t, c, "x")
	if err != nil || len(e) < 2 || e[1].Data["text"] != "Done" {
		t.Fatal(e, err)
	}
}
func TestRuntimeFailuresAndMissingExecutable(t *testing.T) {
	for _, a := range []string{OpenCode, Claude} {
		t.Run(a, func(t *testing.T) {
			c := fakeRuntime(t, a, "fail")
			e, err := collectEvents(t, c, "private prompt")
			if err != nil {
				t.Fatal(err)
			}
			var failure string
			count := 0
			for _, ev := range e {
				if ev.Type == "error" {
					failure = str(ev.Data["message"])
					count++
				}
			}
			if count != 1 || !strings.Contains(failure, "kódem 7") || strings.Contains(failure, "private prompt") {
				t.Fatal(failure)
			}
			if a == OpenCode && !strings.Contains(failure, "kódem 7: context error") {
				t.Fatal(failure)
			}
			if a == Claude && !strings.Contains(failure, "kódem 7: --- End ---") {
				t.Fatal(failure)
			}
			if e[len(e)-1].Data["is_error"] != true {
				t.Fatal(e)
			}
			c.Command = filepath.Join(c.Cwd, "missing")
			e, err = collectEvents(t, c, "x")
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, eventTypes(e), []string{"error", "result"})
		})
	}
}
func TestRuntimeTimeoutsAndResultGrace(t *testing.T) {
	for _, mode := range []string{"timeout", "unread", "eof-hang", "result-hang"} {
		t.Run(mode, func(t *testing.T) {
			c := fakeRuntime(t, Claude, mode)
			c.Timeout = 200 * time.Millisecond
			start := time.Now()
			e, err := collectEvents(t, c, strings.Repeat("x", 2<<20))
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("timeout/grace did not stop process")
			}
			if mode == "result-hang" {
				equalJSON(t, eventTypes(e), []string{"session", "text", "result"})
				if e[2].Data["is_error"] != false {
					t.Fatal(e)
				}
			} else if e[len(e)-1].Data["is_error"] != true {
				t.Fatal(e)
			}
		})
	}
}
func TestRuntimeStderrDeliveredWithoutStdoutAndCancel(t *testing.T) {
	c := fakeRuntime(t, OpenCode, "stderr-live")
	w, _ := NewWrapper(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := false
	err := w.Stream(ctx, "x", func(e Event) error {
		if e.Type == "stderr" && e.Data["line"] == "progress" {
			seen = true
			cancel()
		}
		return nil
	})
	if !seen || !errors.Is(err, context.Canceled) {
		t.Fatal(seen, err)
	}
}
func TestRuntimeCallbackFailureAndSSECollect(t *testing.T) {
	c := fakeRuntime(t, Claude, "result-hang")
	w, _ := NewWrapper(c)
	stop := errors.New("stop")
	if err := w.Stream(context.Background(), "x", func(e Event) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	c.Env["GO_AGENT_MODE"] = "claude"
	w, _ = NewWrapper(c)
	summary, err := w.Collect(context.Background(), "x")
	if err != nil || summary["summary"] != "Done" {
		t.Fatal(summary, err)
	}
	w, _ = NewWrapper(c)
	var b bytes.Buffer
	if err := w.StreamSSE(context.Background(), "x", &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "event: text\ndata: ") || !strings.Contains(b.String(), "\"text\":\"Done\"") {
		t.Fatal(b.String())
	}
}
