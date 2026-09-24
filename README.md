# Dodo Payments — Invoice & Payment Service

A production-grade, concurrency-safe invoice and payment backend built with **Go** and **PostgreSQL**.

This repository implements the Dodo Payments Backend Engineering Take-Home: an invoice lifecycle and payment processing service featuring durable PostgreSQL-backed idempotency, bounded external PSP failure handling, two-phase payment transactions, and reliable asynchronous webhook delivery via a transactional outbox.

---

## 1. Technology Stack & Rationale

- **Language**: Go (1.26)
- **HTTP Framework**: Gin
- **Database**: PostgreSQL 17
- **Database Driver**: `pgx/v5`
- **Query Compiler**: `sqlc` (type-safe SQL without an ORM)
- **Migrations**: `goose`
- **Containerization**: Docker & Docker Compose

## Why Go?

Rust is the preferred language for this assignment. I am not yet familiar
enough with Rust to confidently build and explain a payment service within
the given time constraint.

I chose Go because it is the language I currently have stronger hands-on
experience with for backend development. This allowed me to spend the
assignment time on the problems I believe matter most here: payment
correctness, concurrency, idempotency, database transactions, PSP failure
handling, and reliable webhook delivery, rather than spending most of the
time learning language syntax and frameworks.

Go is also a good fit for this service because of its simple concurrency
model, strong standard library for HTTP services, explicit error handling,
and lightweight deployment model.

I see Rust as a valuable language for building reliable and
performance-sensitive systems, and I am interested in learning it. For this
take-home, however, I preferred to use a language in which I could clearly
reason about, implement, test, and explain the system's correctness.

Choosing Go here is therefore a scope and engineering-quality decision, not
a limitation on my willingness to learn Rust.

---

## 2. Architecture & Design Principles

The service is built as a single, highly cohesive Go backend without unnecessary microservices, external gateways, or caching infrastructure.

```
                              HTTP Requests (Bearer API Key, Idempotency-Key)
                                                    │
                                                    ▼
  ┌─────────────────────────────────────────────────────────────────────────────────┐
  │                        Go Backend Service (cmd/api)                             │
  │                                                                                 │
  │  ┌───────────────────────────────────────────────────────────────────────────┐  │
  │  │  HTTP Middleware: X-Request-ID ── Structured Logger ── Panic Recovery     │  │
  │  │  Auth Middleware: SHA-256 API Key Validation & Business Tenant Scoping    │  │
  │  └───────────────────────────────────────────────────────────────────────────┘  │
  │                                                                                 │
  │  ┌───────────────────────┐  ┌───────────────────────┐  ┌─────────────────────┐  │
  │  │   Customer Handler    │  │    Invoice Handler    │  │   Payment Handler   │  │
  │  │   - Multi-tenant CRUD │  │   - Server-Calculated │  │   - 2-Phase Tx      │  │
  │  │   - Feistel Codec     │  │     Item Totals       │  │   - Idempotency     │  │
  │  └──────────┬────────────┘  └───────────┬───────────┘  └──────────┬──────────┘  │
  │             │                           │                         │             │
  │             │   ┌───────────────────────┴──────────────────────┐  │             │
  │             │   │       Webhook Endpoint Management            │  │             │
  │             │   │       - Endpoint CRUD & HMAC Secrets         │  │             │
  │             │   └───────────────────────┬──────────────────────┘  │             │
  │             │                           │                         │             │
  │             ▼                           ▼                         │             │
  │     ┌────────────────────────────────────────────────────────┐    │             │
  │     │                 PostgreSQL 17 Database                 │    │             │
  │     │  - Businesses, Customers, Invoices, Invoice Items      │    │             │
  │     │  - Row Locks (FOR UPDATE) & Partial Unique Index       │    │             │
  │     │  - Idempotency Keys (SHA-256 Request Fingerprint)      │    │             │
  │     │  - Transactional Outbox (webhook_deliveries)           │    │             │
  │     └───────────────────────────┬────────────────────────────┘    │             │
  │                                 │                                 │             │
  │                                 │ FOR UPDATE                      │ (External   │
  │                                 │ SKIP LOCKED                     │  HTTP Call) │
  │                                 ▼                                 ▼             │
  │  ┌──────────────────────────────────────────────┐    ┌──────────────────────┐  │
  │  │   Background Webhook Worker (Goroutine)      │    │ Bounded PSP Client   │  │
  │  │   - 6-stage exponential retry schedule       │    │ (3s HTTP timeout)    │  │
  │  │   - HMAC-SHA256 signature generator         │    └──────────┬───────────┘  │
  │  └──────────────────────┬───────────────────────┘               │              │
  └─────────────────────────┼───────────────────────────────────────┼──────────────┘
                            │                                       │
                            ▼ HTTP POST (HMAC Signed)               ▼ HTTP POST
                ┌───────────────────────┐               ┌───────────────────────┐
                │ Destination Webhooks  │               │   Mock PSP Service    │
                │ (External Endpoints)  │               │    (cmd/mockpsp)      │
                └───────────────────────┘               └───────────────────────┘
```

