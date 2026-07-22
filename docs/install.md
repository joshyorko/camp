# Install, upgrade, and remove Camp

## Supported platform

Camp targets Linux on `amd64` and `arm64`. Generic archives require a host-provided `pasta` executable. Native Windows and macOS artifacts are not produced. A compatibility claim for a particular distribution requires a clean-host test that this repository does not currently contain.

## Build from source

Use Go 1.25 or newer and build `./cmd/camp`. The resulting binary is the only currently proven installation path for development.

## Generic archives

`packaging/build-archives.sh` produces deterministic Linux archives, completions, and `checksums.txt`. See [the package-specific instructions](../packaging/INSTALL.md). The archive test extracts the `amd64` archive and executes version, help, and completion paths.

No published release URL or immutable checksum is available because [issue #12](https://github.com/joshyorko/camp/issues/12) and [issue #13](https://github.com/joshyorko/camp/issues/13) remain open. Consequently this document does not provide download, upgrade, package-manager, or uninstall commands that would pretend unpublished artifacts exist. Removal of a source-built binary is the operator's responsibility at the exact path where they installed it; Camp does not currently ship an uninstaller.
