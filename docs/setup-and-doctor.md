# Tool setup and host diagnosis

`camp setup` resolves the DevPod and Hauler entries embedded from `tools.lock.yaml`, reuses matching executables, or downloads and checksum-verifies the locked Linux asset into Camp's managed tool directory. It prints the managed paths but does not edit shell startup files. `pasta` remains host-provided.

`camp doctor` reports a versioned capability model. Human and JSON rendering share the same model. The stable probe statuses are `healthy`, `degraded`, `blocked`, and `skipped-not-configured`; a blocked aggregate exits nonzero.

Doctor verifies the managed DevPod and Hauler identities from the embedded lock, runs a disposable Pasta namespace/listener/cleanup probe, and performs a unique file-backend transaction with readback, conditional replacement/conflict, and identity-safe cleanup. S3 diagnosis proves the configured credential chain and performs the same backend transaction when the backend is available. Provider, workspace, forwarding, and service checks are read-only reachability probes for configured active sessions; they are `skipped-not-configured` when no corresponding configuration or active journal record exists. These checks do not create a workspace, mutate a provider, or prove Kubernetes, T3/Sites, or a release.

Credentials are runtime inputs. Do not place access tokens or AWS secrets in Camp configuration, transcripts, journals, or capsules. Probe output is redacted and bounded, but operators should still treat raw third-party tool logs as potentially sensitive.

After setup and doctor, the production command tree exposes observed operational queries and guarded effects through `camp status`, `camp images list`, `camp images capture`, `camp images restore`, `camp serve status`, `camp serve logs`, `camp serve restart`, and the read-only `camp provider list`. These commands do not broaden doctor evidence: status and service operations require recorded session identities, image mutations revalidate ownership and lease state, and provider listing performs a context-scoped read only.
