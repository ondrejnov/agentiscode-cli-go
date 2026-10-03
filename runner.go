package agentiscode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Wrapper provides a unified, synchronous streaming callback API. Stream blocks
// until the runtime exits. Cancel the context to abort the entire process group.
type Wrapper struct {
	Config     Config
	Normalizer *Normalizer
	translator *Translator
}

func NewWrapper(c Config) (*Wrapper, error) {
	n, err := NewNormalizer(c.Adapter)
	if err != nil {
		return nil, err
	}
	t, _ := NewTranslator(c.Adapter)
	return &Wrapper{Config: c, Normalizer: n, translator: t}, nil
}

func (w *Wrapper) Stream(ctx context.Context, prompt string, emit func(Event) error) error {
	result := false
	err := w.StreamNative(ctx, prompt, func(n NativeEvent) error {
		for _, e := range w.translator.Translate(n) {
			if w.Normalizer.Adapter == OpenCode {
				sid := str(first(n.SessionID, w.Normalizer.SessionID))
				if sid != "" {
					if _, ok := e.Data["session_id"]; !ok {
						e.Data["session_id"] = sid
					}
				}
			}
			if e.Type == "result" {
				result = true
			}
			if err := emit(e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !result {
		n := w.Normalizer
		var usage any
		if n.LastUsage != nil {
			usage = n.LastUsage
		}
		return emit(Event{"result", Object{"session_id": nullable(n.SessionID), "usage": usage, "cost_usd": n.LastCostUSD, "is_error": n.LastError != nil}})
	}
	return nil
}

type streamLine struct {
	text        string
	stderr, eof bool
	err         error
}

// pumpLines uses ReadString rather than Scanner: JSON/tool lines may exceed 64KB.
func pumpLines(ctx context.Context, r io.Reader, stderr bool, out chan<- streamLine) {
	b := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := b.ReadString('\n')
		if len(line) > 0 {
			line = strings.ToValidUTF8(strings.TrimSuffix(line, "\n"), "\uFFFD")
			select {
			case out <- streamLine{text: line, stderr: stderr}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			select {
			case out <- streamLine{stderr: stderr, eof: true, err: err}:
			case <-ctx.Done():
			}
			return
		}
	}
}

// StreamNative exposes the original runtime-specific normalized events.
// Runtime failures are error events; callback/cancellation failures are Go errors.
func (w *Wrapper) StreamNative(parent context.Context, prompt string, emit func(NativeEvent) error) error {
	if err := parent.Err(); err != nil {
		return err
	}
	cfg := w.Config
	n := w.Normalizer
	command, args, err := cfg.BuildCommand()
	if err != nil {
		return err
	}
	emitError := func(message string) error {
		n.LastError = Object{"message": message}
		return emit(native("error", Object{"message": message}, nil))
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if cfg.Timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, cfg.Timeout)
		defer timeoutCancel()
	}
	executable, err := resolveExecutable(command, cfg)
	if err != nil {
		return emitError(fmt.Sprintf("%s nelze spustit: %v", n.Adapter, err))
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = cfg.Cwd
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "IS_SANDBOX=1")
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureProcess(cmd)
	// Own the read ends so Wait cannot close them before buffered data is read.
	stdout, outWrite, err := os.Pipe()
	if err != nil {
		return emitError(err.Error())
	}
	defer stdout.Close()
	defer outWrite.Close()
	stderr, errWrite, err := os.Pipe()
	if err != nil {
		return emitError(err.Error())
	}
	defer stderr.Close()
	defer errWrite.Close()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return emitError(err.Error())
	}
	defer stdin.Close()
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	if err := cmd.Start(); err != nil {
		return emitError(fmt.Sprintf("%s nelze spustit: %v", n.Adapter, err))
	}
	outWrite.Close()
	errWrite.Close()
	waitCh := make(chan error, 1)
	processDone := make(chan struct{})
	go func() { err := cmd.Wait(); waitCh <- err; close(processDone) }()
	// Independent watchdog also enforces the deadline during a telemetry request.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-watchDone:
		}
	}()
	defer func() {
		select {
		case <-processDone:
		default:
			killProcessGroup(cmd)
			<-processDone
		}
	}()
	go func() { _, _ = io.WriteString(stdin, prompt); _ = stdin.Close() }()
	lines := make(chan streamLine, 64)
	go pumpLines(ctx, stdout, false, lines)
	go pumpLines(ctx, stderr, true, lines)
	var stderrTail, rawTail []string
	stdoutEOF, stderrEOF, exited, terminal, producedError := false, false, false, false, false
	var waitErr error
	grace := cfg.ExitGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	var graceTimer *time.Timer
	var graceC <-chan time.Time
	startGrace := func() {
		if graceTimer == nil {
			graceTimer = time.NewTimer(grace)
			graceC = graceTimer.C
		}
	}
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	for !exited || !stdoutEOF || !stderrEOF {
		// Once the leader has exited, only time out *idle* inherited pipes.
		// Slow callbacks must not cause buffered output to be discarded.
		if exited && !terminal && graceC != nil {
			if !graceTimer.Stop() {
				select {
				case <-graceTimer.C:
				default:
				}
			}
			graceTimer.Reset(grace)
		}
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
			if parent.Err() != nil {
				return parent.Err()
			}
			return emitError(fmt.Sprintf("timeout po %gs", cfg.Timeout.Seconds()))
		case <-graceC:
			killProcessGroup(cmd)
			stdout.Close()
			stderr.Close()
			graceC = nil
		case waitErr = <-waitCh:
			exited = true
			waitCh = nil
			// Descendants must not keep our pipes open indefinitely after exit.
			startGrace()
		case line := <-lines:
			if line.eof {
				if line.stderr {
					stderrEOF = true
				} else {
					stdoutEOF = true
					if n.Adapter != OpenCode {
						startGrace()
					}
				}
				if line.err != nil && graceC != nil {
					if err := emitError("čtení streamu: " + line.err.Error()); err != nil {
						return err
					}
					producedError = true
				}
				continue
			}
			if line.stderr {
				if strings.TrimSpace(line.text) != "" {
					stderrTail = appendTail(stderrTail, line.text)
					if err := emit(native("stderr", Object{"line": line.text}, nil)); err != nil {
						return err
					}
				}
				continue
			}
			if terminal {
				continue
			}
			text := strings.TrimSpace(line.text)
			if text == "" {
				continue
			}
			var event Object
			if err := json.Unmarshal([]byte(text), &event); err != nil || event == nil {
				rawTail = appendTail(rawTail, text)
				if err := emit(native("raw", Object{"line": text}, nil)); err != nil {
					return err
				}
				continue
			}
			for _, ev := range n.Normalize(event) {
				if ev.Type == "error" {
					producedError = true
				}
				if err := emit(ev); err != nil {
					return err
				}
				if ev.Type == "result" {
					terminal = true
					startGrace()
				}
			}
		}
	}
	if waitErr != nil && !producedError && n.LastResult == nil {
		code := cmd.ProcessState.ExitCode()
		message := fmt.Sprintf("%s skončil s kódem %d", n.Adapter, code)
		summary := ""
		for i := len(stderrTail) - 1; i >= 0; i-- {
			s := strings.TrimSpace(stderrTail[i])
			if n.Adapter == OpenCode && (s == "--- Start ---" || s == "--- End ---") {
				continue
			}
			summary = s
			break
		}
		if summary == "" && len(stderrTail) > 0 {
			summary = strings.TrimSpace(stderrTail[len(stderrTail)-1])
		}
		if summary != "" {
			message += ": " + summary
		}
		if n.Adapter == OpenCode {
			display := append([]string{command}, args...)
			if len(display) > 1 && display[1] == "run" {
				display = append([]string{command, "run", "<stdin>"}, args[1:]...)
			}
			message += "\n\nDetaily:\npříkaz: " + shellJoin(display)
			if cfg.Cwd != "" {
				message += "\ncwd: " + cfg.Cwd
			}
			if len(stderrTail) > 0 {
				message += "\nstderr (posledních 20 řádků):\n" + strings.Join(stderrTail, "\n")
			}
			if len(rawTail) > 0 {
				message += "\nstdout neparsované řádky (posledních 20):\n" + strings.Join(rawTail, "\n")
			}
		}
		n.LastError = Object{"message": message}
		return emit(native("error", Object{"message": message, "exit_code": code, "stderr": strings.Join(stderrTail, "\n"), "stdout": strings.Join(rawTail, "\n")}, nil))
	}
	return nil
}

