#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

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

(
	cd "$repo_dir"
	go build -o "$tmp_dir/kr" ./cmd/kr
	go build -o "$tmp_dir/krel" ./cmd/krel
)

mv "$tmp_dir/kr" "$bin_dir/kr"
mv "$tmp_dir/krel" "$bin_dir/krel"

printf 'installed kr and krel to %s\n' "$bin_dir"
case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*) printf 'warning: %s is not on PATH\n' "$bin_dir" ;;
esac

