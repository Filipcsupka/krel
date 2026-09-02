# Development Guide

`krel` is a Go terminal application built around three layers:

- snapshot loading from Kubernetes APIs
- relationship graph construction
- Bubble Tea rendering and interaction

API discovery supplies every preferred listable kind. Startup loads a bounded,
concurrent relationship profile; kinds outside that profile are loaded on
demand. All-namespace mode loads only the selected kind's relationship
neighborhood to avoid turning CRD-heavy clusters into hundreds of eager API
requests.

## Next-session checkpoint

As of 2026-09-02, work is on the pushed `feature/k9s-quality` branch at
commit `729cd0c`. The branch is clean and mergeable after:

```bash
go test -race ./...
go vet ./...
make build
```

The current UI and graph features are in place. The remaining goal work is
not a release blocker for the read-only core, but still needs implementation:

- `exec` and `port-forward`
- lazy watches for non-log resource updates
- configurable saved views/bookmarks
- more workload/network diagnostics and UX polish
- manual smoke testing against a real Kubernetes/OpenShift cluster

When resuming, start by reading `docs/roadmap.md`, then inspect the existing
snapshot reload path in `internal/tui/model.go` and `internal/kube/snapshot.go`.
Keep changes read-only and add focused graph/TUI tests for each slice.

## Requirements

- Go 1.26 or newer
- A Kubernetes-compatible cluster for manual testing

## Commands

Build both binaries:

```bash
make build
```

Run tests:

```bash
make test
```

Run locally:

```bash
go run ./cmd/kr --namespace default
```

## Layout

```text
cmd/kr        short command entrypoint
cmd/krel      full command entrypoint
internal/cli  flag parsing and non-interactive commands
internal/kube Kubernetes clients, snapshots, events, and logs
internal/graph graph model, relationship builder, and problem checks
internal/tui  Bubble Tea model and rendering
scripts/      install helpers
```

## Adding Relationships

Most relationship work belongs in `internal/graph`.

Generic Kubernetes-shaped references (`kind`/`name`/`namespace`, common
`*Ref`, and well-known `*Name` fields) are connected automatically when the
target is loaded. Add explicit handlers when a resource uses selectors,
labels, implicit defaults, or another domain-specific convention.

When adding a relationship:

1. Add or update graph builder behavior.
2. Include the source field or object path in the edge reason where possible.
3. Add a focused test in `internal/graph`.
4. Confirm the TUI view remains readable for objects with many edges.

## Adding Resource Kinds

Resource loading starts in `internal/kube`.

When adding a kind:

1. Load the resource into the snapshot.
2. Add a graph object representation.
3. Add relationship edges or problem checks where the kind participates in references.
4. Add tests for object creation and graph behavior.

## Manual Testing

Use a small namespace first:

```bash
kr --namespace default
```

Then test a namespace with real workloads, Services, ConfigMaps, Secrets, PVCs, and Ingress or Route objects.
