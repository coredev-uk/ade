package fake

import "time"

// The repo activity feed (GET /repos/{r}/events) is append-only: seeding and
// the write handlers record events as things happen.

func itemPayload(it *item, action string) map[string]any {
	if it.PR != nil {
		return map[string]any{
			"action": action, "number": it.Number,
			"pull_request": map[string]any{"number": it.Number, "title": it.Title, "merged": it.PR.Merged},
		}
	}
	return map[string]any{"action": action, "issue": map[string]any{"number": it.Number, "title": it.Title}}
}

// feedItem records an opened/closed event for an issue or PR.
func (st *state) feedItem(rp *repo, it *item, action, actor string, at time.Time) {
	typ := "IssuesEvent"
	if it.PR != nil {
		typ = "PullRequestEvent"
	}
	st.addFeed(rp, typ, actor, at, itemPayload(it, action))
}

// feedComment records a conversation comment on an issue or PR.
func (st *state) feedComment(rp *repo, it *item, c *comment) {
	st.addFeed(rp, "IssueCommentEvent", c.User, c.Created, map[string]any{
		"action":  "created",
		"issue":   map[string]any{"number": it.Number, "title": it.Title},
		"comment": map[string]any{"id": c.ID, "body": c.Body},
	})
}

// feedPush records a push of one commit to a branch.
func (st *state) feedPush(rp *repo, branch string, c *commit, actor string) {
	st.addFeed(rp, "PushEvent", actor, c.Created, map[string]any{
		"ref":  "refs/heads/" + branch,
		"head": c.SHA,
		"commits": []any{map[string]any{
			"sha": c.SHA, "message": c.Message, "author": map[string]any{"name": actor},
		}},
	})
}
