#!/usr/bin/env bash
set -euo pipefail

old_dist=${1:?old artifact directory is required}
new_dist=${2:?new artifact directory is required}

engine=${CONTAINER_ENGINE:-podman}
command -v "$engine" >/dev/null 2>&1 || {
  echo "required container engine not found: $engine" >&2
  exit 1
}

old_dist=$(cd "$old_dist" && pwd -P)
new_dist=$(cd "$new_dist" && pwd -P)

run_fixture() {
  local name=$1
  local image=$2
  local old_package=$3
  local new_package=$4
  local install=$5
  local upgrade=$6
  local uninstall=$7
  local state
  state=$(mktemp -d "${TMPDIR:-/tmp}/camp-$name-state.XXXXXXXX")
  cleanup_state() {
    if ! rm -rf -- "$state" 2>/dev/null; then
      "$engine" run --rm -v "$state:/state:z" "$image" \
        sh -c 'rm -rf /state/home /state/config /state/data /state/cache'
      rmdir "$state"
    fi
  }
  trap cleanup_state RETURN

  mkdir -p "$state/home" "$state/config" "$state/data" "$state/cache"
  printf 'operator-owned\n' >"$state/config/operator-state"

  "$engine" run --rm \
    --name "camp-package-$name-$$" \
    -v "$old_dist:/old:ro,z" \
    -v "$new_dist:/new:ro,z" \
    -v "$state:/state:z" \
    -e HOME=/state/home \
    -e XDG_CONFIG_HOME=/state/config \
    -e XDG_DATA_HOME=/state/data \
    -e XDG_CACHE_HOME=/state/cache \
    "$image" sh -euxc "
      $install /old/$old_package
      camp --version | grep '0.0.0 (commit 0123456789abcdef0123456789abcdef01234567, built 2026-07-22T00:00:00Z, dirty false)'
      camp completion bash > /tmp/camp.bash
      cmp /tmp/camp.bash /usr/share/bash-completion/completions/camp
      camp completion zsh > /tmp/_camp
      cmp /tmp/_camp /usr/share/zsh/site-functions/_camp
      camp completion fish > /tmp/camp.fish
      cmp /tmp/camp.fish /usr/share/fish/vendor_completions.d/camp.fish
      camp setup
      test -x /state/data/camp/tools/devpod/*/linux-amd64/*/devpod
      test -x /state/data/camp/tools/hauler/*/linux-amd64/*/hauler
      $upgrade /new/$new_package
      camp --version | grep '0.0.1 (commit 0123456789abcdef0123456789abcdef01234567, built 2026-07-22T00:00:00Z, dirty false)'
      test \"\$(cat /state/config/operator-state)\" = operator-owned
      $uninstall
      test ! -e /usr/bin/camp
      test \"\$(cat /state/config/operator-state)\" = operator-owned
      test -x /state/data/camp/tools/devpod/*/linux-amd64/*/devpod
    "
}

run_fixture deb debian:bookworm-slim \
  camp_0.0.0_linux_amd64.deb camp_0.0.1_linux_amd64.deb \
  'apt-get update && apt-get install -y ca-certificates diffutils passt' \
  'apt-get install -y' \
  'apt-get purge -y camp'

run_fixture rpm fedora:44 \
  camp_0.0.0_linux_amd64.rpm camp_0.0.1_linux_amd64.rpm \
  'dnf install -y ca-certificates diffutils passt' \
  'dnf upgrade -y' \
  'dnf remove -y camp'

run_fixture apk alpine:3.22 \
  camp_0.0.0_linux_amd64.apk camp_0.0.1_linux_amd64.apk \
  'apk add --no-cache ca-certificates diffutils passt && apk add --allow-untrusted' \
  'apk add --allow-untrusted --upgrade' \
  'apk del camp'
