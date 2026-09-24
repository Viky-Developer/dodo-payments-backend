# AGENTS.md

# Dodo Payments Backend Take-Home — Agent Instructions

## 1. Purpose

This repository implements the Dodo Payments Backend Engineering Take-Home:

Invoice & Payment Service.

The goal is NOT to build a large production platform.

The goal is to build a small, correct, explainable backend that demonstrates:

- payment correctness
- concurrency handling
- idempotency
- invoice state management
- external PSP failure handling
- asynchronous webhook delivery
- PostgreSQL data modeling
- clear engineering judgment

Prefer correctness and simplicity over additional infrastructure or abstractions.

---

# 2. Required Reading

Before modifying the repository, read:

1. `AGENTS.md` — engineering rules and frozen decisions

2. `DATABASE.md` — authoritative database schema

3. `BOOTSTRAP.md` — development/demo bootstrap behavior

4. `DESIGN.md` — architecture rationale and failure-mode decisions

5. `openapi.yaml` — public HTTP contract

6. the original assignment specification

7. existing migrations

8. existing SQL queries

9. relevant tests

Do not implement based only on assumptions.

`DATABASE.md` is the source of truth for the approved physical

database schema.

`DESIGN.md` is the source of truth for architectural reasoning,

state transitions, failure-mode decisions, and trade-offs.

If code, migrations, DATABASE.md, and DESIGN.md disagree:

STOP.

Explain:

1. what currently exists

2. what conflicts

3. the proposed change

4. why it is needed

5. its trade-offs

Do not silently change the design.

---

# 3. Technology Stack

Use:

- Go
- Gin
- PostgreSQL
- pgx/v5
- sqlc
- Goose
- Docker
- Docker Compose

Do not introduce an ORM.

Do not replace sqlc with GORM or another ORM.

Do not replace Goose with another migration framework.

Do not add infrastructure without an assignment requirement or an
explicitly approved design change.

In particular, do not add:

- Redis
- RabbitMQ
- Kafka
- Kubernetes
- Elasticsearch
- additional databases
- unnecessary microservices

---

# 4. Repository Structure

Keep the repository small and understandable.

Preferred structure:

.
├── cmd/
│   ├── api/
│   │   └── main.go
│   └── mockpsp/
│       └── main.go
│
├── internal/
│   ├── auth/
│   ├── customer/
│   ├── invoice/
│   ├── payment/
│   ├── webhook/
│   ├── psp/
│   └── db/
│
├── db/
│   ├── migrations/
│   └── queries/
│
├── tests/
│
├── docker-compose.yml
├── Dockerfile
├── sqlc.yaml
├── openapi.yaml
├── README.md
├── DESIGN.md
├── DATABASE.md
├── AI_USAGE.md
├── AGENTS.md
├── go.mod
└── go.sum

Do not create additional architectural layers merely for abstraction.

---

# 5. Database Rules

The approved physical schema is defined in:

`DATABASE.md`

Do not invent:

- new tables
- new columns
- new enum/state values
- new foreign keys
- new indexes
- new unique constraints
- new CHECK constraints

without first identifying why the approved schema is insufficient.

If implementation requires a schema change:

STOP and surface the requirement before changing DATABASE.md or
migrations.

Use Goose for all schema changes.

Use sqlc for application SQL access.

Use pgx/v5 as the PostgreSQL driver.

Generated sqlc code may be committed.

Do not manually modify generated sqlc files.

---

# 6. Domain Isolation

Every business-owned resource must be scoped to the authenticated
business.

Never fetch a resource only by its public ID when business ownership
must also be checked.

For example, customer and invoice operations must prevent one
business from reading or modifying another business's resources.

Business isolation is a correctness requirement.

---

# 7. Money Rules

USD only.

Represent all monetary values as integer cents.

Never use:

- float32
- float64
- decimal approximations based on floating point

for the money path.

Example:

$10.50 = 1050 cents

Invoice totals MUST be calculated by the server.

For every invoice item:

line_total = quantity * unit_amount_cents

Invoice total:

total_amount_cents = sum(line_total)

Never trust a total supplied by the client.

Validate arithmetic for overflow where appropriate.

---

# 8. Invoice State Machine

Approved invoice states:

- DRAFT
- OPEN
- PAID
- VOID
- UNCOLLECTIBLE

Do not introduce additional invoice states without approval.

In particular, do NOT add an invoice `processing` state simply because
a PSP request is in progress.

Payment processing state belongs to `payment_attempts`.

Valid transitions must match DESIGN.md.

Invalid transitions must be rejected clearly at the API layer.

Database CHECK constraints guarantee valid state values.

They do NOT by themselves guarantee valid state transitions.

Transition correctness must be enforced by application logic and/or
conditional SQL operations as defined in DESIGN.md.

---

# 9. Payment Attempt States

Approved payment attempt states:

- PROCESSING
- SUCCEEDED
- FAILED
- UNKNOWN

Meaning:

`PROCESSING`
- the payment operation has been claimed and PSP processing has started
  or is about to start

`SUCCEEDED`
- a definitive successful PSP result was received and persisted

`FAILED`
- a definitive payment failure was received and persisted

`UNKNOWN`
- the service cannot safely determine whether the PSP processed the
  payment

Do not convert an uncertain PSP outcome into a definitive failure.

---

# 10. Payment Endpoint

Payment endpoint:

POST /invoices/{id}/pay

Requirements:

- requires an `Idempotency-Key` header
- receives a mock card token
- records a payment attempt
- calls the mock PSP
- updates state according to the result
- prevents concurrent double payment
- returns deterministic responses for completed idempotent requests

The card token is request input.

Do NOT create a card-token vault or card-token table.

Do NOT persist:

- raw card numbers
- CVV
- expiry data
- unnecessary card credentials

The predefined mock tokens are supplied by the caller and interpreted
by the mock PSP.

---

# 11. Mock PSP

Implement the assignment-defined PSP behavior.

Supported tokens:

`tok_success`
- wait approximately 100 ms
- return succeeded
- return a generated `psp_ref`

`tok_insufficient_funds`
- wait approximately 100 ms
- return failed
- code: `insufficient_funds`

`tok_card_declined`
- wait approximately 100 ms
- return failed
- code: `card_declined`

`tok_timeout`
- wait 30 seconds
- then return success

`tok_network_error`
- return HTTP 500 or simulate a connection failure

Do not add undocumented PSP capabilities merely to solve a difficult
failure mode.

In particular, do not invent:

- PSP payment lookup
- PSP reconciliation API
- PSP webhook callbacks
- PSP-side idempotency guarantees

unless clearly described only as a production improvement.

---

# 12. PSP Timeout

The invoice service must not wait 30 seconds for `tok_timeout`.

Use a bounded HTTP client timeout.

Current design:

PSP_TIMEOUT=3s

The value should be configurable.

On timeout:

- do not mark the invoice paid
- do not mark the payment definitively failed
- mark the payment attempt `UNKNOWN`
- persist the uncertainty reason
- preserve enough correlation information for investigation/reconciliation
- return the API response defined in DESIGN.md

Current design uses:

HTTP 202 Accepted

for an uncertain PSP outcome.

Do not automatically call the PSP again for an UNKNOWN attempt.

---

# 13. PSP Network Errors

A network failure does not necessarily prove that the PSP did not
process the request.

Handle ambiguous transport failures conservatively.

Do not blindly retry a charge when the outcome could be unknown.

Persist the appropriate payment attempt state and failure/uncertainty
information according to DESIGN.md.

---

# 14. PSP Request Traceability

Each payment attempt must preserve the approved PSP traceability data
defined in DATABASE.md.

This includes a locally generated stable PSP request/correlation
identifier.