### Core Correctness Guarantees
- **Integer Cents Only**: USD currency only. Floating-point types are strictly forbidden on financial paths (`unit_amount_cents`, `total_amount_cents`).
- **Server-Calculated Totals**: Invoice amounts are calculated on the server as $\sum(\text{quantity} \times \text{unit\_amount\_cents})$. Client-supplied totals are never trusted.
- **2-Phase Payment Transactions**:
  - **Transaction A (Claim)**: Locks invoice (`FOR UPDATE`), checks/inserts idempotency key, records payment attempt as `PROCESSING`. Commits immediately.
  - **PSP Call**: External HTTP call to mock PSP occurs completely **outside** database transactions.
  - **Transaction B (Finalize)**: Records definitive result (`SUCCEEDED`/`FAILED`) or ambiguous outcome (`UNKNOWN`), transitions invoice state (`PAID` on success), stores idempotency response, and atomically enqueues outbox webhooks.
- **Concurrency & Double-Charge Protection**:
  - PostgreSQL row-level locks prevent concurrent claims on the same invoice.
  - A partial unique index (`uq_payment_attempts_unresolved_invoice`) guarantees at most one unresolved (`PROCESSING` or `UNKNOWN`) payment attempt can exist per invoice across distributed application instances.
- **Bounded PSP Timeout**:
  - Calls to the PSP enforce a strict 3-second bounded client timeout (`PSP_TIMEOUT=3s`).
  - When the PSP times out (`tok_timeout`), the service transitions the attempt to `UNKNOWN` and returns `HTTP 202 Accepted` rather than hanging for 30 seconds or converting the state into a false failure.
- **Transactional Outbox & Webhooks**:
  - Webhook delivery jobs are inserted inside the same database transaction that transitions domain state (`invoice.created`, `invoice.paid`, `invoice.payment_failed`).
  - A background Go worker claims jobs using `SELECT FOR UPDATE SKIP LOCKED` and delivers them with HMAC-SHA256 signatures (`X-Webhook-Signature`, `X-Webhook-Timestamp`).
  - Retry policy follows an exact 6-attempt schedule: `immediate`, `+1m`, `+5m`, `+30m`, `+2h`, `+12h`, terminating in `EXHAUSTED`.

---

## 3. Prerequisites

