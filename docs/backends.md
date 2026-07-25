# Backend configuration and evidence

Camp accepts strict credential-free `file:///absolute/path` and `s3://bucket/optional-prefix` backend identities. URL userinfo, credential-shaped query parameters, fragments, and encoded S3 identities are rejected.

`camp setup` persists the machine's non-secret backend and workspace defaults under the user configuration directory with an adjacent lock, mode-`0600` temporary file, fsync, and atomic rename. `camp init <root> --name <id>` resolves omitted values from those defaults and writes the explicit backend and workspace settings to `<root>/.camp/camp.yaml`; machine config never stores an active source or camp.

## File backend

The file backend requires an absolute `file:///` URL. Package and integration tests cover repository behavior, but a docs test is not a real open/sync/close lifecycle.

## S3 and MinIO

An S3 identity contains only bucket and prefix. Endpoint, region, path-style mode, and explicit insecure-transport policy are non-secret settings; credentials come from the standard AWS runtime credential chain. Plaintext HTTP endpoints require explicit insecure opt-in; this policy is not limited to loopback hosts.

The credential-free MinIO integration fixture exercises immutable object creation and read-back against a disposable local container. It does not prove an external S3 account, production credentials, IAM policy, networking, or cleanup. External credential-gated smoke remains unproved unless its exact command, target class, and result are attached to release evidence.
