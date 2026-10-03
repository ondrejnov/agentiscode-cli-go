package agentiscode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func TestCLIInterruptWhileReadingPrompt(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	var out, stderr bytes.Buffer
	if code := RunCLI(ctx, []string{"-a", "oc"}, r, &out, &stderr, noEnv); code != 130 {
		t.Fatal(code)
	}
}
func TestCLIParserAllOptions(t *testing.T) {
	o, err := ParseOptions([]string{"--adapter", "oc", "--model=m", "--effort", "high", "--agent", "build", "--cwd", "/w", "--resume", "s", "--timeout", "30.5", "--json", "--task-id", "t", "--project-id", "p", "--run-id", "r", "--task-status", "4", "--last-message-to-comment", "--primary-session", "false", "--agentis-api", "http://localhost", "--agentis-token", "u", "--agentis-service-token", "s", "--final-output", "f", "--session-output", "sid", "hello"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if o.Config.Adapter != OpenCode || o.Config.Timeout != 30500*time.Millisecond || o.Config.Model != "m" || o.Config.ResumeSessionID != "s" || !o.JSON || !o.Telemetry.LastMessageToComment || *o.Telemetry.PrimarySession || *o.Telemetry.TaskStatus != 4 || o.FinalOutput != "f" || o.SessionOutput != "sid" {
		t.Fatal(o)
	}
	o, err = ParseOptions([]string{"-acc", "first", "-mm", "second", "-ehigh", "--", "--literal"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, o.PromptParts, []string{"first", "second", "--literal"})
	if o.Config.Adapter != Claude || o.Config.Model != "m" || o.Config.Effort != "high" {
		t.Fatal(o)
	}
}
func TestCLIEnvironmentDefaults(t *testing.T) {
	env := map[string]string{"AGENTIS_PROJECT_ID": "p", "AGENTIS_ENDPOINT": "http://localhost/api", "AGENTIS_API_TOKEN": "api", "AGENTIS_TOKEN": "fallback", "AGENTIS_SERVICE_TOKEN": "service"}
	o, err := ParseOptions([]string{"-a", "cp", "--task-id", "t", "--agentis-service-token", "override"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if o.Telemetry.Token != "api" || o.Telemetry.ServiceToken != "override" || o.ProjectID != "p" {
		t.Fatal(o)
	}
	delete(env, "AGENTIS_API_TOKEN")
	o, _ = ParseOptions([]string{"-a", "oc"}, func(k string) string { return env[k] })
	if o.Telemetry.Token != "fallback" {
		t.Fatal(o)
	}
}
func TestCLIUsageErrorsAndBooleans(t *testing.T) {
	for _, args := range [][]string{{}, {"-a", "gemini"}, {"-a", "oc", "--run-id", "r"}, {"-a", "oc", "--task-id", "t"}, {"-a", "oc", "--timeout", "NaN"}, {"-a", "oc", "--timeout", "Inf"}, {"-a", "oc", "--task-status", "x"}, {"-a", "oc", "--primary-session", "perhaps"}, {"-a", "oc", "--bogus"}, {"-a", "oc", "--json=true"}, {"-a"}} {
		if _, err := ParseOptions(args, noEnv); err == nil {
			t.Fatal("accepted", args)
		}
	}
	for _, v := range []string{"true", "1", "Y", " yes ", "on"} {
		if b, err := ParseBool(v); err != nil || !b {
			t.Fatal(v, b, err)
		}
	}
	for _, v := range []string{"false", "0", "N", "no", "OFF"} {
		if b, err := ParseBool(v); err != nil || b {
			t.Fatal(v, b, err)
		}
	}
	var out, stderr bytes.Buffer
	if code := RunCLI(context.Background(), []string{"-a", "oc"}, strings.NewReader(""), &out, &stderr, noEnv); code != 2 || !strings.Contains(stderr.String(), "Chybí prompt") {
		t.Fatal(code, stderr.String())
	}
}
func TestCommandDisplayAndContextTags(t *testing.T) {
	d := CommandDisplay("agentiscode", []string{"-a", "oc", "--agentis-token", "user-secret", "--agentis-service-token=service-secret", "prompt with spaces"})
	if strings.Contains(d, "secret") || !strings.Contains(d, "--agentis-token REDACTED --agentis-service-token=REDACTED 'prompt with spaces'") {
		t.Fatal(d)
	}
	if got := AppendContextIDs("prompt  ", "t", "p"); got != "prompt\n\n<agentis_task_id>t</agentis_task_id>\n<agentis_project_id>p</agentis_project_id>" {
		t.Fatal(got)
	}
	if got := AppendContextIDs("prompt", "", ""); got != "prompt" {
		t.Fatal(got)
	}
}
func installTestRuntime(t *testing.T, c Config) {
	t.Helper()
	t.Setenv("PATH", c.Cwd+string(os.PathListSeparator)+os.Getenv("PATH"))
	for k, v := range c.Env {
		t.Setenv(k, v)
	}
}
func TestCLIJSONOutputsStdinAndResume(t *testing.T) {
	c := fakeRuntime(t, OpenCode, OpenCode)
	installTestRuntime(t, c)
	capture := filepath.Join(c.Cwd, "capture.json")
	t.Setenv("GO_AGENT_CAPTURE", capture)
	final, session := filepath.Join(c.Cwd, "outputs", "final.md"), filepath.Join(c.Cwd, "outputs", "session")
	args := []string{"-a", "oc", "--json", "--cwd", c.Cwd, "--final-output", final, "--session-output", session, "--project-id", "p"}
	var out, stderr bytes.Buffer
	if code := RunCLI(context.Background(), args, strings.NewReader("hello stdin\n"), &out, &stderr, noEnv); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err, line)
		}
		events = append(events, e)
	}
	equalJSON(t, eventTypes(events), []string{"session", "text", "step", "result"})
	for path, want := range map[string]string{final: "Done\n", session: "s\n"} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatal(path, string(b), err)
		}
	}
	b, _ := os.ReadFile(capture)
	p := decode(t, string(b))
	if p["prompt"] != "hello stdin\n\n<agentis_project_id>p</agentis_project_id>" {
		t.Fatal(p)
	}
	out.Reset()
	stderr.Reset()
	args = append(args, "--resume", "s", "continue")
	if code := RunCLI(context.Background(), args, strings.NewReader(""), &out, &stderr, noEnv); code != 0 {
		t.Fatal(code, stderr.String())
	}
	b, _ = os.ReadFile(capture)
	if decode(t, string(b))["prompt"] != "continue" {
		t.Fatal(string(b))
	}
}
func TestCLIHTTPReportingAndFailureJSONCleanliness(t *testing.T) {
	c := fakeRuntime(t, OpenCode, "native-error")
	installTestRuntime(t, c)
	var methods []string
	failed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Object
		_ = json.NewDecoder(r.Body).Decode(&p)
		method := str(p["method"])
		methods = append(methods, method)
		if method == "task.start_run" {
			fmt.Fprint(w, `{"result":{"item":{"id":"run"}}}`)
			return
		}
		if method == "run.adapter_event" && obj(p["params"])["kind"] == "idle" {
			failed = obj(p["params"])["status"] == "failed"
		}
		fmt.Fprint(w, `{"result":{"ok":true}}`)
	}))
	defer srv.Close()
	var out, stderr bytes.Buffer
	code := RunCLI(context.Background(), []string{"-a", "oc", "--json", "--task-id", "t", "--agentis-api", srv.URL, "prompt"}, strings.NewReader(""), &out, &stderr, noEnv)
	if code != 1 || !failed || len(methods) == 0 {
		t.Fatal(code, failed, methods, stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("JSON contaminated:", line)
		}
	}
}
func TestOutputRecorderBoundariesAndWriteErrors(t *testing.T) {
	dir := t.TempDir()
	r := &OutputRecorder{FinalPath: filepath.Join(dir, "f"), SessionPath: filepath.Join(dir, "s")}
	for _, e := range []Event{{"text", Object{"text": "old"}}, {"tool", Object{}}, {"text", Object{"text": "new "}}, {"text", Object{"text": "answer"}}, {"step", Object{}}, {"result", Object{"session_id": "late"}}} {
		r.Handle(e)
	}
	r.Finish()
	b, _ := os.ReadFile(r.FinalPath)
	if string(b) != "new answer\n" {
		t.Fatal(string(b))
	}
	b, _ = os.ReadFile(r.SessionPath)
	if string(b) != "late\n" {
		t.Fatal(string(b))
	}
	r.SessionPath = filepath.Join(dir, "other")
	r.Handle(Event{"session", Object{"session_id": "other"}})
	if _, err := os.Stat(r.SessionPath); !os.IsNotExist(err) {
		t.Fatal("session overwritten")
	}
	var failure error
	r.FinalPath = filepath.Join(r.FinalPath, "child")
	r.OnError = func(err error) { failure = err }
	r.Finish()
	if failure == nil {
		t.Fatal("missing write error")
	}
}
func TestTextRendererAndHelp(t *testing.T) {
	var out, stderr bytes.Buffer
	r := &TextRenderer{Out: &out, Err: &stderr}
	for _, e := range []Event{{"session", Object{"adapter": "claude", "model": "x", "session_id": "s"}}, {"reasoning", Object{"text": "plan"}}, {"tool", Object{"id": "t", "name": "Read", "title": "a.go", "status": "running"}}, {"tool", Object{"id": "t", "status": "error", "output": "bad\nmore"}}, {"text", Object{"text": "Hi"}}, {"stderr", Object{"line": "debug"}}, {"result", Object{"usage": Object{"input_tokens": 3, "output_tokens": 2}, "cost_usd": .01}}} {
		if err := r.Handle(e); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Finish()
	if out.String() != "Hi\n" {
		t.Fatal(out.String())
	}
	for _, s := range []string{"model=x", "session=s", "💭 plan", "Read(a.go)", "✗ Read: bad", "debug", "tokens in=3 out=2  cost=$0.0100"} {
		if !strings.Contains(stderr.String(), s) {
			t.Fatal(stderr.String(), s)
		}
	}
	out.Reset()
	stderr.Reset()
	if code := RunCLI(context.Background(), []string{"--help"}, strings.NewReader(""), &out, &stderr, noEnv); code != 0 || !strings.Contains(out.String(), "--primary-session") {
		t.Fatal(code, out.String())
	}
}
