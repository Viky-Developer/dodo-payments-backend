Dodo Payments Backend Take-Home — Design

1. Overview

This service implements a small invoice and payment backend for the Dodo Payments take-home. The design focuses on payment correctness, concurrency, idempotency, external PSP failures, durable webhook delivery, and clear operational behavior.

The implementation uses Go with Gin, PostgreSQL, pgx/v5, sqlc, Goose, and Docker Compose. Go was chosen for simple concurrency, strong HTTP support, explicit error handling, and a small deployment footprint. PostgreSQL is the main coordination and durability boundary; no Redis, RabbitMQ, Kafka, or additional database is required.

2. Data Model

A Business owns API keys, Customers, Invoices, and Webhook Endpoints. A Customer belongs to one Business. An Invoice belongs to a Business and Customer and contains line items. Payment Attempts record interactions with the mock PSP. Idempotency Keys store durable request identity and completed HTTP responses. Webhook Deliveries are the PostgreSQL-backed durable outbox.

The exact physical schema is defined in DATABASE.md.

Application primary and foreign keys use Snowflake-style signed 64-bit integers stored as PostgreSQL BIGINT. This gives compact 8-byte keys and roughly time-ordered identifiers. created_at remains the authoritative business timestamp.

Raw Snowflake IDs are not exposed directly. At the API boundary, public IDs use resource prefixes such as cus_, inv_, and att_ plus a keyed reversible ID codec. The database stores only the numeric ID. Resource type is part of the codec context so changing a prefix cannot turn one resource ID into another. Public-ID encoding is not authorization; every business-owned query is still scoped by authenticated business_id.

Money is stored only as integer cents. Invoice totals are calculated by the server as quantity * unit_amount_cents; a client-supplied total is never trusted.

Indexes are limited to required access and correctness paths. A partial unique index on payment_attempts(invoice_id) where status is PROCESSING or UNKNOWN prevents more than one unresolved payment for an invoice.

At much larger scale, I would revisit indexes using production query data and consider partitioning high-volume payment/webhook history. Those changes are unnecessary for this assignment.

3. Invoice State Machine

Invoice states are:

DRAFT
OPEN
PAID
VOID
UNCOLLECTIBLE

The normal lifecycle is:

DRAFT → OPEN → PAID
            ↘ VOID
            ↘ UNCOLLECTIBLE

PAID, VOID, and UNCOLLECTIBLE are terminal states for this assignment.

Payment processing does not introduce a PROCESSING invoice state. Processing belongs to the Payment Attempt.

A confirmed successful payment transitions an OPEN invoice to PAID. A definitive payment failure leaves the invoice payable. An uncertain PSP outcome does not mark the invoice PAID or definitively failed.

Application logic and conditional SQL enforce transitions. Database CHECK constraints restrict stored values but do not implement the transition graph.

4. Payment Correctness & Failure Modes

Concurrency mechanism

PostgreSQL is the coordination boundary. Payment claiming uses short transactions, invoice validation/locking, durable idempotency state, and the partial unique unresolved-payment index.

Database coordination was chosen instead of an in-memory mutex because correctness must survive multiple application instances and restarts. Redis/distributed locking is unnecessary because PostgreSQL already provides the required coordination.

A database transaction is never held while waiting for PSP HTTP:

short claim transaction
        ↓
PSP HTTP call
        ↓
short finalization transaction

A. Two simultaneous POST /pay requests

Both requests can arrive concurrently, but they cannot both claim an unresolved payment for the same invoice. The invoice is validated during the claim transaction, and the partial unique index provides a durable invariant.

Only the successfully claimed operation may call the PSP. The competing request returns the documented conflict/in-progress response. This prevents concurrent requests from causing two charges.

B. PSP timeout

tok_timeout waits 30 seconds before returning success. The service uses a configurable bounded HTTP timeout, currently approximately three seconds, so the API does not wait for the entire PSP delay.

A timeout does not prove whether the PSP processed the payment. The Payment Attempt therefore becomes UNKNOWN. The invoice is not marked PAID or definitively failed, and the API returns 202 Accepted.

The unresolved-payment constraint prevents a different idempotency key from starting another charge. Retrying the original completed idempotency operation returns the stored response without another PSP call.

The mock PSP provides no status lookup/reconciliation API, so this implementation does not invent one.

C. PSP succeeds, then application crashes before persistence

A distributed failure window remains:

PROCESSING persisted
→ PSP called
→ PSP charges
→ application crashes
→ success not persisted

PostgreSQL cannot atomically commit an external HTTP side effect. The implementation therefore does not claim exactly-once charging.

