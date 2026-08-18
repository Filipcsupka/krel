# Roadmap

`krel` is in an early phase. The roadmap is intentionally practical and focused on making relationship inspection dependable.

## Near Term

- Improve graph coverage for more Kubernetes resource kinds
- Fetch OLM resources (Subscription, InstallPlan, ClusterServiceVersion) so
  the Owner Chain pane's OLM segment actually populates on OpenShift
- exec and port-forward, so krel can be a daily-driver terminal tool
  alongside kubectl/k9s, not just a read-only relationship inspector
- Add richer problem checks for common workload and networking issues
- Improve keyboard ergonomics in dense namespaces
- Add more tests around edge cases and missing references
- Publish release binaries

## Later

- Optional graph export formats
- Better OpenShift-specific relationship coverage
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
