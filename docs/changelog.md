# Camp change log

## Unreleased

### Developer Experience

- add the RCC-contained developer factory with one repository-local, truthfully
  stamped candidate and machine-readable gate evidence
- upgrade the contained Robot Framework runtime to 7.4.2 and add isolated
  black-box CLI suites with requirement traceability
- run RCC factory jobs alongside the existing direct Go CI during the parity
  period and delegate release packaging to the existing packaging authority

### Portability and Safety

- capture only OCI images explicitly pushed through `CAMP_REGISTRY`; derive the
  transported inventory from the immutable registry cut instead of enumerating
  the workspace engine
- constrain real lifecycle cleanup to exact test-owned DevPod workspace IDs and
  recover interrupted-open IDs from test-owned controller journals
- align real lifecycle fixtures with named Camp initialization and
  directory-based discovery; retain fresh-controller reopen as a failing
  product gate until durable history is available to a new controller
- keep private RCC homes outside the Go module and reject unverified RCC assets,
  mismatched hosts, invalid lock provenance, and candidate drift

### Documentation

- require same-change operational guide improvements and exact evidence receipts
  for human and agent contributions
- distinguish current reusable skills, user-visible changelog entries, gated
  product claims, and published release evidence
