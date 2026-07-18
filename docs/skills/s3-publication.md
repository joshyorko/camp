# S3 Publication

## Current boundary

The S3 adapter implements signed get, head, list, conditional mutation, conditional delete, and a writer-safety probe. Immutable multipart publication is not implemented: `internal/adapters/s3store/store.go` returns `S3 multipart immutable upload is not implemented` from `PutImmutable`.

The executable has no production backend factory or configuration path selecting S3. File-backed publication remains the only composed backend. Do not claim MinIO or clean-machine portability until a real backend fixture and two-process lifecycle pass.

## Writer safety

Before accepting an S3-compatible endpoint for writer use, `ProbeWriterSafety` verifies create-only behavior, readback body and revision, conditional replacement, stale-revision rejection, conditional delete, and post-delete absence. A backend that violates those semantics returns `ErrUnsafeWriter`.

The multipart contract test requires multipart initiation, part upload, completion, and verified readback metadata. It is intentionally red until `PutImmutable` satisfies that contract.

## Evidence

- `internal/adapters/s3store/store.go`
- `internal/adapters/s3store/probe.go`
- `internal/adapters/s3store/store_test.go`
- `internal/adapters/s3store/multipart_test.go`
- `internal/adapters/filebackend/store.go`
