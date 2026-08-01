import os
import pathlib
import json
import re
import shutil
import subprocess
import tempfile


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[2]
CANDIDATE = REPOSITORY_ROOT / "build" / "camp"
EVIDENCE = REPOSITORY_ROOT / "build" / "evidence" / "does-it-work-on-my-machine"


def run(command, *, environment=None, capture=False, cwd=REPOSITORY_ROOT, input_text=None, timeout=None, check=True):
    merged = os.environ.copy()
    if environment:
        merged.update(environment)
    return subprocess.run(
        command,
        cwd=cwd,
        env=merged,
        check=check,
        text=True,
        input=input_text,
        timeout=timeout,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
    )


def managed_tool_environment():
    controller = pathlib.Path(tempfile.mkdtemp(prefix="camp-machine-test-", dir="/tmp"))
    runtime = controller / "runtime"
    runtime.mkdir(mode=0o700)
    environment = {
        "XDG_CONFIG_HOME": str(controller / "config"),
        "XDG_DATA_HOME": str(controller / "data"),
        "XDG_CACHE_HOME": str(controller / "cache"),
        "XDG_RUNTIME_DIR": str(runtime),
    }
    completed = run([str(CANDIDATE), "--json", "setup"], environment=environment, capture=True)
    paths = json.loads(completed.stdout)["result"]["path"]
    environment["PATH"] = os.pathsep.join([*paths, os.environ["PATH"]])
    return controller, environment


def selected_provider():
    explicit_provider = os.environ.get("CAMP_TEST_DEVPOD_PROVIDER")
    explicit_context = os.environ.get("CAMP_TEST_DEVPOD_CONTEXT")
    if explicit_provider and explicit_context:
        return explicit_provider, explicit_context
    completed = run([str(CANDIDATE), "--json", "config", "show", "--effective"], capture=True)
    effective = json.loads(completed.stdout)["result"]
    return (
        explicit_provider or effective["devpodProvider"],
        explicit_context or effective["devpodContext"],
    )


def locked_lifecycle_image():
    lock = (REPOSITORY_ROOT / "tools.lock.yaml").read_text(encoding="utf-8")
    match = re.search(r"(?ms)^  lifecycle:\n    image: (\S+)$", lock)
    if not match:
        raise RuntimeError("tools.lock.yaml lacks fixtures.lifecycle.image")
    return match.group(1)


def write_lightweight_workspace(source):
    devcontainer = source / ".devcontainer"
    devcontainer.mkdir(parents=True)
    (source / "README.md").write_text("# Camp machine acceptance\n", encoding="utf-8")
    (devcontainer / "devcontainer.json").write_text(
        json.dumps({
            "image": locked_lifecycle_image(),
            "privileged": True,
            "remoteUser": "podman",
        }) + "\n",
        encoding="utf-8",
    )


def cleanup_failed_workspace(environment, provider, context):
    listed = run(
        ["devpod", "list", "--context", context, "--output", "json"],
        environment=environment,
        capture=True,
        check=False,
        timeout=2 * 60,
    )
    if listed.returncode:
        print(listed.stdout, end="", flush=True)
        return False
    owned_root = pathlib.Path(environment["XDG_DATA_HOME"]) / "camp" / "remote-data-planes"
    matches = []
    for workspace in json.loads(listed.stdout):
        source = workspace.get("source", {}).get("localFolder", "")
        try:
            pathlib.Path(source).resolve().relative_to(owned_root.resolve())
        except (ValueError, OSError):
            continue
        if workspace.get("context") == context and workspace.get("provider", {}).get("name") == provider:
            matches.append(workspace.get("id", ""))
    if not matches:
        return True
    if len(matches) != 1 or not matches[0]:
        print(f"Refusing ambiguous DevPod cleanup for controller-owned sources: {matches}", flush=True)
        return False
    deleted = run(
        ["devpod", "delete", "--context", context, "--force", matches[0]],
        environment=environment,
        capture=True,
        check=False,
        timeout=10 * 60,
    )
    if deleted.returncode:
        print(deleted.stdout, end="", flush=True)
        return False
    return True


