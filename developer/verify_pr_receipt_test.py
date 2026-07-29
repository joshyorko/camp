import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from developer.verify_pr_receipt import main


class VerifyPRReceiptTest(unittest.TestCase):
    def test_accepts_expected_bullet_prefixed_labels(self):
        with tempfile.TemporaryDirectory() as temporary:
            event = pathlib.Path(temporary) / "event.json"
            event.write_text(
                json.dumps(
                    {
                        "pull_request": {
                            "body": "\n".join(
                                [
                                    "- Passed gates: go test ./...",
                                    "- Failed gates: none",
                                    "- Missing or skipped gates: none",
                                    "- Candidate SHA-256: abc123",
                                    "- Real-tool evidence: attached",
                                    "- Release-note classification: docs/changelog.md",
                                    "- Canonical file changed or proposed: docs/skills/testing-release-evidence.md",
                                    "- Durable learning captured: label parsing stays exact",
                                    "- Evidence: unittest",
                                    "- Stale or ambiguous guidance removed: renamed labels are rejected",
                                    "- Remaining uncertainty: none",
                                ]
                            )
                        }
                    }
                ),
                encoding="utf-8",
            )
            with patch("sys.argv", ["verify_pr_receipt.py", str(event)]):
                self.assertEqual(main(), 0)

    def test_rejects_renamed_labels(self):
        with tempfile.TemporaryDirectory() as temporary:
            event = pathlib.Path(temporary) / "event.json"
            event.write_text(
                json.dumps(
                    {
                        "pull_request": {
                            "body": "\n".join(
                                [
                                    "- Passed checks: go test ./...",
                                    "- Failed gates: none",
                                    "- Missing or skipped gates: none",
                                    "- Candidate SHA-256: abc123",
                                    "- Real-tool evidence: attached",
                                    "- Release-note classification: docs/changelog.md",
                                    "- Canonical file changed or proposed: docs/skills/testing-release-evidence.md",
                                    "- Durable learning captured: label parsing stays exact",
                                    "- Evidence: unittest",
                                    "- Stale or ambiguous guidance removed: renamed labels are rejected",
                                    "- Remaining uncertainty: none",
                                ]
                            )
                        }
                    }
                ),
                encoding="utf-8",
            )
            with patch("sys.argv", ["verify_pr_receipt.py", str(event)]):
                self.assertEqual(main(), 1)


if __name__ == "__main__":
    unittest.main()
