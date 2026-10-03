package agentiscode

// Normalizer converts runtime JSON into native events and keeps run summaries.
type Normalizer struct {
	Adapter, SessionID, Model string
	LastUsage                 Object
	LastCostUSD               any
	LastResult, LastError     Object
	started                   bool
}

func NewNormalizer(adapter string) (*Normalizer, error) {
	a, err := NormalizeAdapter(adapter)
	if err != nil {
		return nil, err
	}
	return &Normalizer{Adapter: a}, nil
}

func (n *Normalizer) Normalize(event Object) []NativeEvent {
	if n.Adapter == OpenCode {
		return n.openCode(event)
	}
	return n.claude(event)
}

func (n *Normalizer) claude(e Object) []NativeEvent {
	out := []NativeEvent{}
	t := str(e["type"])
	if t == "system" && e["subtype"] == "init" {
		n.SessionID = str(first(e["session_id"], n.SessionID))
		n.Model = str(first(e["model"], n.Model))
		n.started = true
		return append(out, native("session_start", Object{"session_id": nullable(n.SessionID), "model": nullable(n.Model), "tools": e["tools"], "cwd": e["cwd"], "mcp_servers": e["mcp_servers"]}, e))
	}
	sid := str(first(e["session_id"], e["sessionId"]))
	if !n.started && t != "result" && sid != "" {
		n.SessionID = sid
		n.Model = str(first(obj(e["message"])["model"], e["model"], n.Model))
		n.started = true
		out = append(out, native("session_start", Object{"session_id": sid, "model": nullable(n.Model), "cwd": e["cwd"]}, e))
		if t != "assistant" && t != "user" {
			return out
		}
	}
	switch t {
	case "assistant":
		n.SessionID = str(first(e["session_id"], n.SessionID))
		m := obj(e["message"])
		mid := str(m["id"])
		for _, b := range list(m["content"]) {
			block := obj(b)
			if block == nil {
				continue
			}
			var ev NativeEvent
			switch block["type"] {
			case "text":
				if !truth(block["text"]) {
					continue
				}
				ev = native("text", Object{"text": block["text"]}, e)
			case "thinking":
				ev = native("thinking", Object{"text": str(block["thinking"])}, e)
			case "tool_use":
				ev = native("tool_use", Object{"id": block["id"], "name": block["name"], "input": block["input"]}, e)
			default:
				ev = native("assistant_block", Object{"block": block}, e)
			}
			withMessageID(ev.Data, mid)
			out = append(out, ev)
		}
		out = append(out, native("assistant_message", withMessageID(Object{"message": m}, mid), e))
	case "user":
		m := obj(e["message"])
		emitted := false
		for _, b := range list(m["content"]) {
			block := obj(b)
			if block["type"] != "tool_result" {
				continue
			}
			out = append(out, native("tool_result", Object{"tool_use_id": block["tool_use_id"], "content": block["content"], "is_error": truth(block["is_error"])}, e))
			emitted = true
		}
		if !emitted {
			out = append(out, native("user_message", Object{"message": m}, e))
		}
	case "result":
		n.SessionID = str(first(e["session_id"], n.SessionID))
		u := obj(e["usage"])
		n.LastUsage = Object{}
		for _, k := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cache_write_tokens", "output_tokens"} {
			v, ok := u[k]
			if !ok {
				v = 0
			}
			n.LastUsage[k] = v
		}
		if c, ok := number(e["total_cost_usd"]); ok {
			n.LastCostUSD = c
		}
		n.LastResult = e
		out = append(out, native("result", Object{"summary": e["result"], "usage": n.LastUsage, "cost_usd": n.LastCostUSD, "session_id": nullable(n.SessionID), "is_error": truth(e["is_error"]), "subtype": e["subtype"]}, e))
	default:
		out = append(out, native("raw", Object{"event_type": e["type"], "event": e}, e))
	}
	return out
}

func (n *Normalizer) openCode(e Object) []NativeEvent {
	out := []NativeEvent{}
	p := obj(e["properties"])
	part := obj(e["part"])
	nested := false
	if part == nil {
		part = obj(p["part"])
		nested = part != nil
	}
	sid := str(first(e["sessionID"], e["session_id"]))
	if sid == "" {
		sid = str(first(p["sessionID"], p["session_id"]))
	}
	if sid == "" {
		sid = str(first(part["sessionID"], part["session_id"]))
	}
	if sid != "" && sid != n.SessionID {
		n.SessionID = sid
		out = append(out, native("session_start", Object{"session_id": sid}, e))
	}
	switch e["type"] {
	case "error":
		err := first(e["error"], Object{})
		message := errorMessage(err)
		if message == "" {
			message = "OpenCode selhal"
		}
		n.LastError = obj(err)
		if n.LastError == nil {
			n.LastError = Object{"message": message}
		}
		out = append(out, native("error", Object{"message": message, "error": err}, e))
	case "tool.execute.before":
		input, ok := e["input"]
		if !ok {
			input = p["input"]
		}
		out = append(out, native("tool_before", Object{"callID": first(e["callID"], p["callID"]), "tool": first(e["tool"], p["tool"]), "input": input}, e))
	default:
		if part != nil && (!nested || part["type"] == "tool" || part["type"] == "step-finish") {
			if part["type"] == "step-finish" {
				if tokens := obj(part["tokens"]); tokens != nil {
					n.LastUsage = openCodeUsage(tokens, "cache_write_tokens")
				}
				if cost, ok := number(part["cost"]); ok {
					n.LastCostUSD = cost
				}
			}
			out = append(out, native("part", Object{"part": part}, e))
		} else if len(out) == 0 {
			out = append(out, native("raw", Object{"event": e}, e))
		}
	}
	for i := range out {
		out[i].SessionID = sid
	}
	return out
}

