package agentiscode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) Object {
	t.Helper()
	var o Object
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return o
}
func equalJSON(t *testing.T, got, want any) {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("got %s\nwant %s", a, b)
	}
}
func normalizeAll(t *testing.T, adapter string, lines ...string) ([]Event, *Normalizer) {
	t.Helper()
	n, err := NewNormalizer(adapter)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := NewTranslator(adapter)
	out := []Event{}
	for _, s := range lines {
		for _, raw := range n.Normalize(decode(t, s)) {
			for _, ev := range tr.Translate(raw) {
				if n.Adapter == OpenCode {
					if id := str(first(raw.SessionID, n.SessionID)); id != "" {
						ev.Data["session_id"] = id
					}
				}
				out = append(out, ev)
			}
		}
	}
	return out, n
}
func eventTypes(events []Event) []string {
	a := []string{}
	for _, e := range events {
		a = append(a, e.Type)
	}
	return a
}

func TestAdapterAliases(t *testing.T) {
	for name, want := range map[string]string{" OpenCode ": OpenCode, "oc": OpenCode, "cloud": Claude, "claudecode": Claude, "claude-code": Claude, "cc": Claude, "CLAUDE": Claude, "claude-p": ClaudeP, "claudep": ClaudeP, "cp": ClaudeP} {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeAdapter(name)
			if err != nil || got != want {
				t.Fatalf("%s %v", got, err)
			}
		})
	}
	if _, err := NormalizeAdapter("gemini"); err == nil {
		t.Fatal("unknown adapter accepted")
	}
}
func TestBuildCommands(t *testing.T) {
	for _, adapter := range []string{OpenCode, Claude, ClaudeP} {
		t.Run(adapter, func(t *testing.T) {
			c := Config{Adapter: adapter, Model: "model", Effort: "high", Agent: "build", ResumeSessionID: "ses", ExtraArgs: []string{"--extra"}}
			cmd, args, err := c.BuildCommand()
			if err != nil || cmd != adapter {
				t.Fatal(cmd, err)
			}
			var want []string
			if adapter == OpenCode {
				want = []string{"run", "--format", "json", "--dangerously-skip-permissions", "--session", "ses", "--model", "model", "--agent", "build", "--variant", "high", "--extra"}
			} else {
				if adapter == Claude {
					want = []string{"--print", "-"}
				}
				want = append(want, "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--disallowedTools", "AskUserQuestion", "--resume", "ses", "--model", "model", "--agent", "build", "--effort", "high", "--extra")
			}
			equalJSON(t, args, want)
		})
	}
	c := Config{Adapter: Claude, Chrome: true, MaxTurns: 3, AppendSystemPromptFile: "system.md", AddDirs: []string{"a", "b"}, RequirePermissions: true}
	_, args, _ := c.BuildCommand()
	joined := strings.Join(args, " ")
	for _, fragment := range []string{"--chrome", "--max-turns 3", "--append-system-prompt-file system.md", "--add-dir a --add-dir b"} {
		if !strings.Contains(joined, fragment) {
			t.Fatal(joined)
		}
	}
	if strings.Contains(joined, "--dangerously") {
		t.Fatal(joined)
	}
	c.ResumeSessionID = "resume"
	_, args, _ = c.BuildCommand()
	if strings.Contains(strings.Join(args, " "), "--append-system") {
		t.Fatal(args)
	}
}

func TestOpenCodeDeltasToolsAndUsage(t *testing.T) {
	events, n := normalizeAll(t, OpenCode,
		`{"type":"text","sessionID":"ses_1","part":{"type":"text","id":"p1","text":"Žlu"}}`,
		`{"type":"text","sessionID":"ses_1","part":{"type":"text","id":"p1","text":"Žluťoučký 🐈"}}`,
		`{"type":"text","sessionID":"ses_1","part":{"type":"text","id":"p1","text":"Žluťoučký 🐈"}}`,
		`{"type":"tool.execute.before","sessionID":"ses_1","callID":"c1","tool":"bash","input":{"command":"ls"}}`,
		`{"type":"tool_use","part":{"type":"tool","callID":"c1","tool":"bash","state":{"status":"running","input":{"command":"ls"}}}}`,
		`{"type":"tool_use","part":{"type":"tool","callID":"c1","tool":"bash","state":{"status":"completed","input":{"command":"ls"},"output":"file.go","title":"List"}}}`,
		`{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":4}},"cost":0.03}}`,
	)
	equalJSON(t, eventTypes(events), []string{"session", "text", "text", "tool", "tool", "step"})
	if events[1].Data["text"] != "Žlu" || events[2].Data["text"] != "ťoučký 🐈" {
		t.Fatal(events)
	}
	if events[4].Data["title"] != "List" || events[4].Data["output"] != "file.go" {
		t.Fatal(events[4])
	}
	equalJSON(t, events[5].Data["usage"], Object{"input_tokens": 10, "output_tokens": 5, "reasoning_tokens": 2, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 4})
	if n.LastUsage["cache_write_tokens"] != int64(4) || n.LastCostUSD != .03 {
		t.Fatal(n)
	}
}

