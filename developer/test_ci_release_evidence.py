import json
import hashlib
import pathlib
import subprocess
import tempfile
import unittest

from ci_release_evidence import normalize_gate_result, write_parity


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

    def test_verify_tag_target_rejects_tag_on_different_commit(self):
        with tempfile.TemporaryDirectory() as temporary:
            repo = pathlib.Path(temporary)
            self._git(repo, "init")
            self._git(repo, "config", "user.name", "Camp Test")
            self._git(repo, "config", "user.email", "camp@example.invalid")
            (repo / "file").write_text("one\n", encoding="utf-8")
            self._git(repo, "add", "file")
            self._git(repo, "commit", "-m", "one")
            first = self._git(repo, "rev-parse", "HEAD").stdout.strip()
            self._git(repo, "tag", "v1.0.0")
            (repo / "file").write_text("two\n", encoding="utf-8")
            self._git(repo, "commit", "-am", "two")
            second = self._git(repo, "rev-parse", "HEAD").stdout.strip()
            self.assertNotEqual(first, second)
            result = subprocess.run(
                [
                    "python3",
                    str(
                        pathlib.Path(__file__).with_name("ci_release_evidence.py")
                    ),
                    "verify-tag-target",
                    "--tag",
                    "v1.0.0",
                    "--candidate-commit",
                    second,
                ],
                cwd=repo,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("does not equal candidate commit", result.stderr)

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
