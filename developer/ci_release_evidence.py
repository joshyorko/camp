#!/usr/bin/env python3
import argparse
import hashlib
import json
import pathlib
import subprocess
import sys


TERMINAL_GATE_RESULTS = {"passed", "failed", "missing", "skipped", "gated"}


def normalize_gate_result(result):
    if result == "success":
        return "passed"
    if result in {"failure", "cancelled", "timed_out", "action_required", "stale"}:
        return "failed"
    if result == "skipped":
        return "skipped"
    if not result:
        return "missing"
    return "gated"


def write_parity(
    *, output, candidate_commit, run_id, run_attempt, run_url, direct, rcc
):
    normalized_direct = {
        name: normalize_gate_result(result) for name, result in direct.items()
    }
    normalized_rcc = {
        name: normalize_gate_result(result) for name, result in rcc.items()
    }
    results = [*normalized_direct.values(), *normalized_rcc.values()]
    if any(result not in TERMINAL_GATE_RESULTS for result in results):
        raise RuntimeError("parity contains an unsupported mandatory gate result")
    record = {
        "schemaVersion": 1,
        "candidateCommit": candidate_commit,
        "run": {"id": run_id, "attempt": run_attempt, "url": run_url},
        "mandatoryGates": {"direct": normalized_direct, "rcc": normalized_rcc},
        "complete": bool(results) and all(result == "passed" for result in results),
        "requiredConsecutiveCompleteRuns": 2,
        "qualifiedHistoricalRuns": [],
    }
    output = pathlib.Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )


def verify_candidate(build_root, commit, expected_sha256=None):
    build_root = pathlib.Path(build_root)
    candidate = build_root / "camp"
    manifest_path = build_root / "evidence" / "candidate.json"
    if not candidate.is_file():
        raise RuntimeError(f"preserved artifact layout is missing {build_root}/camp")
    if not manifest_path.is_file():
        raise RuntimeError(
            f"preserved artifact layout is missing {build_root}/evidence/candidate.json"
        )
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
    if manifest.get("commit") != commit:
        raise RuntimeError("candidate manifest commit does not match requested commit")
    if manifest.get("dirty") is not False:
        raise RuntimeError("candidate manifest does not describe a clean checkout")
    if manifest.get("candidateSha256") != digest:
        raise RuntimeError("downloaded candidate digest does not match manifest")
    if expected_sha256 and digest != expected_sha256:
        raise RuntimeError("downloaded candidate digest does not match requested digest")


def fetch_and_verify_tag_target(tag, candidate_commit, *, repository=None):
    if (
        not isinstance(tag, str)
        or tag != tag.strip()
        or tag.startswith("-")
        or tag.startswith("refs/")
    ):
        raise RuntimeError(f"invalid release tag {tag!r}")
    tag_ref = f"refs/tags/{tag}"
    valid = subprocess.run(
        ["git", "check-ref-format", tag_ref],
        cwd=repository,
        text=True,
        capture_output=True,
        check=False,
    )
    if valid.returncode:
        raise RuntimeError(f"invalid release tag {tag!r}")
    verification_ref = f"refs/camp-release-tags/{tag}"
    fetched = subprocess.run(
        [
            "git",
            "fetch",
            "--no-tags",
            "--force",
            "origin",
            f"+{tag_ref}:{verification_ref}",
        ],
        cwd=repository,
        text=True,
        capture_output=True,
        check=False,
    )
    if fetched.returncode:
        raise RuntimeError(
            f"fetch requested release tag {tag!r} from origin: "
            f"{fetched.stderr.strip()}"
        )
    resolved = subprocess.run(
        ["git", "rev-parse", f"{verification_ref}^{{commit}}"],
        cwd=repository,
        text=True,
        capture_output=True,
        check=False,
    )
    if resolved.returncode:
        raise RuntimeError(f"release tag {tag!r} does not resolve to a commit")
    target = resolved.stdout.strip()
    candidate = subprocess.run(
        ["git", "rev-parse", f"{candidate_commit}^{{commit}}"],
        cwd=repository,
        text=True,
        capture_output=True,
        check=False,
    )
    if candidate.returncode:
        raise RuntimeError(
            f"candidate commit {candidate_commit!r} does not resolve to a commit"
        )
    candidate = candidate.stdout.strip()
    if target != candidate:
        raise RuntimeError(
            f"release tag target {target} does not equal candidate commit {candidate}"
        )


def parse_arguments():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    verify = commands.add_parser("verify-candidate")
    verify.add_argument("--build-root", default="build")
    verify.add_argument("--commit", required=True)
    verify.add_argument("--sha256")
    parity = commands.add_parser("write-parity")
    parity.add_argument("--output", required=True)
    parity.add_argument("--candidate-commit", required=True)
    parity.add_argument("--run-id", required=True)
    parity.add_argument("--run-attempt", required=True)
    parity.add_argument("--run-url", required=True)
    parity.add_argument("--direct", type=json.loads, required=True)
    parity.add_argument("--rcc", type=json.loads, required=True)
    tag = commands.add_parser("fetch-verify-tag")
    tag.add_argument("--tag", required=True)
    tag.add_argument("--candidate-commit", required=True)
    return parser.parse_args()


def main():
    arguments = parse_arguments()
    if arguments.command == "verify-candidate":
        verify_candidate(
            arguments.build_root, arguments.commit, expected_sha256=arguments.sha256
        )
    elif arguments.command == "write-parity":
        write_parity(
            output=arguments.output,
            candidate_commit=arguments.candidate_commit,
            run_id=arguments.run_id,
            run_attempt=arguments.run_attempt,
            run_url=arguments.run_url,
            direct=arguments.direct,
            rcc=arguments.rcc,
        )
    else:
        fetch_and_verify_tag_target(arguments.tag, arguments.candidate_commit)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print(error, file=sys.stderr)
        raise SystemExit(1)
