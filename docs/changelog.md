# Camp change log

## Unreleased

### Lifecycle and Remote Workspaces

- add typed, event-driven lifecycle presentation and wire rich checkpoint,
  sync, close, recovery, status, and failure outcomes through the production
  CLI without deriving state from subprocess text
- add immutable remote Hauler checkpoints with authoritative pointer
  revalidation, service write barriers, resumable attempt adoption, bounded
  worker envelopes, and private immutable return exports
- add portable controller, blueprint, profile, provenance, timeline, and
  execution-binding contracts with strict persisted identity validation

### Developer Experience

- add the RCC-contained developer factory with one repository-local, truthfully
  stamped candidate and machine-readable gate evidence
- make the RCC `local` task link the verified development candidate into
  `~/.local/bin` while retaining `install` as a compatibility alias, and derive
  local package identity from a clean checkout without required shell variables
- upgrade the contained Robot Framework runtime to 7.4.2 and add isolated
  black-box CLI suites with requirement traceability
- run RCC factory jobs alongside the existing direct Go CI during the parity
  period and delegate release packaging to the existing packaging authority
- require exact-candidate RCC lifecycle evidence, ownership-checked cleanup
  receipts, normalized parity results, and candidate-bound release/tag
  verification before publication

### Portability and Safety

- harden CampKit export, IDE launch, MinIO portability, and real-evidence
  discovery so invalid or incomplete inputs fail before production effects
- capture only OCI images explicitly pushed through `CAMP_REGISTRY`; derive the
  transported inventory from the immutable registry cut instead of enumerating
  the workspace engine
- constrain real lifecycle cleanup to exact test-owned DevPod workspace IDs and
  recover interrupted-open IDs from test-owned controller journals
- align real lifecycle fixtures with named Camp initialization and
  directory-based discovery; let a fresh controller reopen from the discovered
  manifest and durable backend pointer when validated local history is empty,
  while retaining the real fresh-controller lifecycle as a product gate until
  the exact candidate passes it
- keep private RCC homes outside the Go module and reject unverified RCC assets,
  mismatched hosts, invalid lock provenance, and candidate drift

### Documentation

- require same-change operational guide improvements and exact evidence receipts
  for human and agent contributions
- distinguish current reusable skills, user-visible changelog entries, gated
  product claims, and published release evidence
