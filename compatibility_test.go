package agentiscode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Optional migration contract test. The Go implementation and its normal test
// suite need no Python. Enable this only to compare with the source application.
func TestPythonCompatibility(t *testing.T) {
	source := os.Getenv("AGENTISCODE_PYTHON_SOURCE")
	if source == "" {
		t.Skip("set AGENTISCODE_PYTHON_SOURCE to run migration conformance")
	}
	python := filepath.Join(source, ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]struct{ adapter, stream string }{
		"opencode-tools": {OpenCode, `{"type":"text","sessionID":"main","part":{"id":"p","type":"text","text":"Žlu"}}
{"type":"text","sessionID":"main","part":{"id":"p","type":"text","text":"Žluťoučký 🐈"}}
{"type":"tool.execute.before","sessionID":"main","callID":"c","tool":"read","input":{"filePath":"/w/a.go"}}
{"type":"message.part.updated","properties":{"sessionID":"main","part":{"type":"tool","callID":"c","tool":"read","state":{"status":"running","input":{"filePath":"/w/a.go"}}}}}
{"type":"tool_use","sessionID":"main","part":{"type":"tool","callID":"c","tool":"read","state":{"status":"completed","input":{"filePath":"/w/a.go"},"output":"content"}}}
{"type":"step_finish","sessionID":"main","part":{"type":"step-finish","tokens":{"input":10,"output":5,"cache":{"read":3,"write":4}},"cost":0.03}}
{"type":"text","sessionID":"main","part":{"id":"final","type":"text","text":"Done"}}
`},
		"opencode-subtasks": {OpenCode, `{"type":"text","sessionID":"main","part":{"id":"p1","type":"text","text":"Delegating"}}
{"type":"message.part.updated","properties":{"sessionID":"main","part":{"type":"text","text":"do not echo user"}}}
{"type":"text","sessionID":"child","part":{"id":"p2","type":"text","text":"Subtask"}}
{"type":"step_finish","sessionID":"child","part":{"type":"step-finish","tokens":{"input":2,"output":1}}}
{"type":"text","sessionID":"main","part":{"id":"p3","type":"text","text":"Done"}}
`},
		"claude-messages": {Claude, `{"type":"system","subtype":"init","session_id":"s","model":"claude-x","cwd":"/w"}
{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"Plan"}],"usage":{"input_tokens":10,"output_tokens":4,"cache_creation_input_tokens":3}}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"c","name":"Bash","input":{"command":"pwd"}}],"usage":{"input_tokens":10,"output_tokens":4,"cache_creation_input_tokens":3}}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"/w"}]}]}}
{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"Done"}],"usage":{"input_tokens":20,"output_tokens":6}}}
{"type":"result","session_id":"s","usage":{"input_tokens":30,"output_tokens":10},"total_cost_usd":0.02}
`},
		"claude-p-mode": {ClaudeP, `{"type":"mode","sessionId":"cp"}
{"type":"assistant","sessionId":"cp","message":{"content":[{"type":"text","text":"Done"}]}}
{"type":"result","session_id":"cp","usage":{"input_tokens":3,"output_tokens":2},"total_cost_usd":0.01}
`},
	}
	for name, fixture := range fixtures {
		for _, telemetryMode := range []string{"off", "owned", "existing"} {
			t.Run(name+"/"+telemetryMode, func(t *testing.T) {
				c := fakeRuntime(t, fixture.adapter, "replay")
				path := filepath.Join(c.Cwd, "events.jsonl")
				if err := os.WriteFile(path, []byte(fixture.stream), 0600); err != nil {
					t.Fatal(err)
				}
				c.Env["GO_AGENT_FIXTURE"] = path
				installTestRuntime(t, c)
				var mu sync.Mutex
				var calls []Object
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var p Object
					_ = json.NewDecoder(r.Body).Decode(&p)
					mu.Lock()
					calls = append(calls, Object{"method": p["method"], "params": p["params"], "userHeader": r.Header.Get(AuthHeader), "serviceHeader": r.Header.Get(ServiceAuthHeader)})
					mu.Unlock()
					if p["method"] == "task.start_run" {
						fmt.Fprint(w, `{"result":{"item":{"id":"run-1"}}}`)
					} else {
						fmt.Fprint(w, `{"result":{"ok":true}}`)
					}
				}))
				defer srv.Close()
				args := []string{"--adapter", fixture.adapter, "--json", "--cwd", c.Cwd, "--project-id", "project", "--final-output", filepath.Join(c.Cwd, "final"), "--session-output", filepath.Join(c.Cwd, "session")}
				if telemetryMode != "off" {
					args = append(args, "--task-id", "task", "--agentis-api", srv.URL, "--agentis-token", "test-user", "--agentis-service-token", "test-service", "--last-message-to-comment", "--task-status", "4")
				}
				if telemetryMode == "existing" {
					args = append(args, "--run-id", "external", "--primary-session", "false", "--resume", "resume")
				}
				cmd := exec.Command(python, append([]string{"-m", "agentiscode"}, args...)...)
				cmd.Dir = source
				cmd.Stdin = strings.NewReader("prompt from stdin")
				for _, kv := range os.Environ() {
					k, _, _ := strings.Cut(kv, "=")
					if !strings.HasPrefix(k, "AGENTIS_") {
						cmd.Env = append(cmd.Env, kv)
					}
				}
				var pyOut, pyErr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &pyOut, &pyErr
				if err := cmd.Run(); err != nil {
					t.Fatal(err, pyErr.String())
				}
				pyFinal, _ := os.ReadFile(filepath.Join(c.Cwd, "final"))
				pySession, _ := os.ReadFile(filepath.Join(c.Cwd, "session"))
				mu.Lock()
				pyCalls := calls
				calls = nil
				mu.Unlock()
				var goOut, goErr bytes.Buffer
				if code := RunCLI(context.Background(), args, strings.NewReader("prompt from stdin"), &goOut, &goErr, noEnv); code != 0 {
					t.Fatal(code, goErr.String())
				}
				goFinal, _ := os.ReadFile(filepath.Join(c.Cwd, "final"))
				goSession, _ := os.ReadFile(filepath.Join(c.Cwd, "session"))
				if string(goFinal) != string(pyFinal) || string(goSession) != string(pySession) {
					t.Fatal("output files differ")
				}
				parse := func(b *bytes.Buffer) []Object {
					var events []Object
					for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
						events = append(events, decode(t, line))
					}
					return events
				}
				equalJSON(t, parse(&goOut), parse(&pyOut))
				mu.Lock()
				goCalls := calls
				mu.Unlock()
				equalJSON(t, canonicalTranscript(goCalls), canonicalTranscript(pyCalls))
			})
		}
	}
}

// Compare generated message/part IDs by identity and ignore wall-clock values.
func canonicalTranscript(v any) any {
	b, _ := json.Marshal(v)
	var value any
	_ = json.Unmarshal(b, &value)
	ids := map[string]string{}
	var walk func(any) any
	walk = func(x any) any {
		switch x := x.(type) {
		case Object:
			out := Object{}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k != "time" {
					out[k] = walk(x[k])
				}
			}
			return out
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		case string:
			// Generated identifiers are random; their exact bytes are not a contract.
			if (strings.HasPrefix(x, "msg_") || strings.HasPrefix(x, "prt_")) && len(x) == 28 {
				if ids[x] == "" {
					ids[x] = fmt.Sprintf("generated-%d", len(ids))
				}
				return ids[x]
			}
			return x
		default:
			return x
		}
	}
	return walk(value)
}