- [Docker](https://docs.docker.com/get-docker/) & [Docker Compose](https://docs.docker.com/compose/)
- *(Optional for local dev without Docker)*:
  - Go 1.26+
  - PostgreSQL 17+
  - `goose` migration tool (`go install github.com/pressly/goose/v3/cmd/goose@latest`)
  - `curl` and `jq` for CLI testing

---

## 4. Quickstart (`docker compose up`)

Start the entire environment with a single command:

```bash
docker compose up --build
```

### Services Started:
1. **`postgres`**: PostgreSQL 17 on port `5432`.
2. **`migrate`**: Runs Goose database migrations automatically on startup (`internal/db/migrations`).
3. **`mockpsp`**: Standalone mock payment gateway service on port `8081`.
4. **`api`**: Main invoice and payment backend on port `8080`.

### Bootstrapped Demo Credentials
Application startup automatically bootstraps a demo business and API key if they do not exist:
- **Business Name**: `Dodo Demo Business`
- **Bearer API Key**: `dp_test_demo_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`

---

## 5. API Walkthrough (`curl` Examples)

Export environment variables for convenience:
```bash
export API_URL="http://localhost:8080"
export API_KEY="dp_test_demo_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
```

### 1. Health Check
```bash
curl -s "$API_URL/health"
```
**Response (200 OK):**
```json
{"status":"ok"}
```

---

### 2. Create a Customer
```bash
curl -s -X POST "$API_URL/customers" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Alice Wonderland",
    "email": "alice@example.com"
  }'
```
**Response (201 Created):**
```json
{
  "id": "cus_01a2b3c4d5",
  "name": "Alice Wonderland",
  "email": "alice@example.com",
  "created_at": "2026-09-24T12:00:00Z",
  "updated_at": "2026-09-24T12:00:00Z"
}
```

---

### 3. Create an Invoice with Line Items
```bash
curl -s -X POST "$API_URL/invoices" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "customer_id": "cus_01a2b3c4d5",
    "currency": "USD",
    "due_date": "2026-12-31",
    "items": [
      {
        "description": "Software Consulting",
        "quantity": 2,
        "unit_amount_cents": 15000
      },
      {
        "description": "Setup Fee",
        "quantity": 1,
        "unit_amount_cents": 5000
      }
    ]
  }'
```
> **Note**: Total is automatically calculated as $(2 \times 15000) + (1 \times 5000) = 35000$ cents ($350.00 USD).

**Response (201 Created):**
```json
{
  "id": "inv_01e9f8d7c6",
  "customer_id": "cus_01a2b3c4d5",
  "total_amount_cents": 35000,
  "currency": "USD",
  "state": "OPEN",
  "due_date": "2026-12-31T00:00:00Z",
  "items": [
    {
      "id": "itm_0111223344",
      "description": "Software Consulting",
      "quantity": 2,
      "unit_amount_cents": 15000,
      "line_total_cents": 30000
    },
    {
      "id": "itm_0555667788",
      "description": "Setup Fee",
      "quantity": 1,
      "unit_amount_cents": 5000,
      "line_total_cents": 5000
    }
  ]
}
```

---

### 4. Register a Webhook Endpoint
```bash
curl -s -X POST "$API_URL/webhook-endpoints" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://webhook.site/test-endpoint"
  }'
```
**Response (201 Created):**
```json
{
  "id": "whe_019a8b7c6d",
  "url": "https://webhook.site/test-endpoint",
  "secret": "whsec_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "is_active": true,
  "created_at": "2026-09-24T12:00:00Z"
}
```

---

### 5. Pay Invoice Successfully (`tok_success`)
Requires the mandatory `Idempotency-Key` header:
```bash
curl -s -X POST "$API_URL/invoices/inv_01e9f8d7c6/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: my-payment-key-001" \
  -H "Content-Type: application/json" \
  -d '{
    "card_token": "tok_success"
  }'
```
**Response (200 OK):**
```json
{
  "attempt_id": "att_0144332211",
  "invoice_id": "inv_01e9f8d7c6",
  "status": "succeeded",
  "amount_cents": 35000,
  "currency": "USD",
  "psp_reference": "psp_ref_abcdef123456",
  "created_at": "2026-09-24T12:00:00Z"
}
```

---

### 6. Idempotent Payment Replay
Resending the **exact same request** with `my-payment-key-001`:
```bash
curl -s -X POST "$API_URL/invoices/inv_01e9f8d7c6/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: my-payment-key-001" \
  -H "Content-Type: application/json" \
  -d '{
    "card_token": "tok_success"
  }'
```
**Response (200 OK - Cached Stored Response):**
Returns the exact same HTTP 200 payload without charging the card or invoking the PSP a second time.

---

### 7. Idempotency Key Conflict (Payload Mismatch)
Reusing `my-payment-key-001` with a **different payload** (different card token or invoice):
```bash
curl -s -X POST "$API_URL/invoices/inv_01e9f8d7c6/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: my-payment-key-001" \
  -H "Content-Type: application/json" \
  -d '{
    "card_token": "tok_card_declined"
  }'
```
**Response (409 Conflict):**
```json
{
  "error": {
    "code": "idempotency_conflict",
    "message": "Idempotency key was already used with a different request"
  }
}
```

---

### 8. Failed Payment (`tok_card_declined`)
```bash
curl -s -X POST "$API_URL/invoices/inv_open_another/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: pay-fail-002" \
  -H "Content-Type: application/json" \
  -d '{
    "card_token": "tok_card_declined"
  }'
```
**Response (402 Payment Required):**
```json
{
  "error": {
    "code": "card_declined",
    "message": "Payment failed: card_declined"
  }
}
```
*The invoice state remains `OPEN` allowing subsequent attempts.*

---

### 9. Bounded Timeout Handling (`tok_timeout`)
The mock PSP simulates a 30-second delay for `tok_timeout`. The service applies a bounded 3-second HTTP timeout and marks the attempt as `UNKNOWN`:
```bash
curl -s -X POST "$API_URL/invoices/inv_open_third/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: pay-timeout-003" \
  -H "Content-Type: application/json" \
  -d '{
    "card_token": "tok_timeout"
  }'
```
**Response (202 Accepted):**
```json
{
  "error": {
    "code": "payment_uncertain",
    "message": "Payment outcome is uncertain due to PSP timeout; attempt recorded as UNKNOWN"
  }
}
```
*The invoice remains payable/open, and duplicate simultaneous payments remain blocked by the partial unique unresolved-attempt index.*

---

## 6. Testing

### Run All Unit Tests
```bash
go test -v ./...
```

### Run Concurrency & Race Detector Tests
```bash
go test -race ./...
```

### Run Automated End-to-End Smoke Test
When Docker Compose or the local server is running:
```bash
./tests/smoke_test.sh
```

### Run Database Concurrency & Idempotency Integration Tests
Requires an active PostgreSQL database:
```bash
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/dodo_payments?sslmode=disable" go test -v ./internal/payment/...
```

---

## 7. Demo Video

- **Video Link**: [Dodo Payments Backend Demo Video](https://www.loom.com/share/65bbb9dd9e4c4b95853d49d86a8302b5)
