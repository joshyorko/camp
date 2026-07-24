#!/usr/bin/env python3
"""Focused vtgrid behavior probes for terminal replay semantics."""

import re
import subprocess
import tempfile
import unittest
from pathlib import Path
from vtgrid import Screen, emit, feed, read_capture


def ansi_text(lines: str) -> str:
    return re.sub(r"\x1b\[[0-9;]*m", "", lines)


def emit_lines(stream: str, cols: int, rows: int):
    s = Screen(cols=cols, rows=rows)
    feed(s, stream)
    return ansi_text(emit(s)).split("\n")


def emit_raw_rows(path: str, cols: int, rows: int):
    if not Path(path).exists():
        raise AssertionError(f"missing capture fixture: {path}")
    s = Screen(cols=cols, rows=rows)
    feed(s, read_capture(path))
    return ansi_text(emit(s)).split("\n")


def generate_scenedump(rows: int, cols: int, state: str, repo_root: Path, dump_dir: Path):
    result = subprocess.run(
        [
            "go",
            "run",
            "./internal/setupui/scenedump",
            "-w",
            str(cols),
            "-h",
            str(rows),
            "-state",
            state,
        ],
        check=True,
        cwd=repo_root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    output_path = dump_dir / f"{state}-{cols}x{rows}.scenedump.txt"
    output_path.write_text(result.stdout, encoding="utf-8")
    rows_text = ansi_text(result.stdout).split("\n")
    if len(rows_text) < rows:
        raise AssertionError(f"scenedump fixture for {state} has only {len(rows_text)} rows")
    return rows_text[:rows]


class TestVTGrid(unittest.TestCase):
    REPO_ROOT = Path(__file__).resolve().parents[2]
    CAPTURE_DIR = REPO_ROOT / ".scene-captures"
    REQUIRED_RAWS = [
        "configure-80x24",
        "configure-120x40",
        "progress-120x40",
        "ready-120x40",
        "ready-80x24",
        "failure-120x40",
        "ready-160x48",
        "resize-120x40-to-160x48",
        "cancel-restored-shell",
    ]

    @classmethod
    def setUpClass(cls):
        cls._dump_dir = tempfile.TemporaryDirectory(prefix="vtgrid-scenedumps-")
        tmpdir = Path(cls._dump_dir.name)
        cls.scene_dumps = {
            "configure": generate_scenedump(24, 80, "configure", cls.REPO_ROOT, tmpdir),
            "ready": generate_scenedump(24, 80, "ready", cls.REPO_ROOT, tmpdir),
        }

    @classmethod
    def tearDownClass(cls):
        cls._dump_dir.cleanup()

    def test_partial_lf_preserves_column(self):
        lines = emit_lines("abcde\r\nF", cols=10, rows=4)
        self.assertEqual("F", lines[1][0])

    def test_wrap_pending_then_lf_breaks_to_column_zero(self):
        lines = emit_lines("12345\n6", cols=5, rows=4)
        self.assertEqual("6", lines[1][0])

    def test_deccs_decrc_restore_attributes_and_cursor(self):
        lines = emit_lines("\x1b[31mA\x1b7\x1b[32mB\x1b8C", cols=40, rows=4)
        self.assertEqual("AC", lines[0][:2])

    def test_alt_screen_1049_restores_main_cursor(self):
        lines = emit_lines("M\x1b[?1049hAAA\x1b[?1049lB", cols=40, rows=4)
        self.assertTrue(lines[0].startswith("MB"))

    def test_scene_80x24_configure_matches_scenedump(self):
        expected_rows = self.scene_dumps["configure"]
        actual_rows = emit_raw_rows(str(self.CAPTURE_DIR / "configure-80x24.raw"), 80, 24)
        self.assertEqual(expected_rows, actual_rows[:24])

    def test_scene_80x24_ready_matches_scenedump(self):
        expected_rows = self.scene_dumps["ready"]
        actual_rows = emit_raw_rows(str(self.CAPTURE_DIR / "ready-80x24.raw"), 80, 24)
        self.assertEqual(expected_rows, actual_rows[:24])

    def test_required_capture_raw_files_exist(self):
        for stem in self.REQUIRED_RAWS:
            raw_path = self.CAPTURE_DIR / f"{stem}.raw"
            self.assertTrue(raw_path.is_file(), f"missing raw capture {raw_path}")
            self.assertGreater(raw_path.stat().st_size, 0, f"empty raw capture {raw_path}")

    def test_resize_capture_contains_alpha_dir(self):
        raw_text = read_capture(str(self.CAPTURE_DIR / "resize-120x40-to-160x48.raw"))
        self.assertIn("/\u00e4lpha dir", raw_text)

    def test_cancel_capture_contains_restore_markers(self):
        raw_text = read_capture(str(self.CAPTURE_DIR / "cancel-restored-shell.raw"))
        self.assertIn("?1049l", raw_text)
        self.assertIn("?25h", raw_text)
        self.assertIn("scenerun exit: canceled (terminal restored)", raw_text)
        self.assertIn("camp$ ", raw_text)

    def test_ready_capture_key_markers_exist(self):
        keys = (self.CAPTURE_DIR / "ready-80x24.keys").read_text(encoding="utf-8")
        self.assertGreater(keys.count("key "), 0, "ready capture key markers missing")

    def test_configure_capture_key_markers_exist(self):
        keys = (self.CAPTURE_DIR / "configure-80x24.keys").read_text(encoding="utf-8")
        self.assertIn("sleep 0.8", keys)


if __name__ == "__main__":
    unittest.main(verbosity=2)
