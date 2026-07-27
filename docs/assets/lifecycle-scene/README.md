# Lifecycle scene review captures

These deterministic development captures exercise `setupui.SampleLifecycleFrame`, the same compositor used by `LifecycleModel`. They show the lifecycle event contract at the supported compact, primary, and wide compositions; they are not black-box CLI evidence.

- `progress-80x24.png` and `progress-120x40.png` show active upload.
- `ready-160x48.png` shows terminal lifecycle success without a readiness claim.
- `failure-120x40.png` shows a failed upload and one recovery command.

Regenerate from the repository root with `go run ./internal/setupui/scenedump -state lifecycle-progress -w 120 -h 40 -out .scene-captures/lifecycle-progress-120x40.ansi`, then render the ANSI file with `python3 tools/ansishot/ansishot.py` into this directory. The `TestLifecycleSceneGoldens` hashes protect the underlying deterministic ANSI frames.
