package agentiscode

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type TelemetryConfig struct {
	TaskID, Prompt, Adapter, Mode, Cwd, RunID string
	TaskStatus                                *int
	LastMessageToComment                      bool
	// Nil means true, preserving the CLI default.
	PrimarySession                *bool
	Endpoint, Token, ServiceToken string
	Timeout                       time.Duration
	Client                        RPC
	OnError                       func(string)
}
type sessionState struct {
	mapper                 *Mapper
	bound, dirty, stepSeen bool
}

// Telemetry is best-effort: RPC failures never fail the coding-agent run.
type Telemetry struct {
	Config           TelemetryConfig
	RunID, SessionID string
	client           RPC
	ownedClient      *Client
	ownsRun          bool
	sessions         map[string]*sessionState
	order            []string
	isError          bool
	final            FinalText
}

func NewTelemetry(c TelemetryConfig) (*Telemetry, error) {
	c.TaskID = strings.TrimSpace(c.TaskID)
	if c.TaskID == "" {
		return nil, fmt.Errorf("task_id must not be empty")
	}
	if c.Mode == "" {
		c.Mode = "build"
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.OnError == nil {
		c.OnError = func(string) {}
	}
	run := strings.TrimSpace(c.RunID)
	t := &Telemetry{Config: c, RunID: run, ownsRun: run == "", client: c.Client, sessions: map[string]*sessionState{}}
	if t.client == nil {
		client, err := NewClient(c.Endpoint, c.Token, c.ServiceToken, c.Timeout)
		if err != nil {
			return nil, err
		}
		t.client, t.ownedClient = client, client
	}
	return t, nil
}
func (t *Telemetry) Close() {
	if t.ownedClient != nil {
		t.ownedClient.Close()
	}
}
func (t *Telemetry) Active() bool { return t.RunID != "" }
func (t *Telemetry) Start(ctx context.Context) string {
	if t.RunID == "" {
		result := obj(t.call(ctx, "task.start_run", Object{"id": t.Config.TaskID, "start_adapter": false}))
		t.RunID = str(obj(result["item"])["id"])
	}
	if t.RunID == "" {
		t.Config.OnError("Agentis task.start_run nevrátil run id; telemetrie je vypnutá.")
		return ""
	}
	t.adapterEvent(ctx, "success", "agentiscode", "běh spuštěn.")
	return t.RunID
}
func (t *Telemetry) Handle(ctx context.Context, e Event) {
	if !t.Active() {
		return
	}
	sid := str(first(e.Data["session_id"], t.SessionID))
	state := t.sessions[sid]
	// Capture failures even before a runtime has emitted its initial session.
	if (sid == t.SessionID || sid == "") && (e.Type == "error" || (e.Type == "result" && truth(e.Data["is_error"]))) {
		t.isError = true
	}
	if e.Type == "session" && sid != "" && state == nil {
		primary := t.SessionID == ""
		prompt := ""
		if primary {
			prompt = t.Config.Prompt
			t.SessionID = sid
		}
		state = &sessionState{mapper: NewMapper(prompt, sid, t.Config.Mode, t.Config.Adapter, t.Config.Cwd)}
		t.sessions[sid] = state
		t.order = append(t.order, sid)
	}
	if state == nil {
		return
	}
	if !state.bound {
		t.bind(ctx, sid, state)
	}
	if sid == t.SessionID {
		t.final.Handle(e)
	}
	if e.Type == "step" {
		state.stepSeen = true
	} else if e.Type == "result" && state.stepSeen {
		return
	}
	if n, ok := UnifiedToNative(e); ok && state.mapper.Consume(n) {
		state.dirty = true
		if state.bound {
			t.push(ctx, sid, state)
		}
	}
}
func (t *Telemetry) Finish(ctx context.Context) {
	if !t.Active() {
		return
	}
	for _, sid := range t.order {
		s := t.sessions[sid]
		if !s.bound {
			t.bind(ctx, sid, s)
		}
		if s.bound && s.dirty {
			t.push(ctx, sid, s)
		}
	}
	if t.Config.LastMessageToComment {
		if body := t.final.Text(); body != "" {
			p := Object{"run_id": t.RunID, "body": body, "comment_type": "primary"}
			if t.Config.TaskStatus != nil {
				p["status"] = *t.Config.TaskStatus
			}
			t.call(ctx, "task.add_agent_comment", p)
		}
	}
	if t.ownsRun {
		status, message := "success", "agentiscode běh doběhl."
		if t.isError {
			status, message = "failed", "agentiscode běh selhal."
		}
		t.adapterEvent(ctx, status, "idle", message)
	}
}
func (t *Telemetry) bind(ctx context.Context, sid string, s *sessionState) {
	primary := sid == t.SessionID && (t.Config.PrimarySession == nil || *t.Config.PrimarySession)
	s.bound = t.succeeded(ctx, "run.store_session_id", Object{"run_id": t.RunID, "session_id": sid, "primary": primary})
	if s.bound && s.dirty {
		t.push(ctx, sid, s)
	}
}
func (t *Telemetry) push(ctx context.Context, sid string, s *sessionState) {
	if t.succeeded(ctx, "session.store_activity_log", Object{"session_id": sid, "messages": s.mapper.Snapshot()}) {
		s.dirty = false
	}
}
func (t *Telemetry) adapterEvent(ctx context.Context, status, kind, message string) {
	t.call(ctx, "run.adapter_event", Object{"run_id": t.RunID, "kind": kind, "status": status, "event_id": kind + ":" + t.RunID, "message": message, "data": Object{}})
}
func (t *Telemetry) call(ctx context.Context, method string, p Object) any {
	ctx, cancel := context.WithTimeout(ctx, t.Config.Timeout)
	defer cancel()
	result, err := t.client.Call(ctx, method, p)
	if err != nil {
		t.Config.OnError(fmt.Sprintf("Agentis %s selhalo: %v", method, err))
		return nil
	}
	return result
}
func (t *Telemetry) succeeded(ctx context.Context, method string, p Object) bool {
	r := t.call(ctx, method, p)
	if r == nil {
		return false
	}
	if ok, exists := obj(r)["ok"]; exists && ok == false {
		t.Config.OnError(fmt.Sprintf("Agentis %s selhalo: %v", method, first(obj(r)["error"], "Agentis vrátil ok=false")))
		return false
	}
	return true
}

// UnifiedToNative bridges the unified stream to the transcript mapper.
func UnifiedToNative(e Event) (NativeEvent, bool) {
	d := e.Data
	switch e.Type {
	case "session":
		return native("session_start", Object{"session_id": d["session_id"], "model": d["model"], "cwd": d["cwd"]}, nil), true
	case "text":
		return native("text", Object{"text": str(d["text"])}, nil), true
	case "reasoning":
		return native("thinking", Object{"text": str(d["text"])}, nil), true
	case "tool":
		if d["status"] == "running" {
			return native("tool_use", Object{"id": d["id"], "name": d["name"], "input": d["input"]}, nil), true
		}
		if d["status"] == "completed" || d["status"] == "error" {
			isError := d["status"] == "error"
			content := d["output"]
			if isError {
				content = first(d["error"], d["output"])
			}
			p := Object{"tool_use_id": d["id"], "content": content, "is_error": isError}
			for _, k := range []string{"name", "input", "message_id"} {
				if v, ok := d[k]; ok {
					p[k] = v
				}
			}
			return native("tool_result", p, nil), true
		}
	case "step":
		return native("step_finish", Object{"usage": d["usage"], "cost_usd": d["cost_usd"]}, nil), true
	case "result":
		return native("result", Object{"session_id": d["session_id"], "usage": d["usage"], "cost_usd": d["cost_usd"], "is_error": truth(d["is_error"])}, nil), true
	}
	return NativeEvent{}, false
}
