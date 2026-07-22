#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C
umask 022

mode=${1:-}
repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
output_dir=${OUTPUT_DIR:-"$repository_root/dist"}

case "$mode" in
  build)
    : "${VERSION:?VERSION is required}"
    : "${COMMIT:?COMMIT is required}"
    : "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH is required}"
    VERSION=${VERSION#v}
    export VERSION COMMIT SOURCE_DATE_EPOCH OUTPUT_DIR="$output_dir"
    "$repository_root/packaging/build-archives.sh"

    amd64_archive="camp_${VERSION}_linux_amd64.tar.gz"
    arm64_archive="camp_${VERSION}_linux_arm64.tar.gz"
    amd64_digest=$(sha256sum "$output_dir/$amd64_archive" | cut -d' ' -f1)
    arm64_digest=$(sha256sum "$output_dir/$arm64_archive" | cut -d' ' -f1)

    for architecture in amd64 arm64; do
      archive="camp_${VERSION}_linux_${architecture}.tar.gz"
      digest=$(sha256sum "$output_dir/$archive" | cut -d' ' -f1)
      cat >"$output_dir/$archive.spdx.json" <<EOF
{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"$archive","documentNamespace":"https://github.com/joshyorko/camp/releases/$COMMIT/$archive","creationInfo":{"created":"$(date --utc --date="@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)","creators":["Tool: camp-release-evidence"]},"documentDescribes":["SPDXRef-Package-$architecture"],"packages":[{"name":"$archive","SPDXID":"SPDXRef-Package-$architecture","downloadLocation":"NOASSERTION","filesAnalyzed":false,"versionInfo":"$VERSION","checksums":[{"algorithm":"SHA256","checksumValue":"$digest"}]}]}
EOF
    done

    sed \
      -e "s|{{VERSION}}|$VERSION|g" \
      -e "s|{{AMD64_URL}}|https://github.com/joshyorko/camp/releases/download/v$VERSION/$amd64_archive|g" \
      -e "s|{{AMD64_SHA256}}|$amd64_digest|g" \
      -e "s|{{ARM64_URL}}|https://github.com/joshyorko/camp/releases/download/v$VERSION/$arm64_archive|g" \
      -e "s|{{ARM64_SHA256}}|$arm64_digest|g" \
      "$repository_root/packaging/homebrew/camp.rb.tmpl" >"$output_dir/camp.rb"

    cat >"$output_dir/evidence.json" <<EOF
{"schemaVersion":1,"commit":"$COMMIT","version":"$VERSION","artifacts":[{"name":"$amd64_archive","platform":"linux/amd64","sha256":"$amd64_digest","sbom":"$amd64_archive.spdx.json","result":"built"},{"name":"$arm64_archive","platform":"linux/arm64","sha256":"$arm64_digest","sbom":"$arm64_archive.spdx.json","result":"built"}],"packages":[{"name":"camp.rb","type":"homebrew-formula","result":"rendered"}],"gates":[{"name":"credentialed-providers","result":"gated","reason":"no credentialed provider profile is currently claimed or authorized"},{"name":"github-release","result":"gated","reason":"publication requires a verified tag or explicit protected-environment approval"}]}
EOF
    ;;
  verify)
    : "${VERSION:?VERSION is required}"
    : "${COMMIT:?COMMIT is required}"
    VERSION=${VERSION#v}
    architecture=${VERIFY_ARCH:-$(go env GOARCH)}
    case "$architecture" in
      amd64|arm64) ;;
      *) echo "unsupported verification architecture: $architecture" >&2; exit 2 ;;
    esac
    archive="camp_${VERSION}_linux_${architecture}.tar.gz"
    digest=$(sha256sum "$output_dir/$archive" | cut -d' ' -f1)
    (
      cd "$output_dir"
      sha256sum --check checksums.txt
    )
    grep -Fq "\"checksumValue\":\"$digest\"" "$output_dir/$archive.spdx.json"
    grep -Fq "\"commit\":\"$COMMIT\"" "$output_dir/evidence.json"

    verify_root=$(mktemp -d "${TMPDIR:-/tmp}/camp-release-verify.XXXXXXXX")
    cleanup() { rm -rf -- "$verify_root"; }
    trap cleanup EXIT
    tar -xzf "$output_dir/$archive" -C "$verify_root"
    package_root="$verify_root/camp_${VERSION}_linux_${architecture}"
    install -D -m 0755 "$package_root/camp" "$verify_root/installed/bin/camp"
    "$verify_root/installed/bin/camp" --version | grep -Fq "$VERSION"
    "$verify_root/installed/bin/camp" --version | grep -Fq "$COMMIT"
    "$verify_root/installed/bin/camp" --help | grep -Fq "Available Commands:"
    for shell in bash zsh fish; do
      "$verify_root/installed/bin/camp" completion "$shell" >/dev/null
    done
    printf '{"commit":"%s","platform":"linux/%s","artifact":"%s","sha256":"%s","result":"passed"}\n' \
      "$COMMIT" "$architecture" "$archive" "$digest" >"$output_dir/verification-$architecture.json"
    ;;
  *)
    echo "usage: $0 build|verify" >&2
    exit 2
    ;;
esac
