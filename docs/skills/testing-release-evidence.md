# Testing and Release Evidence

## Evidence ladder

Run the narrowest affected package first, then the repository gates:

```bash
go test ./... -count=1
go vet ./...
go build ./cmd/camp
git diff --check
```

Report passed, failed, and skipped gates separately. Installed-tool tests may skip without pinned DevPod, Hauler, or `pasta`; those skips leave the real lifecycle unproved. A package test, commit, push, merge, packaged artifact, and deployed release are distinct evidence states.

Hosted Actions admission is a separate state from test execution. When a run
has empty `runner_name` and zero executed steps, record the checks as
runner-admission-blocked/not yet proven; do not interpret their conclusions as
source test failures or successes. Reassess only after a fresh run has an
assigned runner and executed steps.

## Readiness ledger

For a current-state or issue-reconciliation assessment, bind every claim to one
assessed commit and record these states separately: implemented (source exists),
tested (a named local test passes), pushed (the commit is on a remote ref),
merged (the commit is reachable from the assessed base), CI-proven (the exact
commit has a successful hosted check), release-proven (a packaged artifact was
built and verified), and runtime-proven (the real configured lifecycle or
operator path completed). Mark a state missing when its evidence is absent;
never promote source inspection, generated docs, fake adapters, skipped tests,
or a successful build into a stronger state. Keep failed and skipped gates
explicit, including the missing capability and the claim it leaves unproved.
For issue closure, compare the ledger with that issue's own closure rule and
attach the exact command, test, workflow run, artifact, or runtime transcript;
do not close an issue because a dependent implementation merely merged.

For GitHub Actions evidence, record runner assignment and executed steps. A
completed failure with an empty runner name and zero steps is infrastructure
admission evidence only: it proves neither source failure nor source success
and must be rerun against the exact candidate before claiming CI proof.
Inspect each mandatory job conclusion directly while a workflow is running:
the workflow-level `in_progress` state can coexist with an already-completed
failed child job and must never be used as evidence that no gate has failed.

## Durable documentation and release-note boundary

When resuming an interrupted open flow (`completeWorkspaceOpen`) and a session snapshot already contains committed forwarders, Camp now revalidates those records through `Forwarders.Observe` before deciding whether to reuse or restart them. Unit tests should assert both successful observation (no new start) and stale-observation recovery (restarts + replacement records), and treat skipped real-tool gates as still leaving installed-tool evidence incomplete.

Camp-specific policy requires every implementation, review, debugging,
reconnaissance, and verification run to improve the closest canonical guide in
`docs/skills/`. Start from `docs/skills/README.md`; every guide must remain
listed there. Put the guide delta in the same commit or pull request as the work
that established it. The completion receipt in `AGENTS.md` is the review
surface and must identify:

- the canonical file changed or the exact read-only proposal;
- one durable claim and the precise code, test, command result, observed
  failure, or immutable upstream source that supports it;
- stale or ambiguous guidance removed; and
- remaining uncertainty, including skipped environments and unpublished
  release state.

Do not use a skill as a run log. Skills state current reusable procedure,
invariants, recovery, and evidence boundaries. User/operator release notes have
a different job: describe behavior changed since the prior release, migration
or breakage, supported environments, and recovery implications. Internal
refactors and agent-only workflow discoveries stay in skills unless they change
the shipped contract. One change may require both surfaces.

`docs/changelog.md` is Camp's canonical source changelog. Put user-visible
behavior, migration, safety, compatibility, and developer-factory changes under
`Unreleased`; internal-only changes may use an explicit `no-release-note`
classification with a reason in their pull-request receipt. Use
`Release-note classification: docs/changelog.md` when the change updates the
source changelog, or `Release-note classification: no-release-note: REASON`
when it does not. A changelog entry is reviewable
source evidence, not proof that an artifact was packaged, published, installed,
or dogfooded. Do not create dated skill entries as a substitute.

This policy borrows two upstream patterns without inheriting their product
claims:

- `TestCheckpointPublisherRejectsHostileArchiveBeforePointerPublication` in `internal/app/checkpoint_test.go`
  proves hostile-archive input is rejected before pointer publication. The test writes a
  crafted `../escape` archive entry through a checkpoint builder, expects
  `archive.ErrUnsafeArchive`, and asserts no pointer movement (`CurrentPointer` remains on
  the previously committed generation while `RootSnapshotStable` is the only durable pending
  transition.

- Project Bluefin requires same-change skill improvement, one canonical source
  per mutable fact, source checking, and no session diaries. Camp adopts those
  documentation properties, not Bluefin's orchestration or autonomy model.
- RCC makes change visibility executable: its Robot development-process test
  requires `docs/changelog.md` and `common/version.go` in the inspected commit,
  and the built CLI exposes the changelog. Camp adopts the mechanically
  reviewable pairing, not RCC's rule that every change bumps the binary version.

Source evidence:

- [Bluefin agentic contributor guide](https://github.com/projectbluefin/documentation/blob/4bf0eb750d0978ced931919aacaf741ec89f3c6d/docs/agentic-contributing.md)
  and [skill-improvement contract](https://github.com/projectbluefin/bluefin/blob/a6cfecdb791d16e93935bc70f76812231d3b9ef6/docs/skills/skill-improvement/SKILL.md)
- [Bluefin agentic-development post](https://docs.projectbluefin.io/blog/bluefin-agentic-development/)
  and [automated reports/changelogs boundary](https://github.com/projectbluefin/documentation/blob/4bf0eb750d0978ced931919aacaf741ec89f3c6d/blog/2026-02-01-automated-reports-changelogs.md)
- [RCC agent instructions](https://github.com/joshyorko/rcc/blob/2384c4124dadfce48a8eb46cf3fdc3ddebf30e5e/AGENTS.md),
  [developer factory](https://github.com/joshyorko/rcc/blob/2384c4124dadfce48a8eb46cf3fdc3ddebf30e5e/developer/README.md),
  [development-process gate](https://github.com/joshyorko/rcc/blob/2384c4124dadfce48a8eb46cf3fdc3ddebf30e5e/robot_tests/development_process.robot),
  and [source changelog](https://github.com/joshyorko/rcc/blob/2384c4124dadfce48a8eb46cf3fdc3ddebf30e5e/docs/changelog.md)

## RCC developer factory

Camp's canonical contained developer factory is the repository-pinned RCC
wrapper. It supports Linux amd64 hosts; Camp release artifacts still target and
receive native verification on both Linux amd64 and arm64. The wrapper verifies
the exact release and asset SHA-256 declared by the RCC lock before execution,
never falls back to a PATH binary, and creates a private `ROBOCORP_HOME` under
`${XDG_CACHE_HOME:-$HOME/.cache}/camp/rcc-homes` for each invocation. Keep
these homes outside the repository: recursive Go commands otherwise discover
RCC's embedded Go toolchain as part of Camp's module. Tests may override only
the parent with `CAMP_RCC_HOME_ROOT`. `developer/rcc.lock.yaml` is the single
updateable declaration of RCC version, source commit, asset URL, and checksum;
the verified asset checksum is the runtime trust root. Neither the wrapper nor
the contract tests hard-code one permanent RCC release.

```bash
rcc run -r developer/toolkit.yaml --dev -t local
rcc run -r developer/toolkit.yaml --dev -t test
rcc run -r developer/toolkit.yaml -t package
rcc run -r developer/toolkit.yaml -t robot
rcc run -r developer/toolkit.yaml -t robotKubernetes
```

`local` creates one truthfully stamped repository-only `build/camp` and `build/evidence/candidate.json`. `local` stops after repository-only candidate smoke verification. `install` verifies that exact candidate before atomically linking it at `~/.local/bin/camp` so subsequent commands are simply `camp setup`, `camp init`, and `camp open`. Neither task edits shell startup files; Bluefin already includes `~/.local/bin` in PATH. `robot` verifies that candidate digest, asks
the candidate to install the exact DevPod and Hauler assets from
`tools.lock.yaml`, runs named Go evidence directly, then runs black-box Robot
Framework suites against the same executable. Go tests are not hidden inside
Robot keywords. Gate manifests distinguish passed and failed gates; an absent
named test, opt-in skip, Robot skip, missing executable, or candidate mutation
is a failure. RCC `test` builds and verifies its candidate before source gates; every gate ledger has that non-empty candidate SHA-256 and only terminal `passed`, `failed`, `missing`, `skipped`, or `gated` results. A missing mandatory gate is recorded as `missing` and fails the task rather than being hidden by a broad test command. Generated documentation is a gate only when regeneration leaves `docs/generated/` unchanged; a post-generation diff fails it. The RCC and pip Robot Framework declarations are both 7.4.2; that current factory pin supersedes the older 6.1.1 planning reference.

A clean-start rehearsal removes only Camp-owned state. Inventory `camp list
--json` and DevPod workspaces first, run `camp strike --purge --yes` while the
verified CLI still exists, then remove Camp's XDG config/data/runtime roots,
repository `build/` and `dist/`, and `~/.local/bin/camp` only after proving that
it is a symlink to this checkout's `build/camp`. Never delete the shared RCC
Holotree, DevPod provider configuration, unrelated DevPod workspaces, or
unrelated container-engine resources. After cleanup, `local` must run before
`robot` because Robot consumes the exact candidate and manifest produced by
`local`; it never rebuilds them.

The race gate resolves either the conventional `gcc` command or conda-forge's
`x86_64-conda-linux-gnu-cc` compiler shim and passes that exact path through
`CC`, while explicitly setting `CGO_ENABLED=1` for that gate. RCC's surrounding
environment may disable CGO for deterministic cross-builds; a discovered
compiler alone therefore does not prove that `go test -race` can use it.

RCC-backed jobs run alongside the direct Go jobs during parity. The
`parity-evidence` job always writes `build/evidence/parity.json` for the exact
workflow commit and run URL, including every direct and RCC job result. The
mandatory RCC Robot job downloads the `rcc-local` artifact instead of
rebuilding it, verifies the manifest commit and candidate SHA-256, and runs
`robot` against that exact binary. Its always-running artifact upload retains
the Robot gate ledger, Robot XML, log, report, candidate manifest, teardown
observations, and `ci-cleanup-receipt.json`; each path is mandatory and a
missing path fails the evidence job. The downloaded artifact contents are
rooted under `build/`: CI stages `ci-artifact/build/`, uploads the staging
directory's contents, and downloads them into the repository root. Uploading
the files directly would strip `build/` at the artifact boundary. The cleanup receipt is derived from
ownership-checked controller removal followed by an observed absent path; it
is never inferred from a Robot step or job conclusion. A failed or interrupted
Robot run is evidence of failure, not permission to invent a cleanup result.
Its
`qualifiedHistoricalRuns` starts empty: repository tests cannot populate it or
claim hosted parity. Do not remove the direct jobs until two consecutive,
actual complete PR/master runs have passed every recorded mandatory gate; add
those two GitHub Actions run IDs and URLs only after both runs finish.
Do not cache `ROBOCORP_HOME`; the environment is private writable runtime state,
not a verified immutable cache seed.

Parity records translate Actions conclusions into the gate vocabulary
`passed`, `failed`, `missing`, `skipped`, or `gated`; raw values such as
`success` and `cancelled` are not ledger results. Before publication, validate
the requested tag name, fetch only
`+refs/tags/<tag>:refs/camp-release-tags/<tag>` from `origin` with automatic tag
following disabled, peel that fetched private ref to its commit, and require it
to equal `candidate_commit`. This handles annotated and lightweight tags,
refreshes a moved tag instead of trusting a stale local ref, and fails closed
when the exact remote tag is missing or malformed. Only then may publication
run; `gh release create --verify-tag` alone proves that a tag exists, not that
it targets the candidate proven by CI.

Developer workstations use the `rcc` already on PATH and its configured
`ROBOCORP_HOME`; this is the ordinary interactive interface. CI and release
workflows use `developer/rccw` as a non-interactive bootstrap so a clean runner
can verify and execute the repository-declared RCC asset without assuming a
preinstalled binary. Do not require developers to invoke that wrapper.

The local `package` task derives `VERSION=0.0.0-<short-commit>` and the full
`COMMIT` from a clean checkout. Release CI may provide both values explicitly;
providing only one fails closed, and an explicit `COMMIT` must equal the
checked-out `HEAD`. Automatic identity refuses a dirty checkout so
the packaged binary cannot claim clean commit provenance for uncommitted code.

The packaging authority requires a tar implementation that supports GNU
options, including `--sort=name`; BusyBox `tar` is insufficient. A packaging
failure at that option is an environment prerequisite failure, not evidence of
an archive-content defect. Verify `tar --version` before interpreting direct,
RCC `test`, or release-package results.

`robotKubernetes` is a protected, explicitly authorized evidence task. It
requires `CAMP_KUBERNETES_EVIDENCE=1` and the `TestKubernetesLifecycleVertical`
integration test. The test is compiled only with the `kubernetes_evidence` build
tag; `tasks.py` applies that tag to both discovery and execution. Ordinary
`go test ./...` therefore neither runs nor skips the protected vertical, and its
absence from an ordinary run is not Kubernetes evidence. The tagged test enforces
a strict protected contract for candidate identity, provider/profile/context
names, path regularity, and namespace ownership. Missing inputs or unauthorized
context values fail closed with a typed gate reason, and only a fully authorized
vertical can claim Kubernetes support evidence.

### RCC trust and isolation boundaries

- `robot` consumes the existing `build/camp` and candidate manifest from
  `local`; neither the Invoke task nor Robot suites may silently build or choose
  another binary.
- Canonicalize `CAMP_RCC_HOME_ROOT` and reject paths inside the repository.
  Recursive Go commands must never traverse RCC's embedded toolchain.
- The lock's release asset digest is the runtime trust root. Its commit is
  update provenance and must be a full commit ID, but the wrapper cannot prove
  an upstream release asset was built from that commit without separately
  published provenance.
- Preserve digest verification, profile before optimization, and never share a
  writable hardlink between an RCC store and an active environment. These
  boundaries follow the failure modes recorded in `joshyorko/rcc` issue 63.

### Robot dependency contract

Keep the exact Robot Framework version synchronized between
`developer/setup.yaml` and `robot_requirements.txt`; the release-pipeline
contract test rejects drift. Robot Framework 7.4.2 supports the factory's
Python 3.10 runtime: conda-forge publishes 7.4.2 as a noarch package requiring
Python 3.10 or newer. Camp's black-box suites use only Robot Framework's
standard `OperatingSystem` and `Process` libraries. Each additional factory
package expands the verified supply-chain surface, so do not add `rpaframework`
unless a concrete suite requires one of its keywords.

Every Robot test carries one or more stable `req:<ID>` tags registered in
`robot_tests/resources/requirements.json`. The traceability suite rejects
unknown tags, untested `required-now` requirements, and executing
`superseded` requirements. `roadmap-gated` requirements stay visible without
making ordinary PR factory work permanently red; set
`CAMP_REQUIREMENTS_SCOPE=product` to reject a product claim while any remain.
The historical prompt's automatic workspace-engine image discovery is
superseded by explicit publication through `CAMP_REGISTRY`.

The requirements manifest is the holistic traceability registry for the
historical build prompt and approved product-proof plan. Each roadmap entry
names its concrete evidence gate but has no executing Robot test until that
proof exists. PR scope reports the complete roadmap without claiming success;
product scope rejects every remaining gate. Traceability metadata runs without
`CAMP_TEST_BINARY` so evidence-map validation is independent of candidate
execution.

## Generated documentation gate

Generate the command reference, deterministic command transcripts, versioned
presentation examples, and bash/zsh/fish completions from the production Cobra
tree with `go run ./cmd/camp-docs`. Never hand-edit `docs/generated/`.

`go test ./internal/docsgen ./docs -count=1` compares every checked-in generated
artifact byte-for-byte, excludes hidden commands, requires an effect-free
dispatch marker for every visible lifecycle command, executes completion
generation, rejects non-public command mentions in operator docs, and verifies
operator-index links. The command reference must initialize and render Cobra's
built-in help flag for every public command. An exit-zero transcript without the
exact command-specific fixture marker is insufficient dispatch evidence. The
transcript lifecycle proves Cobra parsing and handler dispatch only; it is not DevPod,
Hauler, backend, lifecycle, or release evidence. When the public tree changes,
regenerate and review `git diff -- docs/generated/commands.md` before accepting
the change.
CampKit transcript invocations use Linux `/proc/self/cmdline` only as a stable
regular file that satisfies the CLI's pre-dispatch file guard; the effect-free
fixture does not decode it. Those transcripts prove command wiring, not CampKit
archive integrity or trust.
For `setup` dispatch, the fixture must keep the same Setup signature as
`internal/cli` (`func(context.Context, OutputMode, io.Reader, io.Writer)`), even
when docs execution does not supply stdin.

When a stacked PR's base merges, restack the child directly onto the resulting `master` commit and compare `master...HEAD` before pushing. The post-rebase diff must contain only the child scope; record the old and new head SHAs, then force-push with a lease pinned to the observed old remote head so concurrent updates fail instead of being overwritten.

When several overlapping PRs are intentionally replaced by one integration
PR, start the integration branch at the current remote `master` and merge each
observed PR head with an explicit merge commit in dependency order. Do not use
an octopus merge for overlapping heads: it obscures which parent introduced a
conflict and cannot record an ordered resolution. Before publication, require
`git merge-base --is-ancestor <observed-head> HEAD` for every superseded PR and
review `git diff --stat origin/master...HEAD`; a green combined test run alone
does not prove that every source head was included.

Named acceptance gates require discovery evidence before execution evidence. `go test -run` exits zero even when no matching test exists, so first require the exact test name from `go test -list`, then run with `-v` and retain the matching `=== RUN` and `--- PASS` lines. A package-level `PASS` accompanied by `[no tests to run]` is not acceptance evidence.

```bash
go test ./integration -list '^TestNamedAcceptanceGate$'
go test -v ./integration -run '^TestNamedAcceptanceGate$' -count=1
```

Discovery lists `TestMountedFileBackendParity`, `TestS3TwoWriterConflict`,
`TestMinIOLifecycleVertical`, `TestLocalLifecycleVertical`, and
`TestLocalLifecycleCrashMatrix`. `scripts/verify-real-evidence.sh list`
discovers those names without requiring a candidate. Every execution mode
requires all five names, an executable orchestrator-provided `build/camp`, and
a passing test receipt for every selected gate; an opt-in skip or no-tests
result is a failure. Each selected gate writes its receipt through a uniquely named
`camp-real-evidence.*` temporary file and removes only that file on return, SIGINT,
or SIGTERM. The real lifecycle tests require `CAMP_TEST_BINARY`; they no longer
build independent executables or inspect the host-global DevPod context. Each scenario uses private XDG
config, data, state, cache, and mode-0700 runtime roots plus a unique private
DevPod home, config, SSH config, and non-default context. Before scenario
activity, each initializes the built-in Docker provider with
`devpod provider add docker --context <private-context> --use --silent` under
that private environment and passes `CAMP_DEVPOD_PROVIDER=docker` to Camp. The
Room-of-Requirement remains the devcontainer image fixture, not the DevPod
provider. Before each file or MinIO lifecycle scenario opens Camp's workspace,
the harness creates one unrelated workspace in that same private context. Its
scenario ledger recovers exact Camp workspace IDs, process identities,
cleanup-permitted materializations, verifier-only path identities, and
forwarded endpoints only from durable session
snapshots. The same cleanup path runs after normal completion, controller
failure, or interruption; it closes Camp sessions, deletes only recovered exact
workspace IDs, and verifies every recorded workspace, process, materialization,
path, and listener. A failed `camp close` remains a reported cleanup error while
the harness continues only established fallback cleanup: complete
PID/boot/start process identities use the production process manager and
created materializations use the ownership-marker guard. Forwarding evidence
and scenario runtime paths record device, inode, and exact Linux
regular-file/directory type for verification only. Unprivileged Linux cannot
bind an `fstat`-validated directory entry to a later name-based unlink while a
same-UID writer can mutate the directory, so the harness does not generically
remove those paths. If a recorded path remains or the current entry has a
different identity, cleanup fails and preserves the current entry for explicit
recovery; a live recorded listener likewise fails cleanup and is not stopped by
endpoint alone. Incomplete process or path identities
fail closed rather than being treated as absence. It then proves the unrelated ID remains and deletes that
unrelated ID in its own final step. DevPod workspace IDs are the safe container
cleanup boundary; the harness neither invents a second Docker-container identity
nor treats process namespace observations as independently owned resources. It
does not enumerate or delete ambient Docker, Dagger, RCC, or DevPod resources.
The harness does not reserve or inject Camp service ports: registry and
fileserver assertions use
the recorded forwarding endpoints and prove they are closed after cleanup. This
is harness behavior; it becomes real lifecycle evidence only when the gated
tests pass with the orchestrator-provided `CAMP_TEST_BINARY` and real tools.

The file lifecycle gate drives repeated `camp open` and `camp attach` through
bounded process-group PTYs, then proves that the private DevPod context still
contains exactly the unrelated workspace and the original Camp workspace.
Its file-backend receipt reads the current pointer and immutable generation
sidecar through the production repositories, hashes the archive and sidecar
bytes directly, and proves the earlier generation remains byte-identical after
pointer advancement. Registry evidence accepts a digest only when the
`Docker-Content-Digest` header equals the SHA-256 of a complete single-platform
OCI or Docker manifest body with complete config and layer descriptors; an
index, an incomplete descriptor, or a header/body mismatch fails the gate.

DevPod may change ownership of the unrelated local fixture while creating its
workspace. That fixture therefore lives beneath the private mode-0700 scenario
root but gives the container-owned fixture directory and file enough mode bits
for the owning test process to remove the exact tree after deleting the exact
workspace ID. This is not permission to relax Camp source, materialization, or
verifier-only path ownership rules.

A fresh-controller reopen previously failed intermittently while starting the
second workspace reverse forwarder even after the first controller completed
sync and close. Preserved evidence showed a live replacement `devpod ssh`
process, an empty forwarder log, and an unreachable fileserver endpoint. The
forwarder manager now retains the endpoint probe as the readiness boundary,
replaces one bounded-but-unready process by exact PID/boot/start identity, and
polls the same boundary when adopting a persisted pending registry forwarder.
Focused tests prove the replacement identity is the only committed evidence and
that exhausting both attempts stops both exact processes and removes the owned
evidence path. Preserve bounded forwarder logs and both durable snapshots on
any later failure. The file lifecycle remains unproved until one current
candidate completes the whole gate and exact cleanup after this correction.

These named tests remain executable product gates even while their requirements
are `roadmap-gated`. The RCC `robot` task therefore fails when a current
candidate cannot satisfy them; black-box Robot success alone must not overwrite
that failure. Promote a requirement from `roadmap-gated` only after the same
candidate produces the named real-tool evidence. Hosted CI now makes this
failure-visible Robot task mandatory and retains its exact-candidate evidence;
until the named gates pass, the workflow is expected to stay red and cannot be
used as a release or parity claim.

For filesystem-dependent safety tests, prove determinism with repeated focused execution when practical. The ownership-marker temporary-name substitution test requires injection of the named fallback because a Linux filesystem may support `O_TMPFILE`; the focused test passed 50 repetitions after that injection, and `go test ./internal/capsule -count=1` passed 52 tests.

Supervisor heartbeat tests must synchronize on the complete event being
asserted. The fake lease keeper's `renewed` channel fires inside `Renew`, before
the supervisor records the durable fact and releases its operation lock; using
that signal alone to assert `fact` or `release` is timing-dependent under the
race detector. Wait for the required event-log sequence instead.

## Release gate

Do not describe Camp as released or clean-machine-ready until the packaged binary, locked tools, real local lifecycle, portable backend lifecycle, and required Room/Wolfi/Rust/direct-registry acceptance matrix have concrete passing evidence. The executable is buildable and locally packageable, but that is not release evidence.

The repository CI contract is enforced by `go test ./releasepipeline -count=1`.
Pull-request CI has read-only contents permission and no protected environment,
release write token, attestation token, or secret reference. Its unit, race, vet,
vulnerability, integration, containerized MinIO, real pinned-tool download, and
reproducible-package jobs are mandatory. The MinIO and real-tool jobs name and
run their exact acceptance tests so an absent match or an unset opt-in cannot
turn a skip into release evidence.

Every hosted job selects its Go toolchain from the full patch version in
`go.mod`. Keep that patch at or above the newest standard-library fix required
by `govulncheck`; a language-version-only pin can make otherwise current source
ship reachable vulnerabilities from the runner's selected toolchain.

Credentialed provider runs are separate from credential-free CI. A protected
`release-providers` environment and an explicit named profile are required
before a provider can be claimed. `provider-evidence.yml` is explicit-dispatch
only because its candidate identity and provider selections are required
inputs; it must not pretend a schedule can supply those values. Protected
environment secrets may materialize the authorized kubeconfig and DevPod
configuration, but they must never be used as an `if:` condition that converts
missing authority into a skipped success. The workflow contract parses YAML,
rejects `on.schedule`, and recursively checks every `if` scalar for `secrets.`,
including folded or multiline expressions. The workflow uses strict allowlisted
artifacts and always favors fail-closed evidence generation when secrets,
contexts, or protected inputs are missing. Do not put secret values in evidence
JSON, artifacts, caches, output, plans, or generated config.

Clean-runner CI must not inherit workstation capabilities. Run CLI composition
tests with a PATH containing only the test's declared fakes and the Go/system
tools; `camp init` must not resolve DevPod or Hauler because initialization uses
only the Docker manifest boundary. Linux cancellation tests must spawn a child
process (`sleep 30 & wait`) and prove the whole process group terminates within
the deadline; killing only the shell can leave descendants holding captured
stdout/stderr open until their natural exit.

GitHub's Ubuntu 24.04 runner enables
`kernel.apparmor_restrict_unprivileged_userns=1`. RCC's pinned Pasta executable
lives under a private Holotree path and therefore does not inherit the runner's
path-specific AppArmor allowances. The exact-candidate lifecycle job must
explicitly disable that restriction on its ephemeral runner and prove
`unshare --user --map-root-user true` before invoking RCC; otherwise Pasta exits
before its private PID file and loopback-confined Hauler child become ready.
Keep this authorization scoped to the lifecycle job rather than weakening
Camp's production confinement checks.

Replacement-identity tests must account for immediate inode reuse on GitHub's
Ubuntu filesystems. New materialization and hydration records include Linux
`statx` birth time when the filesystem provides it, in addition to device and
inode. Hydration also records the final directory's Linux `ctime`; this catches
remove-and-recreate replacement when both device/inode and birth time are
unavailable, while ordinary changes to files below the final root do not alter
the root directory's change time. Older records and filesystems without birth
time retain the legacy device/inode comparison, while new records reject a
replacement even when the filesystem reuses the same inode. Cleanup of a
renamed regular file compares
mode, link count, size, and modification time as well as device/inode; change
time cannot be compared across rename because rename itself changes it.

## Generic archive evidence

Run the repository-owned archive builder from the repository root with an
explicit version, full commit, and reproducible timestamp:

```bash
VERSION=0.0.0-test \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
SOURCE_DATE_EPOCH=1784678400 \
OUTPUT_DIR="$PWD/dist" \
./packaging/build-archives.sh
```

The builder requires GNU `tar`, `gzip`, `date`, `sha256sum`, and the Go
toolchain. It produces normalized Linux amd64/arm64 archives plus
`checksums.txt`. Archive order, numeric ownership, timestamps, gzip headers,
Go paths, and VCS stamping are normalized. `go test ./packaging -count=1`
builds twice into isolated directories, compares every output byte, extracts
the amd64 archive, and runs the packaged binary's `--version`, `--help`, and
bash/zsh/fish completion paths.

The generic archive declares `passt`/`pasta` as an external host prerequisite.
`packaging/homebrew/metadata.json` names the intended
`joshyorko/homebrew-tap/Formula/camp.rb` destination, and
`packaging/homebrew/camp.rb.tmpl` records the Linux architecture, checksum,
dependency, completion, and formula-test shape. Its URL, version, and checksum
tokens stay unresolved until a separate publication lane supplies real release
artifacts.

These checks prove reproducible archive and native-package construction. The
non-skipping package fixtures additionally prove clean DEB/RPM/APK and local
Homebrew install, packaged completions, first-use managed-tool bootstrap,
upgrade state preservation, and package-owned uninstall behavior. They do not
prove a GitHub release, a published tap update path, or a real
DevPod/Kubernetes lifecycle.

## Release-candidate evidence

Build the candidate bundle with the same immutable inputs used for generic
archives:

```bash
VERSION=0.0.0-test \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
SOURCE_DATE_EPOCH=1784678400 \
OUTPUT_DIR="$PWD/dist" \
./packaging/build-release-evidence.sh build
```

The bundle contains Linux amd64/arm64 archives, `checksums.txt`, one SPDX 2.3
JSON SBOM per archive, a rendered `camp.rb`, and `evidence.json`. Each SBOM
package checksum must equal the SHA-256 of the final compressed archive, not an
intermediate binary or directory. The evidence manifest records the commit,
version, platform, digest, result, SBOM filename, package result, and every
gated reason.

Verification must happen after artifact download into a fresh job. Run one
native job per supported architecture:

```bash
VERSION=0.0.0-test \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
VERIFY_ARCH=amd64 \
OUTPUT_DIR="$PWD/dist" \
./packaging/build-release-evidence.sh verify
```

Verification checks the downloaded checksum manifest and SBOM digest, extracts
the matching archive, installs its binary into a fresh temporary prefix, and
executes version, help, and bash/zsh/fish completion commands. It emits
`verification-<arch>.json`; both supported architectures must report `passed`.
The release workflow then combines the single package artifact with both native
verification records and runs `packaging/verified_artifacts.py create`. The
resulting `verified-artifacts.json` binds the exact candidate version and
commit plus each archive's architecture, relative path, byte size, SHA-256,
verification-record path and SHA-256, and passed result. Creation fails for a
missing native result, identity/digest mismatch, or an extra release archive.
The release workflow is manual-only and requires the successful CI run ID,
full candidate commit, and exact `build/camp` SHA-256. Before packaging it
checks out that commit, verifies the CI run and its mandatory Robot/parity jobs,
downloads `rcc-candidate-<commit>`, and matches both the manifest identity and
binary digest. Tag pushes cannot supply that evidence identity and therefore do
not start releases. The sealed `verified-release-set-<commit>` is the only archive source for
attestation and publication. Both downstream jobs independently run
`verified_artifacts.py recheck`; neither job invokes RCC, Go, or packaging build
scripts, so a missing, changed, substituted, or newly added archive fails before
any protected side effect.
Checksums stored beside mutable release assets detect accidental or transport
corruption but do not authenticate the publisher. GitHub artifact attestations
bind final archive digests to the workflow identity; protected tag/manual
publication remains downstream of downloaded-artifact verification and
attestation.

A manual release workflow with `publish=false` is the safe hosted validation
lane: it builds, uploads, downloads, checksum-verifies, and natively exercises
both architectures, but skips attestation and publication. Attestation is a
public provenance side effect, so it runs only for an explicitly approved
manual publication. A dry-run artifact proves candidate mechanics; it
does not prove attestation or a published release.

Before closing issue #13, link the successful mandatory CI run, both native
verification jobs, uploaded GitHub artifact digest, archive checksums, exact
SBOM digest bindings, attestations, protected provider evidence for every
claimed profile, and every explicit gated or unsupported lane. A green local
run or draft PR is not closure evidence and must not trigger a real release.

The real Homebrew lifecycle fixture builds two archive versions, serves a
local git tap to the official `homebrew/brew` container, and requires tap,
install, completion, update, upgrade, and uninstall to succeed. It sets
`HOMEBREW_NO_AUTOREMOVE=1` only for uninstall: Camp does not own the shared
`passt` prerequisite, and Homebrew 5.0.13 on Linux can recurse through injected
global dependencies (`bubblewrap`, GCC, and `passt`) while autoremoving them.
The fixture still requires Camp's keg and linked binary to be removed and
verifies operator configuration survives. Run it with GNU tar first on `PATH`;
BusyBox tar cannot build the reproducible input archives because it lacks
`--sort=name` and the other normalization flags. A clean container must also
mark the bind-mounted local tap as a Git safe directory before advancing its
test ref; the fixture removes only Homebrew's test-owned XDG subtree before
the host deletes its temporary root.

The native package lifecycle fixture defaults to Podman and also supports
`CONTAINER_ENGINE=docker`. Managed-tool bootstrap runs as root inside Docker,
so its fallback cleanup removes root-owned fixture state through the same
container engine when the invoking host user cannot remove it directly. A
cleanup permission error after otherwise successful DEB/RPM/APK assertions is
still a failed gate; it must not be reported as package lifecycle evidence.

## Evidence

- `AGENTS.md`
- `cmd/camp/main.go`
- `packaging/build-archives.sh`, `archive_smoke_test.go`, `homebrew_metadata_test.go`, and `homebrew_lifecycle_test.go`
- `packaging/fixtures/homebrew-smoke.sh` (observed against Homebrew 5.0.13)
- `packaging/fixtures/package-smoke.sh` (Podman default and Docker override)
- `packaging/homebrew/metadata.json` and `camp.rb.tmpl`
- `integration/contracts_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
- `internal/app/supervise_test.go`
- `docs/superpowers/plans/2026-07-14-camp.md` (names the currently missing local lifecycle gates)
- `.github/workflows/ci.yml`, `release.yml`, and `provider-evidence.yml`
- `releasepipeline/workflow_contract_test.go` and `evidence_test.go`
- `scripts/kubernetes_evidence.py` and `releasepipeline/kubernetes_evidence_test.go`
- `packaging/build-release-evidence.sh`