def run_selected_provider_first_open(environment, provider, context):
    root = pathlib.Path(tempfile.mkdtemp(prefix="camp-provider-test-", dir="/tmp"))
    succeeded = False
    try:
        source = root / "workspace"
        source.mkdir()
        write_lightweight_workspace(source)
        camp_name = "camp-machine-provider"
        setup_input = "\n".join((str(source), camp_name, "", provider, context, ""))
        run([str(CANDIDATE), "setup"], environment=environment, cwd=source, input_text=setup_input, timeout=10 * 60)
        first = run(
            [str(CANDIDATE), "open"], environment=environment, cwd=source,
            timeout=30 * 60, capture=True, check=False,
        )
        print(first.stdout, end="", flush=True)
        if first.returncode and "error [lifecycle_ambiguous]" in first.stdout:
            print("Retrying the identical open to reconcile DevPod's ambiguous outcome", flush=True)
            run([str(CANDIDATE), "open"], environment=environment, cwd=source, timeout=30 * 60)
        else:
            first.check_returncode()
        succeeded = True
    finally:
        if succeeded:
            closed = run(
                [str(CANDIDATE), "--json", "close", "--discard", "--camp", camp_name],
                environment=environment,
                cwd=source,
                timeout=10 * 60,
                capture=True,
                check=False,
            )
            if closed.returncode == 0:
                shutil.rmtree(root, ignore_errors=True)
            else:
                print(closed.stdout, end="", flush=True)
                workspace_clean = cleanup_failed_workspace(environment, provider, context)
                cleanup = run(
                    [str(CANDIDATE), "--json", "strike", "--purge", "--yes"],
                    environment=environment,
                    cwd=source,
                    timeout=10 * 60,
                    capture=True,
                    check=False,
                )
                if cleanup.returncode == 0 and workspace_clean:
                    shutil.rmtree(root, ignore_errors=True)
                else:
                    print(cleanup.stdout, end="", flush=True)
                    print(f"Preserved failed provider workspace for recovery: {root}", flush=True)
                raise RuntimeError("successful provider open did not close cleanly")
        else:
            workspace_clean = cleanup_failed_workspace(environment, provider, context)
            cleanup = run(
                [str(CANDIDATE), "--json", "strike", "--purge", "--yes"],
                environment=environment,
                cwd=source,
                timeout=10 * 60,
                capture=True,
                check=False,
            )
            if cleanup.returncode == 0 and workspace_clean:
                shutil.rmtree(root, ignore_errors=True)
            else:
                print(cleanup.stdout, end="", flush=True)
                print(f"Preserved failed provider workspace for recovery: {root}", flush=True)


def main():
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    run(["python", "developer/call_invoke.py", "local"])
    provider, context = selected_provider()
    controller, environment = managed_tool_environment()
    environment.update({
        "CAMP_TEST_BINARY": str(CANDIDATE),
        "CAMP_TEST_REAL_LIFECYCLE": "1",
        "CAMP_TEST_DEVPOD_PROVIDER": provider,
        "CAMP_TEST_DEVPOD_CONTEXT": context,
    })
    succeeded = False
    try:
        print(f"Testing Camp first open with DevPod provider {provider!r} in context {context!r}", flush=True)
        run_selected_provider_first_open(environment, provider, context)
        run(
            [
                "python",
                "-m",
                "robot",
                "--outputdir",
                str(EVIDENCE / "robot"),
                "robot_tests",
            ],
            environment=environment,
        )
        succeeded = True
    finally:
        if succeeded:
            shutil.rmtree(controller, ignore_errors=True)
        else:
            print(f"Preserved failed Camp controller for recovery: {controller}", flush=True)


if __name__ == "__main__":
    main()
