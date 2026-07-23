# Install, upgrade, and remove Camp

## Supported platform

Camp targets Linux on `amd64` and `arm64`. Generic archives require a host-provided `pasta` executable. Native Windows and macOS artifacts are not produced. A compatibility claim for a particular distribution requires a clean-host test that this repository does not currently contain.

## Build from source

Use Go 1.25 or newer and build `./cmd/camp`. This is the direct development installation path; place the resulting binary at an operator-chosen location on `PATH` and remove that exact file to uninstall it.

## Generic archives

`packaging/build-archives.sh` produces deterministic Linux archives, completions, and `checksums.txt`. See [the package-specific instructions](../packaging/INSTALL.md). The archive test extracts the `amd64` archive and executes version, help, and completion paths.

`packaging/build-packages.sh` locally builds Linux `amd64` and `arm64` DEB, RPM, and APK files. Container fixtures test install, packaged completions, first-use managed-tool bootstrap, upgrade state preservation, and package-owned uninstall. A separate fixture exercises the same lifecycle through a local Homebrew tap. These are repository test paths, not published package repositories or a published tap.

No published release URL or immutable release checksum is available because [issue #12](https://github.com/joshyorko/camp/issues/12) and [issue #13](https://github.com/joshyorko/camp/issues/13) remain open. Consequently this document does not provide download or package-manager commands that would pretend published artifacts exist. The package lifecycle fixtures do not prove a real DevPod, Kubernetes, backend, or provider lifecycle on an installation host.
