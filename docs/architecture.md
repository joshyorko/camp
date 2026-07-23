# Architecture

```text
operator -> Cobra CLI -> application lifecycle -> typed adapters
                         |                    |-> DevPod workspace/provider
                         |                    |-> Hauler registry/fileserver
                         |                    `-> file or S3 object store
                         `-> durable journal -> recovery and guarded cleanup
```

Cobra owns parsing and presentation mode. Application use cases own lifecycle ordering and recovery decisions. Adapters preserve typed arguments to external tools and storage. The durable journal records intents before effects and facts after observed outcomes; ownership, process identity, leases, digests, and compare-and-swap checks guard destructive or publishing operations.

The generated [command reference](generated/commands.md) is derived from this production composition. ADRs under `docs/adr/` contain the detailed safety contracts and retain the validated artwork supplied with the repository.
