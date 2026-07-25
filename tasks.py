import datetime
import hashlib
import json
import os
import pathlib
import platform
import shlex
import shutil
import subprocess
import time

from invoke import task


ROOT = pathlib.Path(__file__).resolve().parent
BUILD = ROOT / "build"
EVIDENCE = BUILD / "evidence"
CANDIDATE = BUILD / "camp"
CANDIDATE_MANIFEST = EVIDENCE / "candidate.json"


def run(command, *, env=None, capture=False):
    merged = os.environ.copy()
    if env:
        merged.update(env)
    completed = subprocess.run(
        command,
        cwd=ROOT,
        env=merged,
        check=False,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    if completed.returncode:
        detail = ""
        if capture:
            detail = f"\n{completed.stdout}{completed.stderr}"
        raise RuntimeError(f"command failed ({completed.returncode}): {shlex.join(command)}{detail}")
    return completed.stdout.strip() if capture else ""


def sha256(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git(*args):
    return run(["git", *args], capture=True)


def discover_go_test(package, name):
    listed = run(["go", "test", package, "-list", f"^{name}$"], capture=True)
    if name not in listed.splitlines():
        raise RuntimeError(f"missing mandatory Go evidence test: {name}")


def candidate_metadata():
    commit = git("rev-parse", "HEAD")
    short = git("rev-parse", "--short=12", "HEAD")
    dirty = bool(
        git(
            "status",
            "--porcelain",
            "--untracked-files=all",
            "--",
            ".",
            ":(exclude).claude/**",
        )
    )
    source_epoch = os.environ.get("SOURCE_DATE_EPOCH") or git("show", "-s", "--format=%ct", "HEAD")
    build_date = datetime.datetime.fromtimestamp(
        int(source_epoch), tz=datetime.timezone.utc
    ).strftime("%Y-%m-%dT%H:%M:%SZ")
    version = os.environ.get("CAMP_VERSION") or f"0.0.0-{short}"
    if dirty:
        version += ".dirty"
    return {
        "schemaVersion": 1,
        "version": version,
        "commit": commit,
        "dirty": dirty,
        "buildDate": build_date,
        "sourceDateEpoch": int(source_epoch),
    }


def build_candidate():
    BUILD.mkdir(exist_ok=True)
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    metadata = candidate_metadata()
    ldflags = " ".join(
        [
            "-s",
            "-w",
            f"-X main.version={metadata['version']}",
            f"-X main.commit={metadata['commit']}",
            f"-X main.buildDate={metadata['buildDate']}",
            f"-X main.dirty={str(metadata['dirty']).lower()}",
        ]
    )
    run(
        [
            "go",
            "build",
            "-trimpath",
            "-buildvcs=false",
            f"-ldflags={ldflags}",
            "-o",
            str(CANDIDATE),
            "./cmd/camp",
        ],
        env={"CGO_ENABLED": "0"},
    )
    machine = {"x86_64": "amd64", "aarch64": "arm64"}.get(
        platform.machine(), platform.machine()
    )
    metadata.update(
        {
            "platform": f"linux/{machine}",
            "goVersion": run(["go", "version"], capture=True),
            "rccVersion": os.environ.get("CAMP_RCC_VERSION", "unmanaged"),
            "setupSha256": sha256(ROOT / "developer" / "setup.yaml"),
            "toolsLockSha256": sha256(ROOT / "tools.lock.yaml"),
            "candidateSha256": sha256(CANDIDATE),
        }
    )
    CANDIDATE_MANIFEST.write_text(
        json.dumps(metadata, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    return metadata


def verify_candidate():
    if not CANDIDATE.is_file() or not CANDIDATE_MANIFEST.is_file():
        raise RuntimeError("candidate is missing; run the local task first")
    metadata = json.loads(CANDIDATE_MANIFEST.read_text(encoding="utf-8"))
    actual = sha256(CANDIDATE)
    if actual != metadata.get("candidateSha256"):
        raise RuntimeError(
            f"candidate digest changed: got {actual}, want {metadata.get('candidateSha256')}"
        )
    expected_inputs = {
        "setupSha256": sha256(ROOT / "developer" / "setup.yaml"),
        "toolsLockSha256": sha256(ROOT / "tools.lock.yaml"),
    }
    for field, expected in expected_inputs.items():
        if metadata.get(field) != expected:
            raise RuntimeError(
                f"candidate input {field} changed: got {metadata.get(field)}, want {expected}"
            )
    return metadata


def managed_tool_environment():
    setup_root = EVIDENCE / "setup"
    runtime = setup_root / "runtime"
    runtime.mkdir(parents=True, exist_ok=True)
    runtime.chmod(0o700)
    environment = {
        "XDG_CONFIG_HOME": str(setup_root / "config"),
        "XDG_DATA_HOME": str(setup_root / "data"),
        "XDG_CACHE_HOME": str(setup_root / "cache"),
        "XDG_RUNTIME_DIR": str(runtime),
    }
    output = run([str(CANDIDATE), "--json", "setup"], env=environment, capture=True)
    envelope = json.loads(output)
    result = envelope["result"]
    paths = result["path"]
    if len(paths) != 2 or any(not pathlib.Path(path).is_dir() for path in paths):
        raise RuntimeError(f"Camp setup returned invalid managed tool paths: {paths!r}")
    environment["PATH"] = os.pathsep.join([*paths, os.environ["PATH"]])
    return environment


def run_gates(suite, gates, *, env=None):
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    report = {
        "schemaVersion": 1,
        "suite": suite,
        "candidateSha256": sha256(CANDIDATE) if CANDIDATE.is_file() else None,
        "gates": [
            {"name": name, "result": "pending", "durationMs": 0}
            for name, _command in gates
        ],
    }
    report_path = EVIDENCE / f"{suite}-gates.json"
    failures = []
    report_path.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    for index, (name, command) in enumerate(gates):
        started = time.monotonic()
        result = report["gates"][index]
        try:
            run(command, env=env)
            result["result"] = "passed"
        except Exception as error:
            result["result"] = "failed"
            result["reason"] = str(error).splitlines()[0]
            failures.append((name, error))
        finally:
            result["durationMs"] = round((time.monotonic() - started) * 1000)
            report_path.write_text(
                json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
    if failures:
        names = ", ".join(name for name, _error in failures)
        raise RuntimeError(f"{suite} gates failed: {names}")


@task
def local(_context):
    """Build and smoke one truthfully stamped repository-local Camp candidate."""
    metadata = build_candidate()
    run([str(CANDIDATE), "--version"])
    run([str(CANDIDATE), "--help"])
    for shell in ("bash", "zsh", "fish"):
        run([str(CANDIDATE), "completion", shell], capture=True)
    print(json.dumps(metadata, sort_keys=True))


@task(name="test")
def test_task(_context):
    """Run source, race, hygiene, vulnerability, and packaging gates."""
    compiler = shutil.which("gcc") or shutil.which("x86_64-conda-linux-gnu-cc")
    if not compiler:
        raise RuntimeError("race gate requires a C compiler in the RCC environment")
    run_gates(
        "test",
        [
            ("unit", ["go", "test", "./...", "-count=1"]),
            ("race", ["go", "test", "-race", "./...", "-count=1"]),
            ("vet", ["go", "vet", "./..."]),
            (
                "vulnerability",
                ["go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.1.4", "./..."],
            ),
            ("diff", ["git", "diff", "--check"]),
            ("packaging", ["go", "test", "./packaging", "-count=1"]),
            ("releasepipeline", ["go", "test", "./releasepipeline", "-count=1"]),
        ],
        env={"CC": compiler, "CGO_ENABLED": "1"},
    )


@task
def robot(_context):
    """Run mandatory Go evidence and black-box Robot suites on one candidate."""
    metadata = verify_candidate()
    env = managed_tool_environment()
    env.update({
        "CAMP_TEST_BINARY": str(CANDIDATE),
        "CAMP_TEST_REAL_LIFECYCLE": "1",
        "CAMP_TEST_REAL_MINIO_REOPEN": "1",
    })
    run_gates(
        "robot",
        [
            ("real-go-evidence", ["./scripts/verify-real-evidence.sh", "all"]),
            (
                "black-box-robot",
                [
                    "python",
                    "-m",
                    "robot",
                    "--outputdir",
                    str(EVIDENCE / "robot"),
                    "--variable",
                    f"CAMP_BINARY:{CANDIDATE}",
                    "robot_tests",
                ],
            ),
        ],
        env=env,
    )
    if sha256(CANDIDATE) != metadata["candidateSha256"]:
        raise RuntimeError("candidate changed during robot evidence")


@task(name="robotKubernetes")
def robot_kubernetes(_context):
    """Run protected Kubernetes evidence only with an explicit authorization."""
    if os.environ.get("CAMP_KUBERNETES_EVIDENCE") != "1":
        raise RuntimeError(
            "protected Kubernetes evidence requires CAMP_KUBERNETES_EVIDENCE=1"
        )
    metadata = verify_candidate()
    test_name = "TestKubernetesLifecycleVertical"
    discover_go_test("./integration", test_name)
    env = {
        "CAMP_TEST_BINARY": str(CANDIDATE),
        "CAMP_KUBERNETES_EVIDENCE": "1",
    }
    run(
        [
            "go",
            "test",
            "-v",
            "./integration",
            "-run",
            f"^{test_name}$",
            "-count=1",
        ],
        env=env,
    )
    if sha256(CANDIDATE) != metadata["candidateSha256"]:
        raise RuntimeError("candidate changed during Kubernetes evidence")


@task(name="package")
def package_task(_context):
    """Delegate reproducible release evidence to the existing packaging authority."""
    version = os.environ.get("VERSION")
    commit = os.environ.get("COMMIT")
    if not version or not commit:
        raise RuntimeError("package requires VERSION and COMMIT")
    env = {
        "VERSION": version,
        "COMMIT": commit,
        "SOURCE_DATE_EPOCH": os.environ.get("SOURCE_DATE_EPOCH")
        or git("show", "-s", "--format=%ct", "HEAD"),
        "OUTPUT_DIR": os.environ.get("OUTPUT_DIR", str(ROOT / "dist")),
    }
    run(["./packaging/build-release-evidence.sh", "build"], env=env)
