---
ref: ade-demo-env/tracker#3
forge: sandbox-gitlab
project: demo-gl
children: []
mrs: []
stage: assigned
court: you
iteration: Sprint 42
synced: 2026-11-12T12:59:10Z
---

<!-- ade:snapshot:begin (ADE rewrites everything between the markers; write below) -->
# Retry failed webhook deliveries

Labels: type::story, app::api
Assignees: Sandbox Dev (sandbox-dev)

Webhook deliveries that fail are dropped.

## Acceptance
- [ ] Exponential backoff, max 6 attempts
- [ ] Dead-letter list visible to admins

<!-- ade:snapshot:end -->

## Notes