func appendTail(a []string, s string) []string {
	a = append(a, s)
	if len(a) > 20 {
		return a[len(a)-20:]
	}
	return a
}

// Collect is a convenience API for callers that don't need incremental output.
func (w *Wrapper) Collect(ctx context.Context, prompt string) (Object, error) {
	events := []Object{}
	texts := []string{}
	err := w.Stream(ctx, prompt, func(e Event) error {
		events = append(events, e.Payload())
		if e.Type == "text" {
			texts = append(texts, str(e.Data["text"]))
		}
		return nil
	})
	n := w.Normalizer
	summary := str(first(n.LastResult["result"], strings.TrimSpace(strings.Join(texts, "\n\n"))))
	return Object{"events": events, "summary": summary, "session_id": nullable(n.SessionID), "model": nullable(n.Model), "usage": n.LastUsage, "cost_usd": n.LastCostUSD, "result": n.LastResult}, err
}

// StreamSSE streams unified events as Server-Sent Events to an HTTP response.
func (w *Wrapper) StreamSSE(ctx context.Context, prompt string, out io.Writer) error {
	return w.Stream(ctx, prompt, func(e Event) error {
		data, err := e.MarshalJSON()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "event: %s\ndata: %s\n\n", e.Type, data); err != nil {
			return err
		}
		if f, ok := out.(interface{ Flush() }); ok {
			f.Flush()
		}
		return nil
	})
}
