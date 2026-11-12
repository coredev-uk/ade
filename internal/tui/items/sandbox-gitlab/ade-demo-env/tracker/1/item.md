---
ref: ade-demo-env/tracker#1
forge: sandbox-gitlab
project: demo-gl
children: [ade-demo-env/api#1]
mrs: [ade-demo-env/api!1]
stage: review
court: you
iteration: Sprint 42
synced: 2026-11-12T01:43:33Z
---

<!-- ade:snapshot:begin (ADE rewrites everything between the markers; write below) -->
# Rate-limit the export endpoint

Labels: type::story, app::api, workflow::in review
Assignees: @sandbox-dev

Tenants can trigger unbounded CSV exports and starve the worker pool.

## Acceptance
- [x] Agree limits with support (50 req/min per tenant)
- [ ] Token-bucket limiter in api
- [ ] 429 response with Retry-After
- [ ] Dashboard shows remaining quota

<!-- ade:snapshot:end -->

## Notes