The correlation identifier does NOT mean that the supplied mock PSP
supports reconciliation or idempotency.

It exists so the application can identify the external request that
was attempted and so the production design can explain how such an
identifier would be used with a real PSP.

Never claim that PostgreSQL can determine an external PSP result that
was never persisted.

---

# 15. Critical PSP Crash Window

Explicitly preserve this failure model:

1. payment attempt is persisted
2. service calls PSP
3. PSP charges successfully
4. service crashes before success is persisted

This is a distributed-systems failure window.

A PostgreSQL transaction cannot atomically include the external HTTP
side effect.

Do NOT claim exactly-once charging based solely on a local DB
transaction.

The implementation and DESIGN.md must distinguish:

- what this take-home implementation guarantees
- what remains uncertain
- what a production PSP integration would require

Possible production mitigations may be discussed in DESIGN.md:

- PSP-side idempotency keys
- PSP status lookup
- PSP webhooks
- reconciliation workers
- settlement reconciliation

Do not fabricate these capabilities in the mock PSP.

---

# 16. Database Transaction Boundaries

Never keep a PostgreSQL transaction or row lock open while waiting for
the PSP HTTP request.

Payment processing should use short database transactions.

Conceptually:

Transaction A:
- authenticate/scope request
- validate invoice
- handle idempotency
- claim payment operation
- create payment attempt
- commit

External operation:
- call PSP outside DB transaction

Transaction B:
- persist PSP outcome
- transition invoice if appropriate
- create webhook delivery/outbox records
- complete idempotency response
- commit

Exact SQL and constraints must follow DATABASE.md and DESIGN.md.

---

# 17. Concurrent Payments

Two clients may call:

POST /invoices/{id}/pay

for the same invoice at the same time.

The implementation must guarantee:

- at most one payment operation is allowed to charge the invoice at a
  time
- no double charge is caused by concurrent API requests
- final invoice state remains consistent

Use the concurrency mechanism documented in DESIGN.md and implemented
through the approved database constraints/queries.

Do not rely on an in-memory Go mutex as the correctness mechanism.

The database must remain the authoritative coordination boundary.

Do not hold a database lock during PSP network I/O.

---

# 18. Idempotency

Idempotency is durable PostgreSQL state.

Do not implement payment idempotency using:

- in-memory maps
- Redis
- process-local caches

The physical representation is defined in DATABASE.md.

Semantics:

## First request

For:

Idempotency-Key: K
Request body: B

derive a deterministic request fingerprint/hash.

Persist the idempotency operation before making the external payment
call.

## Same key + same request

If K is reused with the same request:

- do not create another payment operation
- do not call PSP again if a completed result already exists
- return the previously stored HTTP status
- return the previously stored response payload

## Same key + different request

If K exists but the request fingerprint differs:

reject the request.

Use a clear conflict response.

Current design:

HTTP 409 Conflict

## Request already processing

If the same operation is already being processed concurrently:

do not call PSP again.

Return the conflict/in-progress behavior documented in DESIGN.md.

## UNKNOWN result

If an idempotent payment resulted in UNKNOWN:

do not automatically call PSP again when the same key is retried.

Return the persisted representation of that operation.

A new idempotency key must not be allowed to bypass protection for an
invoice with an unresolved payment attempt.

---

# 19. Idempotency Request Hash

The request hash represents the payment request identity.

The mock card token participates in the request fingerprint so that:

same idempotency key + different token

is detected as a different request.

Persist the hash, not an unnecessary durable copy of the card token.

Use a deterministic canonical representation before hashing.

---

# 20. Payment Attempt ↔ Idempotency Relationship

Every payment attempt created by the payment API must be associated
with the idempotency operation that created it.

The exact foreign-key column and constraints are defined in
DATABASE.md.

This relationship must remain queryable for:

- duplicate requests
- concurrent requests
- failure investigation
- PSP timeout handling
- crash-window reasoning