The unresolved attempt prevents blind recharging. In production, PSP-side idempotency, status lookup, PSP webhooks, or reconciliation would be required to safely resolve this case.

D. Same Idempotency-Key with different body

The service calculates a deterministic SHA-256 fingerprint from the canonical payment request, including the mock card token.

idempotency_keys enforces:

UNIQUE(business_id, idempotency_key)

Same key + same fingerprint + completed operation returns the stored HTTP status/body without another PSP call.

Same key + different fingerprint returns 409 Conflict.

E. POST /pay on a paid invoice

A PAID invoice is not payable. The service rejects the request before creating another payment operation or calling the PSP.

5. Idempotency

Idempotency is durable PostgreSQL state, not an in-memory map or Redis entry.

The flow is:

receive Idempotency-Key
        ↓
calculate request hash
        ↓
claim durable idempotency operation
        ↓
create PROCESSING payment attempt
        ↓
commit
        ↓
call PSP
        ↓
persist result + stored HTTP response

A completed retry can therefore return the same result after an application restart.

HTTP idempotency status and payment status are intentionally separate. A Payment Attempt can be UNKNOWN while its HTTP idempotency operation is COMPLETED with a stored 202 response.

6. Webhook Design

Businesses can register webhook endpoints. Required events are:

INVOICE.CREATED
INVOICE.PAID
INVOICE.PAYMENT_FAILED

Webhook delivery does not block API responses. Delivery records are persisted in PostgreSQL and processed asynchronously by a Go worker.

When a domain transaction produces an event, its webhook delivery rows are inserted in the same transaction as the domain change. This provides a transactional outbox without introducing a message broker.

Workers claim due rows in short transactions using FOR UPDATE SKIP LOCKED. Claimed rows become PROCESSING; next_attempt_at also acts as a claim-expiry lease so a crashed worker does not permanently strand work. HTTP delivery occurs outside the transaction.

Retry schedule:

immediate
+1 minute
+5 minutes
+30 minutes
+2 hours
+12 hours

After six unsuccessful attempts the delivery becomes EXHAUSTED.

Webhook payloads are signed using HMAC-SHA256 over timestamp + "." + raw_payload, sent through X-Webhook-Timestamp and X-Webhook-Signature.

7. API Key Model

Businesses authenticate using API keys. Full API-key CRUD is intentionally omitted because the assignment requires authentication, not credential-management APIs.

Local bootstrap creates one demo Business and associated API key as defined in BOOTSTRAP.md.

For development, the high-entropy API secret can be generated with:

openssl rand -hex 32

This creates 32 cryptographically random bytes represented as hexadecimal. The plaintext secret is never persisted. PostgreSQL stores a globally unique non-secret lookup prefix and SHA-256(secret).

Authentication parses the presented key, looks up the prefix, verifies it is not revoked, hashes the supplied secret, and compares the hash in constant time. A successful authentication establishes business_id.

SHA-256 is suitable here because the credential is a machine-generated high-entropy secret, not a human password.

The API-key secret and ID_CODEC_SECRET are separate and must not be reused.

8. What You Cut and Why

Redis, RabbitMQ, Kafka, Kubernetes, a second database, and a microservice split were intentionally excluded. PostgreSQL is sufficient for durable idempotency, payment coordination, and the webhook outbox at this scale.

API-key CRUD, card-token storage, unsupported PSP reconciliation APIs, and unnecessary infrastructure were also excluded.

Subscriptions, recurring billing, refunds, partial payments, multi-currency/FX, tax, frontend work, email, OAuth, and production-grade rate limiting remain outside scope.

These cuts keep the implementation focused on the correctness properties being evaluated.

9. Production Readiness Gap

The largest production gap is reconciliation of uncertain external payments. A real PSP integration should provide idempotent charge creation and a way to retrieve transaction status and/or receive PSP webhooks. Reconciliation workers could then safely resolve UNKNOWN attempts.

Snowflake generation requires reliable worker/node assignment and explicit clock-rollback handling across multiple production instances. The public-ID codec requires secure secret storage and a versioned rotation strategy.

Webhook signing secrets and other reversible secrets should use production secret-management/KMS controls. Webhook operations would also need metrics, alerting, replay tooling, retention policies, and operational dashboards.

Other production improvements include API-key provisioning and rotation, stronger auditing, rate limiting, tracing, metrics, database backup/recovery procedures, deployment health checks, and load testing.

These gaps are documented rather than implemented because the assignment favors a small, correct, explainable solution over production-scale infrastructure.