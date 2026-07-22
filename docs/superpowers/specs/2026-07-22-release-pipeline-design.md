# Verifiable Release Pipeline Design

## Goal

Establish a least-privilege GitHub Actions pipeline that proves Camp's credential-free checks and produces verifiable Linux release evidence without allowing pull requests to publish releases or receive protected credentials.

## Architecture

The pipeline has three boundaries. Pull-request CI runs with read-only repository permission and covers unit, race, vet, vulnerability, integration, mandatory containerized MinIO, archive construction, locked-tool bootstrap, and downloaded-artifact smoke tests. A release-candidate workflow builds the same Linux amd64 and arm64 archives, generates checksums and per-artifact SBOMs, downloads the uploaded bundle into a clean job, verifies every digest, installs and exercises the archives, and records commit/platform/result/gating evidence. Publication runs only for a version tag or an explicit manual dry run, uses a protected `release` environment, and receives only the permissions required for GitHub release upload and attestations.

Credentialed provider tests are separate from mandatory credential-free gates. They require an explicit provider profile selected by a scheduled or manual invocation and a protected `release-providers` environment; absence of an authorized profile is recorded as gated evidence, never represented by `if: secrets.* != ''` or a successful skipped job. This branch will not claim a provider profile until a successful protected run exists.

## Release contract

- Supported distributables are reproducible `tar.gz` archives for Linux amd64 and arm64 plus the rendered Homebrew formula metadata derived from those archives.
- `checksums.txt` is generated over final archives and verified only after GitHub artifact download.
- Each SPDX JSON SBOM records the exact SHA-256 digest of its final archive as an external reference and package checksum.
- A machine-readable evidence manifest records commit, version, platform, artifact digest, SBOM, verification result, and every gated or unsupported lane.
- GitHub artifact attestations bind uploaded release assets to the workflow identity when GitHub supports attestation for the event/repository.
- Checksums adjacent to mutable release assets provide integrity, not publisher authenticity; provenance/attestation supplies the identity binding.

## Safety and failure handling

All third-party Actions use immutable commit SHAs. Workflow-level permissions default to read-only or none, concurrency cancels stale PR work but never an in-progress tagged release, and artifacts have explicit retention. Publication cannot run for pull requests or arbitrary branch pushes. Build, verification, and publication are separate jobs so release upload cannot happen unless the downloaded artifacts pass digest, install, CLI, SBOM, and evidence validation.

## Testing

Repository tests parse workflows and release outputs as data. Tests fail first for missing permissions, unpinned Actions, unsafe secret gates, absent mandatory matrices, mismatched checksums/SBOM digests, or smoke tests that exercise workspace binaries. Local verification uses Go tests, `go vet`, `git diff --check`, and containerized workflow linting; it does not install host packages or publish a release.
