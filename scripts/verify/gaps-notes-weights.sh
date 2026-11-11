#!/usr/bin/env bash
# Gap round, notes and weights (docs/PLAN.md 3 "Weights", 4 "Records", 6.1, 6.6, 6.9):
#   ade item <ref> [--edit], notes in agent and AI prompts (--no-notes),
#   ade issue weight <ref> <n|none> (GitLab field on Premium, labels on Free,
#   child follows, audited as forge.issue.weight), weight suggestion in
#   `ade ai classify`, the TUI dialogs (n and =).
#
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT for the
# live part; everything before it runs against the fake GitLab (Free + Premium).
# The live part writes only to the sandbox: tracker#15 goes to weight 2 and back to 1
# (Free label path), and its child's labels are put back as they were. AI is never
# called: only `--dry-run` previews, which cost nothing.
. scripts/verify/lib.sh

step "unit tests: planning, writes, records, prompts, cache key, suggestion, dialogs"
check "weight: label swap removes the old label, Premium writes the field, child follows, audit" go test ./internal/weight/...
check "gitlab: issueSetWeight validates against the schema; Premium sets and clears, Free is unavailable" \
  go test ./internal/forge/gitlab/ -run 'TestSetWeight|TestQueriesMatchSchema'
check "records: notes are replaced in place and survive a resync" go test ./internal/records/...
check "ai: notes are in the prompt and the cache key; weight suggestion is the lower median" go test ./internal/ai/...
check "agent: notes feed the plan, run and address prompts" go test ./internal/agent/... -run 'TestNotes'
check "cli: ade item, ade issue weight, --no-notes, classify --json" \
  go test ./internal/cli/... -run 'TestItem|TestIssueWeight|TestNotesFeed|TestClassifyJSON|TestAIClassify'
check "tui: notes and weight dialogs, classify hand-over, agent notes toggle" \
  go test ./internal/tui/... -run 'TestNotes|TestWeight|TestAgentMenuOffersNotes|TestClassify'

build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
trap 'kill ${FAKE_PID:-} 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT

# A hermetic AI profile: preview only ever prices the prompt, it never runs the command.
cat >"$VERIFY_TMP/omp-stub" <<'EOF'
#!/bin/sh
echo "the verify script never calls the model" >&2
exit 1
EOF
chmod +x "$VERIFY_TMP/omp-stub"
ai_profile() {
  cat <<EOF
[host]
editor = "\$EDITOR"
[ai]
mode = "auto"
profile = "sub"
[ai.profiles.sub]
driver = "omp"
command = "$VERIFY_TMP/omp-stub"
models = { explain = "claude-haiku-4-5", draft = "claude-haiku-4-5", review = "claude-haiku-4-5" }
[ai.profiles.sub.prices.claude-haiku-4-5]
input = 1.0
output = 5.0
[budget]
call_usd = 0.5
EOF
}

# An editor that appends a marker line to the file it is given.
cat >"$VERIFY_TMP/editor.sh" <<'EOF'
#!/bin/sh
printf '%s\n' "$VERIFY_NOTE" >>"$1"
EOF
chmod +x "$VERIFY_TMP/editor.sh"

step "fake GitLab Premium through the real binary: the weight field, and the child follows"
"$VERIFY_TMP/fakegl" -tier premium >"$VERIFY_TMP/fake.url" 2>"$VERIFY_TMP/fake.err" &
FAKE_PID=$!
for _ in $(seq 50); do
  [[ -s $VERIFY_TMP/fake.url ]] && break
  sleep 0.1
