import os
import pathlib
import stat
import subprocess
import tempfile
import unittest

from developer.factory import install_candidate, resolve_package_identity


class FactoryTest(unittest.TestCase):
    def test_install_candidate_links_repository_build_into_user_bin(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            candidate = root / "build" / "camp"
            candidate.parent.mkdir()
            candidate.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            candidate.chmod(candidate.stat().st_mode | stat.S_IXUSR)

            installed = install_candidate(candidate, root / "bin")

            self.assertEqual(installed, root / "bin" / "camp")
            self.assertTrue(installed.is_symlink())
            self.assertEqual(installed.resolve(), candidate.resolve())

    def test_package_identity_defaults_from_clean_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = self._git_repository(pathlib.Path(temporary))

            version, commit = resolve_package_identity(root, {})

            self.assertEqual(version, f"0.0.0-{commit[:12]}")
            self.assertEqual(commit, self._git(root, "rev-parse", "HEAD"))

    def test_package_identity_requires_both_explicit_values(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = self._git_repository(pathlib.Path(temporary))

            with self.assertRaisesRegex(
                RuntimeError, "VERSION and COMMIT must be provided together"
            ):
                resolve_package_identity(root, {"VERSION": "1.2.3"})

    def test_package_identity_refuses_implicit_identity_for_dirty_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = self._git_repository(pathlib.Path(temporary))
            (root / "dirty.txt").write_text("dirty\n", encoding="utf-8")

            with self.assertRaisesRegex(
                RuntimeError, "automatic package identity requires a clean checkout"
            ):
                resolve_package_identity(root, {})

    def _git_repository(self, root):
        self._git(root, "init", "-q")
        self._git(root, "config", "user.name", "Camp Test")
        self._git(root, "config", "user.email", "camp@example.invalid")
        (root / "README.md").write_text("fixture\n", encoding="utf-8")
        self._git(root, "add", "README.md")
        self._git(root, "commit", "-q", "-m", "fixture")
        return root

    def _git(self, root, *arguments):
        return subprocess.check_output(
            ["git", *arguments], cwd=root, text=True, env=os.environ
        ).strip()


if __name__ == "__main__":
    unittest.main()
