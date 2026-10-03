// Package agentiscode runs coding agents and normalizes their streamed events.
// Each Wrapper, Normalizer, Mapper and Telemetry instance belongs to one run;
// calls on the same instance must be serialized.
package agentiscode

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Object holds extensible runtime payloads without discarding provider fields.
type Object = map[string]any

// Event is a normalized event. JSON is flattened: {"type":"text","text":"…"}.
type Event struct {
	Type string
	Data Object
}

func (e Event) Payload() Object {
	p := Object{"type": e.Type}
	for k, v := range e.Data {
		p[k] = v
	}
	return p
}

func (e Event) MarshalJSON() ([]byte, error) { return encodeJSON(e.Payload()) }
func (e *Event) UnmarshalJSON(b []byte) error {
	var p Object
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	e.Type = str(p["type"])
	delete(p, "type")
	e.Data = p
	return nil
}

// NativeEvent preserves the lower-level runtime event and its original payload.
type NativeEvent struct {
	Event
	Raw       Object
	SessionID string
}

func native(t string, d Object, raw Object) NativeEvent {
	return NativeEvent{Event: Event{t, d}, Raw: raw}
}
func obj(v any) Object { m, _ := v.(map[string]any); return m }
func str(v any) string { s, _ := v.(string); return s }
func list(v any) []any { a, _ := v.([]any); return a }
func first(values ...any) any {
	for _, v := range values {
		if truth(v) {
			return v
		}
	}
	if len(values) > 0 {
		return values[len(values)-1]
	}
	return nil
}
func truth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case json.Number:
		return x != "0" && x != ""
	case Object:
		return len(x) > 0
	case []any:
		return len(x) > 0
	default:
		return true
	}
}
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		n, err := x.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}
func integer(v any) int64 {
	if s, ok := v.(string); ok {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	n, _ := number(v)
	return int64(n)
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func copyObject(m Object) Object {
	out := Object{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("generate ID: %w", err))
	}
	return prefix + hex.EncodeToString(b[:])
}
func truncate(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return strings.TrimRight(string(r[:max-1]), " \t\n\r") + "…"
}

// Stringify converts tool results to text, including lists of content blocks.
func Stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if a, ok := v.([]any); ok {
		parts := make([]string, 0, len(a))
		for _, item := range a {
			if s, ok := obj(item)["text"].(string); ok {
				parts = append(parts, s)
			} else {
				parts = append(parts, pythonRepr(item, false))
			}
		}
		return strings.Join(parts, "\n")
	}
	if s, ok := obj(v)["text"].(string); ok {
		return s
	}
	return pythonRepr(v, false)
}

// Preserve the original wrapper's str(dict/list) fallback for non-text blocks.
func pythonRepr(v any, quote bool) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		if !quote {
			return x
		}
		q := "'"
		if strings.Contains(x, "'") && !strings.Contains(x, `"`) {
			q = `"`
		}
		x = strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t", q, "\\"+q).Replace(x)
		return q + x + q
	case Object:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(x))
		for _, k := range keys {
			parts = append(parts, pythonRepr(k, true)+": "+pythonRepr(x[k], true))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, 0, len(x))
		for _, a := range x {
			parts = append(parts, pythonRepr(a, true))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprint(v)
	}
}
