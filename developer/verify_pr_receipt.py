import json
import pathlib
import re
import sys


REQUIRED_FIELDS = (
    "Passed gates:",
    "Failed gates:",
    "Missing or skipped gates:",
    "Candidate SHA-256:",
    "Real-tool evidence:",
    "Release-note classification:",
    "Canonical file changed or proposed:",
    "Durable learning captured:",
    "Evidence:",
    "Stale or ambiguous guidance removed:",
    "Remaining uncertainty:",
)

RECEIPT_LABEL_PREFIX = re.compile(r"^\s*[-*]\s*")


def _receipt_field_value(body_lines, field):
    for line in body_lines:
        normalized = RECEIPT_LABEL_PREFIX.sub("", line, count=1)
        if normalized.startswith(field):
            value = normalized[len(field) :].strip()
            if value:
                return value
    return None


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: verify_pr_receipt.py GITHUB_EVENT_PATH", file=sys.stderr)
        return 2
    event = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    body = (event.get("pull_request") or {}).get("body") or ""
    body_lines = body.splitlines()
    missing = []
    for field in REQUIRED_FIELDS:
        if _receipt_field_value(body_lines, field) is None:
            missing.append(field)
    if missing:
        for field in missing:
            print(f"missing pull request receipt field: {field}", file=sys.stderr)
        return 1
    release_note = _receipt_field_value(body_lines, "Release-note classification:")
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
