# Roadmap

`krel` is in an early phase. The roadmap is intentionally practical and focused on making relationship inspection dependable.

## Near Term

- Improve graph coverage for more Kubernetes resource kinds
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

- Mutating cluster resources
- Replacing `kubectl`
- Becoming a general cluster dashboard

`krel` should stay focused on fast relationship inspection and read-only diagnostics.
