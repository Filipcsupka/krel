# Development Guide

`krel` is a Go terminal application built around three layers:

- snapshot loading from Kubernetes APIs
- relationship graph construction
- Bubble Tea rendering and interaction

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
