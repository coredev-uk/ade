---
ref: ade-demo-env/tracker#3
forge: sandbox-gitlab
project: demo-gl
children: []
mrs: []
stage: assigned
court: you
iteration: Sprint 42
synced: 2026-11-12T01:43:33Z
---

<!-- ade:snapshot:begin (ADE rewrites everything between the markers; write below) -->
# Retry failed webhook deliveries

Labels: type::story, app::api
Assignees: @sandbox-dev

Webhook deliveries that fail are dropped.

## Acceptance
- [ ] Exponential backoff, max 6 attempts
- [ ] Dead-letter list visible to admins

<!-- ade:snapshot:end -->

## Notes

