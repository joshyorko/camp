# Codex build prompt — Camp

You are the principal engineer responsible for building **Camp**, a production-quality Linux CLI. Work autonomously from discovery through implementation, verification, packaging, and polished documentation. Do not stop after a plan, scaffold, prototype, or partial vertical slice. Do not label anything “MVP” or “v1.” Do not leave TODO implementations, fake success paths, or hand-waved integrations.

If the current repository is empty, initialize the project there. If it already contains work, inspect it first and preserve compatible decisions. Make reasonable decisions without asking me to design the product for you. Record material choices in ADRs.

## The product

**Mission:** Make any Linux machine become my machine for a while—and become clean again when I leave.

**Slogan:** **Break camp here. Make camp anywhere.**

**Product promise:** One command opens my whole development world. One command packs it back up.

Camp is not a file-sync client, a dotfiles manager, a new container runtime, or a new artifact format. It is a small, beautiful lifecycle CLI around two excellent tools:

- **Hauler** is the capsule: it packs the entire working directory plus OCI images into one versioned `.tar.zst`, loads it, and serves its files and registry.
- **DevPod** is the runtime: it creates the devcontainer on any supported provider and owns SSH, provider lifecycle, credentials, tunnels, port forwarding, and IDE integration.
- **Camp** owns only the lifecycle: resolve → hydrate → serve → enter → checkpoint → seal → publish → clean up.

The authoritative object while Camp is closed is a versioned Hauler archive. The authoritative working tree while Camp is open is the hydrated **Second Brain root**, not an individual repository inside it.

The experience must feel like this:

```console
$ camp open
Making camp from second-brain@42…
✓ Hydrated 238.4 GB
✓ Registry  127.0.0.1:5000
✓ Files     127.0.0.1:8080
✓ DevPod    second-brain.devpod (docker)
Entering Second Brain…

$ camp open memoryd
# The same whole Second Brain is opened; the shell/IDE lands in MemoryD.

$ camp open memoryd --ide vscode-insiders
# DevPod opens VS Code Insiders in MemoryD.

$ camp sync
✓ Checkpoint 43 published; camp remains open

$ camp close
Breaking camp…
✓ Workspace returned
✓ 17 named OCI images captured
✓ Checkpoint 44 verified and published
✓ DevPod stopped and local materialization removed
```

Default behavior must be quiet, legible, and safe. `--verbose` exposes subprocess commands and logs; `--json` gives stable machine-readable output. Errors must say what failed, what remains safe, and the exact recovery command.

## Non-negotiable product truths

1. **The capsule is the whole directory.** If the root is `~/SecondBrain`, everything user-owned beneath it is part of the checkpoint: repositories, notes, virtual environments, Rust `target` directories, generated artifacts, patches, agent state, and nested projects. Do not invent a component graph. Only exclude Camp’s own transient build/runtime paths to prevent recursion.
2. **The Hauler manifest is the content contract.** Keep `.camp/hauler-manifest.yaml` inside the capsule root. Generate and maintain valid Hauler `Files` and `Images` documents. Do not replace it with a custom artifact manifest. Camp may keep small lifecycle metadata beside it, but Hauler remains the packing contract.
3. **One capsule, optional landing directory.** `camp open memoryd` does not fetch a separate MemoryD capsule. It opens the complete Second Brain and lands in the matching child directory.
4. **No background filesystem synchronization.** `camp sync` is an explicit checkpoint. `camp close` is a final checkpoint followed by teardown. Crashes leave recoverable local state.
5. **DevPod owns runtime concerns.** Do not reimplement providers, SSH config, IDE remote support, credential forwarding, or tunnels.
6. **Hauler owns artifact concerns.** Do not invent a private bundle format or a custom registry.
7. **Linux only.** Target Bluefin/Fedora first and portable Linux second. Do not spend time on macOS or Windows compatibility.
8. **The default development image is my Wolfi Room of Requirement image:** `ghcr.io/joshyorko/room-of-requirement:wolfi`. `secure` is a supported alias. Honor an existing devcontainer configuration in the capsule; use Wolfi only as the deterministic fallback. `--image` and DevPod’s normal devcontainer flags can override it.
9. **No destructive cleanup before verification.** Never delete or overwrite the local workspace until the new haul is uploaded, downloaded or remotely checksummed, validated, and the latest pointer is committed.
10. **Do not pretend build cache is an OCI image.** Automatically capture every named workspace OCI image and restore it on the next machine. The Second Brain tree preserves builds such as Rust `target`. Dangling Docker layers and opaque BuildKit cache are not portable images; document that honestly and support explicit OCI cache exports rather than making false claims.

