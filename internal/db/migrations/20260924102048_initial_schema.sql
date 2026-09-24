-- +goose Up
SELECT 'up SQL query';
-- SQL in this section is executed when the migration is applied.

CREATE TYPE invoice_state_enum AS ENUM (
    'DRAFT',
    'OPEN',
    'PAID',
    'VOID',
    'UNCOLLECTIBLE'
);

CREATE TYPE payment_status_enum AS ENUM (
    'PROCESSING',
    'SUCCEEDED',
    'FAILED',
    'UNKNOWN'
);

CREATE TYPE idempotency_status_enum AS ENUM (
    'PROCESSING',
    'COMPLETED'
);

CREATE TYPE webhook_delivery_status_enum AS ENUM (
    'PENDING',
    'PROCESSING',
    'DELIVERED',
    'EXHAUSTED'
);

CREATE TYPE webhook_event_type_enum AS ENUM (
    'INVOICE.CREATED',
    'INVOICE.PAID',
    'INVOICE.PAYMENT_FAILED'
);

CREATE TABLE businesses (
    id BIGINT PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE api_keys (
    id BIGINT PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id),
    key_prefix VARCHAR(32) NOT NULL,
    secret_hash VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    CONSTRAINT uq_api_keys_key_prefix UNIQUE (key_prefix)
);

CREATE INDEX idx_api_keys_business_id ON api_keys (business_id);

CREATE TABLE customers (
    id BIGINT PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id),
    name VARCHAR(255) NOT NULL,
    email VARCHAR(320) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_customers_id_business_id UNIQUE (id, business_id)
);

CREATE INDEX idx_customers_business_id ON customers (business_id);

CREATE TABLE invoices (
    id BIGINT PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id),
    customer_id BIGINT NOT NULL,
    total_amount_cents BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL,
    state VARCHAR(32) NOT NULL,
    state invoice_state_enum NOT NULL,
    due_date DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_invoices_customer_business FOREIGN KEY (customer_id, business_id) REFERENCES customers(id, business_id),
    CONSTRAINT chk_invoices_total_amount_cents CHECK (total_amount_cents >= 0),
    CONSTRAINT chk_invoices_currency CHECK (currency = 'USD'),
    CONSTRAINT chk_invoices_state CHECK (state IN ('DRAFT', 'OPEN', 'PAID', 'VOID', 'UNCOLLECTIBLE'))
    CONSTRAINT chk_invoices_currency CHECK (currency = 'USD')
);

CREATE INDEX idx_invoices_business_id ON invoices (business_id);
CREATE INDEX idx_invoices_business_state ON invoices (business_id, state);

CREATE TABLE invoice_items (
    id BIGINT PRIMARY KEY,
    invoice_id BIGINT NOT NULL REFERENCES invoices(id),
    description TEXT NOT NULL,
    quantity INTEGER NOT NULL,
    unit_amount_cents BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_invoice_items_quantity CHECK (quantity > 0),
    CONSTRAINT chk_invoice_items_unit_amount_cents CHECK (unit_amount_cents >= 0)
);

CREATE INDEX idx_invoice_items_invoice_id ON invoice_items (invoice_id);

CREATE TABLE idempotency_keys (
    id BIGINT PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id),
    idempotency_key VARCHAR(255) NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    status idempotency_status_enum NOT NULL,
    response_status INTEGER,
    response_body JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_idempotency_keys_business_key UNIQUE (business_id, idempotency_key),
    CONSTRAINT chk_idempotency_keys_status CHECK (status IN ('PROCESSING', 'COMPLETED')),
    CONSTRAINT chk_idempotency_keys_response_status_body CHECK (
        (status = 'PROCESSING' AND response_status IS NULL AND response_body IS NULL) OR
        (status = 'COMPLETED' AND response_status IS NOT NULL AND response_body IS NOT NULL)
    )
);

CREATE TABLE payment_attempts (
    id BIGINT PRIMARY KEY,
    invoice_id BIGINT NOT NULL REFERENCES invoices(id),
    idempotency_key_id BIGINT NOT NULL REFERENCES idempotency_keys(id),
    status VARCHAR(32) NOT NULL,
    status payment_status_enum NOT NULL,
    psp_request_id BIGINT NOT NULL,
    psp_ref VARCHAR(255),
    failure_code VARCHAR(100),
    failure_reason TEXT,
    psp_requested_at TIMESTAMPTZ,
    psp_responded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_payment_attempts_idempotency_key_id UNIQUE (idempotency_key_id),
    CONSTRAINT uq_payment_attempts_psp_request_id UNIQUE (psp_request_id),
    CONSTRAINT chk_payment_attempts_status CHECK (status IN ('PROCESSING', 'SUCCEEDED', 'FAILED', 'UNKNOWN'))
    CONSTRAINT uq_payment_attempts_psp_request_id UNIQUE (psp_request_id)
);

CREATE UNIQUE INDEX uq_payment_attempts_unresolved_invoice ON payment_attempts (invoice_id) WHERE status IN ('PROCESSING', 'UNKNOWN');
CREATE INDEX idx_payment_attempts_invoice_created ON payment_attempts (invoice_id, created_at DESC);

CREATE TABLE webhook_endpoints (
    id BIGINT PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id),
    url TEXT NOT NULL,
    secret TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_webhook_endpoints_business_active ON webhook_endpoints (business_id, is_active);

CREATE TABLE webhook_deliveries (
    id BIGINT PRIMARY KEY,
    webhook_endpoint_id BIGINT NOT NULL REFERENCES webhook_endpoints(id),
    invoice_id BIGINT NOT NULL REFERENCES invoices(id),
    event_type VARCHAR(64) NOT NULL,
    event_type webhook_event_type_enum NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(32) NOT NULL,
    status webhook_delivery_status_enum NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    CONSTRAINT chk_webhook_deliveries_event_type CHECK (event_type IN ('INVOICE.CREATED', 'INVOICE.PAID', 'INVOICE.PAYMENT_FAILED')),
    CONSTRAINT chk_webhook_deliveries_status CHECK (status IN ('PENDING', 'PROCESSING', 'DELIVERED', 'EXHAUSTED')),
    CONSTRAINT chk_webhook_deliveries_attempt_count CHECK (attempt_count >= 0)
);

CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE status IN ('PENDING', 'PROCESSING');

-- +goose Down
SELECT 'down SQL query';
-- SQL in this section is executed when the migration is rolled back.

DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;
DROP TABLE IF EXISTS payment_attempts;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS invoice_items;
DROP TABLE IF EXISTS invoices;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS businesses;

DROP TYPE IF EXISTS webhook_event_type_enum;
DROP TYPE IF EXISTS webhook_delivery_status_enum;
DROP TYPE IF EXISTS payment_status_enum;
DROP TYPE IF EXISTS idempotency_status_enum;
DROP TYPE IF EXISTS invoice_state_enum;
