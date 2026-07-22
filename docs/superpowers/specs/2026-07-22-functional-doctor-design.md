# Functional Doctor Design

## Outcome

`camp doctor` proves the bounded capabilities Camp actually needs and reports stable, redacted human and JSON evidence. It never promotes configuration presence, version output, or a TCP connection into functional health.

## Architecture

Doctor owns small orchestration probes around narrow injected interfaces. Production composition supplies Camp's existing adapters and Linux facilities; tests supply deterministic implementations. Every mutating probe creates a unique identity-bearing resource, records the identity it created, and removes only that exact resource.

The report remains a sorted list of independent capability results. A missing optional configuration produces `skipped-not-configured`; a functional or cleanup failure produces `blocked`; partial non-functional evidence produces `degraded`; only completed behavior plus verified cleanup produces `healthy`.

## Probe boundaries

- Managed DevPod and Hauler identity resolves through the lock-backed managed-tool resolver and compares canonical path, digest, and supported version evidence.
- Linux host probes independently test `/proc/self/fd`, `/dev/net/tun`, user namespaces, LSM context, and the detected host/container boundary.
- Pasta starts a unique disposable namespace/listener, proves listener reachability and scope, then stops the recorded process identity and verifies teardown.
- Backend I/O uses a unique prefix to create, conditionally replace, deliberately conflict with a stale identity, read back exact bytes, and remove only the object whose identity still matches. Cleanup failure is visible and blocks health.
- Provider, forwarding, workspace, and service probes run only when their required configuration exists. They prove their functional contract and identity-safe cleanup; absent configuration is reported explicitly.

## Safety and evidence

Each probe receives its own context deadline. Generated names contain a cryptographically random suffix. Cleanup uses `context.WithoutCancel` with a separate bound so cancellation cannot silently abandon resources. Errors are mapped to stable codes and remediation without embedding raw causes or credentials. Evidence contains only bounded, redacted identifiers, fingerprints, paths, and observed capability facts.

## Testing

Every behavior is introduced test-first and observed failing for the missing capability. Unit tests cover success, timeout, not-configured, conflict, identity mismatch, and cleanup failure. Linux integration tests use unique local resources; the real pasta test skips only when pasta is absent or the host forbids the required namespace operation, and such a skip is not acceptance proof. Final gates are full tests, race tests, vet, build, and whitespace validation.