done
FAKE_URL=$(head -1 "$VERIFY_TMP/fake.url")
[[ -n $FAKE_URL ]] || fail "the fake GitLab did not start: $(cat "$VERIFY_TMP/fake.err")"
G=ade-demo-env
sed 's/^weight .*/weight     = { source = "auto" }/' scripts/sandbox/manifests/gitlab.ade.toml >"$VERIFY_TMP/auto.ade.toml"
export XDG_DATA_HOME="$VERIFY_TMP/fdata" XDG_CACHE_HOME="$VERIFY_TMP/fcache" ADE_CONFIG="$VERIFY_TMP/fake.toml" FAKE_GITLAB_TOKEN=fake-token
cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$FAKE_URL"
token_env = "FAKE_GITLAB_TOKEN"
[update]
on_start = false
[paths]
root = "$VERIFY_TMP/projects"
[[projects]]
source = "$VERIFY_TMP/auto.ade.toml"
EOF
fapi() { curl -fsS -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" "$FAKE_URL/api/v4/$1"; }
weight_of() { fapi "projects/$G%2F$1/issues/$2" | jq -r '.weight'; } # PROJECT IID

[[ $(weight_of tracker 1) == 3 && $(weight_of api 1) == 3 ]] || fail "fake seed: tracker#1 and its child api#1 should both weigh 3"
"$ADE" issue weight "$G/tracker#1" 5 --dry-run --json >"$VERIFY_TMP/p1.json"
expect_json "dry run plans the field write for the tracker and its child" "$VERIFY_TMP/p1.json" \
  '.dry_run == true and (.results | length) == 2 and all(.results[]; .plan.mode == "field" and .applied == false) and .results[1].plan.child == true and .results[0].plan.from == 3 and .results[0].plan.to == 5'
[[ $(weight_of tracker 1) == 3 ]] && pass "the dry run changed nothing" || fail "the dry run wrote"
"$ADE" issue weight "$G/tracker#1" 5 --json >"$VERIFY_TMP/p2.json"
[[ $(weight_of tracker 1) == 5 && $(weight_of api 1) == 5 ]] && pass "tracker#1 and the child api#1 carry weight 5 (GitLab weight field)" || fail "field not written"
"$ADE" issue weight "$G/tracker#1" none --no-child >/dev/null
[[ $(weight_of tracker 1) == null && $(weight_of api 1) == 5 ]] && pass "none clears the field; --no-child leaves the child alone" || fail "clear"
"$ADE" audit --action 'forge.issue.weight' --json >"$VERIFY_TMP/a.json"
expect_json "three audited forge.issue.weight writes (tracker, child, clear)" "$VERIFY_TMP/a.json" \
  'length == 3 and .[0].params.mode == "field" and .[0].params.from == "3" and .[0].params.to == "5" and .[1].params.child == true and .[2].params.to == "none"'
if "$ADE" issue weight "$G/tracker#1" 0 2>"$VERIFY_TMP/e0"; then fail "weight 0 accepted"; fi
grep -q "1 or more" "$VERIFY_TMP/e0" && pass "0 is refused (it would read as unweighted)" || fail "weight 0 error: $(cat "$VERIFY_TMP/e0")"

kill "$FAKE_PID" 2>/dev/null || true
FAKE_PID=
"$VERIFY_TMP/fakegl" -tier free >"$VERIFY_TMP/fake2.url" 2>/dev/null &
FAKE_PID=$!
for _ in $(seq 50); do
  [[ -s $VERIFY_TMP/fake2.url ]] && break
  sleep 0.1
done
FAKE_URL=$(head -1 "$VERIFY_TMP/fake2.url")
sed -i "s#^url = .*#url = \"$FAKE_URL\"#; s#$VERIFY_TMP/auto.ade.toml#$REPO/scripts/sandbox/manifests/gitlab.ade.toml#" "$ADE_CONFIG"
step "fake GitLab Free: weight rides on labels, the old label is removed explicitly"
curl -fsS -X PUT -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" "$FAKE_URL/api/v4/projects/$G%2Ftracker/issues/1" --data-urlencode "add_labels=weight::5" >/dev/null
"$ADE" issue weight "$G/tracker#1" 2 --no-child >/dev/null
labels=$(fapi "projects/$G%2Ftracker/issues/1" | jq -r '.labels | join(",")')
[[ $labels == *weight::2* && $labels != *weight::5* ]] && pass "weight::5 became weight::2 (labels: $labels)" || fail "label swap: $labels"

step "live sandbox (Free tier): tracker#15 to weight 2 and back to 1; notes feed the AI input"
sandbox_gitlab
export VISUAL="$VERIFY_TMP/editor.sh" VERIFY_NOTE="verify-note-$$ the export limit is per tenant, not per token"
ai_profile >>"$ADE_CONFIG"
T=$G/tracker#15
lapi() { curl -fsS -H "PRIVATE-TOKEN: $GITLAB_PAT" "https://gitlab.com/api/v4/$1"; }
tracker_labels() { lapi "projects/$G%2Ftracker/issues/15" | jq -r '.labels | sort | join(",")'; }

"$ADE" sync >/dev/null || fail "sync"
START=$(tracker_labels)
[[ $START == *weight::1* ]] || fail "tracker#15 is expected to start at weight::1 (labels: $START)"
"$ADE" issue weight "$T" 2 --dry-run --json >"$VERIFY_TMP/l1.json"
expect_json "dry run on the Free sandbox plans a label swap (weight::1 out, weight::2 in)" "$VERIFY_TMP/l1.json" \
  '.results[0].plan.mode == "label" and .results[0].plan.add == ["weight::2"] and .results[0].plan.remove == ["weight::1"] and .results[0].plan.from == 1'
CHILD=$(jq -r '.results[1].plan.ref // empty' "$VERIFY_TMP/l1.json")
CHILD_PROJECT=${CHILD%#*}
CHILD_IID=${CHILD#*#}
CHILD_ENC=${CHILD_PROJECT//\//%2F}
child_labels_json() { lapi "projects/$CHILD_ENC/issues/$CHILD_IID" | jq -c '.labels'; }
if [[ -n $CHILD ]]; then
  CHILD_START=$(child_labels_json)
  pass "start work's child $CHILD follows (labels now $CHILD_START)"
else
  fail "tracker#15's child was not found: it should follow the weight"
fi

"$ADE" issue weight "$T" 2 --json >"$VERIFY_TMP/l2.json"
NOW=$(tracker_labels)
[[ $NOW == *weight::2* && $NOW != *weight::1* ]] && pass "tracker#15 is weight::2 and weight::1 is gone ($NOW)" || fail "tracker#15 labels: $NOW"
[[ $(child_labels_json) == *weight::2* ]] && pass "the child $CHILD got weight::2 too" || fail "child labels: $(child_labels_json)"
"$ADE" issue weight "$T" 1 --json >"$VERIFY_TMP/l3.json"
BACK=$(tracker_labels)
[[ $BACK == "$START" ]] && pass "tracker#15 is back to its starting labels ($BACK)" || fail "tracker#15 labels after restoring: $BACK (started $START)"
# The child had no weight label to begin with: put its labels back exactly.
if [[ $(child_labels_json) != "$CHILD_START" ]]; then
  curl -fsS -X PUT -H "PRIVATE-TOKEN: $GITLAB_PAT" "https://gitlab.com/api/v4/projects/$CHILD_ENC/issues/$CHILD_IID" \
    --data-urlencode "labels=$(jq -r 'join(",")' <<<"$CHILD_START")" >/dev/null
  [[ $(child_labels_json) == "$CHILD_START" ]] && pass "the child's labels are restored to how they were" || fail "child restore"
fi
"$ADE" audit --action 'forge.issue.weight' --json >"$VERIFY_TMP/la.json"
expect_json "the live writes are audited: 4 entries (tracker+child to 2, tracker+child to 1), all ok" "$VERIFY_TMP/la.json" \
  'length == 4 and all(.[]; .outcome == "ok") and .[0].params.to == "2" and .[2].params.to == "1"'
"$ADE" issue weight "$T" 1 >"$VERIFY_TMP/l4.out" # already 1 on the tracker; the child is written to match
"$ADE" issue weight "$T" 1 >"$VERIFY_TMP/l5.out"
grep -q "already 1" "$VERIFY_TMP/l5.out" && pass "repeating a weight is a no-op that says so" || fail "repeat: $(cat "$VERIFY_TMP/l5.out")"
if [[ $(child_labels_json) != "$CHILD_START" ]]; then
  curl -fsS -X PUT -H "PRIVATE-TOKEN: $GITLAB_PAT" "https://gitlab.com/api/v4/projects/$CHILD_ENC/issues/$CHILD_IID" \
    --data-urlencode "labels=$(jq -r 'join(",")' <<<"$CHILD_START")" >/dev/null
fi

step "notes: ade item shows them, --edit writes them, the AI input preview includes them"
"$ADE" item "$T" --json >"$VERIFY_TMP/i1.json"
expect_json "ade item shows the snapshot and no notes yet" "$VERIFY_TMP/i1.json" \
  '.ref == "'"$T"'" and (.snapshot | startswith("# Include row counts")) and .notes == ""'
"$ADE" item "$T" --edit >"$VERIFY_TMP/i2.out"
grep -q "$VERIFY_NOTE" "$VERIFY_TMP/i2.out" && pass "--edit opened the configured editor on the record and shows the notes afterwards" || fail "edit: $(cat "$VERIFY_TMP/i2.out")"
"$ADE" sync >/dev/null
"$ADE" item "$T" --json | jq -e --arg n "$VERIFY_NOTE" '.notes | contains($n)' >/dev/null && pass "the notes survive a resync (ADE rewrites only the snapshot)" || fail "notes lost by sync"

MR=$("$ADE" items --json | jq -r --arg r "$T" '.[] | select(.ref == $r) | .mrs[0].ref // empty')
[[ -n $MR ]] || fail "tracker#15 has no MR in the sandbox"
"$ADE" ai summarize "$MR" --dry-run --json >"$VERIFY_TMP/n1.json"
"$ADE" ai summarize "$MR" --dry-run --json --no-notes >"$VERIFY_TMP/n2.json"
expect_json "the summarize input preview says it includes your notes" "$VERIFY_TMP/n1.json" \
  '.dry_run == true and .includes_notes == true and .preflight.includes_notes == true'
expect_json "--no-notes leaves them out" "$VERIFY_TMP/n2.json" '(.includes_notes // false) == false and (.preflight.includes_notes // false) == false'
[[ $(jq .preflight.in_tokens "$VERIFY_TMP/n1.json") -gt $(jq .preflight.in_tokens "$VERIFY_TMP/n2.json") ]] && pass "the notes are in the prompt: it is bigger with them" || fail "token estimates do not differ"
"$ADE" ai summarize "$MR" --dry-run | grep -q "includes your notes" && pass "text preview: 'includes your notes'" || fail "text preview"
[[ $("$ADE" audit --action 'ai.*' --json | jq length) == 0 ]] && pass "previews are free and leave no ai audit entry" || fail "a preview was audited as a call"

step "classify suggests a weight from similar closed issues (deterministic, no model)"
# tracker#11 has no labels; keywords say bug, and the closed bug tracker#9 (weight 1, another
# sprint, so read from the forge's history) shares the word "export".
"$ADE" ai classify "$G/tracker#11" --no-ai --json >"$VERIFY_TMP/c1.json"
expect_json "classify --json carries suggestion.weight 1 with the closed issue it came from" "$VERIFY_TMP/c1.json" \
  '.suggestion.weight == 1 and (.suggestion.weight_reason | contains("median of 1") and contains("tracker#9")) and .suggestion.source == "keywords" and .cost_usd == 0'
"$ADE" ai classify "$G/tracker#11" --no-ai | grep -q "ade issue weight $G/tracker#11 1" && pass "the text output says how to set it" || fail "classify text"
[[ $("$ADE" audit --action 'ai.*' --json | jq length) == 0 ]] && pass "no AI call was made or audited" || fail "classify called the model"

echo
echo "gaps-notes-weights: all checks passed"
