# AI Usage Disclosure

## 1. Tools Used
- **OpenAI Codex / Antigravity**: Used for boilerplate generation, SQL schema and query scaffolding, OpenAPI contract drafting, test scaffolding, and review assistance.
- **GitNexus**: Used for read-only code-flow and change-impact analysis across modules.

---

## 2. How AI Contributed
- **Scaffolding & Boilerplate**: Generated repetitive type mappings, Gin handler routing patterns, and OpenAPI 3.0 schema definitions.
- **Test Scaffolding**: Generated unit test skeletons and mock HTTP fixtures for Gin handlers and PSP endpoints.
- **SQL / Query Design**: Drafted `sqlc` query definitions matching `DATABASE.md` specifications.
- **Edge-Case Brainstorming**: Aided in reviewing distributed crash windows and identifying boundary conditions for idempotency fingerprint hashing.
- **Automated Verification**: Ran formatting (`go fmt`), linting, race-detector testing (`go test -race ./...`), and diff verification across phases.

---

## 3. Decisions Made Independently or Against AI Suggestions

The human author maintained complete architectural ownership and made the following specific engineering decisions, rejecting alternative AI suggestions:

1. **PostgreSQL as Sole Coordination Boundary (No Redis / No Distributed Locks)**:
   - *AI Suggestion*: AI suggested introducing Redis or Redlock for distributed invoice locking and fast idempotency caching.
   - *Human Decision*: Explicitly rejected external caching infrastructure. PostgreSQL row-level locks (`SELECT FOR UPDATE`) and a partial unique index (`uq_payment_attempts_unresolved_invoice WHERE status IN ('PROCESSING', 'UNKNOWN')`) provide absolute ACID durability across application restarts and multiple replicas without adding operational complexity or split-brain risks.

2. **Strict Separation of Invoice State and Payment Attempt State**:
   - *AI Suggestion*: AI proposed adding a `PROCESSING` state to the `invoices` table while the external PSP call was underway.
   - *Human Decision*: Strictly rejected. An invoice remains `OPEN` until a definitive payment succeeds. The in-flight payment attempt alone captures `PROCESSING` or `UNKNOWN`. Adding `PROCESSING` to invoices would violate state machine atomicity during PSP timeouts or network failures.

3. **Two-Phase Transactions with External HTTP Call Completely Outside DB Transactions**:
   - *AI Suggestion*: AI generated an initial payment handler prototype that held an active database transaction open while awaiting the external PSP HTTP response.
   - *Human Decision*: Refactored into two distinct, short database transactions (Transaction A for Claim, Transaction B for Finalize). Holding a database connection and table locks during external network I/O exhausts database connection pools under high traffic or PSP latency.

4. **Bounded Timeout and Preserving Uncertain PSP Outcomes (`UNKNOWN`)**:
   - *AI Suggestion*: AI suggested retrying or marking payments as `FAILED` upon receiving an HTTP timeout from the PSP.
   - *Human Decision*: Rejected marking timeouts as failed. In distributed payment systems, a timeout does not mean the payment failed—it could have succeeded at the gateway. The human author enforced transitioning the attempt to `UNKNOWN`, returning `HTTP 202 Accepted`, and preventing blind retries without reconciliation.

5. **PostgreSQL Transactional Outbox for Webhook Delivery (No RabbitMQ/Kafka)**:
   - *AI Suggestion*: AI suggested introducing RabbitMQ or Kafka to decouple webhook delivery.
   - *Human Decision*: Enforced a PostgreSQL-backed transactional outbox table (`webhook_deliveries`). Domain state transitions (`invoice.created`, `invoice.paid`, `invoice.payment_failed`) atomically insert outbox records within the same transaction. A background Go worker claims jobs safely using `SELECT FOR UPDATE SKIP LOCKED` and an exact 6-attempt backoff schedule.

---

## 4. Corrections and Independent Verification

- **Enum vs. Check Constraint Migration**: AI initially attempted to rewrite the migration files to replace PostgreSQL `CREATE TYPE ... AS ENUM` with `VARCHAR + CHECK`. The human author verified the enum design and instructed AI to fix syntax errors while preserving native PostgreSQL enums.
- **Interface Segregation in Domain Modules**: When integrating transactional webhooks into the invoice creation path, AI initially tried to inject the full database worker interface into `internal/invoice`. The human author corrected this by defining a minimal `OutboxQueuer` interface (`ListActiveWebhookEndpointsByBusinessID` + `CreateWebhookDelivery`), keeping domain boundaries clean.
- **Independent Verification**: All business logic, concurrency guarantees, and money calculations were verified independently using:
  - `go test -v ./...` (100% passing across all domain packages).
  - `go test -race ./...` (zero data races detected).
  - Dedicated concurrency test (`TestConcurrentPaymentChargesOnce` with 8 parallel payment requests).
  - Automated smoke test script (`tests/smoke_test.sh`).
