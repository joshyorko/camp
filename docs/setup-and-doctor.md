# Tool setup and host diagnosis

`camp setup` resolves the DevPod and Hauler entries embedded from `tools.lock.yaml`, reuses matching executables, or downloads and checksum-verifies the locked Linux asset into Camp's managed tool directory. It prints the managed paths but does not edit shell startup files. `pasta` remains host-provided.

`camp doctor` reports a versioned capability model. Human and JSON rendering share the same model. The stable probe statuses are `healthy`, `degraded`, `blocked`, and `skipped-not-configured`; a blocked aggregate exits nonzero.

Doctor currently hashes the resolved DevPod and Hauler binaries and runs their identity commands. Pasta and file-backend probes do not prove functional lifecycle behavior. S3 diagnosis can prove credential-chain resolution but not backend read/write, compare-and-swap, or cleanup. Doctor does not prove DevPod workspace creation, Kubernetes, forwarding, Hauler services, T3/Sites, or a release.

Credentials are runtime inputs. Do not place access tokens or AWS secrets in Camp configuration, transcripts, journals, or capsules. Probe output is redacted and bounded, but operators should still treat raw third-party tool logs as potentially sensitive.
