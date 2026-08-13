# Changelog

All notable changes to `krel` will be documented in this file.

The format follows Keep a Changelog, and this project uses semantic version tags.

## [Unreleased]

### Added

- Initial public project documentation and community files.
- Installer script for local checkout builds, release downloads, and Go fallback installs.
- GitHub Actions workflows for CI and tagged releases.

## [0.1.0] - 2026-08-13

### Added

- Initial read-only Kubernetes relationship TUI.
- `kr` and `krel` command entrypoints.
- Namespace-scoped snapshot loading for common Kubernetes and OpenShift resources.
- Relationship graph views for ownership, Service selectors, Pod references, Ingress backends, and OpenShift Routes.
- Problem detection for missing references, Services selecting zero Pods, and unbound PVCs.
- Non-interactive `why`, `refs`, and `problems` commands.

[Unreleased]: https://github.com/Filipcsupka/krel/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Filipcsupka/krel/releases/tag/v0.1.0
