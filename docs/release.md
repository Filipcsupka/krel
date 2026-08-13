# Release Guide

This project publishes release binaries through GitHub Actions.

## Versioning

Use semantic version tags:

```text
vMAJOR.MINOR.PATCH
```

Example:

```text
v0.1.0
```

## Release Checklist

1. Confirm CI is passing on `main`.
2. Update `CHANGELOG.md`.
3. Create an annotated tag.
4. Push the tag.
5. Confirm the Release workflow uploads all assets.
6. Test the installer from a clean shell.

## Commands

Create and push a release tag:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Verify release assets:

```bash
gh release view v0.1.0
```

Test the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/Filipcsupka/krel/main/scripts/install.sh | sh
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
