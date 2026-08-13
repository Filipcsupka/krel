# krel

`krel` is the initial implementation of the Kubernetes relationship TUI described in `~/Documents/ops-brain/moje/k8s-TUI.md`.

The first usable binary is `krel`, with `kr` as a short alias: a read-only terminal app that loads namespace-scoped Kubernetes/OpenShift objects, builds a relationship graph, and shows object-focused relations, details, events, YAML, and problems.

Kubeconfig loading follows the standard Kubernetes client behavior:

- no flags: load the current context from `$KUBECONFIG`, or `~/.kube/config` when `$KUBECONFIG` is unset
- multiple paths in `$KUBECONFIG` are merged by client-go
- `--kubeconfig <path>` overrides the default loading rules
- `--context <name>` starts directly on that context

## Run

Install the short `kr` command and full `krel` command:

```bash
./scripts/install.sh
```

Or:

```bash
make install
```

By default this installs into `$GOBIN`, or `$(go env GOPATH)/bin` when `$GOBIN` is unset. Override the target directory with:

```bash
BINDIR=/usr/local/bin ./scripts/install.sh
```

After install:

```bash
kr
```

For local development without installing:

```bash
go run ./cmd/kr --namespace default
```

Useful flags:

```bash
go run ./cmd/kr --context <context> --namespace <namespace>
go run ./cmd/kr --kubeconfig <path> --namespace <namespace>
```

Non-interactive Phase 0 commands:

```bash
go run ./cmd/kr --namespace default why pod <pod-name>
go run ./cmd/kr --namespace default refs secret <secret-name>
go run ./cmd/kr --namespace default problems
```

## Keys

- `/` filter resources
- `tab` switch pane
- `:` command mode
- `r` relations
- `d` details
- `y` YAML
- `e` events
- `p` problems
- `?` help
- `q` quit

Command mode:

- `:ctx` list contexts
- `:ctx <name>` switch context and use that context's namespace, or `default`
- `:ns <namespace>` switch namespace on the current context
- `:kubeconfig <path>` or `:kc <path>` switch kubeconfig
- `:refresh` reload the current snapshot

## Current Scope

Phase 1 scaffold:

- Phase 0 CLI commands: `why`, `refs`, `problems`
- default kubeconfig/current-context loading, with live context/kubeconfig/namespace switching
- Bubble Tea TUI
- namespace resource browser
- focus summary
- relation/details/YAML/events/problems views
- support for Pods, Deployments, ReplicaSets, Services, EndpointSlices, ConfigMaps, Secrets, PVCs, ServiceAccounts, Ingresses, and OpenShift Routes
- relationship edges for ownership, Services selecting Pods, Pod references, Ingress backends, and Routes to Services
- basic problem detection for missing refs, Services selecting zero Pods, and unbound PVCs
