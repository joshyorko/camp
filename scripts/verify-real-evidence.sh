#!/usr/bin/env bash
set -euo pipefail

mode="${1:-list}"
package="./integration"
required='TestLocalLifecycleVertical|TestLocalLifecycleCrashMatrix|TestMinIOLifecycleVertical|TestS3TwoWriterConflict|TestMountedFileBackendParity'

require_candidate() {
  if [[ -z "${CAMP_TEST_BINARY:-}" || ! -x "${CAMP_TEST_BINARY}" || "$(basename "${CAMP_TEST_BINARY}")" != "camp" || "$(basename "$(dirname "${CAMP_TEST_BINARY}")")" != "build" ]]; then
    printf 'CAMP_TEST_BINARY must name the executable build/camp candidate\n' >&2
    return 1
  fi
}

run_named() {
  local name="$1" timeout="$2"
  shift 2
  local output
  output="$(mktemp)"
  trap 'rm -f "${output}"' RETURN
  if ! go test -v "${package}" -run "^${name}$" -count=1 -timeout="${timeout}" "$@" | tee "${output}"; then
    return 1
  fi
  if grep -qE "(^|[[:space:]])SKIP|no tests to run" "${output}"; then
    printf 'evidence gate %s skipped or ran no tests\n' "${name}" >&2
    return 1
  fi
  if ! grep -q -- "--- PASS: ${name}" "${output}"; then
    printf 'evidence gate %s produced no passing test receipt\n' "${name}" >&2
    return 1
  fi
}

discover() {
  local listed
  listed="$(go test -list "${required}" "${package}")"
  for name in \
    TestLocalLifecycleVertical \
    TestLocalLifecycleCrashMatrix \
    TestMinIOLifecycleVertical \
    TestS3TwoWriterConflict \
    TestMountedFileBackendParity
  do
    if ! grep -qx "${name}" <<<"${listed}"; then
      printf 'missing evidence gate: %s\n' "${name}" >&2
      return 1
    fi
  done
  printf '%s\n' "${listed}"
}

run_file() {
  run_named TestMountedFileBackendParity 10m
}

run_minio() {
  run_named TestMinIOImmutableLifecycle 10m
  run_named TestS3TwoWriterConflict 20m
  CAMP_TEST_REAL_MINIO_REOPEN=1 run_named TestMinIOLifecycleVertical 60m
}

run_lifecycle() {
  CAMP_TEST_REAL_LIFECYCLE=1 run_named TestLocalLifecycleVertical 60m
  CAMP_TEST_REAL_LIFECYCLE=1 run_named TestLocalLifecycleCrashMatrix 120m
}

require_candidate
discover >/dev/null
case "${mode}" in
  list)
    discover
    ;;
  file)
    run_file
    ;;
  minio)
    run_minio
    ;;
  lifecycle)
    run_lifecycle
    ;;
  all)
    run_file
    run_minio
    run_lifecycle
    ;;
  *)
    printf 'usage: %s [list|file|minio|lifecycle|all]\n' "$0" >&2
    exit 2
    ;;
esac
