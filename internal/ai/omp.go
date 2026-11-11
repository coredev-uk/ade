package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

// ompExitMaxCost is omp's exit status when --max-cost stopped the run.
const ompExitMaxCost = 3

// omp drives the omp CLI in print mode. omp reports a list-price cost per
// turn (usage.cost.total); on a subscription that is not what is billed, but
// it is the figure ADE budgets against (docs/PLAN.md §9).
type omp struct {
	cmd string
}

func newOMP(p config.AIProfile) *omp {
	c := p.Command
	if c == "" {
		c = "omp"
	}
	return &omp{cmd: c}
}

func (d *omp) Name() string { return "omp" }

// ompArgs builds an explain-style invocation: no session on disk, no tools,
// no extensions or context files, and a minimal system prompt. omp's default
// prompt alone costs ~2.2k input tokens per call (docs/PLAN.md §6.5). The
// prompt itself goes on stdin, which has no length limit.
func ompArgs(c Call) []string {
	args := []string{
		"-p", "--mode", "json", "--no-session", "--no-tools",
		"--no-extensions", "--no-skills", "--no-rules", "--no-context-files", "--no-lsp", "--no-title",
		"--system-prompt", c.System,
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Thinking != "" {
		args = append(args, "--thinking", c.Thinking)
	}
	if c.MaxCostUSD > 0 {
		args = append(args, "--max-cost", strconv.FormatFloat(c.MaxCostUSD, 'f', 6, 64))
	}
	return args
}

func (d *omp) Complete(ctx context.Context, c Call) (Response, error) {
	cmd := exec.CommandContext(ctx, d.cmd, ompArgs(c)...)
	cmd.Stdin = strings.NewReader(c.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	resp, perr := parseOMP(stdout.Bytes())
	resp.Bytes = len(c.Prompt) + len(c.System)
	resp.CostSource = CostReported
	var ee *exec.ExitError
	switch {
	case errors.As(runErr, &ee) && ee.ExitCode() == ompExitMaxCost:
		return resp, &StopError{Source: "omp", Message: fmt.Sprintf("omp stopped at --max-cost $%.2f (spent $%.4f)", c.MaxCostUSD, resp.CostUSD)}
	case runErr != nil && ctx.Err() != nil:
		return resp, ctx.Err()
	case errors.Is(runErr, exec.ErrNotFound):
		return resp, &NotInstalledError{Driver: "omp", Err: runErr}
	case runErr != nil:
		if msg := tail(stderr.String(), 400); msg != "" {
			return resp, fmt.Errorf("omp: %w: %s", runErr, msg)
		}
		return resp, fmt.Errorf("omp: %w", runErr)
	case perr != nil:
		return resp, perr
	}
	return resp, nil
}

// parseOMP reads omp's JSON event stream: usage and cost are summed over
// turns, the answer is the last turn's text.
func parseOMP(out []byte) (Response, error) {
	var r Response
	var failure string
	turns := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Role         string `json:"role"`
				Model        string `json:"model"`
				Provider     string `json:"provider"`
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
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != "turn_end" || ev.Message.Role != "assistant" {
			continue
		}
		turns++
		m := ev.Message
		r.Usage.In += m.Usage.Input + m.Usage.CacheWrite
		r.Usage.Out += m.Usage.Output
		r.Usage.Cache += m.Usage.CacheRead
		r.CostUSD += m.Usage.Cost.Total
		if m.Model != "" {
			r.Model = m.Model
		}
		if m.Provider != "" {
			r.DestHost = "omp:" + m.Provider // omp talks to the provider; the audit trail says which
		}
		var text strings.Builder
		for _, b := range m.Content {
			if b.Type == "text" {
				text.WriteString(b.Text)
			}
		}
		r.Text = text.String()
		if m.StopReason == "error" || m.StopReason == "aborted" {
			failure = firstNonEmpty(m.ErrorMessage, "stop reason "+m.StopReason)
		} else {
			failure = ""
		}
	}
	switch {
	case turns == 0:
		return r, errors.New("omp: no assistant turn in the output")
	case failure != "":
		return r, fmt.Errorf("omp: %s", failure)
	}
	return r, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}

// Prices reads omp's model catalogue (`omp models --json`), keyed by both the
// bare id and the provider/id selector.
func (d *omp) Prices(ctx context.Context) (map[string]config.Price, error) {
	out, err := exec.CommandContext(ctx, d.cmd, "models", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("omp models: %w", err)
	}
	var cat struct {
		Models []struct {
			ID       string `json:"id"`
			Selector string `json:"selector"`
			Cost     *struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cacheRead"`
				CacheWrite float64 `json:"cacheWrite"`
			} `json:"cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(out, &cat); err != nil {
		return nil, fmt.Errorf("omp models: decode: %w", err)
	}
	prices := map[string]config.Price{}
	for _, m := range cat.Models {
		if m.Cost == nil {
			continue
		}
		p := config.Price{Input: m.Cost.Input, Output: m.Cost.Output, CacheRead: m.Cost.CacheRead, CacheWrite: m.Cost.CacheWrite}
		prices[m.Selector] = p
		if _, dup := prices[m.ID]; !dup {
			prices[m.ID] = p
		}
	}
	return prices, nil
}

func (d *omp) Models(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, d.cmd, "models", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("omp models: %w", err)
	}
	var cat struct {
		Models []struct {
			Selector string `json:"selector"`
		} `json:"models"`
	}
	if err := json.Unmarshal(out, &cat); err != nil {
		return nil, fmt.Errorf("omp models: decode: %w", err)
	}
	ids := make([]string, 0, len(cat.Models))
	for _, m := range cat.Models {
		ids = append(ids, m.Selector)
	}
	return ids, nil
}
