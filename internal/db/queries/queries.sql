-- name: CreateApiKey :one
INSERT INTO api_keys (id, business_id, key_prefix, secret_hash, created_at, revoked_at)
VALUES ($1, $2, $3, $4, NOW(), NULL) RETURNING *;

-- name: CreateBusiness :one
INSERT INTO businesses (id, name, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW()) RETURNING *;

-- name: CreateCustomer :one
INSERT INTO customers (id, business_id, name, email, created_at, updated_at)
VALUES ($1, $2, $3, $4, NOW(), NOW()) RETURNING *;

-- name: CreateInvoice :one
INSERT INTO invoices (id, business_id, customer_id, total_amount_cents, currency, state, due_date, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW()) RETURNING *;

-- name: CreateInvoiceItem :one
INSERT INTO invoice_items (id, invoice_id, description, quantity, unit_amount_cents, created_at)
VALUES ($1, $2, $3, $4, $5, NOW()) RETURNING *;

-- name: GetApiKeyByPrefix :one
SELECT * FROM api_keys WHERE key_prefix = $1;

-- name: GetBusinessByID :one
SELECT * FROM businesses WHERE id = $1;

-- name: GetBusinessByName :one
SELECT * FROM businesses WHERE name = $1;

-- name: GetCustomerByID :one
SELECT * FROM customers WHERE id = $1 AND business_id = $2;

-- name: GetInvoiceByID :one
SELECT * FROM invoices WHERE id = $1 AND business_id = $2;

-- name: ListApiKeysByBusinessID :many
SELECT * FROM api_keys WHERE business_id = $1 ORDER BY created_at DESC;

-- name: ListCustomersByBusinessID :many
SELECT * FROM customers WHERE business_id = $1 ORDER BY created_at DESC;

-- name: ListInvoiceItemsByInvoiceID :many
SELECT * FROM invoice_items WHERE invoice_id = $1 ORDER BY id ASC;

-- name: ListInvoiceItemsByInvoiceIDs :many
SELECT * FROM invoice_items WHERE invoice_id = ANY($1::bigint[]) ORDER BY id ASC;

-- name: ListInvoicesByBusinessID :many
SELECT * FROM invoices WHERE business_id = $1 ORDER BY created_at DESC;

-- name: UpdateInvoiceState :one
UPDATE invoices SET state = $3, updated_at = NOW()
WHERE id = $1 AND business_id = $2 RETURNING *;