Do not remove this relationship without an explicit design change.

---

# 21. API Key Authentication

API keys are scoped to businesses.

Never store the full plaintext API key secret.

Generate secrets using a cryptographically secure random generator.

Persist only the approved identifier/prefix and one-way secret hash
defined in DATABASE.md.

Current design uses SHA-256 for the random high-entropy API-key
secret.

Reason:

API keys are machine-generated high-entropy secrets rather than
human-generated passwords. A deliberately slow password KDF is
therefore not required for the current design.

Document this reasoning in DESIGN.md.

Support revocation.

Never log the plaintext API key.

---

# 22. Webhooks

Businesses can register webhook endpoint URLs.

Required events:

- invoice.created
- invoice.paid
- invoice.payment_failed

Webhook delivery must NOT block the API response.

Do not synchronously deliver webhooks from the request path.

Do not add RabbitMQ/Kafka solely for webhook delivery.

Use the PostgreSQL-backed durable delivery/outbox design documented in
DATABASE.md and DESIGN.md.

Domain state changes and corresponding webhook delivery records should
be persisted atomically where required.

A background Go worker processes pending deliveries.

Use safe database claiming so multiple workers cannot process the same
delivery concurrently.

`FOR UPDATE SKIP LOCKED` may be used according to the approved query
design.

---

# 23. Webhook Signing

Use:

HMAC-SHA256

Sign:

timestamp + "." + raw_payload

Headers:

X-Webhook-Timestamp
X-Webhook-Signature

The receiver can:

1. verify the HMAC
2. verify the timestamp is within an acceptable window
3. reject stale/replayed requests

Never log webhook signing secrets.

---

# 24. Webhook Retry Policy

Current retry policy:

1. immediate
2. +1 minute
3. +5 minutes
4. +30 minutes
5. +2 hours
6. +12 hours

After the final unsuccessful attempt:

status = exhausted

Persist:

- delivery state
- attempt count
- next attempt time
- last error
- successful delivery time where applicable

Do not describe this retry schedule as a 24-hour schedule.

The final attempt occurs approximately 14 hours and 36 minutes after
the initial attempt.

---

# 25. Background Worker Safety

Webhook workers must use durable database state.

Do not use fire-and-forget goroutines as the only delivery guarantee.

A goroutine may execute work, but the work itself must already exist
durably in PostgreSQL.

Worker crashes must not permanently lose webhook events.

---

# 26. HTTP Error Format

Use a consistent API error structure.

Example shape:

{
  "error": {
    "code": "idempotency_conflict",
    "message": "Idempotency key was already used with a different request"
  }
}

Do not return unrelated error shapes from different handlers.

Avoid leaking:

- SQL errors
- stack traces
- API keys
- webhook secrets
- internal credentials

---

# 27. Logging

Use structured, useful logging.

Useful identifiers include:

- request ID
- business ID
- invoice ID
- payment attempt ID
- PSP request/correlation ID
- webhook delivery ID

Do not log sensitive secrets.

Do not add a large observability stack for this take-home.

Production observability belongs in DESIGN.md's Production Readiness
Gap unless explicitly required.

---

# 28. Required Tests

Prioritize the assignment-required correctness tests.

## Concurrent payment test

Fire N concurrent:

POST /invoices/{id}/pay

requests against the same invoice.

Assert:

- at most one payment operation charges successfully
- no double charge
- final invoice state is consistent

## Idempotency test

Send the same payment request twice with the same idempotency key.

Assert:

- same stored result/response
- no second PSP call
- no duplicate payment operation

Also test:

same key + different body

is rejected.

## PSP failure test

Use:

- tok_timeout

or:

- tok_network_error

Assert:

- endpoint does not hang
- invoice is not corrupted
- payment attempt records the correct uncertainty/failure state

Prefer these high-value tests over exhaustive handler coverage.

---

