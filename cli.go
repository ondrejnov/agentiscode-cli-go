package agentiscode

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is overridden by the release build using -ldflags -X.
var Version = "0.1.0"

const Help = `Usage: agentiscode --adapter NAME [OPTIONS] [PROMPT ...]

Sjednocený commandline wrapper nad OpenCode, Claude Code a claude-p.
Bez PROMPT se zadání načte ze stdin. --json vypisuje sjednocené JSON Lines.

  -a, --adapter NAME             opencode | claude | claude-p (oc, cloud, cc, cp…)
  -m, --model MODEL              Model předaný agentovi
  -e, --effort VALUE             Claude --effort / OpenCode --variant
      --agent NAME              Pojmenovaný agent nebo mode CLI
      --cwd PATH                Pracovní adresář (default: aktuální)
      --resume SESSION_ID       Navázání na existující session
      --timeout SECONDS         Časový limit celého běhu (0 = bez limitu)
      --json                    Sjednocené JSON Lines na stdout
      --task-id TASK_ID         Založit Agentis run a ukládat telemetrii
      --project-id PROJECT_ID   Kontext projektu (AGENTIS_PROJECT_ID)
      --run-id RUN_ID           Existující run; vyžaduje --task-id
      --task-status STATUS_ID   Stav tasku u finálního komentáře
      --last-message-to-comment Poslat finální odpověď jako primary komentář
      --primary-session BOOL    Primární session (default: true)
      --agentis-api URL         JSON-RPC endpoint (AGENTIS_ENDPOINT)
      --agentis-token TOKEN     User token (AGENTIS_API_TOKEN / AGENTIS_TOKEN)
      --agentis-service-token T Service token (AGENTIS_SERVICE_TOKEN)
      --final-output PATH       Zapsat poslední souvislý text odpovědi
      --session-output PATH     Zapsat session ID hned po jeho získání
  -h, --help                    Zobrazit nápovědu
      --version                 Zobrazit verzi

Nové sessions dostanou agentis_task_id a agentis_project_id XML tagy v promptu.
Externí --run-id ukončuje workflow orchestrátor, nikoli tento příkaz.
`

type Options struct {
	Config                                Config
	Telemetry                             TelemetryConfig
	JSON, Help, Version                   bool
	ProjectID, FinalOutput, SessionOutput string
	PromptParts                           []string
}

func ParseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected true/false")
	}
}

