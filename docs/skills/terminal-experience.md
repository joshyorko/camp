# Terminal Experience

Camp's campsite presentation is a view of authoritative state, not a progress simulation. Populate toolchain values from the embedded tool lock and resolved binaries, runtime values from the persisted DevPod provider/context and observed provider kind, capsule values from the effective sanitized configuration, and storage values from the resolved backend plus committed generation state. Missing fields are errors; never invent a percentage, provider, context, generation, or readiness claim to complete the picture.

Interactive color output may render the Trailhead Topography scene only after terminal capability detection. JSON, `NO_COLOR`, CI, non-TTY, dumb-terminal, redirected, and narrow-width paths must remain deterministic plain text without cursor controls. Animation stages advance from completed application events rather than elapsed time. Cancellation or failure stops the scene and preserves the underlying error plus its exact recovery command.

Presentation metadata is untrusted at the rendering boundary. Reject control characters and credential-bearing URLs before writing any bytes. Render only already-sanitized backend/source identities; never display credentials, raw access tokens, secret query values, or environment contents.

Terminal selection is fail-closed. Full-color output requires an actual output file descriptor with a readable width of at least 80 columns, `COLORTERM=truecolor` or `24bit`, a non-dumb `TERM`, and no `CI` or `NO_COLOR` signal. Buffers, pipes, redirected files, JSON mode, cancellation, narrow terminals, failed terminal probes, and terminals without declared true color use stable control-free output. Never infer interactivity from environment variables alone.

Setup reads DevPod and Hauler versions from the embedded distribution lock, not installed binary banners. Emit each tool waypoint only after `Ensure` returns its verified resolution; a failed ensure must not advance the trail. Once persistent configuration exists, its effective source, capsule, provider, context, and sanitized backend are combined with the newest durable journal snapshot for that same capsule. A journal for another capsule must never override the displayed provider, context, runtime kind, or generation. Generation display prefers a committed checkpoint generation, then the current base, then the opened generation; if none exists, say `no committed generation`. Local-versus-remote DevPod is the only provider kind currently proven by persisted/session state. Do not label an arbitrary remote provider as Kubernetes until Camp records that observed kind.

When persistent configuration is absent, human setup transitions through source, capsule, backend, DevPod provider, and context prompts before any initialization or persistence. Empty answers select the displayed defaults: current directory, its basename, the resolved XDG file backend, `docker`, and `default`. EOF fails before configured `init` is called, so no partial configuration is written. JSON setup never prompts. After configured `init` validates, initializes, and atomically persists the answers, setup advances only through verified tool events and the existing campsite; normal human output never exposes managed executable paths, checksums, or a PATH export, while JSON retains the detailed machine identity.

The interactive setup animation is a sequence of full-screen redraws (`clear` plus `home`) caused by ordered waypoint events: toolchain, runtime, capsule, then storage. A frame may mark only its event and earlier events complete; `CAMP IS READY` appears only in the storage frame. Do not add timers, sleeps, synthetic percentage counters, or cursor animation between events. Plain fallback emits the same ordered facts once each without screen controls, followed by the ready/next-command line only after storage.

Compact lifecycle output is emitted only after the application result proves the corresponding fact. Open may show workspace-ready and opened facts from `OpenResult`; sync may show the published generation from `CheckpointResult`; close may show publication, cleanup, and closed facts from `CloseResult`. These lines are event frames, not percentages or elapsed-time claims. On sync/close failure, preserve the original error and use the result/session identity to print exactly one `camp recover <session>` command. If an open failure occurs before a durable session identity exists, do not invent a recovery command.

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

Golden coverage lives under `internal/presentation/testdata/` and must cover the wide true-color campsite, plain and narrow campsite, plain failure, and cancellation-with-no-output paths. Capability matrix tests separately bind JSON, CI, non-TTY, `TERM=dumb`, `NO_COLOR`, narrow width, and missing true-color declaration to the plain fallback.

Color composition is measured, not hand-typed. `internal/presentation/layout.go` strips ANSI to compute visible width, centers by left-padding only (never trailing spaces, which would leave whitespace in committed goldens), and lays out the four waypoints as a measured column block rather than literal-space strings. The scene composer (`internal/presentation/scene.go`) is the single renderer behind the static campsite, every in-progress waypoint frame, the ready state, and failures — the setup animator only ever changes which `WaypointStatus` (`pending`, `active`, `completed`, `failed`) and which optional failure it passes in, so no stage of the experience is a visually disconnected fragment. The composed scene letterboxes to at most 96 measured columns and vertically distributes blank margins across the probed terminal height (`internal/cli/terminal_linux.go`'s `probeTerminal` reads winsize rows as well as columns); terminals shorter than 20 rows drop the decorative sky/topography rows to stay legible without clipping. `CAMP IS READY` and the next command remain gated to the frame where `ready` is true and no failure is present; a failed waypoint replaces that band with the real error message and exactly one recovery command, never both.

Configuration prompts (source, capsule, backend, DevPod provider, DevPod context) render inside the same full-screen scene once a true-color terminal is detected, each redraw showing prior answers in a `CONFIGURE` panel and the active prompt with its default on the last line so the terminal's own cursor — never manually positioned — stays the visible focus indicator. Plain, JSON, and narrow terminals keep the original line-based `Label [default]: ` prompts byte-for-byte; EOF or an empty required answer still fails before any byte is written to persisted configuration.
