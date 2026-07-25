import os
import pathlib
import secrets
import stat
import subprocess


def install_candidate(candidate, install_dir):
    candidate = pathlib.Path(candidate).resolve(strict=True)
    candidate_mode = candidate.stat().st_mode
    if not stat.S_ISREG(candidate_mode) or not os.access(candidate, os.X_OK):
        raise RuntimeError(f"candidate is not an executable regular file: {candidate}")

    install_dir = pathlib.Path(install_dir).expanduser()
    install_dir.mkdir(parents=True, exist_ok=True)
    install_dir = install_dir.resolve(strict=True)
    target = install_dir / "camp"
    temporary = install_dir / (
        f".camp.{os.getpid()}.{secrets.token_hex(4)}"
    )
    try:
        temporary.symlink_to(candidate)
        os.replace(temporary, target)
    finally:
        temporary.unlink(missing_ok=True)
    return target


def resolve_package_identity(repository_root, environment):
    version = environment.get("VERSION", "").strip()
    commit = environment.get("COMMIT", "").strip()
    if version or commit:
        if not version or not commit:
            raise RuntimeError("VERSION and COMMIT must be provided together")
        return version, commit

    repository_root = pathlib.Path(repository_root)
    dirty = _git(
        repository_root,
        "status",
        "--porcelain",
        "--untracked-files=all",
        "--",
        ".",
        ":(exclude).claude/**",
    )
    if dirty:
        raise RuntimeError(
            "automatic package identity requires a clean checkout; "
            "commit the work or provide VERSION and COMMIT explicitly"
        )
    commit = _git(repository_root, "rev-parse", "HEAD")
    short = _git(repository_root, "rev-parse", "--short=12", "HEAD")
    return f"0.0.0-{short}", commit


def _git(repository_root, *arguments):
    completed = subprocess.run(
        ["git", *arguments],
        cwd=repository_root,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if completed.returncode:
        raise RuntimeError(
            f"git {' '.join(arguments)} failed: {completed.stderr.strip()}"
        )
    return completed.stdout.strip()
