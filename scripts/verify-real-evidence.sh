#!/usr/bin/env bash
set -euo pipefail

mode="${1:-list}"
package="./integration"
required='TestLocalLifecycleVertical|TestLocalLifecycleCrashMatrix|TestMinIOLifecycleVertical|TestS3TwoWriterConflict|TestMountedFileBackendParity'

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
  go test -v "${package}" -run '^TestMountedFileBackendParity$' -count=1 -timeout=10m
}

run_minio() {
  go test -v "${package}" -run '^TestS3TwoWriterConflict$' -count=1 -timeout=20m
  CAMP_TEST_REAL_MINIO_REOPEN=1 go test -v "${package}" -run '^TestMinIOLifecycleVertical$' -count=1 -timeout=60m
}

run_lifecycle() {
  CAMP_TEST_REAL_LIFECYCLE=1 go test -v "${package}" -run '^TestLocalLifecycleVertical$' -count=1 -timeout=60m
  CAMP_TEST_REAL_LIFECYCLE=1 go test -v "${package}" -run '^TestLocalLifecycleCrashMatrix$' -count=1 -timeout=120m
}

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
