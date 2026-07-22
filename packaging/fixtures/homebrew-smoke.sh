#!/usr/bin/env bash
set -euo pipefail

old_dist=${1:?old artifact directory is required}
new_dist=${2:?new artifact directory is required}
engine=${CONTAINER_ENGINE:-docker}
command -v "$engine" >/dev/null 2>&1 || {
  echo "required container engine not found: $engine" >&2
  exit 1
}

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
old_dist=$(cd "$old_dist" && pwd -P)
new_dist=$(cd "$new_dist" && pwd -P)
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/camp-homebrew.XXXXXXXX")
cleanup() {
  rm -rf -- "$fixture_root"
}
trap cleanup EXIT

tap="$fixture_root/tap"
state="$fixture_root/state"
mkdir -p "$tap/Formula" "$state/config"
printf 'operator-owned\n' >"$state/config/operator-state"

render_formula() {
  local version=$1
  local dist=$2
  local output=$3
  VERSION="$version" \
  AMD64_URL="file:///art/$version/camp_${version}_linux_amd64.tar.gz" \
  AMD64_SHA256=$(sha256sum "$dist/camp_${version}_linux_amd64.tar.gz" | cut -d' ' -f1) \
  ARM64_URL="file:///art/$version/camp_${version}_linux_arm64.tar.gz" \
  ARM64_SHA256=$(sha256sum "$dist/camp_${version}_linux_arm64.tar.gz" | cut -d' ' -f1) \
  OUTPUT="$output" \
    "$repository_root/packaging/render-homebrew.sh"
}

render_formula 0.0.0 "$old_dist" "$tap/Formula/camp.rb"
git -C "$tap" init -b main
git -C "$tap" config user.name 'Camp package fixture'
git -C "$tap" config user.email 'fixture@camp.invalid'
git -C "$tap" add Formula/camp.rb
git -C "$tap" commit -m 'camp 0.0.0'
old_commit=$(git -C "$tap" rev-parse HEAD)
render_formula 0.0.1 "$new_dist" "$tap/Formula/camp.rb"
git -C "$tap" add Formula/camp.rb
git -C "$tap" commit -m 'camp 0.0.1'
new_commit=$(git -C "$tap" rev-parse HEAD)
git -C "$tap" update-ref refs/heads/main "$old_commit"
chmod -R a+rwX "$fixture_root"

"$engine" run --rm \
  --name "camp-homebrew-$$" \
  -v "$tap:/tap:z" \
  -v "$old_dist:/art/0.0.0:ro,z" \
  -v "$new_dist:/art/0.0.1:ro,z" \
  -v "$state:/state:z" \
  -e XDG_CONFIG_HOME=/state/config \
  docker.io/homebrew/brew:latest bash -euxo pipefail -c "
    brew tap joshyorko/camp file:///tap
    brew install joshyorko/camp/camp
    camp --version | grep '0.0.0 (commit 0123456789abcdef0123456789abcdef01234567, built 2026-07-22T00:00:00Z, dirty false)'
    cmp \"\$(brew --prefix)/etc/bash_completion.d/camp\" <(camp completion bash)
    git -C /tap update-ref refs/heads/main $new_commit
    brew update
    brew upgrade joshyorko/camp/camp
    camp --version | grep '0.0.1 (commit 0123456789abcdef0123456789abcdef01234567, built 2026-07-22T00:00:00Z, dirty false)'
    test \"\$(cat /state/config/operator-state)\" = operator-owned
    HOMEBREW_NO_AUTOREMOVE=1 brew uninstall --force --ignore-dependencies joshyorko/camp/camp
    test ! -e \"\$(brew --prefix)/bin/camp\"
    test \"\$(cat /state/config/operator-state)\" = operator-owned
  "
