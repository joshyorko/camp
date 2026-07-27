import datetime
import hashlib
import json
import os
import pathlib
import platform
import re
import shlex
import shutil
import subprocess
import tempfile
import time

from invoke import task

from developer.factory import install_candidate, resolve_package_identity


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


def discover_go_test(package, name, *, tags=None):
    command = ["go", "test"]
    if tags:
        command.append(f"-tags={','.join(tags)}")
    command.extend([package, "-list", f"^{name}$"])
    listed = run(command, capture=True)
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


def build_and_smoke_candidate():
    metadata = build_candidate()
    run([str(CANDIDATE), "--version"])
    run([str(CANDIDATE), "--help"])
    for shell in ("bash", "zsh", "fish"):
        run([str(CANDIDATE), "completion", shell], capture=True)
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
        "gates": [],
    }
    report_path = EVIDENCE / f"{suite}-gates.json"
    failures = []
    report_path.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    declared = {name for name, _command in gates}
    for name in MANDATORY_TEST_GATES if suite == "test" else ():
        if name not in declared:
            report["gates"].append(
                {
                    "name": name,
                    "command": None,
                    "result": "missing",
                    "reason": "mandatory gate is not declared",
                    "durationMs": 0,
                }
            )
    for name, command in gates:
        result = {
            "name": name,
            "command": shlex.join(command),
            "result": "pending",
            "durationMs": 0,
        }
        report["gates"].append(result)
        started = time.monotonic()
        try:
            run(command, env=env)
            result["result"] = "passed"
        except Exception as error:
            result["result"] = "failed"
            result["reason"] = sanitize_failure(error)
            failures.append((name, error))
        finally:
            result["durationMs"] = round((time.monotonic() - started) * 1000)
            report_path.write_text(
                json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
    missing = [gate["name"] for gate in report["gates"] if gate["result"] == "missing"]
    if failures or missing:
        names = ", ".join(name for name, _error in failures)
        details = ", ".join(filter(None, [names, *missing]))
        raise RuntimeError(f"{suite} gates failed: {details}")


def sanitize_failure(error):
    reason = str(error).splitlines()[0]
    reason = re.sub(r"https?://[^\s/@:]+:[^\s/@]+@", "https://***:***@", reason)
    reason = re.sub(r"(?i)(token|password|secret)=\S+", r"\1=***", reason)
    return reason[:512]


MANDATORY_TEST_GATES = (
    "unit",
    "race",
    "vet",
    "vulnerability",
    "generated-documentation",
    "rcc-freeze",
    "packaging",
    "release-pipeline",
    "deterministic-amd64",
    "deterministic-arm64",
    "contribution-receipt",
    "whitespace",
)


def require_pattern(path, pattern, description):
    if not re.search(pattern, pathlib.Path(path).read_text(encoding="utf-8"), re.MULTILINE):
        raise RuntimeError(f"RCC freeze validation failed: {description}")


def validate_freeze():
    setup = ROOT / "developer" / "setup.yaml"
    requirements = ROOT / "robot_requirements.txt"
    go_module = ROOT / "go.mod"
    rcc_lock = ROOT / "developer" / "rcc.lock.yaml"
    tools_lock = ROOT / "tools.lock.yaml"
    require_pattern(go_module, r"^go \\d+\\.\\d+\\.\\d+$", "go.mod lacks an exact Go version")
    require_pattern(rcc_lock, r"^version: v\\d+\\.\\d+\\.\\d+$", "RCC lock lacks an exact version")
    require_pattern(rcc_lock, r"^host: linux/amd64$", "RCC factory host is not linux/amd64")
    setup_robot = re.search(r"^  - robotframework=([^\\s]+)$", setup.read_text(encoding="utf-8"), re.MULTILINE)
    pip_robot = re.fullmatch(r"robotframework==([^\\s]+)\\s*", requirements.read_text(encoding="utf-8"))
    if not setup_robot or not pip_robot or setup_robot.group(1) != pip_robot.group(1):
        raise RuntimeError("RCC freeze validation failed: Robot declarations disagree")
    for tool in ("devpod", "hauler"):
        require_pattern(tools_lock, rf"^  {tool}:\\n(?:.*\\n)*?    version: v\\d+\\.\\d+\\.\\d+$", f"{tool} lacks an exact locked version")
    require_pattern(tools_lock, r"^  room:\\n(?:.*\\n)*?    version: v\\d+\\.\\d+\\.\\d+$", "Room lacks an exact locked version")


def generated_documentation():
    run(["go", "run", "./cmd/camp-docs"])
    run(["go", "test", "./internal/docsgen", "./docs", "-count=1"])


def verify_deterministic_build(architecture):
    with tempfile.TemporaryDirectory() as temporary:
        first = pathlib.Path(temporary) / "first"
        second = pathlib.Path(temporary) / "second"
        env = {"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": architecture}
        command = ["go", "build", "-trimpath", "-buildvcs=false", "-o"]
        run([*command, str(first), "./cmd/camp"], env=env)
        run([*command, str(second), "./cmd/camp"], env=env)
        if sha256(first) != sha256(second):
            raise RuntimeError(f"non-deterministic linux/{architecture} build")


@task
def local(_context):
    """Build and smoke one repository-only truthfully stamped candidate."""
    metadata = build_and_smoke_candidate()
    print(json.dumps(metadata, sort_keys=True))


@task(name="install")
def install_task(_context):
    """Verify and atomically install the exact local candidate."""
    metadata = verify_candidate()
    install_dir = pathlib.Path(
        os.environ.get("CAMP_INSTALL_DIR", pathlib.Path.home() / ".local" / "bin")
    )
    installed = install_candidate(CANDIDATE, install_dir)
    run([str(installed), "--version"])
    print(f"Installed development link: {installed}")
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
            (
                "python-unit",
                [
                    "python",
                    "-m",
                    "unittest",
                    "discover",
                    "-s",
                    "developer",
                    "-p",
                    "test_*.py",
                ],
            ),
            ("unit", ["go", "test", "./...", "-count=1"]),
            ("race", ["go", "test", "-race", "./...", "-count=1"]),
            ("vet", ["go", "vet", "./..."]),
            (
                "vulnerability",
                ["go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.1.4", "./..."],
            ),
            ("generated-documentation", ["python", "-c", "import tasks; tasks.generated_documentation()"]),
            ("rcc-freeze", ["python", "-c", "import tasks; tasks.validate_freeze()"]),
            ("packaging", ["go", "test", "./packaging", "-count=1"]),
            ("release-pipeline", ["go", "test", "./releasepipeline", "-count=1"]),
            ("deterministic-amd64", ["python", "-c", "import tasks; tasks.verify_deterministic_build('amd64')"]),
            ("deterministic-arm64", ["python", "-c", "import tasks; tasks.verify_deterministic_build('arm64')"]),
            ("contribution-receipt", ["go", "test", "./releasepipeline", "-run", "TestPullRequestReceiptRequiresReleaseNoteClassification", "-count=1"]),
            ("whitespace", ["git", "diff", "--check"]),
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
    discover_go_test("./integration", test_name, tags=["kubernetes_evidence"])
    env = managed_tool_environment()
    env.update({
        "CAMP_TEST_BINARY": str(CANDIDATE),
        "CAMP_KUBERNETES_EVIDENCE": "1",
    })
    result_path = EVIDENCE / "kubernetes-go-test.json"
    result_path.parent.mkdir(parents=True, exist_ok=True)
    with result_path.open("w", encoding="utf-8") as stream:
        completed = subprocess.run(
            [
            "go",
            "test",
            "-tags=kubernetes_evidence",
            "-json",
            "./integration",
            "-run",
            f"^{test_name}$",
            "-count=1",
        ],
            cwd=ROOT,
            env={**os.environ, **env},
            check=False,
            text=True,
            stdout=stream,
            stderr=subprocess.STDOUT,
        )
    if completed.returncode:
        raise RuntimeError(
            f"protected Kubernetes lifecycle failed; sanitize {result_path} before retention"
        )
    if sha256(CANDIDATE) != metadata["candidateSha256"]:
        raise RuntimeError("candidate changed during Kubernetes evidence")


@task(name="package")
def package_task(_context):
    """Delegate reproducible release evidence to the existing packaging authority."""
    version, commit = resolve_package_identity(ROOT, os.environ)
    head = git("rev-parse", "HEAD")
    if commit != head:
        raise RuntimeError(
            f"package COMMIT must match checked-out HEAD: got {commit}, want {head}"
        )
    env = {
        "VERSION": version,
        "COMMIT": commit,
        "SOURCE_DATE_EPOCH": os.environ.get("SOURCE_DATE_EPOCH")
        or git("show", "-s", "--format=%ct", "HEAD"),
        "OUTPUT_DIR": os.environ.get("OUTPUT_DIR", str(ROOT / "dist")),
    }
    run(["./packaging/build-release-evidence.sh", "build"], env=env)
