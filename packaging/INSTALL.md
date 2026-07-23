# Install Camp from a generic archive

The archive contains a static Linux `camp` binary for the architecture named
in the archive filename. Copy it to a directory on `PATH`, then install the
completion file for your shell from `completions/` if desired.

Camp requires the host-provided `pasta` executable from the `passt` project for
loopback-confined services. Generic archives do not install this external host
capability. Install `passt` using the supported mechanism for the Linux host and
verify that `pasta` is on `PATH` before running a lifecycle command.

The first lifecycle command automatically downloads or reuses Camp's pinned,
checksum-verified DevPod and Hauler binaries under the XDG data directory. Camp
passes those verified executable paths directly to its adapters, so no PATH export
or shell-startup edit is required. `camp setup` remains available to
prewarm and inspect the same managed installation before a lifecycle command.
The generic archive does not bundle those tools, and archive smoke tests do not
prove that downloads or a real lifecycle work on the installation host.
