#!/usr/bin/env python3
"""Create and validate the strict allowlist for protected Kubernetes evidence."""

import argparse
import json
import pathlib
import re
import sys


MAX_FILE_BYTES = 256 * 1024
HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")
SENSITIVE_KEY = re.compile(
    r"(?:^|[_-])(environment|env|kubeconfig|token|password|credential|certificate|private[_-]?key|client[_-]?key)(?:$|[_-])",
    re.IGNORECASE,
)
SENSITIVE_VALUE = (
    re.compile(r"-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----"),
    re.compile(r"\bBearer\s+[A-Za-z0-9._~+/=-]{12,}", re.IGNORECASE),
    re.compile(r"https?://[^/\s:@]+:[^/\s@]+@"),
    re.compile(r"\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\b"),
)

SCHEMAS = {
    "candidate-identity.json": {
        "schemaVersion", "candidateCommit", "candidateSha256", "relevantChangeCommit",
    },
    "gate-status.json": {
        "schemaVersion", "result", "candidateCommit", "candidateSha256", "provider",
        "devpodContext", "kubernetesContext", "ociCapability", "detail", "gate",
    },
    "cleanup-receipt.json": {
        "schemaVersion", "result", "detail", "candidateCommit", "candidateSha256",
        "scenarioId", "namespace", "resources", "workspaceIds",
    },
    "robot-results.json": {"schemaVersion", "result", "tests"},
}


def fail(message):
    raise ValueError(message)


def inspect_sensitive(value, path="$"):
    if isinstance(value, dict):
        for key, child in value.items():
            if SENSITIVE_KEY.search(key):
                fail(f"forbidden key at {path}.{key}")
            inspect_sensitive(child, f"{path}.{key}")
    elif isinstance(value, list):
        for index, child in enumerate(value):
            inspect_sensitive(child, f"{path}[{index}]")
    elif isinstance(value, str):
        for pattern in SENSITIVE_VALUE:
            if pattern.search(value):
                fail(f"sensitive value shape at {path}")


def load_allowlisted(directory):
    directory = pathlib.Path(directory)
    entries = sorted(path for path in directory.iterdir() if path.is_file())
    undeclared = [path.name for path in entries if path.name not in SCHEMAS]
    if undeclared:
        fail("undeclared evidence file: " + ", ".join(undeclared))
    documents = {}
    for path in entries:
        if path.stat().st_size > MAX_FILE_BYTES:
            fail(f"unbounded evidence file: {path.name}")
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
            fail(f"invalid JSON evidence {path.name}: {error}")
        if not isinstance(value, dict):
            fail(f"{path.name} must contain one JSON object")
        inspect_sensitive(value)
        extra = sorted(set(value) - SCHEMAS[path.name])
        if extra:
            fail(f"undeclared keys in {path.name}: {', '.join(extra)}")
        documents[path.name] = value
    return documents


def validate(args):
    documents = load_allowlisted(args.directory)
    if not HEX40.fullmatch(args.commit) or not HEX64.fullmatch(args.sha256):
        fail("expected candidate identity must be exact lowercase hex")
    required = set(SCHEMAS)
    missing = sorted(required - set(documents))
    if args.require_pass and missing:
        fail("missing required evidence file: " + ", ".join(missing))
    for name in ("candidate-identity.json", "gate-status.json", "cleanup-receipt.json"):
        value = documents.get(name)
        if value is None:
            continue
        if value.get("candidateCommit") != args.commit or value.get("candidateSha256") != args.sha256:
            fail(f"{name} does not match exact candidate commit and SHA-256")
    identity = documents.get("candidate-identity.json")
    if identity is not None:
        relevant = identity.get("relevantChangeCommit")
        if not isinstance(relevant, str) or not HEX40.fullmatch(relevant):
            fail("candidate-identity.json relevantChangeCommit must be exact lowercase hex")
        if args.relevant_change and relevant != args.relevant_change:
            fail("protected evidence predates or mismatches the latest Kubernetes/provider/confinement change")
    if args.require_pass:
        for name in ("gate-status.json", "cleanup-receipt.json", "robot-results.json"):
            if documents[name].get("result") != "passed":
                fail(f"{name} is not passing protected evidence")
        tests = documents["robot-results.json"].get("tests")
        if tests != [{"name": "TestKubernetesLifecycleVertical", "result": "passed"}]:
            fail("robot-results.json does not prove the named Kubernetes lifecycle vertical")


