package agentiscode

import "testing"

func consume(m *Mapper, kind string, data Object) bool { return m.Consume(native(kind, data, nil)) }
func assistants(m *Mapper) []Message {
	out := []Message{}
	for _, msg := range m.Snapshot() {
		if msg.Info["role"] == "assistant" {
			out = append(out, msg)
		}
	}
	return out
}
func TestMapperSessionRebindingAndSnapshotIsolation(t *testing.T) {
	m := NewMapper("prompt", "", "", "", "")
	consume(m, "text", Object{"text": "before"})
	if !consume(m, "session_start", Object{"session_id": "real", "model": "m", "cwd": "/w"}) {
		t.Fatal("unchanged")
	}
	for _, entry := range m.Snapshot() {
		if entry.Info["sessionID"] != "real" {
			t.Fatal(entry)
		}
		for _, p := range entry.Parts {
			if p["sessionID"] != "real" {
				t.Fatal(p)
			}
		}
	}
	snap := m.Snapshot()
	snap[0].Parts[0]["text"] = "changed"
	if m.Snapshot()[0].Parts[0]["text"] != "prompt" {
		t.Fatal("snapshot aliases mapper")
	}
}
func TestMapperPerMessageUsageDeduplication(t *testing.T) {
	m := NewMapper("x", "sid", "build", Claude, "/w")
	u1 := Object{"input_tokens": 10, "output_tokens": 3, "cache_read_input_tokens": 22040, "cache_creation_input_tokens": 9133}
	consume(m, "thinking", Object{"text": "plan", "message_id": "m1"})
	consume(m, "assistant_message", Object{"message_id": "m1", "message": Object{"usage": u1}})
	consume(m, "tool_use", Object{"id": "t1", "name": "Bash", "input": Object{"command": "pwd"}, "message_id": "m1"})
	if consume(m, "assistant_message", Object{"message_id": "m1", "message": Object{"usage": u1}}) {
		t.Fatal("usage duplicated")
	}
	consume(m, "tool_result", Object{"tool_use_id": "t1", "content": "/w"})
	consume(m, "text", Object{"text": "done", "message_id": "m2"})
	consume(m, "assistant_message", Object{"message": Object{"id": "m2", "usage": Object{"input_tokens": 8, "output_tokens": 2}}})
	consume(m, "result", Object{"usage": Object{"input_tokens": 18, "output_tokens": 5}, "cost_usd": 0.1})
	a := assistants(m)
	if len(a) != 2 {
		t.Fatal(a)
	}
	if integer(obj(a[0].Info["tokens"])["input"]) != 10 || integer(obj(a[1].Info["tokens"])["input"]) != 8 {
		t.Fatal(a)
	}
	equalJSON(t, obj(obj(a[0].Info["tokens"])["cache"]), Object{"read": 22040, "write": 9133})
	for _, msg := range a {
		count := 0
		for _, p := range msg.Parts {
			if p["type"] == "step-finish" {
				count++
			}
		}
		if count != 1 {
			t.Fatal(msg)
		}
	}
}
func TestMapperLegacyUsageAndResultFallback(t *testing.T) {
	for _, withSteps := range []bool{true, false} {
		m := NewMapper("x", "s", "", "", "")
		consume(m, "text", Object{"text": "first"})
		if withSteps {
			consume(m, "assistant_message", Object{"message": Object{"usage": Object{"input_tokens": 10}}})
			consume(m, "text", Object{"text": "second"})
			consume(m, "assistant_message", Object{"message": Object{"usage": Object{"input_tokens": 20}}})
		}
		consume(m, "result", Object{"usage": Object{"input_tokens": 30, "cache_write_tokens": 12}, "cost_usd": .01})
		a := assistants(m)
		if withSteps {
			if len(a) != 2 || integer(obj(a[1].Info["tokens"])["input"]) != 20 {
				t.Fatal(a)
			}
		} else {
			if len(a) != 1 || integer(obj(obj(a[0].Info["tokens"])["cache"])["write"]) != 12 {
				t.Fatal(a)
			}
		}
	}
}
func TestMapperToolLifecycleAndMissingEvents(t *testing.T) {
	m := NewMapper("x", "s", "", "", "/w")
	consume(m, "text", Object{"text": "before"})
	consume(m, "tool_use", Object{"id": "t", "name": "Read", "input": Object{"file_path": "/w/a.go"}})
	consume(m, "text", Object{"text": "after"})
	consume(m, "step_finish", Object{"usage": Object{"input_tokens": 1}})
	p := m.findTool("t")
	if obj(p["state"])["status"] != "completed" {
		t.Fatal(p)
	}
	if consume(m, "tool_use", Object{"id": "t", "name": "Read"}) {
		t.Fatal("completed tool reopened")
	}
	consume(m, "tool_result", Object{"tool_use_id": "t", "content": "late result"})
	if obj(p["state"])["output"] != "late result" {
		t.Fatal(p)
	}
	if !consume(m, "tool_result", Object{"tool_use_id": "t2", "name": "bash", "input": Object{"command": "false"}, "content": "bad", "is_error": true}) {
		t.Fatal("lost completed-only tool")
	}
	if obj(m.findTool("t2")["state"])["error"] != "bad" {
		t.Fatal(m.Snapshot())
	}
	if consume(m, "tool_result", Object{"tool_use_id": "missing", "content": "x"}) {
		t.Fatal("unknown tool manufactured")
	}
	if consume(m, "stderr", Object{"line": "ignore"}) {
		t.Fatal("stderr mapped")
	}
}
func TestNormalizeToolInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
		title string
		alias string
	}{
		{"Read", Object{"file_path": "/w/app/a.go"}, "app/a.go", "filePath"},
		{"NotebookEdit", Object{"notebook_path": "/w/n.ipynb"}, "n.ipynb", "filePath"},
		{"Bash", Object{"command": "ls", "description": "List files"}, "List files", ""},
		{"Bash", Object{"command": "ls -la"}, "ls -la", ""},
		{"Glob", Object{"pattern": "*.go", "path": "/w/app"}, "*.go  (in app)", ""},
		{"Grep", Object{"pattern": "foo"}, "foo", ""},
		{"TodoWrite", Object{"todos": []any{1, 2}}, "2 todos", ""},
		{"Task", Object{"subagent_type": "general", "description": "Audit"}, "Audit", "subagentType"},
		{"Task", Object{"prompt": "Do it"}, "Do it", ""},
		{"WebFetch", Object{"url": "https://example.com"}, "https://example.com", ""},
		{"WebSearch", Object{"query": "Go"}, "Go", ""},
		{"AskUserQuestion", Object{"questions": []any{1}}, "Asked 1 questions", ""},
		{"Mystery", Object{"foo": "bar"}, "Mystery", ""},
		{"Mystery", 42, "Mystery", "value"},
	} {
		t.Run(tc.name+tc.title, func(t *testing.T) {
			in, title := NormalizeToolInput(tc.name, tc.input, "/w")
			if title != tc.title {
				t.Fatalf("%s != %s", title, tc.title)
			}
			if tc.alias != "" && in[tc.alias] == nil {
				t.Fatal(in)
			}
		})
	}
	for path, want := range map[string]string{"/w": ".", "/var/www/agentiscraft/01234567-0123-0123-0123-012345678901/src/a.go": "src/a.go", "/a/b/c/d/e/f": ".../c/d/e/f"} {
		if got := shortPath(path, "/w"); got != want {
			t.Fatal(got, want)
		}
	}
}
