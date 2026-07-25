import json
import pathlib
import sys


REQUIRED_FIELDS = (
    "Passed gates:",
    "Failed gates:",
    "Missing or skipped gates:",
    "Release-note classification:",
    "Canonical file changed or proposed:",
    "Durable learning captured:",
    "Evidence:",
    "Stale or ambiguous guidance removed:",
    "Remaining uncertainty:",
)


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: verify_pr_receipt.py GITHUB_EVENT_PATH", file=sys.stderr)
        return 2
    event = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    body = (event.get("pull_request") or {}).get("body") or ""
    missing = []
    for field in REQUIRED_FIELDS:
        matching = [line for line in body.splitlines() if field in line]
        if not matching or not matching[0].split(field, 1)[1].strip():
            missing.append(field)
    if missing:
        for field in missing:
            print(f"missing pull request receipt field: {field}", file=sys.stderr)
        return 1
    release_note = next(
        line.split("Release-note classification:", 1)[1].strip()
        for line in body.splitlines()
        if "Release-note classification:" in line
    )
    if release_note != "docs/changelog.md" and not (
        release_note.startswith("no-release-note:")
        and release_note.removeprefix("no-release-note:").strip()
    ):
        print(
            "invalid release-note classification: use docs/changelog.md or "
            "no-release-note: REASON",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