// ParseOptions supports both --key=value and --key value, short aliases, and
// options interspersed with prompt words. A bare -- ends option parsing.
func ParseOptions(args []string, getenv func(string) string) (Options, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	o := Options{ProjectID: getenv("AGENTIS_PROJECT_ID")}
	o.Telemetry.Endpoint = getenv("AGENTIS_ENDPOINT")
	o.Telemetry.Token = str(first(getenv("AGENTIS_API_TOKEN"), getenv("AGENTIS_TOKEN")))
	o.Telemetry.ServiceToken = getenv("AGENTIS_SERVICE_TOKEN")
	stringsTo := map[string]*string{
		"adapter": &o.Config.Adapter, "model": &o.Config.Model, "effort": &o.Config.Effort, "agent": &o.Config.Agent,
		"cwd": &o.Config.Cwd, "resume": &o.Config.ResumeSessionID,
		"task-id": &o.Telemetry.TaskID, "project-id": &o.ProjectID, "run-id": &o.Telemetry.RunID,
		"agentis-api": &o.Telemetry.Endpoint, "agentis-token": &o.Telemetry.Token, "agentis-service-token": &o.Telemetry.ServiceToken,
		"final-output": &o.FinalOutput, "session-output": &o.SessionOutput,
	}
	short := map[string]string{"a": "adapter", "m": "model", "e": "effort", "h": "help"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.PromptParts = append(o.PromptParts, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			o.PromptParts = append(o.PromptParts, a)
			continue
		}
		key, value, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !strings.HasPrefix(a, "--") {
			if len(a) < 2 {
				return o, fmt.Errorf("unknown option %s", a)
			}
			var ok bool
			key, ok = short[a[1:2]]
			if !ok {
				return o, fmt.Errorf("unknown option %s", a)
			}
			value = strings.TrimPrefix(a[2:], "=")
			hasValue = len(a) > 2
		}
		if key == "help" || key == "version" || key == "json" || key == "last-message-to-comment" {
			if hasValue {
				return o, fmt.Errorf("--%s does not take a value", key)
			}
			switch key {
			case "help":
				o.Help = true
				return o, nil
			case "version":
				o.Version = true
				return o, nil
			case "json":
				o.JSON = true
			case "last-message-to-comment":
				o.Telemetry.LastMessageToComment = true
			}
			continue
		}
		if stringsTo[key] == nil && key != "timeout" && key != "task-status" && key != "primary-session" {
			return o, fmt.Errorf("unknown option %s", a)
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return o, fmt.Errorf("--%s requires a value", key)
			}
			value = args[i]
		}
		if dest := stringsTo[key]; dest != nil {
			*dest = value
			continue
		}
		switch key {
		case "timeout":
			seconds, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds > float64(math.MaxInt64)/float64(time.Second) {
				return o, fmt.Errorf("--timeout: invalid seconds %q", value)
			}
			if seconds > 0 {
				o.Config.Timeout = time.Duration(seconds * float64(time.Second))
			}
		case "task-status":
			n, err := strconv.Atoi(value)
			if err != nil {
				return o, fmt.Errorf("--task-status: expected integer")
			}
			o.Telemetry.TaskStatus = &n
		case "primary-session":
			b, err := ParseBool(value)
			if err != nil {
				return o, fmt.Errorf("--primary-session: %w", err)
			}
			o.Telemetry.PrimarySession = &b
		}
	}
	if o.Config.Adapter == "" {
		return o, fmt.Errorf("the following argument is required: --adapter")
	}
	a, err := NormalizeAdapter(o.Config.Adapter)
	if err != nil {
		return o, err
	}
	o.Config.Adapter = a
	if o.Telemetry.RunID != "" && o.Telemetry.TaskID == "" {
		return o, fmt.Errorf("--run-id vyžaduje --task-id kvůli identifikaci tasku.")
	}
	if o.Telemetry.TaskID != "" && o.Telemetry.Endpoint == "" {
		return o, fmt.Errorf("--task-id/--run-id vyžaduje --agentis-api URL (nebo $AGENTIS_ENDPOINT).")
	}
	return o, nil
}

func AppendContextIDs(prompt, taskID, projectID string) string {
	tags := []string{}
	if taskID != "" {
		tags = append(tags, "<agentis_task_id>"+taskID+"</agentis_task_id>")
	}
	if projectID != "" {
		tags = append(tags, "<agentis_project_id>"+projectID+"</agentis_project_id>")
	}
	if len(tags) == 0 {
		return prompt
	}
	return strings.TrimRight(prompt, " \t\r\n") + "\n\n" + strings.Join(tags, "\n")
}

var shellSafe = regexp.MustCompile(`^[a-zA-Z0-9_@%+=:,./-]+$`)

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if shellSafe.MatchString(a) {
			quoted[i] = a
		} else {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\"'\"'") + "'"
		}
	}
	return strings.Join(quoted, " ")
}
func CommandDisplay(executable string, args []string) string {
	display := []string{executable}
	redact := false
	for _, a := range args {
		if redact {
			display = append(display, "REDACTED")
			redact = false
			continue
		}
		option, _, equals := strings.Cut(a, "=")
		if option == "--agentis-token" || option == "--agentis-service-token" {
			if equals {
				display = append(display, option+"=REDACTED")
			} else {
				display = append(display, option)
				redact = true
			}
			continue
		}
		display = append(display, a)
	}
	return shellJoin(display)
}