# 29. Mock PSP Testing

Tests must be able to determine how many PSP calls occurred.

Keep the mechanism simple.

Do not add external infrastructure just to count mock PSP calls.

---

# 30. SQL Rules

Prefer explicit SQL.

Use sqlc-generated typed queries.

Queries should clearly show:

- business scoping
- ownership checks
- payment claiming
- state transitions
- idempotency handling
- webhook job claiming

Avoid hidden database behavior that makes correctness difficult to
explain.

Use transactions only when atomicity is required.

Keep transactions short.

---

# 31. Schema Constraints

DATABASE.md defines the exact constraints.

Important classes of constraints include:

- primary keys
- foreign keys
- uniqueness
- valid state/status values
- positive quantities
- non-negative monetary values
- USD-only currency
- concurrency-related uniqueness/conditions

Do not weaken database invariants merely to make application code
easier.

Remember:

CHECK constraints validate allowed values.

They do not automatically implement the invoice state machine.

---

# 32. Migrations

Use Goose.

Migrations must be:

- deterministic
- version controlled
- executable on a clean PostgreSQL instance

`docker compose up` must arrange for migrations to run automatically.

A reviewer must not need to manually execute migrations after starting
the repository.

Do not modify an already-applied migration during normal development
when a new migration is appropriate.

---

# 33. Docker Compose

`docker compose up` must bring up:

- PostgreSQL
- migrations/setup as required
- invoice API
- mock PSP

with no manual setup steps.

Use health checks/dependencies where necessary so migrations do not run
before PostgreSQL is ready.

Keep Docker configuration simple.

---

# 34. API Documentation

Maintain:

`openapi.yaml`

Document:

- authentication
- customers
- invoices
- payment endpoint
- webhook registration
- request shapes
- response shapes
- Idempotency-Key requirement
- consistent error format

When handler behavior changes, update OpenAPI.

---

# 35. README

README.md must contain at least:

- project overview
- chosen technology stack
- brief justification for using Go
- architecture summary
- prerequisites
- `docker compose up` instructions
- useful curl examples
- create customer example
- create invoice example
- successful payment example
- failed payment example
- test instructions
- Demo Video section and link

Keep README practical.

Detailed architectural reasoning belongs in DESIGN.md.

Detailed physical schema belongs in DATABASE.md.

---

# 36. DESIGN.md

DESIGN.md is the primary design deliverable.

Aim for approximately 800–1500 words as requested by the assignment.

It must explicitly cover:

1. Data Model
2. Invoice State Machine
3. Payment Correctness & Failure Modes
4. Webhook Design
5. API Key Model
6. What You Cut and Why
7. Production Readiness Gap

For the data model, explain:

- table shape
- indexes
- PK strategy
- why the chosen shape
- alternatives considered
- what changes at 100x scale

DATABASE.md may contain the detailed schema, but DESIGN.md must still
contain the required reasoning.

---

# 37. Required Failure Modes in DESIGN.md

DESIGN.md must explicitly explain:

A. Two simultaneous POST /pay requests.

B. PSP `tok_timeout` taking 30 seconds.

C. PSP succeeds and the application crashes before success is
persisted.

D. Same idempotency key reused with a different request body.

E. POST /pay against an already-paid invoice.

For each case, explain the actual implementation behavior.

Do not write generic distributed-systems theory without connecting it
to the code.

Name the chosen concurrency mechanism and explain why it was selected
over alternatives.

---

# 38. DATABASE.md

DATABASE.md contains the approved physical PostgreSQL design.

It should contain:

- ER/relationship diagram
- every table
- every column
- PostgreSQL data types
- nullability
- primary keys
- foreign keys
- UNIQUE constraints
- CHECK constraints
- indexes
- concurrency-related constraints
- short notes for non-obvious columns

Do not use DATABASE.md as a second DESIGN.md.

DATABASE.md answers:

"What exactly exists in PostgreSQL?"

