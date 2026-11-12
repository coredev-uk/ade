---
ref: ade-demo-env/tracker#5
forge: sandbox-gitlab
project: demo-gl
children: []
mrs: []
stage: assigned
court: blocked
iteration: Sprint 42
synced: 2026-11-12T01:43:33Z
---

<!-- ade:snapshot:begin (ADE rewrites everything between the markers; write below) -->
# Export timestamps ignore tenant timezone

Labels: type::bug, app::api, workflow::blocked
Assignees: @sandbox-dev

Exports always use UTC. Tenants with a configured timezone expect local time with offset.

<!-- ade:snapshot:end -->

## Notes

