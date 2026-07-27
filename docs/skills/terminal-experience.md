# Terminal Experience

Camp's campsite presentation is a view of authoritative state, not a progress simulation. Setup reads machine defaults only: backend, workspace provider/context, ports, and non-secret S3 compatibility settings. Camp identity and source come from a validated `.camp/camp.yaml`, never machine config. Populate toolchain values from the embedded tool lock and resolved binaries, runtime values from the selected manifest or journal, and storage values from the resolved backend plus committed generation state. Missing fields are errors; never invent a percentage, provider, context, generation, or readiness claim to complete the picture.

## Visual authority

The approved art direction for the first-run experience is the Camp campsite artwork under `docs/assets/` (the `camp-hero` composition and its labeled variant). Those raster images are the **visual authority**: a night camp with a dense starfield, layered mountain ridges, topographic contours, a pine forest, a glowing connected trail, and illustrated toolchain/runtime/tent/campfire landmarks with the four setup stages attached to the trail. The terminal experience is a faithful **terminal-native realization** of that art — not a pixel reproduction, and never a reduction to a handful of dots and triangles. Acceptance is tied to visual comparison against those references (see Capture workflow), not merely to a renderer that produces stable bytes.

## Architecture

The interactive rich path is a single [Bubble Tea](https://charm.land) program (`internal/setupui`), one long-lived model from the first configuration prompt through completion. First-run setup collects camp root/name and machine defaults, prepares tools, writes the manifest, and initializes the capsule without leaving the TUI. Direct `camp init` remains available for additional camps and scripts. Both workflows provide their fields and waypoint labels to the same `setupui.Model`. It follows the event-loop architecture proven by the MIT-licensed Basecamp/ONCE UI:

- **One model, typed messages.** `setupui.Model` owns terminal width/height, the current phase (configure, provision, ready, failed, canceled), the workflow-defined config form, and authoritative waypoint state. It reacts only to typed messages; it runs no setup or initialization operations itself.
- **Real events, honest animation.** Provisioning state advances only from messages a `setupui.Pipeline` emits as real setup milestones complete (`ConfigAcceptedMsg`, `WaypointCompletedMsg`, `WaypointFailedMsg`, `AllReadyMsg`). The only timer is the presentation-only 33 ms starfield tick; it never advances lifecycle state or invents progress. All lifecycle logic (config validation, persistence, tool resolution, journal reads) lives in the CLI package's pipeline, never in presentation.
- **Composited cell grid.** The scene is one composited grid (`Canvas`) that layers, back to front: a model-owned animated 2×4 Braille starfield adapted from Basecamp/ONCE, mountain ridges, topographic contours, a pine forest, a glowing beaded trail whose nodes carry per-stage state, illustrated landmarks anchored to the trail, waypoint labels and metadata, and a foreground band (config form, or the ready/failure closing composition). The starfield uses a fixed seed for deterministic captures while its depth advances during the live rich TTY session. There is no clear-and-print loop and no visually detached fragment — every element shares one buffer, and the composer emits each row with color runs coalesced and no trailing whitespace.
- **Landmark art is checked-in.** Landmarks (crate, Kubernetes helm, canvas tent, campfire, sunrise, pines) are authored as JSON sprites under `internal/setupui/assets/sprites/`, embedded with `go:embed` and consumed by the Go compositor. The same JSON is the source the review tooling renders, so there is one source of truth — no Python-only artwork.

## Responsive compositions

The scene always uses the full terminal width; there is no fixed column ceiling. Height selects a composition tuned to the space:

- **69×20** — narrow integrated-terminal floor: the full scene remains bounded and recognizable, with decorative metadata trimmed to fit.
- **80×24** — compact layout with more room for the mountains, forest, trail, tent, fire, and four-stage journey.
- **120×40** — the full primary experience.
- **160×48 and wider** — the extra width is spent on a longer, more sinuous trail and denser landscape, never a small island surrounded by dead space.

A `SizeGuard` shows a legible "terminal too small" prompt if a live resize shrinks the window below the supported minimum, rather than clipping the scene. `WindowSizeMsg` is handled continuously, so resizing reflows the scene without restarting Camp.

## Terminal lifecycle and restoration

The rich path runs in the alternate screen. Bubble Tea restores the terminal — leaves the alternate screen and shows the cursor — on every exit path: normal quit, error, EOF, Ctrl-C, and a panic inside the program. The shell is never left with the scene above the prompt or a hidden cursor. The cursor is visible only while a configuration field is focused (the focused text input is the visible focus indicator); provisioning, ready, and failure frames show no cursor. Cancellation writes no partial configuration.
`tab` and `shift+tab` move between fields, `enter` validates or advances, `esc` returns to the previous field and cancels only from the first field, `?` toggles the keyboard overlay, and `ctrl+c` or EOF cancels. Typed `ActivityMsg` values display the current real operation while leaving the active waypoint incomplete; only the corresponding completion message advances it.
Rich setup uses the event-free production tool setup helper and emits its own toolchain completion only after both managed tool resolutions return successfully.
Rich init maps the production initializer's real events to `MANIFEST`, `CAPSULE`, `RUNTIME`, and `READY`: `Writing camp manifest…` and `Initializing capsule…` are activity only, while the post-fsync manifest fact and successful capsule initialization advance their respective waypoints. Plain init prints the same events append-only; explicit noninteractive and JSON init retain their existing final-output contracts.
Static ready renders (for deterministic re-runs) are intentionally printed with one trailing row left for the shell and terminate with a trailing newline so the prompt and cursor appear cleanly on the next line.

## Fallback boundary

Terminal selection is fail-closed. The rich interactive path is entered only for human output on an actual true-color TTY with a real keyboard (`COLORTERM=truecolor`/`24bit`, non-dumb `TERM`, readable size ≥ 69×20, no `CI`, and input that is itself a terminal). Every other path keeps the deterministic, control-free, line-based output byte-for-byte: JSON, redirected output, non-TTY, piped input, `TERM=dumb`, `CI`, and terminals below the supported size. `NO_COLOR` is not a terminal-capability signal and does not suppress the rich path. Rich-mode ambition never weakens plain-mode reliability or lifecycle truth.

The full-screen setup form appears whenever human setup cannot discover a camp from the current directory, even when machine defaults already exist. This prevents machine preparation from stranding the user at a second required command. Its first fields are **Camp root** and **Camp name**; **DevPod context** is explicitly the named DevPod configuration context, not a project directory. From an initialized camp, setup verifies tools and renders the existing camp without rewriting its manifest. Additional camps can use `camp init [root]`, whose rich workflow uses the same terminal-capability selection.

Presentation metadata is untrusted at the rendering boundary. Reject control characters and credential-bearing URLs before writing any bytes (`setupui.SafeText`, mirrored by the campsite sanitizer). Render only already-sanitized backend/source identities; never display credentials, raw access tokens, secret query values, or environment contents. On failure, preserve the exact sanitized cause and print exactly one sanitized recovery command; never both a readiness claim and a failure.

The rich path requires the minimum terminal floor of `69 × 20`; smaller screens do not engage rich mode and must fall back to deterministic plain output. This floor covers common split and integrated terminals, including the verified `69 × 23` VS Code terminal case.

Rich-mode cancellation is terminal-normal: the shell is restored without failure semantics, and no recovery command is emitted.

When rich-mode provisioning fails, `camp setup` maps to the same lifecycle failure shape as the line-mode flow and reports exactly one recovery command.

## Lifecycle scene event contract

The reusable lifecycle scene consumes `presentation.RichLifecycleEvent`, not subprocess output. Its event kind makes activity, completion, terminal success, and failure distinct: activity may activate a stage and display its exact safe detail, but only a typed completion changes that waypoint to complete. A failure marks its typed stage failed, preserves one safe recovery command, and never renders a ready band. Unknown stage identifiers have no visual label and therefore cannot fabricate authoritative progress.

The stable lifecycle stage IDs and labels are `hydrate`, `services`, `devpod`, `attach`, `mirror`, `image-capture`, `archive`, `upload`, `pointer`, `cleanup`, and `recovery`. The campsite still has four landmark slots, so the model shows the active four-stage window while retaining state for the complete sequence; the final window uses a non-progress `COMPLETE` slot only to fill unused scenery. Lifecycle adapters must emit these typed facts directly from application boundaries, and must not derive them by matching command output.

`internal/setupui/scenedump` can render deterministic lifecycle sample states (`lifecycle-progress`, `lifecycle-ready`, and `lifecycle-failure`) for review. The tracked images in `docs/assets/lifecycle-scene/` are compositor review captures, not black-box CLI acceptance evidence. `TestLifecycleSceneGoldens` protects deterministic 80×24, 120×40, and 160×48 progress, ready, and failure frames; CLI capability selection and exact-candidate Robot captures remain separate integration gates.

The Bubble Tea UI context and provisioning-worker context are separate. User exit cancels provisioning without killing Bubble Tea's restoration path, and the CLI does not return until a started worker has actually exited. Cancellation before submission is terminal: it closes the never-started completion and message streams, prevents a late start from launching effects, and repeated starts share the original message stream rather than creating an unclosed one.

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

Golden and invariant coverage lives under `internal/presentation/testdata/` (plain/JSON/lifecycle fallbacks) and `internal/setupui/` tests (scene composition invariants: sprites rectangular, frames never exceed the requested width/height across supported sizes, no trailing whitespace, ready/progress/failure/configure semantics, sanitization, and model state transitions). Capability-matrix tests bind JSON, CI, non-TTY, `TERM=dumb`, narrow width, and missing true-color declaration to the plain fallback, while separately proving that inherited `NO_COLOR` does not suppress a capable interactive terminal.