func errorMessage(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	m := obj(v)
	if s, ok := obj(m["data"])["message"].(string); ok {
		return s
	}
	return str(first(m["message"], m["name"]))
}
func openCodeUsage(t Object, writeKey string) Object {
	c := obj(t["cache"])
	return Object{"input_tokens": integer(t["input"]), "output_tokens": integer(t["output"]), "reasoning_tokens": integer(t["reasoning"]), "cache_read_input_tokens": integer(c["read"]), writeKey: integer(c["write"])}
}
func withMessageID(d Object, id string) Object {
	if id != "" {
		d["message_id"] = id
	}
	return d
}

// Translator produces append-only unified events from native runtime events.
type Translator struct {
	Adapter    string
	textSeen   map[string]int
	toolStatus map[string]string
	stepsSeen  map[string]bool
}

func NewTranslator(adapter string) (*Translator, error) {
	a, err := NormalizeAdapter(adapter)
	if err != nil {
		return nil, err
	}
	return &Translator{a, map[string]int{}, map[string]string{}, map[string]bool{}}, nil
}
func (t *Translator) Translate(e NativeEvent) []Event {
	if t.Adapter == OpenCode {
		return t.openCode(e)
	}
	d := e.Data
	mid := str(d["message_id"])
	switch e.Type {
	case "session_start":
		return []Event{{"session", Object{"adapter": t.Adapter, "session_id": d["session_id"], "model": d["model"], "provider": "anthropic", "cwd": d["cwd"]}}}
	case "text", "thinking":
		if s := str(d["text"]); s != "" {
			kind := "text"
			if e.Type == "thinking" {
				kind = "reasoning"
			}
			return []Event{{kind, withMessageID(Object{"text": s}, mid)}}
		}
	case "tool_use":
		return []Event{{"tool", withMessageID(Object{"id": d["id"], "name": d["name"], "status": "running", "input": d["input"], "title": ToolTitle(str(d["name"]), d["input"])}, mid)}}
	case "tool_result":
		status := "completed"
		if truth(d["is_error"]) {
			status = "error"
		}
		return []Event{{"tool", Object{"id": d["tool_use_id"], "status": status, "output": Stringify(d["content"])}}}
	case "result":
		return []Event{{"result", Object{"session_id": d["session_id"], "usage": d["usage"], "cost_usd": d["cost_usd"], "is_error": truth(d["is_error"])}}}
	case "assistant_message":
		u := obj(obj(d["message"])["usage"])
		if len(u) == 0 || (mid != "" && t.stepsSeen[mid]) {
			return nil
		}
		if mid != "" {
			t.stepsSeen[mid] = true
		}
		return []Event{{"step", withMessageID(Object{"usage": copyObject(u), "cost_usd": nil}, mid)}}
	case "error":
		return []Event{{"error", Object{"message": d["message"]}}}
	case "stderr":
		return []Event{{"stderr", Object{"line": d["line"]}}}
	}
	return nil
}
func (t *Translator) openCode(e NativeEvent) []Event {
	d := e.Data
	switch e.Type {
	case "session_start":
		return []Event{{"session", Object{"adapter": OpenCode, "session_id": d["session_id"], "provider": OpenCode}}}
	case "error":
		return []Event{{"error", Object{"message": d["message"]}}}
	case "stderr":
		return []Event{{"stderr", Object{"line": d["line"]}}}
	case "tool_before":
		return t.tool(d["callID"], d["tool"], "running", d["input"], nil)
	case "part":
		p := obj(d["part"])
		switch p["type"] {
		case "text", "reasoning":
			text := []rune(str(p["text"]))
			id := str(p["id"])
			seen := 0
			if id != "" {
				seen = t.textSeen[id]
				t.textSeen[id] = len(text)
			}
			if seen >= len(text) {
				return nil
			}
			return []Event{{str(p["type"]), Object{"text": string(text[seen:])}}}
		case "tool":
			s := obj(p["state"])
			extra := Object{}
			for _, k := range []string{"output", "error"} {
				if s[k] != nil {
					extra[k] = s[k]
				}
			}
			if truth(s["title"]) {
				extra["title"] = s["title"]
			}
			return t.tool(p["callID"], p["tool"], str(first(s["status"], "running")), s["input"], extra)
		case "step-finish":
			if tokens := obj(p["tokens"]); tokens != nil {
				var cost any
				if c, ok := number(p["cost"]); ok {
					cost = c
				}
				return []Event{{"step", Object{"usage": openCodeUsage(tokens, "cache_creation_input_tokens"), "cost_usd": cost}}}
			}
		}
	}
	return nil
}
func (t *Translator) tool(callID, name any, status string, input any, extra Object) []Event {
	id := str(callID)
	if id == "" {
		return nil
	}
	if t.toolStatus[id] == status && !truth(extra["output"]) && !truth(extra["error"]) {
		return nil
	}
	t.toolStatus[id] = status
	d := Object{"id": id, "name": nullable(str(name)), "status": status, "input": input, "title": ToolTitle(str(name), input)}
	for k, v := range extra {
		d[k] = v
	}
	return []Event{{"tool", d}}
}
