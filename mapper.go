package agentiscode

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Message struct {
	Info  Object   `json:"info"`
	Parts []Object `json:"parts"`
}

type assistantState struct {
	index                 int
	nativeID              string
	text, reasoning       int
	usageRecorded, closed bool
}

// Mapper builds OpenCode-compatible session.messages from native Claude events.
type Mapper struct {
	Prompt, SessionID, ModelID, Mode, Agent, ProviderID, Cwd string
	messages                                                 []Message
	assistant                                                *assistantState
	tokensFromSteps                                          bool
}

func nowSeconds() float64 { return float64(time.Now().UnixMicro()) / 1e6 }
func NewMapper(prompt, sessionID, mode, agent, cwd string) *Mapper {
	if mode == "" {
		mode = "build"
	}
	if agent == "" {
		agent = Claude
	}
	m := &Mapper{Prompt: prompt, SessionID: sessionID, Mode: mode, Agent: agent, ProviderID: "anthropic", Cwd: cwd}
	id := newID("msg_")[:28]
	now := nowSeconds()
	entry := Message{Info: Object{"id": id, "sessionID": sessionID, "role": "user", "time": Object{"created": now}}, Parts: []Object{}}
	if prompt != "" {
		entry.Parts = append(entry.Parts, Object{"id": newID("prt_")[:28], "sessionID": sessionID, "messageID": id, "type": "text", "text": prompt, "time": Object{"start": now, "end": now}})
	}
	m.messages = append(m.messages, entry)
	return m
}

// Snapshot returns an independent copy, safe to keep after subsequent Consume calls.
func (m *Mapper) Snapshot() []Message {
	b, _ := json.Marshal(m.messages)
	var out []Message
	_ = json.Unmarshal(b, &out)
	return out
}

func (m *Mapper) ensureAssistant(id string) *assistantState {
	if s := m.assistant; s != nil && !s.closed {
		if id == "" || s.nativeID == "" {
			if s.nativeID == "" {
				s.nativeID = id
			}
			return s
		}
		if s.nativeID == id {
			return s
		}
		m.closeAssistant(s)
	}
	m.messages = append(m.messages, Message{Info: Object{
		"id": newID("msg_")[:28], "sessionID": m.SessionID, "role": "assistant", "time": Object{"created": nowSeconds()},
		"modelID": m.ModelID, "providerID": m.ProviderID, "mode": m.Mode, "agent": m.Agent,
		"path": Object{"cwd": m.Cwd, "root": m.Cwd}, "cost": 0, "tokens": tokensFromUsage(nil),
	}, Parts: []Object{}})
	m.assistant = &assistantState{index: len(m.messages) - 1, nativeID: id, text: -1, reasoning: -1}
	return m.assistant
}

func (m *Mapper) part(s *assistantState, kind string) Object {
	return Object{"id": newID("prt_")[:28], "sessionID": m.SessionID, "messageID": m.messages[s.index].Info["id"], "type": kind}
}

