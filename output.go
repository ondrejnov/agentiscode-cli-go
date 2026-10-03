package agentiscode

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FinalText retains the last contiguous text block (tool/reasoning/step breaks it).
type FinalText struct {
	chunks []string
	open   bool
}

func (f *FinalText) Handle(e Event) {
	if e.Type == "text" {
		if text := str(e.Data["text"]); text != "" {
			if !f.open {
				f.chunks = nil
			}
			f.chunks = append(f.chunks, text)
			f.open = true
		}
	} else if e.Type == "reasoning" || e.Type == "tool" || e.Type == "step" {
		f.open = false
	}
}
func (f *FinalText) Text() string { return strings.TrimSpace(strings.Join(f.chunks, "")) }

type OutputRecorder struct {
	FinalPath, SessionPath string
	OnError                func(error)
	final                  FinalText
	sessionWritten         bool
}

func (r *OutputRecorder) Handle(e Event) {
	r.final.Handle(e)
	if !r.sessionWritten && (e.Type == "session" || e.Type == "result") && r.SessionPath != "" {
		if s := str(e.Data["session_id"]); s != "" {
			r.write(r.SessionPath, s+"\n")
			r.sessionWritten = true
		}
	}
}
func (r *OutputRecorder) Finish() {
	if r.FinalPath != "" {
		r.write(r.FinalPath, r.final.Text()+"\n")
	}
}
func (r *OutputRecorder) write(path, content string) {
	err := os.MkdirAll(filepath.Dir(path), 0755)
	if err == nil {
		err = os.WriteFile(path, []byte(content), 0644)
	}
	if err != nil && r.OnError != nil {
		r.OnError(fmt.Errorf("nelze zapsat output %s: %w", path, err))
	}
}

type Renderer interface {
	Handle(Event) error
	Finish() error
}
type JSONRenderer struct{ Out io.Writer }

func (r *JSONRenderer) Handle(e Event) error {
	b, err := e.MarshalJSON()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.Out, "%s\n", b)
	return err
}
func (r *JSONRenderer) Finish() error { return nil }

type TextRenderer struct {
	Out, Err  io.Writer
	Color     bool
	tools     map[string]string
	wroteText bool
}

func (r *TextRenderer) activity(text string, dim bool) error {
	if dim && r.Color {
		text = "\033[2m" + text + "\033[0m"
	}
	_, err := fmt.Fprintln(r.Err, text)
	return err
}
func (r *TextRenderer) Handle(e Event) error {
	d := e.Data
	switch e.Type {
	case "session":
		parts := []string{str(first(d["adapter"], "agent"))}
		if truth(d["model"]) {
			parts = append(parts, "model="+str(d["model"]))
		}
		if truth(d["session_id"]) {
			parts = append(parts, "session="+str(d["session_id"]))
		}
		return r.activity("⏺ "+strings.Join(parts, "  "), true)
	case "text":
		if s := str(d["text"]); s != "" {
			_, err := io.WriteString(r.Out, s)
			r.wroteText = true
			return err
		}
	case "reasoning":
		if s := strings.TrimSpace(str(d["text"])); s != "" {
			return r.activity("  💭 "+s, true)
		}
	case "tool":
		id := str(d["id"])
		if d["status"] == "running" {
			name := str(first(d["name"], "tool"))
			if r.tools == nil {
				r.tools = map[string]string{}
			}
			if id != "" {
				r.tools[id] = name
			}
			title := str(first(d["title"], name))
			label := name
			if title != name {
				label += "(" + title + ")"
			}
			return r.activity("  ⚙ "+label, false)
		} else if d["status"] == "error" {
			name := str(first(r.tools[id], "tool"))
			message := strings.TrimSpace(Stringify(first(d["error"], d["output"])))
			if message == "" {
				message = "chyba"
			}
			return r.activity("  ✗ "+name+": "+strings.SplitN(message, "\n", 2)[0], false)
		}
	case "result":
		u := obj(d["usage"])
		bits := []string{}
		if u["input_tokens"] != nil || u["output_tokens"] != nil {
			bits = append(bits, fmt.Sprintf("tokens in=%d out=%d", integer(u["input_tokens"]), integer(u["output_tokens"])))
		}
		if c, ok := number(d["cost_usd"]); ok {
			bits = append(bits, fmt.Sprintf("cost=$%.4f", c))
		}
		state := "done"
		if truth(d["is_error"]) {
			state = "error"
		}
		suffix := ""
		if len(bits) > 0 {
			suffix = "  " + strings.Join(bits, "  ")
		}
		return r.activity("⏺ "+state+suffix, true)
	case "error":
		return r.activity("✗ "+str(first(d["message"], "chyba")), false)
	case "stderr":
		if s := strings.TrimRight(str(d["line"]), " \t\r\n"); s != "" {
			return r.activity("  "+s, true)
		}
	}
	return nil
}
func (r *TextRenderer) Finish() error {
	if r.wroteText {
		_, err := fmt.Fprintln(r.Out)
		return err
	}
	return nil
}
