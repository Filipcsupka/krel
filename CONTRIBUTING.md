# Contributing

Thanks for considering a contribution to `krel`.

## Workflow

1. Open an issue for larger changes so the approach can be discussed.
2. Keep pull requests focused.
3. Add or update tests for behavior changes.
4. Run `make test` before opening a pull request.

## Development Setup

```bash
git clone https://github.com/Filipcsupka/krel.git
cd krel
make test
go run ./cmd/kr --namespace default
```

## Pull Request Checklist

- The change is read-only with respect to Kubernetes clusters.
- Tests cover new graph behavior, problem checks, or CLI behavior.
- Documentation is updated when user-visible behavior changes.
- `make test` passes.

## Releases

Releases are cut from `main` with semantic version tags. See [docs/release.md](docs/release.md).

## Code Style

Use standard Go formatting:

```bash
gofmt -w .
```

Prefer small functions with clear responsibilities. Relationship logic should include enough context in edge reasons to help users understand where a link came from.

## Reporting Bugs

Include:

- `krel` version or commit
- Kubernetes distribution and version when relevant
- Command and flags used
- Resource kinds involved
- Expected behavior
- Actual behavior

Do not include secrets, kubeconfig credentials, tokens, or sensitive YAML.