## Source contracts to inspect before implementation

Do not code against memory. Read the pinned source and verify the command contracts with executable contract tests.

### DevPod

Use the maintained community fork **`skevetter/devpod`**, pinned initially to release **`v0.9.11`** / commit **`8a047ebd3f392aed6dc7a90e57d68cc7d5fb0e7b`**. Put the version and checksums in a committed tool lock file; upgrades must be explicit and tested.

Read at minimum:

- `cmd/up.go`
- `cmd/ssh.go`
- `cmd/stop.go`
- `cmd/delete.go`
- `cmd/list.go`
- `cmd/status.go`
- `cmd/export.go` and `cmd/import.go`
- `pkg/config/ide.go`
- `pkg/ide/vscode/*`
- the provider and local-folder workspace documentation

Important verified contracts:

- `devpod up [workspace-path|workspace-name]` supports `--id`, `--provider`, `--context`, `--machine`, `--provider-option`, `--devcontainer-image`, `--devcontainer-path`, `--devcontainer-id`, `--fallback-image`, `--additional-features`, `--mount`, `--workspace-env`, `--workspace-env-file`, `--dotfiles`, `--recreate`, `--reset`, `--prebuild-repository`, `--ide`, `--ide-option`, `--open-ide`, and SSH configuration.
- The exact VS Code Insiders identifier is **`vscode-insiders`**. Its local launcher is `code-insiders`.
- Terminal mode must explicitly use `--ide none --open-ide=false`; an omitted DevPod IDE value may select VS Code.
- A successful workspace exposes the generated SSH host `WORKSPACE_NAME.devpod`.
- `devpod ssh` already supports `--workdir`, `--command`, `-L/--forward-ports`, `-R/--reverse-forward-ports`, agent forwarding, GPG forwarding, environment injection, and terminal behavior.
- For a local path on a remote provider, DevPod uploads the path into the provider. It does not continuously synchronize changes back.
- DevPod’s hidden `export`/`import` commands export DevPod provider/machine/workspace **configuration**, not the changed workspace files. Do not misuse them as the checkpoint transport.

### Hauler

Use Hauler **`v2.0.1`**, pinned to the release commit **`4f47155d6f8ccec22ba6f609f2f1f4919b02fce1`**, with verified release checksums.

Read its v2 source and documentation for:

- `hauler store sync --filename …`
- `hauler store add file …`
- `hauler store add image … --local`
- `hauler store save --filename …`
- `hauler store load --filename …`
- `hauler store extract …`
- `hauler store copy …`
- `hauler store serve registry --readonly=false`
- `hauler store serve fileserver`

Important verified contract: the served registry uses its own Distribution backend. Writes pushed to the writable registry are not magically written back into the original Hauler store. Before sealing, Camp must sync/copy those registry contents into the store that will be saved.

The manifest uses multi-document YAML such as:

```yaml
apiVersion: content.hauler.cattle.io/v1
kind: Files
metadata:
  name: camp-second-brain
spec:
  files:
    - path: .camp/build/second-brain.tar.zst
      name: second-brain.tar.zst
---
apiVersion: content.hauler.cattle.io/v1
kind: Images
metadata:
  name: camp-second-brain-images
spec:
  images: []
```

Camp creates `.camp/build/second-brain.tar.zst` while excluding `.camp/build/` and `.camp/runtime/` from the inner root archive. The manifest itself remains inside the root archive. Camp regenerates the `Images` entries from the currently captured image inventory before `hauler store sync`.

Use `local: true` only for images actually available from the local Docker-compatible daemon. Images produced inside a remote DevPod workspace must be pushed through Camp’s live registry and ingested from that registry over plain HTTP while it is running. Do not put semantically false `local: true` entries in the manifest.

