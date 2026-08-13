#!/usr/bin/env sh
set -eu

repo=Filipcsupka/krel
module=github.com/filipcsupka/krel

bin_dir=${BINDIR:-}
if [ -z "$bin_dir" ]; then
	if [ -n "${GOBIN:-}" ]; then
		bin_dir=$GOBIN
	else
		gopath=$(go env GOPATH)
		bin_dir=$gopath/bin
	fi
fi

mkdir -p "$bin_dir"

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

has() {
	command -v "$1" >/dev/null 2>&1
}

download() {
	url=$1
	out=$2
	if has curl; then
		curl -fsSL "$url" -o "$out"
	elif has wget; then
		wget -qO "$out" "$url"
	else
		return 1
	fi
}

install_from_source() {
	if ! has go; then
		return 1
	fi
	GOBIN=$bin_dir go install "$module/cmd/kr@${VERSION:-latest}"
	GOBIN=$bin_dir go install "$module/cmd/krel@${VERSION:-latest}"
}

install_from_checkout() {
	script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
	repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
	if [ ! -f "$repo_dir/go.mod" ]; then
		return 1
	fi
	(
		cd "$repo_dir"
		go build -o "$tmp_dir/kr" ./cmd/kr
		go build -o "$tmp_dir/krel" ./cmd/krel
	)
	mv "$tmp_dir/kr" "$bin_dir/kr"
	mv "$tmp_dir/krel" "$bin_dir/krel"
}

install_from_release() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	arch=$(uname -m)
	case "$os" in
		linux|darwin) ;;
		*) return 1 ;;
	esac
	case "$arch" in
		x86_64|amd64) arch=amd64 ;;
		arm64|aarch64) arch=arm64 ;;
		*) return 1 ;;
	esac

	archive="$tmp_dir/krel_${os}_${arch}.tar.gz"
	if [ -n "${VERSION:-}" ] && [ "$VERSION" != "latest" ]; then
		url="https://github.com/$repo/releases/download/$VERSION/krel_${os}_${arch}.tar.gz"
	else
		url="https://github.com/$repo/releases/latest/download/krel_${os}_${arch}.tar.gz"
	fi
	download "$url" "$archive" || return 1
	tar -xzf "$archive" -C "$tmp_dir"
	mv "$tmp_dir/kr" "$bin_dir/kr"
	mv "$tmp_dir/krel" "$bin_dir/krel"
}

if ! install_from_checkout; then
	if ! install_from_release; then
		install_from_source || {
			printf 'error: install failed. Install curl or wget for release downloads, or Go for source install.\n' >&2
			exit 1
		}
	fi
fi

printf 'installed kr and krel to %s\n' "$bin_dir"
case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*) printf 'warning: %s is not on PATH\n' "$bin_dir" ;;
esac
