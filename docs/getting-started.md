# Getting Started

This guide gets `krel` running against a Kubernetes-compatible cluster.

## Install

From a local checkout:

```bash
make install
```

This installs:

- `kr`, the short command
- `krel`, the full command

To install somewhere specific:

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
