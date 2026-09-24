### Development/demo bootstrap data

The assignment requires Business authentication using API keys but does not
require Business or API-key management CRUD endpoints.

For local development and demo purposes, application startup bootstraps:

1. One demo Business
2. One API key associated with that Business

#### Demo Business

The bootstrap process first checks whether the demo Business already exists.

If it does not exist:

- generate a Snowflake `BIGINT` ID
- create the Business
- use the generated Business ID when creating the API key

Example:

businesses:
- id: generated Snowflake ID
- name: Dodo Demo Business
- created_at: current timestamp
- updated_at: current timestamp

#### Demo API Key

After resolving the demo Business, bootstrap its API key.

Generate the development secret using:

openssl rand -hex 32

Configuration:

DEMO_API_KEY_PREFIX=dp_test_demo
DEMO_API_KEY_SECRET=<generated-secret>

Store:

api_keys:
- id: generated Snowflake ID
- business_id: demo Business ID
- key_prefix: dp_test_demo
- secret_hash: SHA-256(DEMO_API_KEY_SECRET)
- created_at: current timestamp
- revoked_at: NULL

The plaintext API-key secret must never be stored in PostgreSQL.

#### Bootstrap behavior

The bootstrap operation must be idempotent:

PostgreSQL ready
    ↓
Goose migrations
    ↓
Find/create demo Business
    ↓
Find/create demo API key
    ↓
Start API

Repeated `docker compose up` executions must reuse the existing demo Business
and API key rather than creating duplicate records.

Goose migrations are responsible only for schema versioning.
Demo Business and API-key creation belongs to the application bootstrap/seed
step.

Customer, invoice, payment-attempt, and webhook demo records are not seeded.
They are created through the API during the demo.