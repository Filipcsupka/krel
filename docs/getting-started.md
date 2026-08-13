# Getting Started

This guide gets `krel` running against a Kubernetes-compatible cluster.

## Install

Install the latest release with one command:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | sh
```

The installer downloads prebuilt release binaries when they are available. If a release asset is unavailable and Go is installed, it falls back to `go install`.

The installer puts both `kr` and `krel` in `$BINDIR` when set, then `$GOBIN`, then `~/.local/bin` when that directory exists or is already on PATH, and finally `$(go env GOPATH)/bin`.

To install somewhere specific:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR=/usr/local/bin sh
```

To install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | VERSION=v0.1.0 sh
```

Install with Go:

```bash
go install github.com/filipcsupka/krel/cmd/kr@latest
go install github.com/filipcsupka/krel/cmd/krel@latest
```

From a local checkout:

```bash
make install
```

This installs:

- `kr`, the short command
- `krel`, the full command

Local checkout with a custom target:

```bash
BINDIR=/usr/local/bin ./scripts/install.sh
```

## Start The TUI

Use the current kubeconfig context:

```bash
kr
```

Open a specific namespace:

```bash
kr --namespace default
```

Use a specific context:

```bash
kr --context my-cluster --namespace apps
```

Use a specific kubeconfig:

```bash
kr --kubeconfig ~/.kube/config --namespace apps
```

## Navigate

Use `/` to filter resources, `tab` to switch panes, and the view keys to change the focused panel:

```text
r relations
d details
y YAML
e events
p problems
```

Use `:` for command mode:

```text
:ctx                  list contexts
:ctx <name>           switch context
:ns <namespace>       switch namespace
:kubeconfig <path>    switch kubeconfig
:kc <path>            short form
:refresh              reload the snapshot
```

## Use Non-Interactive Commands

Show the relationship explanation for one object:

```bash
kr --namespace default why pod <pod-name>
```

Show references and consumers:

```bash
kr --namespace default refs service <service-name>
```

Show graph-derived problems:

```bash
kr --namespace default problems
```

## Permissions

`krel` needs read access to the namespace resources it inspects. If your kubeconfig user cannot list or get a resource kind, that kind may be missing from the snapshot or the command may fail with a Kubernetes API error.
