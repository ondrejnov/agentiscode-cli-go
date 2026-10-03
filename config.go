package agentiscode

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	OpenCode = "opencode"
	Claude   = "claude"
	ClaudeP  = "claude-p"
)

var adapterAliases = map[string]string{
	"opencode": OpenCode, "oc": OpenCode,
	"claude": Claude, "claudecode": Claude, "claude-code": Claude, "cloud": Claude, "cc": Claude,
	"claude-p": ClaudeP, "claudep": ClaudeP, "cp": ClaudeP,
}

func NormalizeAdapter(name string) (string, error) {
	if a, ok := adapterAliases[strings.ToLower(strings.TrimSpace(name))]; ok {
		return a, nil
	}
	keys := make([]string, 0, len(adapterAliases))
	for k := range adapterAliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "", fmt.Errorf("Neznámý adaptér %q. Dostupné: %s", name, strings.Join(keys, ", "))
}

// Config configures a run. Command optionally overrides the runtime executable.
// Timeout <= 0 means unlimited. ExitGrace defaults to ten seconds for Claude.
// Permissions are skipped by default, matching the original CLI.
type Config struct {
	Adapter, Command, Model, Effort, Agent, Cwd, ResumeSessionID string
	Timeout, ExitGrace                                           time.Duration
	ExtraArgs                                                    []string
	Env                                                          map[string]string
	Chrome                                                       bool
	MaxTurns                                                     int
	AppendSystemPromptFile                                       string
	AddDirs                                                      []string
	RequirePermissions                                           bool
}

func (c Config) BuildCommand() (string, []string, error) {
	a, err := NormalizeAdapter(c.Adapter)
	if err != nil {
		return "", nil, err
	}
	command := c.Command
	if command == "" {
		command = a
	}
	args := []string{}
	if a == OpenCode {
		args = append(args, "run", "--format", "json")
		if !c.RequirePermissions {
			args = append(args, "--dangerously-skip-permissions")
		}
		if c.ResumeSessionID != "" {
			args = append(args, "--session", c.ResumeSessionID)
		}
	} else {
		if a != ClaudeP {
			args = append(args, "--print", "-")
		}
		args = append(args, "--output-format", "stream-json", "--verbose")
		if !c.RequirePermissions {
			args = append(args, "--dangerously-skip-permissions")
		}
		args = append(args, "--disallowedTools", "AskUserQuestion")
		if c.ResumeSessionID != "" {
			args = append(args, "--resume", c.ResumeSessionID)
		}
		if c.Chrome {
			args = append(args, "--chrome")
		}
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Agent != "" {
		args = append(args, "--agent", c.Agent)
	}
	if c.Effort != "" {
		flag := "--effort"
		if a == OpenCode {
			flag = "--variant"
		}
		args = append(args, flag, c.Effort)
	}
	if a != OpenCode {
		if c.MaxTurns > 0 {
			args = append(args, "--max-turns", strconv.Itoa(c.MaxTurns))
		}
		if c.AppendSystemPromptFile != "" && c.ResumeSessionID == "" {
			args = append(args, "--append-system-prompt-file", c.AppendSystemPromptFile)
		}
		for _, d := range c.AddDirs {
			args = append(args, "--add-dir", d)
		}
	}
	return command, append(args, c.ExtraArgs...), nil
}

func ToolTitle(name string, input any) string {
	fallback := name
	if fallback == "" {
		fallback = "tool"
	}
	m := obj(input)
	if m == nil {
		return fallback
	}
	var value any
	max := 80
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "edit", "multiedit", "write", "notebookedit":
		value = first(m["file_path"], m["filePath"], m["notebook_path"])
	case "bash":
		value = m["command"]
		if strings.TrimSpace(str(m["description"])) != "" {
			value = m["description"]
		}
	case "grep", "glob":
		value = m["pattern"]
	case "webfetch":
		value = m["url"]
		max = 120
	case "websearch", "toolsearch":
		value = m["query"]
		max = 120
	case "task", "agent":
		value = m["description"]
	}
	if s := str(value); strings.TrimSpace(s) != "" {
		return truncate(s, max)
	}
	return fallback
}
