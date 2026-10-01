# Follow-ups

Out-of-scope work noticed while implementing a slice. Add an entry instead of doing the work.
Format: `- [ ] (<prompt that found it>) <what> — <why it matters>`.

- [x] (P0-01) Add testcontainers-go integration tests (Postgres, SeaweedFS) to the `test` job once there is product code. Done in P1-01 for PostgreSQL (`internal/platform/testdb`); CI fails if they skip. SeaweedFS tests arrive with the first S3 code (P1-04).
- [ ] (P0-01) Run actionlint (with shellcheck) in CI. It currently runs only locally. Candidate: a shared reusable workflow. Owner: P1-19 or earlier.
- [ ] (P1-01) Add the problem types `idempotency-key-required` (400) and `idempotency-key-in-progress` (409, with `Retry-After`), and the `Idempotent-Replayed` response header, to OpenAPI v0 in the next relay-contracts release. relay-api already returns them (docs/platform.md); the spec names only `idempotency-key-reused`. Owner: next contracts change (P1-02 or P1-03).
- [ ] (P1-01) Run the API with a least-privilege database role: the migration Job keeps the owner role `relay`, and relay-api and relay-worker use a DML-only role (CNPG managed role plus default privileges per module schema). Today all three use `relay`. Owner: P1-19.
- [ ] (P1-01) Give relay-api an insert-only River client and enqueue jobs in the request transaction (`db.TxFrom`), and add the worker's Kubernetes RBAC and API-server egress for the Argo Workflows bridge. Owner: P1-05.
- [ ] (P1-01) The request transaction spans the whole mutating handler. That is right for short JSON operations. Any operation that calls an external service (S3, Keycloak, a hosted transcription API) must do that work in a River job, not inside the request, so the transaction and the Idempotency-Key row lock stay short. Enforce it in review from P1-04 onward.