func TestOpenCodeNestedPartsAndSessions(t *testing.T) {
	events, _ := normalizeAll(t, OpenCode,
		`{"type":"message.part.updated","properties":{"sessionID":"main","part":{"id":"p1","type":"text","text":"user prompt"}}}`,
		`{"type":"tool.execute.before","properties":{"sessionID":"main","callID":"c","tool":"read","input":{"filePath":"a.go"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"sessionID":"child","type":"tool","callID":"sub","tool":"bash","state":{"status":"error","error":"bad"}}}}`,
		`{"type":"text","sessionID":"main","part":{"type":"text","text":"Done"}}`,
	)
	equalJSON(t, eventTypes(events), []string{"session", "tool", "session", "tool", "session", "text"})
	for i, sid := range []string{"main", "main", "child", "child", "main", "main"} {
		if events[i].Data["session_id"] != sid {
			t.Fatal(events)
		}
	}
}
func TestOpenCodeErrorVariants(t *testing.T) {
	for _, raw := range []string{`"bad"`, `{"data":{"message":"bad"}}`, `{"message":"bad"}`, `{"name":"bad"}`} {
		e, n := normalizeAll(t, OpenCode, `{"type":"error","error":`+raw+`}`)
		if len(e) != 1 || e[0].Data["message"] != "bad" || n.LastError == nil {
			t.Fatal(e, n)
		}
	}
}
func TestClaudeDeduplicatesStepAndPreservesMessageID(t *testing.T) {
	events, n := normalizeAll(t, "cloud",
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-x","cwd":"/w"}`,
		`{"type":"assistant","session_id":"s1","message":{"id":"m1","content":[{"type":"thinking","thinking":"Plan"}],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/a.go"}}],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"data"}],"is_error":false}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
		`{"type":"result","session_id":"s1","usage":{"input_tokens":13,"output_tokens":3,"cache_creation_input_tokens":17},"total_cost_usd":0.02}`,
	)
	equalJSON(t, eventTypes(events), []string{"session", "reasoning", "step", "tool", "tool", "text", "step", "result"})
	for _, i := range []int{1, 2, 3} {
		if events[i].Data["message_id"] != "m1" {
			t.Fatal(events[i])
		}
	}
	equalJSON(t, events[3].Data, Object{"id": "t1", "name": "Read", "status": "running", "input": Object{"file_path": "/w/a.go"}, "title": "/w/a.go", "message_id": "m1"})
	if events[4].Data["output"] != "data" || n.LastUsage["cache_creation_input_tokens"] != float64(17) {
		t.Fatal(events, n)
	}
}
func TestClaudePSessionInferredOnce(t *testing.T) {
	e, _ := normalizeAll(t, ClaudeP,
		`{"type":"mode","sessionId":"cp1"}`,
		`{"type":"assistant","sessionId":"cp1","message":{"content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"result","session_id":"cp1","is_error":true}`)
	equalJSON(t, eventTypes(e), []string{"session", "text", "result"})
	if e[0].Data["adapter"] != ClaudeP || e[2].Data["is_error"] != true {
		t.Fatal(e)
	}
	e, _ = normalizeAll(t, Claude, `{"type":"assistant","session_id":"s","message":{"model":"m","content":[{"type":"text","text":"Hi"}]}}`)
	equalJSON(t, eventTypes(e), []string{"session", "text"})
}
func TestMalformedAndUnknownRuntimeFields(t *testing.T) {
	for _, adapter := range []string{Claude, OpenCode} {
		e, _ := normalizeAll(t, adapter, `{}`, `{"type":"future","payload":true}`, `{"type":"assistant","message":[]}`, `{"part":{"type":"text","text":null}}`)
		if len(e) != 0 {
			t.Fatal(e)
		}
	}
}
func TestToolTitlesAndStringify(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
		want  string
	}{
		{"Read", Object{"file_path": "/w/a"}, "/w/a"}, {"read", Object{"filePath": "b"}, "b"},
		{"Bash", Object{"description": "list files", "command": "ls"}, "list files"}, {"bash", Object{"command": "ls -la"}, "ls -la"},
		{"Grep", Object{"pattern": "x.*"}, "x.*"}, {"WebFetch", Object{"url": "https://example.com"}, "https://example.com"},
		{"WebSearch", Object{"query": "Go"}, "Go"}, {"Task", Object{"description": "Audit"}, "Audit"}, {"Mystery", true, "Mystery"},
	} {
		if got := ToolTitle(tc.name, tc.input); got != tc.want {
			t.Errorf("%s: %s != %s", tc.name, got, tc.want)
		}
	}
	if got := ToolTitle("bash", Object{"command": strings.Repeat("ž", 100)}); len([]rune(got)) != 80 || !strings.HasSuffix(got, "…") {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		input any
		want  string
	}{{nil, ""}, {"x", "x"}, {Object{"text": "text"}, "text"}, {[]any{Object{"text": "one"}, Object{"text": "two"}}, "one\ntwo"}, {Object{"value": true}, "{'value': True}"}} {
		if got := Stringify(tc.input); got != tc.want {
			t.Errorf("%q != %q", got, tc.want)
		}
	}
}
func TestEventFlattenedJSON(t *testing.T) {
	e := Event{"text", Object{"text": "<ž>"}}
	b, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `\u003c`) {
		t.Fatal(string(b))
	}
	equalJSON(t, e, Object{"type": "text", "text": "<ž>"})
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, back, e)
}

func FuzzNormalization(f *testing.F) {
	for _, seed := range []string{`{}`, `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`, `{"type":"error","error":{"message":"bad"}}`, `{"part":{"type":"tool","callID":"x","state":{"status":"running"}}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var e Object
		if json.Unmarshal([]byte(s), &e) != nil {
			return
		}
		for _, a := range []string{Claude, OpenCode} {
			n, _ := NewNormalizer(a)
			tr, _ := NewTranslator(a)
			for _, native := range n.Normalize(e) {
				for _, event := range tr.Translate(native) {
					if _, err := event.MarshalJSON(); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	})
}
