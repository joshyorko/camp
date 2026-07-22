# Release evidence and limitations

Camp is not currently released or clean-machine-ready. [Issue #13](https://github.com/joshyorko/camp/issues/13) remains open and the repository has no established release workflow.

The repository can build reproducible generic Linux archives and a checksum manifest. Homebrew metadata is a template with unresolved version, URL, and checksum tokens. There is currently no published artifact URL, SBOM, signed checksum, provenance attestation, native DEB/RPM/APK lifecycle, tap update, or clean install/upgrade/uninstall result to link.

A future release claim must identify the exact git commit and immutable artifact; attach its checksums, SBOM, and provenance; execute package smoke from the produced artifact; and separate credential-free fixtures from credential-gated external smoke. Skipped DevPod, Hauler, `pasta`, Docker, MinIO, provider, or credential gates remain skipped evidence.

The generated docs gate proves the command tree and completions are current. The repository Go tests, race tests, vet, build, and whitespace checks prove their named scopes only. None of them alone proves publication or deployment.
