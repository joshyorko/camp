# Install Camp from a generic archive

The archive contains a static Linux `camp` binary for the architecture named
in the archive filename. Copy it to a directory on `PATH`, then install the
completion file for your shell from `completions/` if desired.

Camp requires the host-provided `pasta` executable from the `passt` project for
loopback-confined services. Generic archives do not install this external host
capability. Install `passt` using the supported mechanism for the Linux host and
verify that `pasta` is on `PATH` before running a lifecycle command.

The binary can bootstrap its pinned DevPod and Hauler tools on first use once
that behavior lands in Camp. The generic archive itself does not bundle those
tools or prove that unfinished bootstrap path.
