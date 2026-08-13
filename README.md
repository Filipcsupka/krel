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
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | sh
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
- Non-interactive commands for scripts and quick checks
- Support for Pods, Deployments, ReplicaSets, Services, EndpointSlices, ConfigMaps, Secrets, PVCs, ServiceAccounts, Ingresses, and OpenShift Routes
- Relationship edges for ownership, Services selecting Pods, Pod references, Ingress backends, and Routes pointing to Services
- Basic problem detection for missing references, Services selecting zero Pods, and unbound PVCs

## Install

Install the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | sh
```

The install script installs both `kr` and `krel`. It uses `$BINDIR` when set, then `$GOBIN`, then `~/.local/bin` when that directory exists or is already on PATH, and finally `$(go env GOPATH)/bin`.

To choose the target directory:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR=/usr/local/bin sh
```

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | VERSION=v0.1.0 sh
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

## Keyboard

| Key | Action |
| --- | --- |
| `/` | Filter resources |
| `tab` | Switch pane |
| `:` | Command mode |
| `r` | Relations view |
| `d` | Details view |
| `y` | YAML view |
| `e` | Events view |
| `p` | Problems view |
| `?` | Help |
| `q` | Quit |

Command mode:

```text
:ctx
:ctx <name>
:ns <namespace>
:kubeconfig <path>
:kc <path>
:refresh
```

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
