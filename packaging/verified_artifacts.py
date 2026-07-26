#!/usr/bin/env python3
"""Create and recheck the immutable release-artifact verification boundary."""

import argparse
import hashlib
import json
import pathlib
import sys


ARCHITECTURES = ("amd64", "arm64")


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load(path):
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def fail(message):
    raise SystemExit(message)


def expected_artifacts(root, evidence):
    version = evidence.get("version")
    commit = evidence.get("commit")
    if not version or not commit:
        fail("release evidence lacks candidate version or commit")
    declared = {item.get("platform"): item for item in evidence.get("artifacts", [])}
    artifacts = []
    for architecture in ARCHITECTURES:
        platform = f"linux/{architecture}"
        archive = f"camp_{version}_linux_{architecture}.tar.gz"
        item = declared.get(platform)
        if not item or item.get("name") != archive or item.get("result") != "built":
            fail(f"release evidence does not declare built archive {archive}")
        path = root / archive
        if not path.is_file():
            fail(f"verified archive is missing: {archive}")
        digest = sha256(path)
        if item.get("sha256") != digest:
            fail(f"release evidence digest mismatch for {archive}")
        verification_path = root / f"verification-{architecture}.json"
        if not verification_path.is_file():
            fail(f"native verification is missing for {architecture}")
        verification = load(verification_path)
        expected = {
            "commit": commit,
            "platform": platform,
            "artifact": archive,
            "sha256": digest,
            "result": "passed",
        }
        for field, value in expected.items():
            if verification.get(field) != value:
                fail(f"native verification {architecture} has invalid {field}")
        artifacts.append(
            {
                "architecture": architecture,
                "path": archive,
                "size": path.stat().st_size,
                "sha256": digest,
                "verification": verification_path.name,
                "verificationSha256": sha256(verification_path),
                "result": "passed",
            }
        )
    actual_archives = sorted(path.name for path in root.glob("camp_*.tar.gz"))
    expected_archives = sorted(item["path"] for item in artifacts)
    if actual_archives != expected_archives:
        fail(
            "archive set differs from release evidence: "
            f"got {actual_archives}, want {expected_archives}"
        )
    return version, commit, artifacts


def create(root, manifest_path):
    evidence_path = root / "evidence.json"
    if not evidence_path.is_file():
        fail("release evidence is missing")
    evidence = load(evidence_path)
    version, commit, artifacts = expected_artifacts(root, evidence)
    manifest = {
        "schemaVersion": 1,
        "candidate": {"version": version, "commit": commit},
        "artifacts": artifacts,
        "verificationResult": "passed",
    }
    manifest_path.write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )


def recheck(root, manifest_path):
    if not manifest_path.is_file():
        fail("verified-artifact manifest is missing")
    manifest = load(manifest_path)
    evidence = load(root / "evidence.json")
    version, commit, expected = expected_artifacts(root, evidence)
    if manifest.get("schemaVersion") != 1:
        fail("unsupported verified-artifact manifest schema")
    if manifest.get("candidate") != {"version": version, "commit": commit}:
        fail("verified-artifact candidate identity changed")
    if manifest.get("verificationResult") != "passed":
        fail("verified-artifact manifest is not passed")
    if manifest.get("artifacts") != expected:
        fail("verified-artifact manifest entries changed")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("create", "recheck"))
    parser.add_argument("directory", type=pathlib.Path)
    parser.add_argument(
        "--manifest", type=pathlib.Path, default=pathlib.Path("verified-artifacts.json")
    )
    args = parser.parse_args()
    root = args.directory.resolve()
    manifest = args.manifest
    if not manifest.is_absolute():
        manifest = root / manifest
    if args.mode == "create":
        create(root, manifest)
    else:
        recheck(root, manifest)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(error, file=sys.stderr)
        raise SystemExit(1) from error