### Room of Requirement

Use **`joshyorko/room-of-requirement`** release **`v1.18.0`** / commit **`0aabf18ad291c590498bd8e904a7d09f66378b85`** as the compatibility fixture. Inspect its root and per-image devcontainer configurations.

Test these published image variants where the registry supports the current architecture:

- `ghcr.io/joshyorko/room-of-requirement:wolfi` — Camp default
- `:secure` — Wolfi alias
- `:latest`
- `:ubuntu-noble`
- `:ubuntu-noble-dind`
- `:codespaces`
- `:debian-trixie`

The Wolfi devcontainer is privileged and includes a nested Docker workflow. Exercise it; do not merely confirm that the image pulls.

## User-facing command contract

Implement a cohesive command tree with shell completions and excellent `--help`:

```text
camp open [target]
camp attach [target]
camp sync
camp close
camp status
camp list
camp history
camp recover [checkpoint|session]
camp init [root]
camp config …
camp doctor
camp serve [status|logs|restart]
camp images [list|capture|restore]
camp provider …
camp devpod -- …
camp hauler -- …
```

Use Cobra in Go unless the repository already has an equally strong Go CLI foundation. Produce a single `camp` binary. Keep orchestration behind interfaces so subprocesses, clocks, ports, storage, and failure points are testable.

### `camp open [target]`

`target` is a landing directory, resolved in this order:

1. an absolute path beneath the capsule root;
2. a path relative to the capsule root;
3. a unique directory basename beneath the capsule root;
4. zoxide query output, accepted only when it resolves beneath the capsule root.

Ambiguity is an error with a short candidate list. Do not silently choose the wrong directory. `camp open` without a target lands at the root.

On an uninitialized machine:

- Read `~/.config/camp/config.yaml` and `CAMP_*` environment overrides for the default capsule and backend.
- If given a local directory that has no `.camp`, automatically adopt the nearest intended root, create `.camp/hauler-manifest.yaml`, generate the fallback devcontainer overlay if needed, and open it. `camp init` remains available for explicit setup but is not a ritual required before the obvious first `camp open ~/SecondBrain`.
- If the default remote already exists, download its `latest.json` and current haul.
- If neither a local root nor a configured remote exists, fail with one exact setup command; do not launch a long wizard.

Opening performs this state machine, durably journaled after each transition:

1. Acquire the capsule writer lease unless `--readonly` was supplied.
2. Fetch and verify `latest.json` and the selected `.tar.zst`.
3. `hauler store load` into a session-specific local store.
4. Extract the complete Second Brain archive into `~/.local/share/camp/work/<capsule>/root` using an atomic temporary directory and rename.
5. Start the Hauler fileserver and writable registry bound to loopback. Use configurable preferred ports, select free ports safely, record them, and supervise both processes.
6. Create/reuse a deterministic DevPod workspace ID and run `devpod up` on the hydrated local root.
7. If the provider is remote, keep independent `devpod ssh -R` forwarding sessions alive so the workspace can reach the local Camp registry and fileserver. Inject `CAMP_REGISTRY`, `CAMP_FILESERVER`, `CAMP_CAPSULE`, and `CAMP_CHECKPOINT` with DevPod workspace environment flags. Do not write credentials into the capsule.
8. Restore captured OCI images into the workspace engine and retag their original names.
9. No IDE flag: explicitly bring the workspace up with `--ide none --open-ide=false`, then enter it with `devpod ssh --workdir <resolved-target>`. Attach to a deterministic tmux session when tmux is present; fall back to the shell without making tmux mandatory.
10. With `--ide <name>`: keep IDE server installation, tunneling, and SSH ownership in DevPod. Support `--ide vscode-insiders` exactly and accept `--insiders` as a friendly alias. DevPod’s current `up` command opens the configured container workspace root and does not expose a nested-folder override. Therefore, when the requested target is below the root for a VS Code-family IDE, run `devpod up` with that IDE and `--open-ide=false`, then invoke the matching local launcher with the same `vscode-remote://ssh-remote+<workspace>.devpod/<absolute-target>` URI shape used by DevPod’s own `pkg/ide/vscode/open.go`. For Insiders the launcher is `code-insiders`. This is a narrow landing-folder adapter over DevPod’s generated SSH host, not a second SSH implementation. Root opens and IDEs without a target override continue through DevPod normally. Do not start a redundant interactive SSH shell.

