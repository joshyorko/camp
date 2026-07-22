#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C
umask 022

: "${VERSION:?VERSION is required}"
: "${COMMIT:?COMMIT is required}"
: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH is required}"

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
output_dir=${OUTPUT_DIR:-"$repository_root/dist"}
build_root=$(mktemp -d "${TMPDIR:-/tmp}/camp-native-packaging.XXXXXXXX")
cleanup() {
  rm -rf -- "$build_root"
}
trap cleanup EXIT

OUTPUT_DIR="$output_dir" "$repository_root/packaging/build-archives.sh"

render_config() {
  local architecture=$1
  local stage=$2
  local config=$3
  sed \
    -e "s|{{ARCH}}|$architecture|g" \
    -e "s|{{VERSION}}|$VERSION|g" \
    -e "s|{{STAGE}}|$stage|g" \
    "$repository_root/packaging/nfpm.yaml.tmpl" >"$config"
}

for architecture in amd64 arm64; do
  archive_root="camp_${VERSION}_linux_${architecture}"
  tar -xzf "$output_dir/$archive_root.tar.gz" -C "$build_root"
  stage="$build_root/$archive_root"
  config="$build_root/nfpm-$architecture.yaml"
  render_config "$architecture" "$stage" "$config"
  for format in deb rpm apk; do
    target="$output_dir/camp_${VERSION}_linux_${architecture}.$format"
    SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" \
      go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.4 package \
        --config "$config" \
        --packager "$format" \
        --target "$target"
  done
done

(
  cd "$output_dir"
  sha256sum camp_*.tar.gz camp_*.deb camp_*.rpm camp_*.apk >checksums.txt
)
