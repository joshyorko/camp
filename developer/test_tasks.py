import hashlib
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import tasks


class TasksTest(unittest.TestCase):
    def test_validate_freeze_accepts_current_exact_locks(self):
        tasks.validate_freeze()

    def test_validate_freeze_rejects_mismatched_robot_lock(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            self._write_freeze_fixture(root, setup_robot="7.4.2", pip_robot="7.4.1")
            with mock.patch.object(tasks, "ROOT", root):
                with self.assertRaisesRegex(RuntimeError, "Robot declarations disagree"):
                    tasks.validate_freeze()

    def test_generated_documentation_rejects_post_generation_diff(self):
        with mock.patch.object(tasks, "run") as run:
            run.side_effect = ["", RuntimeError("command failed (1): git diff --exit-code")]
            with self.assertRaisesRegex(RuntimeError, "git diff"):
                tasks.generated_documentation()
        self.assertEqual(
            run.call_args_list[1].args[0],
            ["git", "diff", "--exit-code", "--", "docs/generated"],
        )

    def test_run_gates_persists_candidate_digest_and_terminal_results(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            candidate = root / "camp"
            candidate.write_bytes(b"candidate")
            evidence = root / "evidence"
            with mock.patch.object(tasks, "CANDIDATE", candidate), mock.patch.object(tasks, "EVIDENCE", evidence), mock.patch.object(tasks, "verify_candidate", return_value={"candidateSha256": hashlib.sha256(b"candidate").hexdigest()}), mock.patch.object(tasks, "run"):
                tasks.run_gates("robot", [("smoke", ["true"])])
            report = json.loads((evidence / "robot-gates.json").read_text(encoding="utf-8"))
            self.assertEqual(report["candidateSha256"], hashlib.sha256(b"candidate").hexdigest())
            self.assertEqual(report["gates"][0]["result"], "passed")
            self.assertNotIn("pending", {gate["result"] for gate in report["gates"]})

    def test_run_gates_records_missing_mandatory_gate_and_fails(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            candidate = root / "camp"
            candidate.write_bytes(b"candidate")
            evidence = root / "evidence"
            with mock.patch.object(tasks, "CANDIDATE", candidate), mock.patch.object(tasks, "EVIDENCE", evidence), mock.patch.object(tasks, "MANDATORY_TEST_GATES", ("unit", "vet")), mock.patch.object(tasks, "verify_candidate", return_value={"candidateSha256": hashlib.sha256(b"candidate").hexdigest()}), mock.patch.object(tasks, "run"):
                with self.assertRaisesRegex(RuntimeError, "vet"):
                    tasks.run_gates("test", [("unit", ["true"])])
            report = json.loads((evidence / "test-gates.json").read_text(encoding="utf-8"))
            missing = next(gate for gate in report["gates"] if gate["name"] == "vet")
            self.assertEqual(missing["result"], "missing")

    def test_run_gates_writes_every_declared_gate_as_terminal_or_gated_before_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            candidate = root / "camp"
            candidate.write_bytes(b"candidate")
            evidence = root / "evidence"
            digest = hashlib.sha256(b"candidate").hexdigest()
            with mock.patch.object(tasks, "CANDIDATE", candidate), mock.patch.object(tasks, "EVIDENCE", evidence), mock.patch.object(tasks, "verify_candidate", return_value={"candidateSha256": digest}), mock.patch.object(tasks, "run", side_effect=KeyboardInterrupt):
                with self.assertRaises(KeyboardInterrupt):
                    tasks.run_gates("robot", [("first", ["true"]), ("second", ["true"])])
            report = json.loads((evidence / "robot-gates.json").read_text(encoding="utf-8"))
            self.assertEqual({gate["name"] for gate in report["gates"]}, {"first", "second"})
            self.assertEqual({gate["result"] for gate in report["gates"]}, {"gated"})

    def test_test_task_verifies_candidate_before_running_gates(self):
        with mock.patch.object(tasks, "build_and_smoke_candidate") as build, mock.patch.object(tasks, "verify_candidate") as verify, mock.patch.object(tasks, "run_gates") as gates, mock.patch.object(tasks.shutil, "which", return_value="gcc"):
            tasks.test_task.body(None)
        build.assert_called_once_with()
        verify.assert_called_once_with()
        gates.assert_called_once()

    def _write_freeze_fixture(self, root, *, setup_robot, pip_robot):
        (root / "developer").mkdir()
        (root / "go.mod").write_text("module example.invalid/camp\n\ngo 1.26.5\n", encoding="utf-8")
        (root / "developer" / "rcc.lock.yaml").write_text("version: v18.17.7\nhost: linux/amd64\n", encoding="utf-8")
        (root / "developer" / "setup.yaml").write_text(f"  - robotframework={setup_robot}\n", encoding="utf-8")
        (root / "robot_requirements.txt").write_text(f"robotframework=={pip_robot}\n", encoding="utf-8")
        (root / "tools.lock.yaml").write_text("""  devpod:
    version: v0.26.1
  hauler:
    version: v2.0.2
  room:
    version: v1.18.3
""", encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
