# ADR 0006: Confine Hauler services with supervised pasta units

![A supervised service tent and supply carts remain inside a closed loopback safety perimeter](../assets/adr-0006-loopback-confinement.png)

## Status

Accepted on 2026-07-14.

## Context

Pinned Hauler v2.0.1 binds both its registry and fileserver broadly and exposes no bind-address option. Starting either service directly would violate Camp's loopback-only contract. A runtime probe on Bluefin proved that `pasta` can put Hauler in a private network namespace while publishing exactly one host-loopback mapping.

## Decision

Each Hauler registry or fileserver is one `PastaLoopback` service unit owned by Camp's persistent supervisor. Direct execution of a Hauler service command is prohibited. The only accepted launch shape is:

```text
pasta --foreground --quiet --log-file PRIVATE_LOG --pid PRIVATE_PID \
  --ipv4-only --host-lo-to-ns-lo \
  --tcp-ports 127.0.0.1/HOST:GUEST \
  --udp-ports none --tcp-ns none --udp-ns none -- \
  HAULER_ARGV...
```

Camp resolves a compatible `pasta` executable before acquiring a lease, creating or adopting a materialization, publishing endpoints, detaching a supervisor, or starting Hauler. Missing or incompatible confinement fails closed with a typed error naming the external `passt` package and the detected host/container boundary. Camp never falls back to raw Hauler, installs a host package, changes firewall state, or silently selects another confinement mechanism.

Before launch, the journal records a stable launch token, desired host/guest mapping, launcher path and version, environment fingerprint, and private pid/log paths. Before readiness is acknowledged it records the exact helper and child identities, boot ID and start ticks, observed executables and argv, parent/group/session identities, child network-namespace inode, endpoints, and desired/observed state. The desired launcher and observed helper executable are separate facts because an installed dispatcher may exec an optimized binary.

Readiness requires exactly one validated helper with exactly one direct Hauler child, shared dedicated PGID/SID, a child network namespace distinct from the supervisor, proven pre-bind absence, an IPv4 host listener exactly on `127.0.0.1:HOST`, no host IPv6 or wildcard listener and no host listener at `GUEST`, the Hauler-owned guest listener at `GUEST`, causal agreement with the recorded mapping, and service-specific HTTP 200 through the host mapping. Host `ss users:(...)` ownership is optional evidence because it is not observable on every supported host.

Recovery discovers a pending unit by launch token, private pidfile, and exact argv. It adopts the unit only when every identity and network postcondition matches; otherwise it revalidates and reaps only those exact processes before restart. Helper loss can leave an isolated Hauler orphan, so restart first stops that exact child. Child loss reaps any surviving helper. An unknown stable-port occupant fails closed and is never signalled or replaced with a different endpoint after DevPod environment injection. If `pasta` later disappears or becomes incompatible, Camp blocks restart but still cleans up already-recorded live identities without requiring the executable.

Shutdown is child-first. Camp revalidates and terminates the exact Hauler child, waits for the helper to exit, then revalidates and terminates the helper if necessary. It never signals a process group without validating every member. Success is the observed absence of both identities, the group, namespace, mapping, and owned sandbox references; the helper exit code is not authoritative.

`pasta` is an external host capability, not a `tools.lock.yaml` asset. Native packages depend on `passt`, the Homebrew formula depends on `passt`, and generic archives document it as a prerequisite. Setup and doctor validate the option surface and a real namespace/mapping probe but never invoke a host package manager.

## Consequences

Broad Hauler listeners exist only inside an isolated child namespace. Partial launch, helper loss, PID reuse, stable-port conflict, and cleanup remain recoverable without signalling unrelated processes or exposing a wildcard host listener. Supported Linux environments must supply a capability-compatible `pasta`; package presence alone is not proof that user namespaces, `/dev/net/tun`, device policy, or the host LSM permit the runtime contract.
