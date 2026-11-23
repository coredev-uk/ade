package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// claudeDriver runs Claude Code headless (`claude -p --output-format
// stream-json`, https://docs.claude.com/en/docs/claude-code/cli-reference).
// The CLI enforces --max-budget-usd itself; ADE also adds up the per-turn
// usage against the profile's price table (when it has one) so the running
// cost is visible and the hard stop does not depend on the CLI alone. The
// final `result` event's total_cost_usd replaces the estimate.
type claudeDriver struct{ cmd string }

func (d *claudeDriver) Name() string     { return "claude" }
func (d *claudeDriver) Binary() string   { return d.cmd }
func (d *claudeDriver) NeedsPrice() bool { return false }

const (
	claudeReadTools  = "Read,Grep,Glob"
	claudeWriteTools = "Read,Grep,Glob,Edit,Write,Bash"
)

func (d *claudeDriver) Command(s Spec) Cmd {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	// Isolation, like omp's --no-extensions: no MCP servers (--strict-mcp-config
	// with no --mcp-config loads none, and mcp__* is denied for the tools that
	// slip through), no project or local settings (a repo's hooks), no slash
	// commands or skills. The user's own settings still load: that is where
	// the CLI's credentials and proxy live.
	deny := []string{"mcp__*"}
	args = append(args, "--strict-mcp-config", "--setting-sources", "user", "--disable-slash-commands")
	if s.Writes() {
		args = append(args, "--tools", claudeWriteTools, "--allowedTools", claudeWriteTools, "--permission-mode", "acceptEdits")
	} else {
		args = append(args, "--tools", claudeReadTools, "--allowedTools", claudeReadTools, "--permission-mode", "plan")
	}
	// --add-dir grants "read and edit": the required repos are readable only,
	// so the file-editing tools are denied there (// anchors an absolute path).
	for _, dir := range s.AddDirs {
		args = append(args, "--add-dir", dir)
		deny = append(deny, "Edit(/"+filepath.ToSlash(dir)+"/**)", "Write(/"+filepath.ToSlash(dir)+"/**)")
	}
	args = append(args, "--disallowedTools")
	args = append(args, deny...)
	if s.MaxCostUSD > 0 {
		args = append(args, "--max-budget-usd", ftoa(s.MaxCostUSD))
	}
	return Cmd{Path: d.cmd, Args: args, Stdin: s.Prompt, Dir: s.Dir}
}

func (d *claudeDriver) Parser(s Spec) Parser { return &claudeParser{spec: s} }

type claudeParser struct {
	spec Spec
	p    Progress
	// msgs is the usage of each assistant message by id. Claude Code emits
	// one event per content block of a message, each carrying the message's
	// whole usage (anthropics/claude-code#6805), so adding per event would
	// count a message once per block: the last event of an id replaces the
	// earlier ones. anon numbers the events that carry no id.
	msgs map[string]claudeUsage
	anon int
	// reported is set once the result event's total_cost_usd has replaced
	// the price-table estimate.
	reported bool
}

func (c *claudeParser) Progress() Progress { return c.p }

type claudeUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

// sum is the usage of every message seen.
func (c *claudeParser) sum() (u claudeUsage) {
	for _, m := range c.msgs {
		u.Input += m.Input
		u.Output += m.Output
		u.CacheRead += m.CacheRead
		u.CacheWrite += m.CacheWrite
	}
	return u
}

// setTokens copies u into the progress totals.
func (c *claudeParser) setTokens(u claudeUsage) {
	c.p.In, c.p.Out, c.p.Cache = u.Input+u.CacheWrite, u.Output, u.CacheRead
}

