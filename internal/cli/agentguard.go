package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/workspace"
)

// annAgentRefused is the cobra annotation of a command an agent process must
// not run: its value says why. ADE sets ADE_AGENT_RUN=1 in the environment of
// every agent it starts, and the root refuses annotated commands while it is
// set, so an agent that was told (by an issue, a review thread) to run
// `ade review submit` or `ade agent approve` cannot act as the user. It is
// hygiene, not a sandbox: the agent could clear the variable, as it could read
// a token_cmd's token. A command that writes to a forge, approves, accepts a
// triage or starts an agent MUST carry it (refuseInAgentRun, or a line in
// agentRefused).
const annAgentRefused = "ade.agent-run-refused"

// refuseInAgentRun marks cmd as refused while ADE_AGENT_RUN=1; why finishes
// the sentence "… is refused in an agent run: it …".
func refuseInAgentRun(cmd *cobra.Command, why string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annAgentRefused] = why
}

// agentRefused lists the commands, by path under `ade`, that write to a forge,
// approve something, accept a triage or start an agent, with the reason.
var agentRefused = map[string]string{
	"mr reply":       "writes to the forge",
	"mr resolve":     "writes to the forge",
	"mr approve":     "approves on the forge",
	"mr unapprove":   "writes to the forge",
	"mr ready":       "writes to the forge",
	"mr draft":       "writes to the forge",
	"mr merge":       "merges on the forge",
	"mr retry":       "writes to the forge",
	"mr create":      "creates a merge request on the forge",
	"mr from-branch": "creates a merge request on the forge",

	"issue weight": "writes to the forge",
	"issue move":   "writes to the forge",
	"issue child":  "creates an issue on the forge",
	"issue assign": "writes to the forge",

	"start": "creates issues, branches and merge requests on the forge",

	"review submit": "posts a review to the forge",

	"ai triage accept": "decides which drafts are sent: that is the user's triage",
	"ai triage deny":   "decides which drafts are sent: that is the user's triage",
	"ai triage edit":   "decides which drafts are sent: that is the user's triage",
	"ai describe":      "can write the MR description to the forge",
	"ai classify":      "can write labels to the forge",

	"agent plan":    "starts an agent",
	"agent address": "starts an agent",
	"agent approve": "approves a plan: that is the user's yes",
	"agent run":     "starts an agent",
	"agent accept":  "takes an agent's work into the branch: that is the user's yes",
	"agent discard": "throws an agent's work away: that is the user's call",
	"agent push":    "pushes to the forge",
	"agent cancel":  "stops a run: that is the user's call",
}

// markAgentRefused applies agentRefused to the built command tree.
func markAgentRefused(root *cobra.Command) {
	for path, why := range agentRefused {
		if cmd, _, err := root.Find(strings.Fields(path)); err == nil && cmd != root && cmd.CommandPath() == "ade "+path {
			refuseInAgentRun(cmd, why)
		}
	}
}

// checkAgentRun refuses cmd (or a parent of it) when it is annotated and an
// agent run's environment is set.
func checkAgentRun(cmd *cobra.Command) error {
	if os.Getenv(workspace.AgentRunEnv) != "1" {
		return nil
	}
	for c := cmd; c != nil; c = c.Parent() {
		if why, ok := c.Annotations[annAgentRefused]; ok {
			return fmt.Errorf("%q %s, so it is refused while an agent run is in progress (%s=1): the person you work for runs it", c.CommandPath(), why, workspace.AgentRunEnv)
		}
	}
	return nil
}
