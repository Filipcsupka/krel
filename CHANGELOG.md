# Changelog

All notable changes to `krel` will be documented in this file.

The format follows Keep a Changelog, and this project uses semantic version tags.

## [Unreleased]

## [0.1.3] - 2026-08-14

### Added

- Four-pane layout: resource list (top-left), Status pane with health/problems/events/env values (bottom-left), clickable Relations pane (top-right), and a persistent Logs pane (bottom-right).
- Relations pane lists Services, ConfigMaps, Secrets, ServiceAccounts, PVCs, and other refs as clickable entries: `j`/`k` to move, `enter` to open the referenced object's values in place.

### Changed

- Log lines no longer prefix with `<pod>/<container>`; the leading Kubernetes timestamp (when enabled) now renders in a distinct color from the rest of the line.

## [0.1.2] - 2026-08-14

### Added

- `docs/painpoints.md` documenting relationship-inspection gaps krel targets that k9s doesn't cover (cross-namespace relations, blast-radius/impact view, root-cause chains).
- Logs pane is now tab-focusable with `j`/`k`/arrow scroll, `G` to jump back to the live tail, and `/` fulltext search with `n`/`N` to step between matches.

### Changed

- Details view no longer prints `apiVersion` and `uid` (noise for relationship work).
- Top/log split is now 50/50 instead of 1/3-2/3, giving logs more room.
- Resource list rows are now compact single-line entries, closer to k9s' table layout.

## [0.1.1]

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

[Unreleased]: https://github.com/Filipcsupka/krel/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/Filipcsupka/krel/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/Filipcsupka/krel/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/Filipcsupka/krel/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Filipcsupka/krel/releases/tag/v0.1.0