func (m *Mapper) Consume(e NativeEvent) bool {
	d := e.Data
	switch e.Type {
	case "session_start":
		if v := str(d["model"]); v != "" {
			m.ModelID = v
		}
		if v := str(d["cwd"]); v != "" {
			m.Cwd = v
		}
		if v := str(d["session_id"]); v != "" && v != m.SessionID {
			m.SessionID = v
			for _, msg := range m.messages {
				msg.Info["sessionID"] = v
				for _, p := range msg.Parts {
					p["sessionID"] = v
				}
			}
		}
		return true
	case "text", "thinking":
		text := str(d["text"])
		if text == "" {
			return false
		}
		s := m.ensureAssistant(str(d["message_id"]))
		index := &s.text
		kind := "text"
		if e.Type == "thinking" {
			index = &s.reasoning
			kind = "reasoning"
		}
		msg := &m.messages[s.index]
		if *index < 0 {
			p := m.part(s, kind)
			p["text"] = text
			p["time"] = Object{"start": nowSeconds()}
			msg.Parts = append(msg.Parts, p)
			*index = len(msg.Parts) - 1
		} else {
			p := msg.Parts[*index]
			p["text"] = str(p["text"]) + text
		}
		return true
	case "tool_use":
		return m.toolUse(d)
	case "tool_result":
		id := str(d["tool_use_id"])
		if id == "" {
			return false
		}
		p := m.findTool(id)
		_, hasName := d["name"]
		_, hasInput := d["input"]
		if p == nil && (hasName || hasInput) {
			m.toolUse(Object{"id": id, "name": d["name"], "input": d["input"], "message_id": d["message_id"]})
			p = m.findTool(id)
		}
		if p == nil {
			return false
		}
		old := obj(p["state"])
		timing := copyObject(obj(old["time"]))
		timing["end"] = nowSeconds()
		state := Object{"status": "completed", "input": first(old["input"], Object{}), "metadata": first(old["metadata"], Object{}), "time": timing}
		if truth(d["is_error"]) {
			state["status"] = "error"
			state["error"] = Stringify(d["content"])
		} else {
			state["output"] = Stringify(d["content"])
			state["title"] = str(first(old["title"], p["tool"]))
		}
		p["state"] = state
		return true
	case "assistant_message":
		msg := obj(d["message"])
		usage := obj(msg["usage"])
		if len(usage) == 0 {
			return false
		}
		m.tokensFromSteps = true
		id := str(first(d["message_id"], msg["id"]))
		if id == "" {
			return m.finalize(usage, nil, "stop")
		}
		s := m.ensureAssistant(id)
		if s.usageRecorded {
			return false
		}
		m.recordUsage(s, usage, nil, "stop")
		return true
	case "step_finish":
		m.tokensFromSteps = true
		return m.finalize(obj(d["usage"]), d["cost_usd"], "stop")
	case "result":
		if m.tokensFromSteps {
			if m.assistant != nil && !m.assistant.closed {
				return m.closeAssistant(m.assistant)
			}
			return false
		}
		finish := "stop"
		if truth(d["is_error"]) {
			finish = str(first(d["subtype"], "error"))
		}
		return m.finalize(obj(d["usage"]), d["cost_usd"], finish)
	}
	return false
}

func (m *Mapper) toolUse(d Object) bool {
	id := str(d["id"])
	if id == "" || m.findTool(id) != nil {
		return false
	}
	s := m.ensureAssistant(str(d["message_id"]))
	name := str(d["name"])
	input, title := NormalizeToolInput(name, d["input"], m.Cwd)
	p := m.part(s, "tool")
	p["callID"] = id
	p["tool"] = name
	p["state"] = Object{"status": "running", "input": input, "title": title, "metadata": Object{}, "time": Object{"start": nowSeconds()}}
	m.messages[s.index].Parts = append(m.messages[s.index].Parts, p)
	s.text, s.reasoning = -1, -1
	return true
}
func (m *Mapper) findTool(id string) Object {
	for i := len(m.messages) - 1; i >= 0; i-- {
		for _, p := range m.messages[i].Parts {
			if p["type"] == "tool" && p["callID"] == id {
				return p
			}
		}
	}
	return nil
}
func tokensFromUsage(u Object) Object {
	return Object{"input": integer(u["input_tokens"]), "output": integer(u["output_tokens"]), "reasoning": integer(u["reasoning_tokens"]), "cache": Object{"read": integer(u["cache_read_input_tokens"]), "write": integer(first(u["cache_creation_input_tokens"], u["cache_write_tokens"]))}}
}
func (m *Mapper) recordUsage(s *assistantState, usage Object, cost any, finish string) {
	info := m.messages[s.index].Info
	obj(info["time"])["completed"] = nowSeconds()
	info["tokens"] = tokensFromUsage(usage)
	if c, ok := number(cost); ok {
		info["cost"] = c
	}
	info["finish"] = finish
	s.usageRecorded = true
}
func (m *Mapper) closeAssistant(s *assistantState) bool {
	if s.closed {
		return false
	}
	msg := &m.messages[s.index]
	now := nowSeconds()
	for _, p := range msg.Parts {
		state := obj(p["state"])
		if p["type"] != "tool" || (state["status"] != "running" && state["status"] != "pending") {
			continue
		}
		state["status"] = "completed"
		if _, ok := state["output"]; !ok {
			state["output"] = ""
		}
		timing := copyObject(obj(state["time"]))
		if _, ok := timing["start"]; !ok {
			timing["start"] = now
		}
		timing["end"] = now
		state["time"] = timing
	}
	p := m.part(s, "step-finish")
	p["reason"] = first(msg.Info["finish"], "stop")
	p["cost"] = msg.Info["cost"]
	p["tokens"] = msg.Info["tokens"]
	msg.Parts = append(msg.Parts, p)
	s.closed = true
	return true
}
func (m *Mapper) finalize(usage Object, cost any, finish string) bool {
	s := m.ensureAssistant("")
	m.recordUsage(s, usage, cost, finish)
	return m.closeAssistant(s)
}