func isTerminal(w any) bool {
	if f, ok := w.(*os.File); ok {
		info, err := f.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	return false
}

// RunCLI executes the CLI with injectable streams/environment for embedding and tests.
// Returns 0 on success, 1 for runtime failure, 2 for usage errors, 130 on cancellation.
func RunCLI(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, getenv func(string) string) int {
	fmt.Fprintln(stderr, "[agentiscode] command: "+CommandDisplay("agentiscode", args))
	o, err := ParseOptions(args, getenv)
	usageError := func(err error) int {
		fmt.Fprintln(stderr, "agentiscode: error:", err)
		fmt.Fprintln(stderr, "Použij --help pro nápovědu.")
		return 2
	}
	if err != nil {
		return usageError(err)
	}
	if o.Help {
		fmt.Fprint(out, Help)
		return 0
	}
	if o.Version {
		fmt.Fprintln(out, "agentiscode "+Version+" (Go)")
		return 0
	}
	prompt := strings.TrimSpace(strings.Join(o.PromptParts, " "))
	if len(o.PromptParts) == 0 && !isTerminal(in) {
		type inputResult struct {
			data []byte
			err  error
		}
		input := make(chan inputResult, 1)
		go func() { data, err := io.ReadAll(in); input <- inputResult{data, err} }()
		select {
		case result := <-input:
			if result.err != nil {
				return usageError(fmt.Errorf("stdin: %w", result.err))
			}
			prompt = strings.TrimSpace(string(result.data))
		case <-ctx.Done():
			return 130
		}
	}
	if prompt == "" {
		return usageError(fmt.Errorf("Chybí prompt (zadej ho jako argument nebo na stdin)."))
	}
	if o.Config.ResumeSessionID == "" {
		prompt = AppendContextIDs(prompt, o.Telemetry.TaskID, o.ProjectID)
	}
	if o.Config.Cwd == "" {
		o.Config.Cwd, err = os.Getwd()
		if err != nil {
			return usageError(err)
		}
	}
	var telemetry *Telemetry
	if o.Telemetry.TaskID != "" {
		o.Telemetry.Prompt, o.Telemetry.Adapter, o.Telemetry.Cwd = prompt, o.Config.Adapter, o.Config.Cwd
		o.Telemetry.Mode = str(first(o.Config.Agent, "build"))
		o.Telemetry.OnError = func(message string) { fmt.Fprintln(stderr, "[agentiscode] "+message) }
		telemetry, err = NewTelemetry(o.Telemetry)
		if err != nil {
			return usageError(err)
		}
		defer telemetry.Close()
	}
	r := &OutputRecorder{FinalPath: o.FinalOutput, SessionPath: o.SessionOutput, OnError: func(err error) { fmt.Fprintln(stderr, "[agentiscode]", err) }}
	var renderer Renderer = &TextRenderer{Out: out, Err: stderr, Color: isTerminal(stderr)}
	if o.JSON {
		renderer = &JSONRenderer{Out: out}
	}
	w, err := NewWrapper(o.Config)
	if err != nil {
		return usageError(err)
	}
	if telemetry != nil {
		telemetry.Start(ctx)
	}
	exit := 0
	err = w.Stream(ctx, prompt, func(e Event) error {
		if e.Type == "error" || (e.Type == "result" && truth(e.Data["is_error"])) {
			exit = 1
		}
		if err := renderer.Handle(e); err != nil {
			return err
		}
		r.Handle(e)
		if telemetry != nil {
			telemetry.Handle(ctx, e)
		}
		return nil
	})
	finishErr := renderer.Finish()
	if ctx.Err() != nil {
		return 130
	}
	if err != nil || finishErr != nil {
		if err == nil {
			err = finishErr
		}
		fmt.Fprintln(stderr, "[agentiscode]", err)
		exit = 1
		if telemetry != nil {
			telemetry.Handle(ctx, Event{"error", Object{"message": err.Error()}})
		}
	}
	r.Finish()
	if telemetry != nil {
		telemetry.Finish(ctx)
	}
	return exit
}
