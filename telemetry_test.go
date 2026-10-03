package agentiscode

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type rpcCall struct {
	method string
	params Object
}
type fakeRPC struct {
	calls   []rpcCall
	results map[string]any
	fail    map[string]int
}

func (f *fakeRPC) Call(_ context.Context, method string, params any) (any, error) {
	f.calls = append(f.calls, rpcCall{method, obj(params)})
	if f.fail[method] > 0 {
		f.fail[method]--
		return nil, errors.New("temporary failure")
	}
	if r, ok := f.results[method]; ok {
		return r, nil
	}
	return Object{"ok": true}, nil
}
func (f *fakeRPC) forMethod(method string) []Object {
	p := []Object{}
	for _, c := range f.calls {
		if c.method == method {
			p = append(p, c.params)
		}
	}
	return p
}
func makeTelemetry(t *testing.T, f *fakeRPC, run string) *Telemetry {
	t.Helper()
	tr, err := NewTelemetry(TelemetryConfig{TaskID: "task-1", Prompt: "prompt", Adapter: Claude, RunID: run, Client: f})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func telemetryStream(tr *Telemetry) {
	for _, e := range []Event{
		{"session", Object{"session_id": "s", "model": "claude-x", "cwd": "/w"}},
		{"text", Object{"text": "Before"}},
		{"tool", Object{"id": "t", "name": "Read", "status": "running", "input": Object{"file_path": "/w/a.go"}}},
		{"tool", Object{"id": "t", "status": "completed", "output": "data"}},
		{"text", Object{"text": "Final "}}, {"text", Object{"text": "answer"}},
		{"result", Object{"session_id": "s", "usage": Object{"input_tokens": 3}, "cost_usd": .02, "is_error": false}},
	} {
		tr.Handle(context.Background(), e)
	}
}
func TestTelemetryOwnAndExternalRunLifecycle(t *testing.T) {
	for _, external := range []bool{false, true} {
		f := &fakeRPC{results: map[string]any{"task.start_run": Object{"item": Object{"id": "run-9"}}}}
		run := ""
		if external {
			run = "external"
		}
		tr := makeTelemetry(t, f, run)
		tr.Start(context.Background())
		telemetryStream(tr)
		tr.Finish(context.Background())
		bind := f.forMethod("run.store_session_id")
		if len(bind) != 1 {
			t.Fatal(bind)
		}
		equalJSON(t, bind[0], Object{"run_id": tr.RunID, "session_id": "s", "primary": true})
		bindingSeen := false
		for _, c := range f.calls {
			if c.method == "run.store_session_id" {
				bindingSeen = true
			}
			if c.method == "session.store_activity_log" && !bindingSeen {
				t.Fatal("unbound log")
			}
		}
		events := f.forMethod("run.adapter_event")
		if events[0]["status"] != "success" || events[0]["kind"] != "agentiscode" {
			t.Fatal(events)
		}
		if external {
			if len(f.forMethod("task.start_run")) != 0 || len(events) != 1 {
				t.Fatal(f.calls)
			}
		} else {
			if len(events) != 2 || events[1]["kind"] != "idle" || events[1]["status"] != "success" {
				t.Fatal(events)
			}
		}
		if len(f.forMethod("task.add_agent_comment")) != 0 {
			t.Fatal("comment without opt-in")
		}
		logs := f.forMethod("session.store_activity_log")
		messages := logs[len(logs)-1]["messages"].([]Message)
		if messages[0].Parts[0]["text"] != "prompt" || messages[1].Info["role"] != "assistant" {
			t.Fatal(messages)
		}
	}
}
func TestTelemetrySessionsAndPerTurnTokens(t *testing.T) {
	f := &fakeRPC{}
	tr := makeTelemetry(t, f, "run")
	ctx := context.Background()
	tr.Start(ctx)
	for _, e := range []Event{
		{"session", Object{"session_id": "main"}}, {"text", Object{"text": "First", "session_id": "main"}},
		{"step", Object{"usage": Object{"input_tokens": 10}, "session_id": "main"}},
		{"session", Object{"session_id": "child"}}, {"text", Object{"text": "Subtask result", "session_id": "child"}},
		{"step", Object{"usage": Object{"input_tokens": 2}, "session_id": "child"}},
		{"session", Object{"session_id": "main"}}, {"text", Object{"text": "Second", "session_id": "main"}},
		{"step", Object{"usage": Object{"input_tokens": 20}, "session_id": "main"}},
		{"result", Object{"session_id": "main", "usage": Object{"input_tokens": 30}}},
	} {
		tr.Handle(ctx, e)
	}
	tr.Finish(ctx)
	equalJSON(t, f.forMethod("run.store_session_id"), []Object{{"run_id": "run", "session_id": "main", "primary": true}, {"run_id": "run", "session_id": "child", "primary": false}})
	a := assistants(tr.sessions["main"].mapper)
	if len(a) != 2 || integer(obj(a[0].Info["tokens"])["input"])+integer(obj(a[1].Info["tokens"])["input"]) != 30 {
		t.Fatal(a)
	}
	for sid, state := range tr.sessions {
		for _, msg := range state.mapper.Snapshot() {
			if msg.Info["sessionID"] != sid {
				t.Fatal(msg)
			}
			for _, p := range msg.Parts {
				if p["sessionID"] != sid || (sid == "main" && p["text"] == "Subtask result") {
					t.Fatal(p)
				}
			}
		}
	}
}
func TestTelemetryFinalCommentAndSecondaryBinding(t *testing.T) {
	f := &fakeRPC{}
	tr := makeTelemetry(t, f, "run")
	secondary := false
	status := 4
	tr.Config.PrimarySession = &secondary
	tr.Config.LastMessageToComment = true
	tr.Config.TaskStatus = &status
	tr.Start(context.Background())
	telemetryStream(tr)
	tr.Finish(context.Background())
	if f.forMethod("run.store_session_id")[0]["primary"] != false {
		t.Fatal(f.calls)
	}
	equalJSON(t, f.forMethod("task.add_agent_comment")[0], Object{"run_id": "run", "body": "Final answer", "comment_type": "primary", "status": 4})
}
func TestTelemetryRetriesBindingAndDirtySnapshots(t *testing.T) {
	f := &fakeRPC{fail: map[string]int{"run.store_session_id": 1}}
	tr := makeTelemetry(t, f, "run")
	ctx := context.Background()
	var reported []string
	tr.Config.OnError = func(s string) { reported = append(reported, s) }
	tr.Start(ctx)
	tr.Handle(ctx, Event{"session", Object{"session_id": "s"}})
	if len(f.forMethod("session.store_activity_log")) != 0 {
		t.Fatal("unbound snapshot sent")
	}
	tr.Handle(ctx, Event{"tool", Object{"id": "t", "name": "bash", "status": "running", "input": Object{"command": "true"}}})
	f.fail["session.store_activity_log"] = 1
	tr.Handle(ctx, Event{"tool", Object{"id": "t", "status": "completed", "output": "ok"}})
	if !tr.sessions["s"].dirty {
		t.Fatal("lost failed snapshot")
	}
	tr.Finish(ctx)
	if tr.sessions["s"].dirty || len(f.forMethod("run.store_session_id")) != 2 || len(reported) != 2 {
		t.Fatal(f.calls, reported)
	}
	logs := f.forMethod("session.store_activity_log")
	equalJSON(t, logs[len(logs)-1], logs[len(logs)-2])
}
func TestTelemetryOKFalseIsRetried(t *testing.T) {
	f := &fakeRPC{results: map[string]any{"session.store_activity_log": Object{"ok": false, "error": "run not found"}}}
	tr := makeTelemetry(t, f, "run")
	var reported []string
	tr.Config.OnError = func(s string) { reported = append(reported, s) }
	ctx := context.Background()
	tr.Start(ctx)
	tr.Handle(ctx, Event{"session", Object{"session_id": "s"}})
	tr.Finish(ctx)
	if len(f.forMethod("session.store_activity_log")) != 2 || !strings.Contains(reported[0], "run not found") {
		t.Fatal(f.calls, reported)
	}
}
func TestTelemetryMissingRunAndEarlyError(t *testing.T) {
	f := &fakeRPC{}
	tr := makeTelemetry(t, f, "")
	var errors []string
	tr.Config.OnError = func(s string) { errors = append(errors, s) }
	if tr.Start(context.Background()) != "" || tr.Active() {
		t.Fatal("unexpected run")
	}
	telemetryStream(tr)
	tr.Finish(context.Background())
	if len(f.calls) != 1 || len(errors) != 1 {
		t.Fatal(f.calls, errors)
	}
	f = &fakeRPC{results: map[string]any{"task.start_run": Object{"item": Object{"id": "run"}}}}
	tr = makeTelemetry(t, f, "")
	tr.Start(context.Background())
	tr.Handle(context.Background(), Event{"error", Object{"message": "executable missing"}})
	tr.Finish(context.Background())
	events := f.forMethod("run.adapter_event")
	if events[len(events)-1]["status"] != "failed" {
		t.Fatal(events)
	}
}
func TestTelemetryConfigurationAndToolErrorContent(t *testing.T) {
	if _, err := NewTelemetry(TelemetryConfig{TaskID: " "}); err == nil {
		t.Fatal("blank task accepted")
	}
	if _, err := NewTelemetry(TelemetryConfig{TaskID: "t"}); err == nil {
		t.Fatal("blank endpoint accepted")
	}
	n, ok := UnifiedToNative(Event{"tool", Object{"id": "t", "status": "error", "output": "Claude tool error"}})
	if !ok || n.Data["content"] != "Claude tool error" {
		t.Fatal(n)
	}
}
