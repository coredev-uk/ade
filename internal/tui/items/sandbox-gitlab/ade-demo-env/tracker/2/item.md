---
ref: ade-demo-env/tracker#2
forge: sandbox-gitlab
project: demo-gl
children: [ade-demo-env/web#1]
mrs: [ade-demo-env/web!1]
stage: draft
court: you
iteration: Sprint 42
synced: 2026-11-12T12:59:10Z
---

<!-- ade:snapshot:begin (ADE rewrites everything between the markers; write below) -->
# Paginate the audit log view

Labels: type::bug, app::web, workflow::in progress
Assignees: Sandbox Dev (sandbox-dev)

The audit log page renders every row and freezes for large tenants.

## Acceptance
- [ ] Cursor pagination in the UI
- [ ] Keep current filters when paging

<!-- ade:snapshot:end -->

## Notes

