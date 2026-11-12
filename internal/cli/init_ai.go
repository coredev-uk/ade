package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/onboard"
)

// initAIRoles are the roles `ade init` asks a model for, in order. plan and
// implement run through the agent CLI (omp by default) and are set by hand.
var initAIRoles = []string{"explain", "classify", "draft", "review"}

// aiFlags are the gateway answers of `ade init`.
type aiFlags struct {
	gateway string
	keyEnv  string
	keyCmd  string
	profile string
	models  map[string]*string // role -> --ai-model-<role>
}

func bindAIFlags(cmd *cobra.Command, f *aiFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.gateway, "ai-gateway", "", "route AI through this LiteLLM (OpenAI-compatible) gateway, e.g. https://litellm.example.internal/v1")
	fl.StringVar(&f.keyEnv, "ai-key-env", "", "environment variable holding the gateway key")
	fl.StringVar(&f.keyCmd, "ai-key-cmd", "", "command that prints the gateway key")
	fl.StringVar(&f.profile, "ai-profile", "", "name of the gateway profile (default "+onboard.DefaultGatewayProfile+")")
	f.models = map[string]*string{}
	for _, role := range config.AIRoles {
		v := new(string)
		f.models[role] = v
		fl.StringVar(v, "ai-model-"+role, "", "gateway model alias for the "+role+" role")
	}
}

func (f aiFlags) any() bool {
	if f.gateway != "" || f.keyEnv != "" || f.keyCmd != "" || f.profile != "" {
		return true
	}
	for _, v := range f.models {
		if *v != "" {
			return true
		}
	}
	return false
}

// gatherAI resolves the optional gateway profile: flags first, then (with pr
// set) prompts that list the gateway's own models, then an error naming the
// missing flag. nil, nil means no AI setup was asked for.
func gatherAI(ctx context.Context, pr *prompter, f aiFlags, say io.Writer) (*onboard.AIGateway, error) {
	if f.gateway == "" {
		if f.any() {
			return nil, errors.New("--ai-key-env, --ai-key-cmd, --ai-profile and --ai-model-* configure a gateway: add --ai-gateway <url>")
		}
		if pr == nil {
			return nil, nil
		}
		fmt.Fprintln(say)
		use, err := pr.choose("Route AI through a LiteLLM gateway?", []string{"y", "n"}, "n")
		if err != nil || use == "n" {
			return nil, err
		}
		if f.gateway, err = pr.ask("Gateway URL (https://litellm.example.internal/v1)", ""); err != nil {
			return nil, err
		}
	}
	base, err := onboard.NormalizeGatewayURL(f.gateway)
	if err != nil {
		return nil, err
	}
	g := &onboard.AIGateway{Profile: f.profile, BaseURL: base, KeyEnv: f.keyEnv, KeyCmd: f.keyCmd, Models: map[string]string{}}
	if g.KeyEnv == "" && g.KeyCmd == "" {
		if pr == nil {
			return nil, errors.New("no AI key source: pass --ai-key-env (a variable name) or --ai-key-cmd")
		}
		src, err := pr.ask("Gateway key source: an environment variable name, or !command (the key itself is never stored)", "ADE_AI_KEY")
		if err != nil {
			return nil, err
		}
		if g.KeyEnv, g.KeyCmd, err = onboard.ParseTokenSource(src); err != nil {
			return nil, err
		}
	}
	for role, v := range f.models {
		if *v != "" {
			g.Models[role] = strings.TrimSpace(*v)
		}
	}

	// The key and the gateway are checked now, so a typo in either is found
	// before the config is written; the model list feeds the prompts.
	offered, perr := offeredModels(ctx, g)
	switch {
	case perr == nil:
		fmt.Fprintf(say, "gateway ok: %s offers %s\n", g.BaseURL, plural(len(offered), "model"))
	case g.Models["explain"] == "" && pr == nil:
		return nil, fmt.Errorf("gateway %s: %w\npass --ai-model-explain (and optionally --ai-model-classify, --ai-model-draft, --ai-model-review)", g.BaseURL, perr)
	default:
		fmt.Fprintf(say, "warning: could not list the gateway's models (%v); the models are not verified\n", perr)
	}
	if g.Models["explain"] == "" && pr == nil {
		return nil, fmt.Errorf("no explain model: pass --ai-model-explain; the gateway offers: %s", strings.Join(offered, ", "))
	}
	if pr != nil {
		if err := askModels(pr, g, offered, say); err != nil {
			return nil, err
		}
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	for _, role := range config.AIRoles {
		if m := g.Models[role]; m != "" && perr == nil && !slices.Contains(offered, m) {
			fmt.Fprintf(say, "warning: the gateway does not list %q (%s role); check `ade ai models` once set up\n", m, role)
		}
	}
	return g, nil
}

// offeredModels asks the gateway what the key may use.
func offeredModels(ctx context.Context, g *onboard.AIGateway) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	d, err := ai.DriverFor(ctx, g.AIProfile(), nil)
	if err != nil {
		return nil, err
	}
	ms, err := d.Models(ctx)
	if err != nil {
		return nil, err
	}
	slices.Sort(ms)
	return ms, nil
}

// askModels fills the roles the flags left open. explain is required; the
// others default to "use explain's model", with a pointer at small models for
// classify.
func askModels(pr *prompter, g *onboard.AIGateway, offered []string, say io.Writer) error {
	if len(offered) > 0 {
		fmt.Fprintln(say, "\nmodels the gateway offers:")
		for i, m := range offered {
			fmt.Fprintf(say, "  %2d  %s\n", i+1, m)
		}
	}
	hints := map[string]string{
		"explain":  "summaries, thread summaries, standup",
		"classify": "short label suggestions: a small, cheap model is plenty; blank uses explain's",
		"draft":    "replies and MR text; blank uses explain's",
		"review":   "agent review passes; blank uses draft's",
	}
	for _, role := range initAIRoles {
		if g.Models[role] != "" {
			continue
		}
		def := ""
		if role == "explain" && len(offered) > 0 {
			def = offered[0]
		}
		for {
			ans, err := pr.ask(fmt.Sprintf("Model for %s (%s)", role, hints[role]), def)
			if err != nil {
				return err
			}
			if n, err := strconv.Atoi(ans); err == nil {
				if n < 1 || n > len(offered) {
					fmt.Fprintf(say, "  pick 1-%d or type a model id\n", len(offered))
					continue
				}
				ans = offered[n-1]
			}
			if ans == "" && role == "explain" {
				fmt.Fprintln(say, "  explain is required: every role falls back to it")
				continue
			}
			if ans != "" {
				g.Models[role] = ans
			}
			break
		}
	}
	return nil
}

// initAIEnvHint tells the user to export the key before the first AI call.
func initAIEnvHint(w io.Writer, g *onboard.AIGateway) {
	if g.KeyEnv != "" && os.Getenv(g.KeyEnv) == "" {
		fmt.Fprintf(w, "  export %s=<your gateway key>   before the first AI call\n", g.KeyEnv)
	}
}

// initAIResult is the gateway profile `ade init` wrote, for --json.
type initAIResult struct {
	Profile string            `json:"profile"`
	BaseURL string            `json:"base_url"`
	Models  map[string]string `json:"models"`
}
