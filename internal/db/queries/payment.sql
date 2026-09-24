-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys
WHERE business_id = $1 AND idempotency_key = $2;

-- name: CreateIdempotencyKey :one
INSERT INTO idempotency_keys (
    id, business_id, idempotency_key, request_hash, status,
    response_status, response_body, created_at, updated_at
) VALUES ($1, $2, $3, $4, 'PROCESSING', NULL, NULL, NOW(), NOW())
RETURNING *;

-- name: CompleteIdempotencyKey :one
UPDATE idempotency_keys
SET status = 'COMPLETED', response_status = $2, response_body = $3, updated_at = NOW()
WHERE id = $1 AND status = 'PROCESSING'
RETURNING *;

-- name: GetInvoiceForPayment :one
SELECT * FROM invoices
WHERE id = $1 AND business_id = $2
FOR UPDATE;

-- name: CreatePaymentAttempt :one
INSERT INTO payment_attempts (
    id, invoice_id, idempotency_key_id, status, psp_request_id,
    psp_requested_at, created_at, updated_at
) VALUES ($1, $2, $3, 'PROCESSING', $4, NOW(), NOW(), NOW())
RETURNING *;

-- name: GetPaymentAttemptByIdempotencyKeyID :one
SELECT * FROM payment_attempts WHERE idempotency_key_id = $1;

-- name: MarkPaymentAttemptSucceeded :one
UPDATE payment_attempts
SET status = 'SUCCEEDED', psp_ref = $2, psp_responded_at = NOW(), updated_at = NOW()
WHERE id = $1 AND status = 'PROCESSING'
RETURNING *;

-- name: MarkPaymentAttemptFailed :one
UPDATE payment_attempts
SET status = 'FAILED', failure_code = $2, failure_reason = $3,
    psp_responded_at = NOW(), updated_at = NOW()
WHERE id = $1 AND status = 'PROCESSING'
RETURNING *;

-- name: MarkPaymentAttemptUnknown :one
UPDATE payment_attempts
SET status = 'UNKNOWN', failure_reason = $2, updated_at = NOW()
WHERE id = $1 AND status = 'PROCESSING'
RETURNING *;

-- name: MarkInvoicePaid :one
UPDATE invoices SET state = 'PAID', updated_at = NOW()
WHERE id = $1 AND business_id = $2 AND state = 'OPEN'
RETURNING *;
