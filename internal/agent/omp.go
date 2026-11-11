package agent

import (
	"encoding/json"
	"strings"
)

// OMPExitMaxCost is omp's exit status when --max-cost stopped it.
const OMPExitMaxCost = 3

// ompDriver runs `omp -p --mode json`. Cost is the usage.cost.total omp
// reports per turn (the same figure the AI layer budgets against), so the
// running total is exact after every turn.
type ompDriver struct{ cmd string }

func (d *ompDriver) Name() string     { return "omp" }
func (d *ompDriver) Binary() string   { return d.cmd }
func (d *ompDriver) NeedsPrice() bool { return false }

// omp tools by kind: a plan only reads; a run may edit, write and run
// commands (tests). Everything else (web, tasks, memory) stays off.
const (
	ompReadTools  = "read,grep,glob"
	ompWriteTools = "read,grep,glob,edit,write,bash"
)

func (d *ompDriver) Command(s Spec) Cmd {
	args := []string{
		"-p", "--mode", "json", "--no-session",
		// Context comes from ADE's prompt (issue, comments, AGENTS.md), not
		// from discovery: the same input every time, and no extension code.
		"--no-extensions", "--no-skills", "--no-rules", "--no-context-files", "--no-lsp", "--no-title",
		"--cwd", s.Dir,
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Writes() {
		args = append(args, "--tools", ompWriteTools, "--auto-approve", "--thinking", "low")
	} else {
		args = append(args, "--tools", ompReadTools, "--thinking", "low")
	}
	for _, dir := range s.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	if s.MaxCostUSD > 0 {
		args = append(args, "--max-cost", ftoa(s.MaxCostUSD))
	}
	return Cmd{Path: d.cmd, Args: args, Stdin: s.Prompt, Dir: s.Dir}
}

func (d *ompDriver) Parser(Spec) Parser { return &ompParser{} }

type ompParser struct{ p Progress }

func (o *ompParser) Progress() Progress { return o.p }

func (o *ompParser) Feed(line []byte) {
	var ev struct {
		Type     string `json:"type"`
		ToolName string `json:"toolName"`
		Message  struct {
			Role         string `json:"role"`
			Model        string `json:"model"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
			Content      []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				Input      int `json:"input"`
				Output     int `json:"output"`
				CacheRead  int `json:"cacheRead"`
				CacheWrite int `json:"cacheWrite"`
				Cost       struct {
					Total float64 `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "tool_execution_start":
		o.p.Activity = ev.ToolName
	case "agent_end":
		o.p.Done = true
	case "turn_end":
		m := ev.Message
		if m.Role != "assistant" {
			return
		}
		o.p.Turns++
		o.p.Source = "reported"
		o.p.In += m.Usage.Input + m.Usage.CacheWrite
		o.p.Out += m.Usage.Output
		o.p.Cache += m.Usage.CacheRead
		o.p.CostUSD += m.Usage.Cost.Total
		if m.Model != "" {
			o.p.Model = m.Model
		}
		var text strings.Builder
		for _, b := range m.Content {
			if b.Type == "text" {
				text.WriteString(b.Text)
			}
		}
		if t := strings.TrimSpace(text.String()); t != "" {
			o.p.Text = t
		}
		switch m.StopReason {
		case "error", "aborted":
			o.p.Failed = firstNonEmpty(m.ErrorMessage, "stop reason "+m.StopReason)
		default:
			o.p.Failed = ""
		}
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
