#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C
umask 022

: "${VERSION:?VERSION is required}"
: "${COMMIT:?COMMIT is required}"
: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH is required}"

case "$VERSION" in
  *[!0-9A-Za-z._+-]*)
    echo "VERSION contains an unsupported archive-name character" >&2
    exit 2
    ;;
esac
case "$COMMIT" in
  *[!0-9a-f]*)
    echo "COMMIT must be a lowercase hexadecimal revision" >&2
    exit 2
    ;;
esac
case "$SOURCE_DATE_EPOCH" in
  ''|*[!0-9]*)
    echo "SOURCE_DATE_EPOCH must be an integer Unix timestamp" >&2
    exit 2
    ;;
esac

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
output_dir=${OUTPUT_DIR:-"$repository_root/dist"}
build_root=$(mktemp -d "${TMPDIR:-/tmp}/camp-packaging.XXXXXXXX")
cleanup() {
  rm -rf -- "$build_root"
}
trap cleanup EXIT

mkdir -p "$output_dir" "$build_root/completions"
build_date=$(date --utc --date="@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)
ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$build_date -X main.dirty=false"

CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$build_root/camp-host" ./cmd/camp
"$build_root/camp-host" completion bash >"$build_root/completions/camp.bash"
"$build_root/camp-host" completion zsh >"$build_root/completions/_camp"
"$build_root/camp-host" completion fish >"$build_root/completions/camp.fish"

for architecture in amd64 arm64; do
  package_name="camp_${VERSION}_linux_${architecture}"
  package_root="$build_root/$package_name"
  mkdir -p "$package_root/completions"

  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build \
    -trimpath \
    -buildvcs=false \
    -ldflags "$ldflags" \
    -o "$package_root/camp" \
    ./cmd/camp
  cp README.md "$package_root/README.md"
  cp packaging/INSTALL.md "$package_root/INSTALL.md"
  cp "$build_root/completions/"* "$package_root/completions/"
  chmod 0755 "$package_root/camp"
  chmod 0644 "$package_root/README.md" "$package_root/INSTALL.md" "$package_root/completions/"*
  find "$package_root" -exec touch --no-dereference --date="@$SOURCE_DATE_EPOCH" {} +

  tar \
    --sort=name \
    --format=ustar \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    --mtime="@$SOURCE_DATE_EPOCH" \
    -C "$build_root" \
    -cf - "$package_name" | gzip -n >"$output_dir/$package_name.tar.gz"
done

(
  cd "$output_dir"
  sha256sum "camp_${VERSION}_linux_amd64.tar.gz" "camp_${VERSION}_linux_arm64.tar.gz" >checksums.txt
)
