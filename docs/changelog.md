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
- let ambiguous-open recovery wait for the exact DevPod workspace to leave its
  documented transient `Busy` state without issuing a second `devpod up`
- make successful close remove identity-verified forwarder evidence and the
  allowlisted private session runtime tree while preserving unexpected entries

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
- run hosted exact-candidate lifecycle evidence with a digest-pinned,
  privileged Podman fixture instead of the heavyweight default Room
  image hydration
- make the parity evidence job check out its candidate before invoking the
  repository verifier, and enforce candidate, real-tool, and publication
  receipt fields

### Portability and Safety

- harden CampKit export, IDE launch, MinIO portability, and real-evidence
  discovery so invalid or incomplete inputs fail before production effects
- capture only OCI images explicitly pushed through `CAMP_REGISTRY`; derive the
  transported inventory from the immutable registry cut instead of enumerating
  the workspace engine
- constrain real lifecycle cleanup to exact test-owned DevPod workspace IDs and
  recover interrupted-open IDs from test-owned controller journals
- escape terminal controls in bounded DevPod failure diagnostics and
  revalidate workspace source identity after transient readiness waits
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
