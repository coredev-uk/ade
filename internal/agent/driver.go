package agent

import (
	"fmt"
	"os/exec"
	"slices"
	"strconv"

	"github.com/coredev-uk/ade/internal/config"
)

// Spec is one agent invocation.
type Spec struct {
	Kind  Kind // plan | run | address: plan is read-only, the others may write
	Model string
	// Dir is the worktree the agent works in; AddDirs are readable extras
	// (required repos' trees), never written.
	Dir     string
	AddDirs []string
	Prompt  string
	// MaxCostUSD is what this call may still spend; 0 = no cap known.
	MaxCostUSD float64
	// Price is the model's list price, for drivers whose CLI reports tokens
	// but no cost.
	Price *config.Price
}

// Writes reports whether the agent may change files.
func (s Spec) Writes() bool { return s.Kind != KindPlan }

// Cmd is the process to start. The prompt goes on stdin: no length limit and
// never visible in the process list.
type Cmd struct {
	Path  string
	Args  []string
	Stdin string
	Dir   string
	Env   []string // appended to the environment
	Unset []string // variables removed from the inherited environment
}

// Progress is what a driver's output stream says so far.
type Progress struct {
	CostUSD float64
	Source  string // reported | computed | unknown
	In, Out int
	Cache   int
	Turns   int
	Model   string
	// Text is the last assistant message: the plan, or the run's summary.
	Text string
	// Activity is the latest tool or step, for the run list.
	Activity string
	// Failed is the error the stream ended with, if any.
	Failed string
	Done   bool
}

// Parser reads a driver's stdout line by line.
type Parser interface {
	Feed(line []byte)
	Progress() Progress
}

// Driver builds and reads one agent CLI.
type Driver interface {
	// Name is the config name: omp, claude or codex.
	Name() string
	// Binary is the executable looked up on PATH (or the configured path).
	Binary() string
	// Command builds the process for s. It is pure: it starts nothing.
	Command(s Spec) Cmd
	// Parser reads the process's stdout.
	Parser(s Spec) Parser
	// NeedsPrice reports that the CLI reports no cost, so the cap can only
	// be enforced from a price table.
	NeedsPrice() bool
}

// Available says whether d's binary is installed.
func Available(d Driver) error {
	if _, err := exec.LookPath(d.Binary()); err != nil {
		return fmt.Errorf("%s: %q not found on PATH", d.Name(), d.Binary())
	}
	return nil
}

// ForProfile builds the agent driver a profile asks for; override (a
// --driver flag) wins over the profile's own setting.
func ForProfile(p config.AIProfile, override string) (Driver, error) {
	name := override
	if name == "" {
		name = p.Agent
	}
	if name == "" {
		name = "omp"
	}
	cmd := p.AgentCommand
	if cmd == "" && name == "omp" && p.Driver == "omp" {
		cmd = p.Command
	}
	switch name {
	case "omp":
		return &ompDriver{cmd: orDefault(cmd, "omp")}, nil
	case "claude":
		return &claudeDriver{cmd: orDefault(cmd, "claude")}, nil
	case "codex":
		return &codexDriver{cmd: orDefault(cmd, "codex")}, nil
	}
	return nil, fmt.Errorf("unknown agent driver %q: want omp, claude or codex", name)
}

// DriverNames lists the drivers in the order doctor shows them.
func DriverNames() []string { return slices.Clone(config.AgentDrivers) }

// Binaries returns the default executable of each driver, for doctor.
func Binaries() map[string]string {
	return map[string]string{"omp": "omp", "claude": "claude", "codex": "codex"}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// priceUSD is tokens × a USD-per-million price table.
func priceUSD(p config.Price, in, out, cacheRead, cacheWrite int) float64 {
	return (float64(in)*p.Input + float64(out)*p.Output + float64(cacheRead)*p.CacheRead + float64(cacheWrite)*p.CacheWrite) / 1e6
}
