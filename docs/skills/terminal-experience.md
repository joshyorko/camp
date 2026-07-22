# Terminal Experience

Camp's campsite presentation is a view of authoritative state, not a progress simulation. Populate toolchain values from the embedded tool lock and resolved binaries, runtime values from the persisted DevPod provider/context and observed provider kind, capsule values from the effective sanitized configuration, and storage values from the resolved backend plus committed generation state. Missing fields are errors; never invent a percentage, provider, context, generation, or readiness claim to complete the picture.

Interactive color output may render the Trailhead Topography scene only after terminal capability detection. JSON, `NO_COLOR`, CI, non-TTY, dumb-terminal, redirected, and narrow-width paths must remain deterministic plain text without cursor controls. Animation stages advance from completed application events rather than elapsed time. Cancellation or failure stops the scene and preserves the underlying error plus its exact recovery command.

Presentation metadata is untrusted at the rendering boundary. Reject control characters and credential-bearing URLs before writing any bytes. Render only already-sanitized backend/source identities; never display credentials, raw access tokens, secret query values, or environment contents.

Terminal selection is fail-closed. Full-color output requires an actual output file descriptor with a readable width of at least 80 columns, `COLORTERM=truecolor` or `24bit`, a non-dumb `TERM`, and no `CI` or `NO_COLOR` signal. Buffers, pipes, redirected files, JSON mode, cancellation, narrow terminals, failed terminal probes, and terminals without declared true color use stable control-free output. Never infer interactivity from environment variables alone.

Setup reads DevPod and Hauler versions from the embedded distribution lock, not installed binary banners. Emit each tool waypoint only after `Ensure` returns its verified resolution; a failed ensure must not advance the trail. Once persistent configuration exists, its effective source, capsule, provider, context, and sanitized backend are combined with the newest durable journal snapshot. Generation display prefers a committed checkpoint generation, then the current base, then the opened generation; if none exists, say `no committed generation`. Local-versus-remote DevPod is the only provider kind currently proven by persisted/session state. Do not label an arbitrary remote provider as Kubernetes until Camp records that observed kind.

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