Expose the commonly used DevPod flags directly and faithfully: `--provider`, `--context`, `--machine`, repeated `--provider-option`, `--id`, `--image`, `--devcontainer-path`, `--devcontainer-id`, `--prebuild-repository`, `--mount`, `--workspace-env`, `--workspace-env-file`, `--dotfiles`, `--recreate`, `--reset`, `--ide`, and repeated `--ide-option`. Add `--devpod-arg` and a documented `--` escape hatch for forward compatibility. Reject conflicts instead of constructing surprising argv.

`--readonly` hydrates and opens normally but never acquires a writer lease, never publishes, and makes `sync`/`close` skip checkpoint creation while still tearing down safely. `--branch <name>` creates a new named checkpoint lineage rather than pretending to merge files.

### `camp attach [target]`

- With no IDE: use `devpod ssh <workspace-id> --workdir <resolved-target>` and tmux attachment when available.
- With `--ide`: reopen through DevPod. Apply the same narrow VS Code-family nested-folder adapter described above when a child target was requested; do not implement a second SSH transport or editor server.
- Forward DevPod SSH flags for local/reverse ports, user, environment, agent forwarding, GPG forwarding, and terminal behavior.

### Live registry and image capture

While Camp is open, its Hauler registry and fileserver are live. Show their addresses in `camp status` and expose them to the workspace.

On `camp images capture`, `camp sync`, and `camp close`:

1. Detect the workspace engine (`docker`, then `podman`; make the detector extensible).
2. Enumerate every **named/tagged** local image in the DevPod workspace. Ignore dangling images. Preserve the original reference, digest, platform, and creation metadata in `.camp/images.json`.
3. Push each captured image into a deterministic private namespace in Camp’s writable registry through the DevPod reverse tunnel. Avoid collisions by encoding the original registry/repository/tag and verifying the digest after the push.
4. Regenerate the `Images` portion of `.camp/hauler-manifest.yaml` from that inventory, with valid source semantics.
5. Ingest/copy the writable registry backend into the fresh Hauler store before saving it.

On open, serve those images, pull them into the workspace engine, verify digests, and retag their original names. Make repeated capture/restore idempotent. An explicit image exclude list and label-based opt-out are allowed, but the default is all named images because Camp is intentionally a personal junk registry.

Support direct user pushes to `$CAMP_REGISTRY`. Before sealing, enumerate the registry catalog as well as the workspace engine so manually pushed images are retained.

### `camp sync`

`sync` is a beam-up checkpoint that leaves the workspace, DevPod, registry, fileserver, and forwarding sessions running.

- For the local DevPod provider, the hydrated local tree is already the working tree. Do not copy it onto itself.
- For a remote provider, use the SSH host configured by DevPod (`<workspace-id>.devpod`) and `rsync` to mirror the complete remote workspace root back to the hydrated local root. Preserve symlinks, permissions, hardlinks, dotfiles, and deletes. Use a tar-over-SSH fallback when rsync is unavailable. Never write a custom SSH transport.
- Query the effective remote workspace folder rather than assuming `/workspaces/<name>` when an existing devcontainer overrides it.
- Capture OCI images, rebuild the inner root archive, regenerate the Hauler manifest, create a fresh Hauler store, run Hauler sync/copy, save one generation `.tar.zst`, compute SHA-256 and size, upload it, verify it remotely, and conditionally update `latest.json`.
- Refresh the live served store only after the new generation is safe. Existing clients must not see a half-written registry.

### `camp close`

`close` means final sync plus clean teardown:

1. Refuse concurrent close/sync operations with a clear lock owner.
2. Return the authoritative remote workspace to local staging.
3. Capture images and publish a verified checkpoint.
4. Stop or delete the DevPod workspace according to configuration. Default to delete after a verified checkpoint because Hauler is authoritative; `--keep-workspace` stops it instead.
5. Stop forwarding sessions, Hauler registry, and fileserver.
6. Release the remote lease.
7. Remove the local hydrated root and session store only after every prior safety condition passed.

