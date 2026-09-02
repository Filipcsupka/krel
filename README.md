# krel

[![CI](https://github.com/Filipcsupka/krel/actions/workflows/ci.yml/badge.svg)](https://github.com/Filipcsupka/krel/actions/workflows/ci.yml)
[![Release](https://github.com/Filipcsupka/krel/actions/workflows/release.yml/badge.svg)](https://github.com/Filipcsupka/krel/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`krel` is a read-only Kubernetes relationship TUI for understanding how namespace-scoped objects connect.

It is built for operators and developers who want a fast terminal workflow for answering questions like:

- Which Pods does this Service select?
- Which ConfigMaps, Secrets, PVCs, and ServiceAccounts does this workload use?
- Why is this object relevant to the selected resource?
- What graph-derived problems are visible in this namespace?

The primary binary is `kr`, with `krel` also available as the full command name.

## Quick Install

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR="$HOME/.local/bin" sh
```

Then run:

```bash
kr
```

## Status

`krel` is early-stage community software. The current release is useful for exploration and diagnostics, but the project is still evolving quickly.

The tool is intentionally read-only: it loads Kubernetes/OpenShift objects, builds a relationship graph, and renders focused views in a terminal UI.

## Features

- Bubble Tea terminal UI for browsing namespace resources
- Kubernetes client-go kubeconfig loading
- Context, namespace, and kubeconfig switching inside the TUI
- Object summaries, relations, YAML, events, and problem views
- Owner Chain pane: generic `metadata.ownerReferences` walk (Pod -> ReplicaSet -> Deployment, Job -> CronJob, ...), extended with the OLM `Subscription -> InstallPlan -> ClusterServiceVersion` chain on OpenShift/OLM clusters and an ArgoCD `Application` node (sync/health status) or a `managed-by: argocd|flux|helm` line when GitOps-managed; Argo CD identity is detected from the instance label or tracking annotation
- Fullscreen logs (`l`, k9s-style) with grep, previous-container, search, wrap, timestamps, and live-tail follow
- Read-only live snapshot refresh every 3 seconds with selected-resource preservation
- True API follow streams: the viewport autoscrolls at the tail, freezes without dropping lines when paused/scrolled, and resumes with `G` or space
- Non-interactive commands for scripts and quick checks
- Discovery-driven browsing for every listable built-in and CRD advertised by the active cluster; kind, plural, and API short-name commands are supported
- A fast relationship profile covering workloads, RBAC, storage/snapshots, Gateway API, External Secrets, Prometheus monitors, KEDA, Strimzi, Velero/OADP, OpenShift, OLM, and ArgoCD, plus generic CRD reference extraction
- Graph-derived service health checks: selector mismatches and Services with no ready EndpointSlice/Endpoints backends
- Cross-namespace mode (`-A` or `:ns all`) with namespace-qualified rows and focused relationship neighborhoods
- Blast-radius analysis (`i`), graph-derived root-cause paths, resource quota/certificate/restart/ArgoCD/condition checks, and consolidated workload safeguards
- Secret keys remain inspectable for relationship work, but Secret payloads are redacted from YAML

## Install

Install the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR="$HOME/.local/bin" sh
```

The install script installs both `kr` and `krel`. It uses `$BINDIR` when set, then `$GOBIN`, then `~/.local/bin` when that directory exists or is already on PATH, and finally `$(go env GOPATH)/bin`.

To choose the target directory:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR=/usr/local/bin sh
```

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR="$HOME/.local/bin" VERSION=v0.1.0 sh
```

Install with Go:

```bash
go install github.com/filipcsupka/krel/cmd/kr@latest
go install github.com/filipcsupka/krel/cmd/krel@latest
```

Install from a local checkout:

```bash
make install
```

Or:

```bash
BINDIR=/usr/local/bin ./scripts/install.sh
```

You can also run without installing:

```bash
go run ./cmd/kr --namespace default
```

## Usage

Start the TUI with your current kubeconfig context:

```bash
kr
```

Open a specific namespace:

```bash
kr --namespace default
```

Inspect across all namespaces:

```bash
kr -A
```

Use a specific context or kubeconfig:

```bash
kr --context my-cluster --namespace apps
kr --kubeconfig ~/.kube/config --namespace apps
```

Kubeconfig loading follows standard Kubernetes client behavior:

- without flags, `krel` loads the current context from `$KUBECONFIG`, or `~/.kube/config` when `$KUBECONFIG` is unset
- multiple paths in `$KUBECONFIG` are merged by client-go
- `--kubeconfig <path>` overrides the default loading rules
- `--context <name>` starts directly on that context

## Layout

On normal terminals, the resource list gets the largest useful share and the
other panes size themselves to their content. On narrow or short terminals,
only the focused pane is shown; `tab` and `shift+tab` move between panes
without clipping the screen.

- top-left: resource list, headed by the crumb `config: <kubeconfig> ctx: <context> ns: <namespace>  kind:... [count]  sort:...`; Pod rows include readiness, restarts, node, and (on wide terminals) Pod IP
- bottom-left: Owner Chain — `metadata.ownerReferences` walked generically (Pod -> ReplicaSet -> Deployment, Job -> CronJob, ...), extended upward through OLM's `Subscription -> InstallPlan -> ClusterServiceVersion` and an ArgoCD `Application` node when present. Argo CD tracking supports `argocd.argoproj.io/instance` and `argocd.argoproj.io/tracking-id`. `j`/`k` to move, `enter` opens the selected owner's values.
- top-right, small strip: Usage — 2 lines (cpu, mem), each a gauge from metrics-server when available plus requests/limits, and a `|`-separated extra field (pod/restart count, node placement)
- right, below Usage: Relations — Services, ConfigMaps, Secrets (including `env`, `envFrom`, `imagePullSecrets`, classic mounts, and projected volume sources), ServiceAccounts, PVCs, and other refs for the selected object. Clickable: `j`/`k` to move, `enter` opens the referenced object's values in-place, `esc` returns to the relations list.
- bottom-right, the largest pane: Status — why it's failing (problems), per-container `running`/`waiting`/`terminated` state with readiness and restart counts, non-Ready `status.conditions` (covers Nodes, Certificates, HPAs, and most CRDs that follow the conditions convention), recent events, then environment values grouped by container. Scrollable with `j`/`k`.

Logs are not a permanent pane — press `l` for a fullscreen, k9s-style log view of the selected resource; `esc` (or `l` again) returns to the 4-pane layout.

The Relations pane resets to the relations list whenever you move to a different resource, even if you'd switched it to Details/YAML/Events/Problems.

## Keyboard

| Key | Action |
| --- | --- |
| `/` | Filter resources, or search logs when the fullscreen log view is open |
| `/!regex` | Inverse resource filter; exclude matching rows |
| `/-l key=value` | Filter the resource view by Kubernetes labels |
| `/-f filter` | Fuzzy-filter the resource view |
| `tab` | Switch pane (resources / owner chain / relations / status) |
| `shift+tab` | Switch to the previous pane |
| `-` | Toggle between the last two resource views |
| `[` / `]` | Go back/forward through executed command history |
| `shift+n` | Sort resources by name |
| `shift+s` | Sort resources by status |
| `shift+p` | Sort resources by namespace (when viewing all namespaces) |
| `l` | Open fullscreen logs for the selected resource; `esc` or `l` to close |
| `:` | Command mode |
| `j` / `k` (relations pane) | Move between relations |
| `enter` (relations pane) | Open the selected relation's values |
| `j` / `k` (owner chain pane) | Move between owners; `ctrl+f` filters the chain; `enter` opens the selected one |
| `shift+j` | Focus the owner chain pane |
| `j` / `k` (fullscreen logs) | Scroll logs, `G` returns to the live tail |
| `r` | Relations view |
| `d` | Details view |
| `y` | YAML view |
| `e` | Events view |
| `p` | Problems view |
| `i` | Impact / blast-radius view |
| `u` | Direct UsedBy / consumer view |
| `z` | View ReplicaSets for the selected Deployment |
| `!` | Snapshot load warnings (RBAC or unavailable APIs) |
| `?` | Help |
| `q` | Quit |
| `f` | Toggle fullscreen for the active YAML/details viewer |
| `ctrl+r` | Refresh the current snapshot |

Resource commands also accept `:pod -l app=backend` for a label selector and
`:pod /!worker` for an inverse filter. These filters persist across redraws,
refreshes, and terminal resizes.

| `ctrl+a` | Show all discovered resource aliases |
| `ctrl+e` | Toggle the resource list header |
| `ctrl+g` | Toggle context/namespace breadcrumbs |
| `ctrl+z` | Show only warning Events (Event view) |
| `ctrl+w` | Toggle wide resource columns (in command mode, delete previous word) |
| `ctrl+u` | Clear the active command/search input |
| `c` | Copy selected resource name |
| `n` | Copy selected namespace (or next status-search match when searching) |
| `ctrl+s` | Copy selected resource YAML; Secret payloads remain redacted |
| `space` | Mark the selected resource; `ctrl+space` marks a range; `ctrl+\\` clears marks |

Command mode:

```text
:ctx
:ctx <name>
:ns <namespace>
:ns all
:pod <namespace>
:pod @<context>
:pod /filter
:pod app=backend,env=prod
:kubeconfig <path>
:kc <path>
:refresh
:watch / :nowatch
:help / :aliases
:yaml / :describe / :events / :problems / :impact / :xray / :usedby / :warnings
:sort age|old|status|namespace|default
```

`:ctx` opens a filterable context picker; `Enter` switches context and `Esc`
returns to the current resource view.

In command mode, `tab` completes any discovered kind, plural resource name,
or server-provided short name (for example `:kafkatopics`, `:clusteroperators`,
or `:kt` where the API advertises that short name). When multiple API groups
reuse a kind, use the fully qualified resource such as
`:providerconfigs.aws.example.io`.

## CLI Commands

Show why an object is connected to other objects:

```bash
kr --namespace default why pod <pod-name>
```

Show references and consumers for an object:

```bash
kr --namespace default refs secret <secret-name>
```

Show detected graph problems:

```bash
kr --namespace default problems
```

Kind aliases such as `po`, `pod`, `deploy`, `svc`, `cm`, `pvc`, `sa`, `ing`, and `route` are supported.

## Development

Requirements:

- Go 1.26 or newer
- Access to a Kubernetes-compatible cluster for manual testing

Common commands:

```bash
make build
make test
go run ./cmd/kr --namespace default
```

Project layout:

```text
cmd/kr        short command entrypoint
cmd/krel      full command entrypoint
internal/cli  flag parsing and command execution
internal/kube Kubernetes snapshot loading
internal/graph relationship graph model and builder
internal/tui  Bubble Tea terminal UI
scripts/      local install helpers
docs/         project guides
```

## Documentation

- [Getting Started](docs/getting-started.md)
- [Painpoints krel solves (vs k9s)](docs/painpoints.md)
- [Development Guide](docs/development.md)
- [Release Guide](docs/release.md)
- [Roadmap](docs/roadmap.md)
- [Contributing](CONTRIBUTING.md)
- [Security Policy](SECURITY.md)
- [Support](SUPPORT.md)

## Contributing

Contributions are welcome. Good first areas include relationship detection, problem checks, documentation, tests, and UX improvements.

Before opening a pull request, run:

```bash
make test
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution workflow.

## License

MIT. See [LICENSE](LICENSE).