DESIGN.md answers:

"Why was it designed this way?"

---

# 39. AI_USAGE.md

AI use must be disclosed honestly.

Do not fabricate human decisions.

AI_USAGE.md must identify:

- which AI tools were used
- what each tool was used for
- three decisions made independently or against AI suggestions
- at least one thing AI got wrong/corrected, or how correctness was
  independently verified

The human author must own the engineering decisions.

AI may assist with:

- boilerplate
- grammar
- formatting
- identifying edge cases
- discussing alternatives
- test scaffolding
- SQL/code review

AI must not silently rewrite approved design decisions.

---

# 40. Human Decision Boundary

The following are deliberate design decisions and must not be silently
changed:

- Go + Gin
- PostgreSQL
- pgx/v5
- sqlc
- Goose
- integer cents
- USD only
- separate durable idempotency records
- payment-attempt/idempotency relationship
- no card-token table
- payment attempt UNKNOWN state
- bounded PSP timeout
- no DB transaction across PSP HTTP call
- PostgreSQL-backed webhook delivery/outbox
- HMAC-SHA256 webhook signatures
- explicit webhook retry schedule
- hashed API-key secrets
- no Redis/RabbitMQ/Kafka/Kubernetes
- no claim of exactly-once external PSP execution

If a better alternative is discovered:

do not silently implement it.

Present the conflict and trade-off first.

---

# 41. Scope — Do Not Build

Do not implement:

- subscriptions
- recurring billing
- plans
- proration
- refunds
- partial payments
- multi-currency
- FX
- tax calculation
- frontend/UI
- email sending
- production-grade rate limiting
- OAuth

These can be discussed as future work where appropriate.

The assignment explicitly values restraint.

---

# 42. Do Not Overengineer

Do not introduce:

- generic repository frameworks
- generic event buses
- CQRS
- event sourcing
- distributed locks
- unnecessary interfaces for every struct
- dependency-injection frameworks
- complex domain frameworks
- unnecessary services

A small implementation that clearly demonstrates correctness is
preferred.

Every dependency should be explainable.

---

# 43. Code Style

Keep Go code idiomatic and easy to walk through during the demo.

Prefer:

- small functions
- explicit error handling
- clear transaction boundaries
- clear names
- dependency injection through constructors where useful
- context propagation
- HTTP client timeouts

Avoid clever abstractions.

Comments should explain non-obvious correctness decisions rather than
repeat the code.

---

# 44. Before Implementing a Change

For any meaningful change:

1. read the relevant design/documentation
2. inspect the current implementation
3. inspect relevant DB queries/migrations
4. identify affected invariants
5. make the smallest correct change
6. update tests
7. update OpenAPI if API behavior changed
8. update DATABASE.md if approved DB schema changed
9. update DESIGN.md if an architectural decision changed
10. update AI_USAGE.md when AI materially influenced a decision

---

# 45. Before Finishing

Verify:

- `docker compose up` works from a clean environment
- migrations run automatically
- API starts successfully
- mock PSP starts successfully
- no floating-point money
- customer data is business scoped
- invoice data is business scoped
- server calculates invoice totals
- payment concurrency test passes
- idempotency test passes
- PSP failure/timeout test passes
- `tok_timeout` does not hang the endpoint
- completed idempotent retry does not call PSP again
- different request with same key is rejected
- UNKNOWN payment is not automatically charged again
- webhook delivery does not block API response
- webhook retry state survives process restart
- secrets are not logged
- OpenAPI matches implementation
- DATABASE.md matches migrations
- DESIGN.md matches actual behavior
- README contains required curl examples
- README contains Demo Video section
- AI_USAGE.md is specific and truthful

---

# 46. Final Principle

When choosing between:

more infrastructure

and

a smaller implementation with stronger correctness and clearer
reasoning,

choose the smaller implementation.

This assignment evaluates engineering judgment, not feature count.