# Release Guide

This project publishes release binaries through GitHub Actions.

## Versioning

Semantic version tags:

```text
vMAJOR.MINOR.PATCH
```

## Automated releases

Releases are automatic. On every push to `main` (i.e. every merged PR):

1. **CI** (`.github/workflows/ci.yml`) runs gofmt check, `go vet`, race-enabled
   tests, a build, and an informational `govulncheck` pass (reports but
   doesn't block — it also flags Go stdlib/toolchain CVEs that trail Go
   patch releases and aren't fixable from this repo).
2. If CI succeeds, **Auto Tag** (`.github/workflows/auto-tag.yml`) bumps the
   patch version from the latest `vX.Y.Z` tag and pushes the new tag. It
   skips silently if the commit is already tagged.
3. Pushing that tag would normally trigger **Release**
   (`.github/workflows/release.yml`), but tags pushed with the default
   `GITHUB_TOKEN` don't trigger other workflows (GitHub's loop-prevention
   rule) — so Auto Tag explicitly dispatches Release for the new tag via
   `gh workflow run release.yml --ref <tag>`.
4. Release builds `krel_<os>_<arch>.tar.gz` for linux/darwin ×
   amd64/arm64 and uploads them as GitHub release assets.

Net effect: merge a PR to `main`, wait for CI + Auto Tag + Release to finish,
and the new binary is live for the installer to pick up. No manual tagging
needed for routine patch releases.

## Manual releases (minor/major bumps, or off-schedule)

Create and push a tag yourself; Release still triggers normally since this
push isn't going through the auto-tag bot:

```bash
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

Verify release assets:

```bash
gh release view v0.2.0
```

Test the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | BINDIR="$HOME/.local/bin" sh
kr
```

## Release Assets

The Release workflow builds:

- `krel_linux_amd64.tar.gz`
- `krel_linux_arm64.tar.gz`
- `krel_darwin_amd64.tar.gz`
- `krel_darwin_arm64.tar.gz`

Each archive contains both binaries:

- `kr`
- `krel`