If anything before verification fails, leave the workspace and journal recoverable and print `camp recover <session-id>`. If cleanup alone fails after publication, report the successful checkpoint separately from the cleanup failure.

### Recovery, concurrency, and generations

Implement local journaling and remote optimistic concurrency without turning Camp into a distributed database.

Remote layout:

```text
<backend>/<capsule>/latest.json
<backend>/<capsule>/generations/<generation>-<sha256>.tar.zst
<backend>/<capsule>/leases/writer.json
<backend>/<capsule>/branches/<branch>/latest.json
```

`latest.json` contains schema version, capsule, branch, generation, object key, archive SHA-256, size, parent generation/digest, creation time, tool versions, and session ID.

- A writer lease has a session ID, machine, opened generation, creation time, expiry, and heartbeat. A second writer may refuse, open read-only, or create a branch. Never silently steal a live lease.
- The final latest-pointer update must compare against the generation/digest opened by the session. If it changed, preserve the uploaded generation but do not move the pointer; tell the user to branch or recover.
- `camp recover` resumes from the durable journal, an unpublished verified local haul, or an uploaded generation whose pointer was not updated.

Implement backends for:

- `file://` directories, including a mounted Longhorn PVC;
- S3-compatible object storage, including MinIO, with endpoint, region, bucket, prefix, path-style, TLS, and credential-chain support.

Keep the backend interface narrow and fully tested. Secrets live in environment/credential stores or a mode-0600 host config, never in the haul.

## Configuration and state

Use XDG paths:

```text
~/.config/camp/config.yaml            # user defaults, mode 0600 when secrets exist
~/.local/share/camp/work/             # hydrated roots
~/.local/share/camp/stores/           # Hauler stores
~/.local/share/camp/sessions/         # journals and PIDs
~/.cache/camp/                         # downloads and disposable cache
```

Inside the capsule:

```text
.camp/hauler-manifest.yaml            # authoritative Hauler content contract
.camp/images.json                      # captured image inventory and original-name mapping
.camp/capsule.yaml                     # minimal stable capsule metadata
.camp/lock.yaml                        # resolved image/tool compatibility inputs
.camp/build/                           # transient; excluded from inner archive
.camp/runtime/                         # transient; excluded from inner archive
```

Keep configuration precedence conventional and documented: flags → environment → capsule config → user config → defaults. Implement `camp config show --effective --redact`.

Resolve the Room of Requirement Wolfi tag to a digest during initialization and record it in `.camp/lock.yaml`. Provide an explicit update command; do not silently drift the environment. Existing capsule devcontainer files take precedence unless the user passes an override.

## Installation and dependency policy

Camp vendors **CLIs as managed tools**, not their codebases as rewritten libraries.

- Prefer compatible `hauler` and `devpod` binaries already on `PATH` when they satisfy the lock.
- Otherwise install pinned, checksum-verified Linux amd64/arm64 binaries under Camp’s data directory through `camp setup`/automatic first-use bootstrap.
- Never curl-pipe-shell.
- Make `camp doctor` verify Linux, architecture, storage backend, Hauler, the pinned DevPod fork, SSH, rsync/tar, tmux (optional), zoxide (optional), a compatible local container engine/provider, ports, backend credentials, and Room image reachability.
- Add a Homebrew formula suitable for a tap, Linux packages/release archives, checksums, SBOM generation, and shell completions for bash, zsh, and fish.
- `brew install camp` followed by `camp open …` must be the intended path. Do not require me to manually install a pile of subordinate tools when Camp can provision its pinned CLIs safely.

## Engineering quality

Build the lifecycle as an explicit state machine with idempotent transitions. Use structured subprocess execution—never shell-concatenated user input. Redact secrets from logs. Validate all extracted archive paths against traversal and symlink escapes. Bind the personal registry/fileserver to loopback by default. Use strict file permissions for credentials, journals, and leases. Handle SIGINT/SIGTERM without publishing corrupt state.