func (c *claudeParser) Feed(line []byte) {
	var ev struct {
		Type     string  `json:"type"`
		Subtype  string  `json:"subtype"`
		IsError  bool    `json:"is_error"`
		Result   string  `json:"result"`
		Cost     float64 `json:"total_cost_usd"`
		NumTurns int     `json:"num_turns"`
		Message  struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Name string `json:"name"`
			} `json:"content"`
			Usage claudeUsage `json:"usage"`
		} `json:"message"`
		Usage claudeUsage `json:"usage"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "assistant":
		m := ev.Message
		if c.msgs == nil {
			c.msgs = map[string]claudeUsage{}
		}
		id := m.ID
		if id == "" {
			c.anon++
			id = fmt.Sprintf("anon-%d", c.anon)
		}
		c.msgs[id] = m.Usage
		c.p.Turns = len(c.msgs)
		if m.Model != "" {
			c.p.Model = m.Model
		}
		var text strings.Builder
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use":
				c.p.Activity = b.Name
			}
		}
		if t := strings.TrimSpace(text.String()); t != "" {
			c.p.Text = t
		}
		u := c.sum()
		c.setTokens(u)
		if c.spec.Price != nil && !c.reported {
			c.p.CostUSD, c.p.Source = priceUSD(*c.spec.Price, u.Input, u.Output, u.CacheRead, u.CacheWrite), CostComputed
		}
	case "result":
		c.p.Done = true
		if ev.Result != "" && !ev.IsError {
			c.p.Text = strings.TrimSpace(ev.Result)
		}
		if ev.NumTurns > 0 {
			c.p.Turns = ev.NumTurns
		}
		// The result's usage is the run's total: it replaces what the
		// per-message events added up to.
		if u := ev.Usage; u != (claudeUsage{}) {
			c.setTokens(u)
		}
		if ev.Cost > 0 || !c.reported {
			c.reported, c.p.CostUSD, c.p.Source = true, ev.Cost, CostReported
		}
		if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
			c.p.Failed = firstNonEmpty(strings.TrimSpace(ev.Result), "claude: "+ev.Subtype)
		}
	}
}

// Cost sources, named as the AI layer names them.
const (
	CostReported = "reported"
	CostComputed = "computed"
	CostUnknown  = "unknown"
)

// codexDriver runs `codex exec --json` (https://developers.openai.com/codex/cli/reference).
// Codex reports tokens, not cost, so the run needs a price table entry for
// the model: without one the cap could not be enforced and the run is refused.
// Even with one, the cost only arrives when a turn completes, and one turn
// covers everything up to the final answer: a codex run that edits files
// cannot be stopped at the cap (see Manager.checkUncapped).
type codexDriver struct{ cmd string }

func (d *codexDriver) Name() string     { return "codex" }
func (d *codexDriver) Binary() string   { return d.cmd }
func (d *codexDriver) NeedsPrice() bool { return true }

func (d *codexDriver) Command(s Spec) Cmd {
	// --ephemeral keeps the session's rollout files off the disk. Codex has
	// no flag that drops the user's MCP servers or hooks. --add-dir is never
	// passed: it grants write access, and codex already reads outside the
	// workspace, so the required repos stay readable and unwritable.
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-C", s.Dir}
	if s.Model != "" {
		args = append(args, "-m", s.Model)
	}
	if s.Writes() {
		args = append(args, "-s", "workspace-write")
	} else {
		args = append(args, "-s", "read-only")
	}
	// "-" reads the prompt from stdin.
	args = append(args, "-")
	return Cmd{Path: d.cmd, Args: args, Stdin: s.Prompt, Dir: s.Dir}
}

func (d *codexDriver) Parser(s Spec) Parser { return &codexParser{spec: s} }

type codexParser struct {
	spec Spec
	p    Progress
}

func (c *codexParser) Progress() Progress { return c.p }

func (c *codexParser) Feed(line []byte) {
	var ev struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Item    struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Command string `json:"command"`
		} `json:"item"`
		Usage struct {
			Input     int `json:"input_tokens"`
			Cached    int `json:"cached_input_tokens"`
			Output    int `json:"output_tokens"`
			Reasoning int `json:"reasoning_output_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "item.started", "item.completed":
		switch ev.Item.Type {
		case "agent_message":
			if ev.Type == "item.completed" {
				if t := strings.TrimSpace(ev.Item.Text); t != "" {
					c.p.Text = t
				}
			}
		case "command_execution":
			c.p.Activity = "bash"
		case "file_change":
			c.p.Activity = "edit"
		}
	case "turn.completed":
		c.p.Turns++
		// Codex counts cached tokens inside input_tokens.
		fresh := max(ev.Usage.Input-ev.Usage.Cached, 0)
		c.p.In += fresh
		c.p.Cache += ev.Usage.Cached
		c.p.Out += ev.Usage.Output
		if c.spec.Price != nil {
			c.p.CostUSD += priceUSD(*c.spec.Price, fresh, ev.Usage.Output, ev.Usage.Cached, 0)
			c.p.Source = CostComputed
		} else {
			c.p.Source = CostUnknown
		}
		c.p.Failed, c.p.Done = "", true
	case "turn.failed":
		c.p.Failed = firstNonEmpty(ev.Error.Message, "codex: turn failed")
		c.p.Done = true
	case "error":
		c.p.Failed = firstNonEmpty(ev.Message, "codex: error")
	}
}
