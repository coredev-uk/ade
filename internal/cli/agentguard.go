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
	"mr close":       "closes the merge request on the forge",
	"mr reopen":      "reopens the merge request on the forge",
	"mr describe":    "rewrites the merge request description on the forge",

	"issue weight": "writes to the forge",
	"issue move":   "writes to the forge",
	"issue child":  "creates an issue on the forge",
	"issue assign": "writes to the forge",
	"issue stage":  "writes to the forge",

	"start": "creates issues, branches and merge requests on the forge",

	"project bootstrap": "can open a merge request on the forge (--publish)",
	"cache reset":       "deletes the local cache, unsubmitted review drafts included",

	"review submit": "posts a review to the forge",
	"review add":    "adds to the user's review drafts",
	"review rm":     "removes from the user's review drafts",

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

// annAgentAllowed marks a command that was looked at and is fine for an agent
// to run: it reads, or it only touches the local machine. The test
// TestEveryCommandIsClassifiedForAgentRuns fails for a runnable command with
// neither annotation, so a new write command cannot land unguarded.
const annAgentAllowed = "ade.agent-run-allowed"

// agentAllowed lists, by path under `ade`, the commands an agent may run.
// init, setup, config, doctor, prompt and version replace the root's pre-run
// hook (they run before a config exists), so the guard does not run for them:
// none writes to a forge, and setup/init only act on the user's own machine and
// config. `ade sync` reads the forge and writes its cache and the inbox.
var agentAllowed = map[string]string{
	"adopt": "local worktrees", "agent diff": "reads", "agent list": "reads", "agent review": "opens lazygit locally", "agent show": "reads",
	"ai ci-triage": "AI call, prints", "ai digest": "AI call, prints", "ai draft-reply": "AI call, prints", "ai models": "reads",
	"ai research": "AI call, local record", "ai review": "AI call, local drafts, nothing posted", "ai standin": "AI call, prints",
	"ai summarize": "AI call, prints", "ai triage": "lists drafts",
	"audit": "reads", "audit tail": "reads", "budget": "reads", "cd": "prints", "cleanup": "local, removes only clean merged trees",
	"config check": "reads", "config schema": "local file", "dash": "terminal UI", "doctor": "reads", "dotfiles": "reads", "dotfiles status": "reads",
	"gc": "local, removes only clean merged trees", "inbox": "reads", "inbox read": "local inbox", "inbox show": "reads", "init": "user's own config",
	"item": "reads", "items": "reads", "launch": "terminal session", "mr checkout": "local worktree", "mr diff": "reads", "mr pipeline": "reads",
	"mr show": "reads", "note": "local notes", "notify send": "local notification", "notify test": "local notification",
	"project add": "local clone", "project list": "reads", "project remove": "local", "project status": "reads", "prompt": "reads",
	"reindex": "local cache", "review": "reads", "review list": "reads", "services down": "local processes", "services install": "local",
	"services open": "local", "services status": "reads", "services up": "local processes", "setup": "user's own machine and config",
	"sprint review": "reads", "sprint velocity": "reads", "standup": "reads", "sync": "reads the forge, writes the cache", "task run": "local task",
	"update": "replaces the ade binary", "version": "prints", "vscode tasks": "local file", "vscode workspace": "local file",
	"ws down": "local processes", "ws install": "local", "ws layout": "prints", "ws restart": "local processes", "ws status": "reads", "ws up": "local processes",
}

// markAgentRefused applies agentRefused and agentAllowed to the built command tree.
func markAgentRefused(root *cobra.Command) {
	for path, why := range agentRefused {
		if cmd, _, err := root.Find(strings.Fields(path)); err == nil && cmd != root && cmd.CommandPath() == "ade "+path {
			refuseInAgentRun(cmd, why)
		}
	}
	for path, why := range agentAllowed {
		if cmd, _, err := root.Find(strings.Fields(path)); err == nil && cmd != root && cmd.CommandPath() == "ade "+path {
			if cmd.Annotations == nil {
				cmd.Annotations = map[string]string{}
			}
			cmd.Annotations[annAgentAllowed] = why
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