var worktreePath = regexp.MustCompile(`^/var/www/[^/]+/[0-9a-f-]{20,}/(.+)$`)

func shortPath(path, cwd string) string {
	if cwd != "" {
		cwd = strings.TrimRight(cwd, "/")
		if path == cwd {
			return "."
		}
		if strings.HasPrefix(path, cwd+"/") {
			return strings.TrimPrefix(path, cwd+"/")
		}
	}
	if match := worktreePath.FindStringSubmatch(path); len(match) > 1 {
		return match[1]
	}
	p := strings.Split(path, "/")
	if len(p) > 5 {
		return ".../" + strings.Join(p[len(p)-4:], "/")
	}
	return path
}

// NormalizeToolInput supplies UI-friendly camelCase aliases and concise titles.
func NormalizeToolInput(name string, raw any, cwd string) (Object, string) {
	fallback := name
	if fallback == "" {
		fallback = "tool"
	}
	if obj(raw) == nil {
		if raw == nil {
			return Object{}, fallback
		}
		return Object{"value": raw}, fallback
	}
	in := copyObject(obj(raw))
	title := fallback
	alias := func(src, dst string) {
		if _, ok := in[dst]; !ok && in[src] != nil {
			in[dst] = in[src]
		}
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "edit", "multiedit", "write", "notebookedit":
		alias("file_path", "filePath")
		alias("notebook_path", "filePath")
		if p := str(first(in["filePath"], in["file_path"])); p != "" {
			title = shortPath(p, cwd)
		}
	case "bash":
		if d := str(in["description"]); strings.TrimSpace(d) != "" {
			title = truncate(d, 80)
		} else if c := str(in["command"]); strings.TrimSpace(c) != "" {
			title = truncate(c, 80)
		}
	case "glob", "grep":
		if p := str(in["pattern"]); p != "" {
			title = truncate(p, 80)
			if strings.EqualFold(name, "glob") && str(in["path"]) != "" {
				title += "  (in " + shortPath(str(in["path"]), cwd) + ")"
			}
		}
	case "todowrite":
		if a, ok := in["todos"].([]any); ok {
			title = fmt.Sprintf("%d todos", len(a))
		}
	case "task", "agent":
		alias("subagent_type", "subagentType")
		for _, key := range []string{"description", "subagentType", "subagent_type", "prompt"} {
			if s := str(in[key]); strings.TrimSpace(s) != "" {
				title = truncate(s, 80)
				break
			}
		}
	case "webfetch":
		if s := str(in["url"]); s != "" {
			title = truncate(s, 120)
		}
	case "websearch", "toolsearch":
		if s := str(in["query"]); s != "" {
			title = truncate(s, 120)
		}
	case "askuserquestion":
		if a, ok := in["questions"].([]any); ok {
			title = fmt.Sprintf("Asked %d questions", len(a))
		}
	}
	return in, title
}
