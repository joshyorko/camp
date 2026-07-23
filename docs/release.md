# Release evidence and limitations

Camp is not currently released or clean-machine-ready. [Issue #13](https://github.com/joshyorko/camp/issues/13) remains open. The repository has a release-candidate workflow, but no successful run or published release is linked here as release evidence.

The repository can build reproducible Linux archives and native packages, checksums, per-archive SPDX SBOMs, a rendered Homebrew formula, and a machine-readable evidence manifest. Local container fixtures exercise native-package and local-tap install, upgrade, and uninstall. The release workflow verifies downloaded candidates on `amd64` and `arm64`, then gates attestation and publication behind a tag or explicit publish request.

Those candidate mechanics are not publication evidence. There is currently no published Camp artifact, immutable release checksum, successful attestation, GitHub release, published tap update, or credentialed provider result to link. The Homebrew source template retains unresolved version, URL, and checksum tokens until rendered from real release inputs.

A future release claim must identify the exact git commit and immutable artifact; attach its checksums, SBOM, and provenance; execute package smoke from the produced artifact; and separate credential-free fixtures from credential-gated external smoke. Skipped DevPod, Hauler, `pasta`, Docker, MinIO, provider, or credential gates remain skipped evidence.

The generated docs gate proves the command tree and completions are current. The repository Go tests, race tests, vet, build, and whitespace checks prove their named scopes only. None of them alone proves publication or deployment.