def validate_provenance(args):
    try:
        run_sha = int(args.workflow_id)
    except (TypeError, ValueError):
        fail("workflow-id must be decimal")

    run = json.loads(pathlib.Path(args.run).read_text(encoding="utf-8"))
    jobs = json.loads(pathlib.Path(args.jobs).read_text(encoding="utf-8"))
    if run.get("event") != "workflow_dispatch":
        fail("expected workflow_dispatch run event")
    if run.get("status") != "completed":
        fail("expected completed workflow run status")
    if run.get("conclusion") != "success":
        fail("expected successful workflow run")
    if not HEX40.fullmatch(run.get("head_sha", "")):
        fail("expected run head SHA-256 must be exact lowercase 40 hex")
    if run.get("head_sha") != args.commit:
        fail("run head sha does not match candidate commit")
    if run.get("workflow_name") != args.workflow_name:
        fail("run workflow name does not match protected provider workflow")
    if int(run.get("workflow_id")) != run_sha:
        fail("run workflow database id does not match provider workflow")
    inputs = run.get("inputs", {})
    if not isinstance(inputs, dict):
        fail("run inputs must be a JSON object")
    if inputs.get("candidate_commit") != args.commit:
        fail("run did not receive the expected candidate_commit input")
    if inputs.get("candidate_sha256") != args.sha256:
        fail("run did not receive the expected candidate_sha256 input")

    if not args.required_environment:
        fail("required environment is required")
    entries = jobs.get("jobs")
    if not isinstance(entries, list):
        fail("run jobs payload must be a JSON object named jobs")
    for entry in entries:
        if entry.get("name") != args.job_name:
            continue
        if entry.get("conclusion") != "success":
            fail("evidence job did not succeed")
        if entry.get("status") != "completed":
            fail("evidence job did not complete")
        environment = entry.get("environment")
        if not isinstance(environment, dict) or environment.get("name") != args.required_environment:
            fail("evidence job did not run in the protected workflow environment")
        return
    fail(f"run missing expected job {args.job_name}")


def sanitize_go_test(args):
    tests = {}
    with pathlib.Path(args.input).open(encoding="utf-8") as stream:
        for line in stream:
            if len(line) > 64 * 1024:
                fail("unbounded go test event")
            event = json.loads(line)
            name = event.get("Test")
            action = event.get("Action")
            if name != "TestKubernetesLifecycleVertical":
                continue
            if action == "run":
                tests[name] = "running"
            elif action in ("pass", "fail"):
                tests[name] = "passed" if action == "pass" else "failed"
    result = tests.get("TestKubernetesLifecycleVertical", "missing")
    document = {
        "schemaVersion": 1,
        "result": "passed" if result == "passed" else "failed",
        "tests": [{"name": "TestKubernetesLifecycleVertical", "result": result}],
    }
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def parser():
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)
    check = commands.add_parser("validate")
    check.add_argument("--directory", required=True)
    check.add_argument("--commit", required=True)
    check.add_argument("--sha256", required=True)
    check.add_argument("--relevant-change")
    check.add_argument("--require-pass", action="store_true")
    check.set_defaults(function=validate)
    sanitize = commands.add_parser("sanitize-go-test")
    sanitize.add_argument("--input", required=True)
    sanitize.add_argument("--output", required=True)
    sanitize.set_defaults(function=sanitize_go_test)
    provenance = commands.add_parser("validate-provenance")
    provenance.add_argument("--run", required=True)
    provenance.add_argument("--jobs", required=True)
    provenance.add_argument("--commit", required=True)
    provenance.add_argument("--sha256", required=True)
    provenance.add_argument("--workflow-id", required=True)
    provenance.add_argument("--workflow-name", required=True)
    provenance.add_argument("--job-name", required=True)
    provenance.add_argument("--required-environment", required=True)
    provenance.set_defaults(function=validate_provenance)
    return root


def main():
    args = parser().parse_args()
    try:
        args.function(args)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"kubernetes evidence rejected: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
