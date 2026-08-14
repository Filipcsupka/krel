# Changelog

All notable changes to `krel` will be documented in this file.

The format follows Keep a Changelog, and this project uses semantic version tags.

## [Unreleased]

### Added

- CI now runs `go vet`, race-enabled tests, and an informational `govulncheck` pass (reports, doesn't block on stdlib/toolchain-lag CVEs) alongside gofmt/build.
- Bumped `go.mod` to `go 1.26.2` and `golang.org/x/net` to v0.58.0 to close the vulnerabilities that were actually fixable from this repo.
- Releases are automatic: after CI passes on `main`, an Auto Tag workflow bumps the patch version and dispatches the Release build — no manual `git tag` for routine releases.

### Fixed

- Every bordered pane rendered 2 rows taller than requested (lipgloss `Height()` sets content height; the border adds 2 more on top of that). With 5 stacked panes this compounded enough to push the resource list header and the Usage panel off the top of the terminal. Fixed at the source in the shared pane renderers; added a regression test that drives `View()` at several terminal sizes and fails if it ever overflows again.
- The resource list pane was sized wider than its padded content area, so bubbles' list component wrapped its own rows and silently grew taller than its box — same overflow, different cause. List content is now sized to match the actual padded area.
- Opening a Secret/ConfigMap/etc. from the Relations pane left it stuck on the opened object's YAML with no way back; `esc` now returns the Relations pane to its default relations list.
- `r`/`d`/`y`/`e`/`p` now also move keyboard focus to the Relations pane, so you don't have to separately `tab` to it before the pane responds to `j`/`k`/`enter`. The Relations pane's title also shows its keybindings while focused.

## [0.1.4] - 2026-08-14

### Fixed

- Relations pane could get stuck showing Details/YAML/Events instead of relations after navigating to a different resource; it now resets to the relations list on every selection change.
- Resource list pane was too small (narrow + short) to show its header (context/namespace/kind) or enough rows; widened and given more height.
- Status pane env output repeated `env: <container>/` as a prefix on every line; now a single `env:` header with variables grouped per container underneath.
- Status pane had no coloring; problems now render in red, environment values in a distinct color, and pod/status health colors are preserved without breaking line-wrapping.

### Added

- Small Usage panel above Relations: CPU/memory gauges from metrics-server (when installed) plus requests/limits summed across the selected object's pods.

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

[Unreleased]: https://github.com/Filipcsupka/krel/compare/v0.1.4...HEAD
[0.1.4]: https://github.com/Filipcsupka/krel/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/Filipcsupka/krel/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/Filipcsupka/krel/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/Filipcsupka/krel/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Filipcsupka/krel/releases/tag/v0.1.0
