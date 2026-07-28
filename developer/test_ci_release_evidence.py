import hashlib
import json
import pathlib
import subprocess
import tempfile
import unittest

from ci_release_evidence import (
    fetch_and_verify_tag_target,
    normalize_gate_result,
    write_parity,
)


class CIReleaseEvidenceTest(unittest.TestCase):
    def test_parity_normalizes_actions_results_to_gate_vocabulary(self):
        self.assertEqual(normalize_gate_result("success"), "passed")
        self.assertEqual(normalize_gate_result("failure"), "failed")
        self.assertEqual(normalize_gate_result("cancelled"), "failed")
        self.assertEqual(normalize_gate_result("skipped"), "skipped")
        self.assertEqual(normalize_gate_result(""), "missing")
        self.assertEqual(normalize_gate_result("in_progress"), "gated")

    def test_write_parity_emits_only_terminal_gate_vocabulary(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = pathlib.Path(temporary) / "parity.json"
            write_parity(
                output=output,
                candidate_commit="a" * 40,
                run_id="12",
                run_attempt="3",
                run_url="https://example.invalid/runs/12",
                direct={"test": "success", "integration": "failure"},
                rcc={"local": "skipped", "robot": ""},
            )
            record = json.loads(output.read_text(encoding="utf-8"))
            results = {
                *record["mandatoryGates"]["direct"].values(),
                *record["mandatoryGates"]["rcc"].values(),
            }
            self.assertEqual(results, {"passed", "failed", "skipped", "missing"})
            self.assertFalse(record["complete"])
            self.assertEqual(record["qualifiedHistoricalRuns"], [])

    def test_verify_candidate_requires_preserved_build_layout(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            flattened = root / "camp"
            flattened.write_bytes(b"candidate")
            result = subprocess.run(
                [
                    "python3",
                    "developer/ci_release_evidence.py",
                    "verify-candidate",
                    "--build-root",
                    str(root / "build"),
                    "--commit",
                    "a" * 40,
                ],
                cwd=pathlib.Path(__file__).resolve().parents[1],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("build/camp", result.stderr)

    def test_verify_candidate_accepts_exact_preserved_build_layout(self):
        with tempfile.TemporaryDirectory() as temporary:
            build = pathlib.Path(temporary) / "build"
            (build / "evidence").mkdir(parents=True)
            candidate = build / "camp"
            candidate.write_bytes(b"candidate")
            digest = hashlib.sha256(b"candidate").hexdigest()
            (build / "evidence" / "candidate.json").write_text(
                json.dumps(
                    {
                        "commit": "a" * 40,
                        "dirty": False,
                        "candidateSha256": digest,
                    }
                ),
                encoding="utf-8",
            )
            result = subprocess.run(
                [
                    "python3",
                    "developer/ci_release_evidence.py",
                    "verify-candidate",
                    "--build-root",
                    str(build),
                    "--commit",
                    "a" * 40,
                    "--sha256",
                    digest,
                ],
                cwd=pathlib.Path(__file__).resolve().parents[1],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_fetch_and_verify_tag_target_fetches_only_lightweight_requested_tag(self):
        with tempfile.TemporaryDirectory() as temporary:
            author, publisher, first, second = self._remote_fixture(
                pathlib.Path(temporary)
            )
            self._git(author, "tag", "v1.0.0", first)
            self._git(author, "tag", "-a", "v2.0.0", "-m", "other", second)
            self._git(author, "push", "origin", "refs/tags/v1.0.0")
            self._git(author, "push", "origin", "refs/tags/v2.0.0")

            fetch_and_verify_tag_target(
                "v1.0.0", first, repository=publisher
            )

            fetched = self._git(
                publisher, "show-ref", "--verify", "refs/camp-release-tags/v1.0.0"
            ).stdout
            self.assertIn(first, fetched)
            absent = subprocess.run(
                [
                    "git",
                    "show-ref",
                    "--verify",
                    "refs/camp-release-tags/v2.0.0",
                ],
                cwd=publisher,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertNotEqual(absent.returncode, 0)

    def test_fetch_and_verify_tag_target_peels_annotated_tag(self):
        with tempfile.TemporaryDirectory() as temporary:
            author, publisher, _first, second = self._remote_fixture(
                pathlib.Path(temporary)
            )
            self._git(author, "tag", "-a", "v2.0.0", "-m", "release", second)
            self._git(author, "push", "origin", "refs/tags/v2.0.0")

            fetch_and_verify_tag_target(
                "v2.0.0", second, repository=publisher
            )

    def test_fetch_and_verify_tag_target_rejects_moved_tag(self):
        with tempfile.TemporaryDirectory() as temporary:
            author, publisher, first, second = self._remote_fixture(
                pathlib.Path(temporary)
            )
            self._git(author, "tag", "v1.0.0", first)
            self._git(author, "push", "origin", "refs/tags/v1.0.0")
            fetch_and_verify_tag_target(
                "v1.0.0", first, repository=publisher
            )
            self._git(author, "tag", "-f", "v1.0.0", second)
            self._git(author, "push", "--force", "origin", "refs/tags/v1.0.0")

            with self.assertRaisesRegex(
                RuntimeError, "does not equal candidate commit"
            ):
                fetch_and_verify_tag_target(
                    "v1.0.0", first, repository=publisher
                )

    def test_fetch_and_verify_tag_target_rejects_missing_or_malformed_tag(self):
        with tempfile.TemporaryDirectory() as temporary:
            _author, publisher, first, _second = self._remote_fixture(
                pathlib.Path(temporary)
            )
            self._git(publisher, "tag", "v9.9.9", first)
            with self.assertRaisesRegex(RuntimeError, "fetch requested release tag"):
                fetch_and_verify_tag_target(
                    "v9.9.9", first, repository=publisher
                )
            with self.assertRaisesRegex(RuntimeError, "invalid release tag"):
                fetch_and_verify_tag_target(
                    "../v1.0.0", first, repository=publisher
                )

    def _remote_fixture(self, root):
        remote = root / "origin.git"
        author = root / "author"
        publisher = root / "publisher"
        self._git(root, "init", "--bare", str(remote))
        self._git(root, "init", str(author))
        self._git(author, "config", "user.name", "Camp Test")
        self._git(author, "config", "user.email", "camp@example.invalid")
        (author / "file").write_text("one\n", encoding="utf-8")
        self._git(author, "add", "file")
        self._git(author, "commit", "-m", "one")
        first = self._git(author, "rev-parse", "HEAD").stdout.strip()
        (author / "file").write_text("two\n", encoding="utf-8")
        self._git(author, "commit", "-am", "two")
        second = self._git(author, "rev-parse", "HEAD").stdout.strip()
        self._git(author, "remote", "add", "origin", str(remote))
        self._git(author, "push", "origin", "HEAD:refs/heads/main")
        self._git(
            root,
            "clone",
            "--no-tags",
            "--branch",
            "main",
            str(remote),
            str(publisher),
        )
        return author, publisher, first, second

    def _git(self, repo, *arguments):
        return subprocess.run(
            ["git", *arguments],
            cwd=repo,
            text=True,
            capture_output=True,
            check=True,
        )


if __name__ == "__main__":
    unittest.main()
