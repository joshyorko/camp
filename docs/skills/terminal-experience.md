# Terminal Experience

Camp's campsite presentation is a view of authoritative state, not a progress simulation. Populate toolchain values from the embedded tool lock and resolved binaries, runtime values from the persisted DevPod provider/context and observed provider kind, capsule values from the effective sanitized configuration, and storage values from the resolved backend plus committed generation state. Missing fields are errors; never invent a percentage, provider, context, generation, or readiness claim to complete the picture.

## Visual authority

The approved art direction for the first-run experience is the Camp campsite artwork under `docs/assets/` (the `camp-hero` composition and its labeled variant). Those raster images are the **visual authority**: a night camp with a dense starfield, layered mountain ridges, topographic contours, a pine forest, a glowing connected trail, and illustrated toolchain/runtime/tent/campfire landmarks with the four setup stages attached to the trail. The terminal experience is a faithful **terminal-native realization** of that art — not a pixel reproduction, and never a reduction to a handful of dots and triangles. Acceptance is tied to visual comparison against those references (see Capture workflow), not merely to a renderer that produces stable bytes.

## Architecture

The interactive rich path is a single [Bubble Tea](https://charm.land) program (`internal/setupui`), one long-lived model from the first configuration prompt through `CAMP IS READY`. It follows the event-loop architecture proven by the MIT-licensed Basecamp/ONCE UI:

- **One model, typed messages.** `setupui.Model` owns terminal width/height, the current phase (configure, provision, ready, failed, canceled), the config form, and authoritative waypoint state. It reacts only to typed messages; it runs no setup operations itself.
- **Real events, no timers.** Provisioning state advances only from messages a `setupui.Pipeline` emits as real setup milestones complete (`ConfigAcceptedMsg`, `WaypointCompletedMsg`, `WaypointFailedMsg`, `AllReadyMsg`). There are no timers, sleeps, synthetic percentages, or cursor animation. All lifecycle logic (config validation, persistence, tool resolution, journal reads) lives in the CLI package's pipeline, never in presentation.
- **Composited cell grid.** The scene is one composited grid (`Canvas`) that layers, back to front: a deterministic starfield, mountain ridges, topographic contours, a pine forest, a glowing beaded trail whose nodes carry per-stage state, illustrated landmarks anchored to the trail, waypoint labels and metadata, and a foreground band (config form, or the ready/failure closing composition). There is no clear-and-print loop and no visually detached fragment — every element shares one buffer, and the composer emits each row with color runs coalesced and no trailing whitespace.
- **Landmark art is checked-in.** Landmarks (crate, Kubernetes helm, canvas tent, campfire, sunrise, pines) are authored as JSON sprites under `internal/setupui/assets/sprites/`, embedded with `go:embed` and consumed by the Go compositor. The same JSON is the source the review tooling renders, so there is one source of truth — no Python-only artwork.

## Responsive compositions

The scene always uses the full terminal width; there is no fixed column ceiling. Height selects a composition tuned to the space:

- **80×24** — compact but still recognizable: the mountains, forest, trail, tent, fire, and four-stage journey remain legible; decorative metadata callouts are trimmed to fit.
- **120×40** — the full primary experience.
- **160×48 and wider** — the extra width is spent on a longer, more sinuous trail and denser landscape, never a small island surrounded by dead space.

A `SizeGuard` shows a legible "terminal too small" prompt if a live resize shrinks the window below the supported minimum, rather than clipping the scene. `WindowSizeMsg` is handled continuously, so resizing reflows the scene without restarting Camp.

## Terminal lifecycle and restoration

The rich path runs in the alternate screen. Bubble Tea restores the terminal — leaves the alternate screen and shows the cursor — on every exit path: normal quit, error, EOF, Ctrl-C, and a panic inside the program. The shell is never left with the scene above the prompt or a hidden cursor. The cursor is visible only while a configuration field is focused (the focused text input is the visible focus indicator); provisioning, ready, and failure frames show no cursor. Cancellation writes no partial configuration.

## Fallback boundary

Terminal selection is fail-closed. The rich interactive path is entered only for human output on an actual true-color TTY with a real keyboard (`COLORTERM=truecolor`/`24bit`, non-dumb `TERM`, readable width ≥ 80, no `CI`/`NO_COLOR`, and input that is itself a terminal). Every other path keeps the deterministic, control-free, line-based output byte-for-byte: JSON, redirected output, non-TTY, piped input, `NO_COLOR`, `TERM=dumb`, `CI`, and terminals below the supported size. Rich-mode ambition never weakens plain-mode reliability or lifecycle truth.

Presentation metadata is untrusted at the rendering boundary. Reject control characters and credential-bearing URLs before writing any bytes (`setupui.SafeText`, mirrored by the campsite sanitizer). Render only already-sanitized backend/source identities; never display credentials, raw access tokens, secret query values, or environment contents. On failure, preserve the exact sanitized cause and print exactly one sanitized recovery command; never both a readiness claim and a failure.

## Lifecycle transcripts (non-setup)

Compact lifecycle output for open/sync/close is emitted only after the application result proves the corresponding fact, as event frames rather than percentages or elapsed-time claims. On sync/close failure, preserve the original error and print exactly one `camp recover <session>` command; if an open failure occurs before a durable session identity exists, do not invent a recovery command.

Deterministic plain transcript shape:

```text
sync: published generation 42
sync: sync complete
```

Failure transcript shape:

```text
error [lifecycle_failed]: checkpoint upload failed
next: camp recover session-1
```

## Capture workflow

Visual review is part of acceptance; passing goldens is not sufficient by itself.

Capture generation is driven by `tools/ansishot/capture.sh` and is development-only:

- `capture.sh` builds and runs a development `internal/setupui/scenerun` binary.
- It exercises `setupui.Run` plus the real `Model`/`Pipeline` composition and terminal lifecycle through a PTY.
- It does **not** run `ProductionLifecycle` logic or machine/provider config; it only replays scripted interactive steps.
- There is no tooling-sprite tree used by the capture workflow. Sprite validation/checks receive JSON paths directly from `internal/setupui/assets/sprites/*.json`.

The canonical sprite source for review is `internal/setupui/assets/sprites/`, embedded in the Go compositor and passed directly to review tooling.

Exact tracked capture set: nine PNGs under `docs/assets/setup-scene/`
`configure-80x24`, `configure-120x40`, `ready-80x24`, `ready-120x40`, `ready-160x48`, `progress-120x40`, `failure-120x40`, `resize-120x40-to-160x48`, `cancel-restored-shell`.

Execution artifacts and scratch files are in `.scene-captures/` (`*.keys`, `*.raw`, and transient renders) and should stay untracked.
PTY streams are byte artifacts: capture and load them in binary mode. Text-mode universal-newline handling removes carriage returns from CRLF and invalidates cursor replay.

Acceptance steps:

- regenerate all captures end-to-end from `capture.sh`;
- compare the stable alternate-screen grid against references built from the same fixture facts (reference-row diff for each state capture);
- **For 80x24 captures, all 24 reconstructed rows must match fresh references from `python3 tools/ansishot/test_vtgrid.py`** (which regenerates configure/ready scene baselines and compares all rows).
- visually inspect every resulting PNG;
- verify resize capture retains typed input after the resize sequence;
- verify the cancel capture shows alt-screen exit, cursor visibility restoration, binary exit (`scenerun exit`), and shell prompt return.
- `capture.sh` executes `python3 tools/ansishot/test_vtgrid.py` as part of the workflow; a workflow is incomplete if that validator fails.

Prerequisites for reproducible captures: Go, Bash, Python 3, Pillow, PTY tooling, and stable terminal fonts.
Because fixed sleeps and font/pixel variance can shift raster output, semantic validation is required; do not treat byte-identical PTY replay output as a sufficient pass.

## Golden coverage

Golden and invariant coverage lives under `internal/presentation/testdata/` (plain/JSON/lifecycle fallbacks) and `internal/setupui/` tests (scene composition invariants: sprites rectangular, frames never exceed the requested width/height across supported sizes, no trailing whitespace, ready/progress/failure/configure semantics, sanitization, and model state transitions). Capability-matrix tests bind JSON, CI, non-TTY, `TERM=dumb`, `NO_COLOR`, narrow width, and missing true-color declaration to the plain fallback.