Write tests before or alongside each behavior, including:

### Unit and contract tests

- target/path/zoxide resolution and ambiguity;
- configuration precedence and redaction;
- exact DevPod argv for terminal, VS Code Insiders, providers, SSH forwarding, attach, stop, and delete;
- exact Hauler argv and valid multi-document manifest generation;
- lifecycle transition and retry idempotency;
- generation naming, checksums, pointer compare-and-swap, leases, branches, and recovery;
- image reference encoding, capture inventory, registry catalog merge, and restore retagging;
- path traversal, symlink, permissions, signal, and subprocess-failure cases;
- fake `devpod`, `hauler`, `ssh`, `rsync`, `docker`, and `podman` executables that record argv and inject failures.

### Integration tests

- real Hauler: file + images → store → save → load → extract → serve;
- prove a push into the writable served registry is absent from the source store until Camp copies/syncs it back, then present after save/load;
- real DevPod local provider: hydrate, `up --ide none`, generated SSH host, attach, edit, sync, close, reopen, and verify bytes;
- VS Code Insiders command path using a fake `code-insiders` opener while retaining real DevPod setup;
- remote-provider contract using an SSH fixture that proves changes return to staging; paid cloud providers remain opt-in;
- local filesystem and MinIO/S3 backends, including interrupted multipart upload and conditional pointer conflict;
- crash injection after every lifecycle transition, especially after upload but before pointer update and after pointer update but before cleanup.

### Room of Requirement matrix

Exercise the real published image tags listed above. For Wolfi, create a source file in the Second Brain, start through DevPod, run a shell command over DevPod SSH, build and tag a small nested Docker image, close Camp, open on a clean fixture, restore the image, run it, and verify the source file. Test Rust continuity by compiling a small crate into a `target` directory, closing, reopening, and proving the artifact remains usable without a clean rebuild.

Heavy registry/provider tests may be separate CI jobs, but they must exist, be documented, and run in the release workflow. Do not replace them with mocks.

Run formatting, linting, unit tests, integration tests available in the environment, race tests, vulnerability scanning, and release-build smoke tests. Report anything that truly requires unavailable external credentials, but finish everything that does not.

## Documentation quality

The README/docs page should feel intentional, compact, and beautiful—not like generated option dumps. Write it around the product’s point of view.

Required structure:

1. Hero: **Camp** / **Break camp here. Make camp anywhere.**
2. One-sentence promise and a short terminal demo.
3. “Why Camp exists”: not sync, not backup, not a pet VM; a disposable workstation capsule.
4. Three-command quick start.
5. `open`, nested target, terminal default, and VS Code Insiders examples.
6. How Hauler and DevPod divide responsibility.
7. Live registry/fileserver and image checkpoint examples.
8. Providers: local, SSH, Kubernetes, and cloud are DevPod providers, not Camp plugins.
9. Failure and recovery guarantees.
10. Configuration examples for MinIO and a Longhorn-mounted `file://` backend.
11. Security/trust model and what is deliberately not captured.
12. Complete command reference generated from the real CLI.

Include one compact architecture diagram and polished terminal transcripts. Keep the first screen focused on intent, not implementation trivia. Add `docs/architecture.md`, ADRs, troubleshooting, backend configuration, release/install docs, and contribution guidance. Ensure every documented command is executed in docs tests or golden tests.

## Delivery process

1. Inspect the workspace and the pinned upstream sources.
2. Write a short implementation plan and ADRs, then immediately execute the plan.
3. Build complete vertical behavior, not disconnected packages.
4. Continuously run focused tests and preserve user changes in the repository.
5. Perform an independent final audit against every requirement in this prompt.
6. Finish with a concise report containing architecture, files changed, exact verification results, any credential-gated tests, installation command, and a copy-paste smoke test.

You are done only when I can install Camp on Linux, point it at my Second Brain and a file/MinIO backend, open the whole capsule through DevPod, land in any named child directory, use a terminal or VS Code Insiders, build named OCI images, sync or close into a verified Hauler generation, erase the local workspace safely, and open the same files and images again on a clean machine.
