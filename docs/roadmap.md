# Roadmap

`krel` is in an early phase. The roadmap is intentionally practical and focused on making relationship inspection dependable.

## Current Checkpoint (2026-09-02)

The `feature/k9s-quality` branch is pushed and currently mergeable. The
read-only relationship TUI is implemented through the split-pane workflow,
live log streams, discovery-driven resource browsing, graph navigation,
impact/problem views, filters, marks, auto-refresh, Secret redaction, and
ArgoCD/OLM relationship hints. CI-equivalent local checks pass with
`go test -race ./...`, `go vet ./...`, and `make build`.

The latest graph diagnostics detect Services whose loaded EndpointSlice or
legacy Endpoints objects have no ready backend, and HPA failures in
`AbleToScale`, `ScalingActive`, `ScalingLimited`, or current/desired replica
status.

Not yet complete: `exec`, `port-forward`, event-driven watches for non-log
resources, saved views/bookmarks, additional UX/problem-check polish, and a
manual smoke test against a real cluster. Release binaries are described in
the CI/release workflow but have not been manually verified as a published
release from this branch.

Next implementation priority: add the next focused diagnostics/UX slice,
then implement a client-owned watch lifecycle for the active resource view;
keep the existing three-second refresh as a fallback until watches are
proven stable.

## Near Term

- exec and port-forward, so krel can be a daily-driver terminal tool
  alongside kubectl/k9s, not just a read-only relationship inspector
- Add richer problem checks for common workload and networking issues
- Improve keyboard ergonomics in dense namespaces
- Add more tests around edge cases and missing references
- Publish release binaries
- Add lazy watches for non-log resources so status and relations update without a full refresh
- Add configurable saved resource/namespace views

## Later

- Optional graph export formats
- Pluggable checks
- Saved views or bookmarks
- More focused workflows for incident triage

## Non-Goals

- Creating, editing, patching, deleting, or scaling cluster resources (exec
  and port-forward are the one exception — attach/stream actions, not
  mutations of resource state)
- Becoming a general cluster dashboard

`krel` should stay focused on fast relationship inspection and diagnostics,
now extending into being a daily-driver terminal companion alongside
`kubectl`/k9s rather than a strict read-only viewer.
