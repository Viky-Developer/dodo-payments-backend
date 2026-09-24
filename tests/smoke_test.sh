#!/usr/bin/env bash
set -euo pipefail

# Configuration
BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-dp_test_demo_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}"

GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

pass() {
  echo -e "${GREEN}[PASS]${NC} $1"
}

fail() {
  echo -e "${RED}[FAIL]${NC} $1"
  exit 1
}

info() {
  echo -e "${BLUE}[INFO]${NC} $1"
}

info "Target Base URL: $BASE_URL"

# 1. Health check
info "1. Checking health endpoint..."
HEALTH_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/health")
if [ "$HEALTH_STATUS" -eq 200 ]; then
  pass "Health check OK (HTTP 200)"
else
  fail "Health check failed with HTTP $HEALTH_STATUS"
fi

# 2. Unauthorized request check
info "2. Verifying authentication requirement..."
UNAUTH_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/customers")
if [ "$UNAUTH_STATUS" -eq 401 ]; then
  pass "Unauthenticated request correctly rejected (HTTP 401)"
else
  fail "Expected HTTP 401 for missing auth, got $UNAUTH_STATUS"
fi

# 3. Create customer
info "3. Creating a customer..."
CREATE_CUST_RESP=$(curl -s -X POST "$BASE_URL/customers" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "Alice Wonderland", "email": "alice@example.com"}')

CUST_ID=$(echo "$CREATE_CUST_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4 || true)
if [[ "$CUST_ID" =~ ^cus_ ]]; then
  pass "Customer created successfully: $CUST_ID"
else
  fail "Failed to create customer: $CREATE_CUST_RESP"
fi

# 4. Fetch customer
info "4. Fetching customer details..."
GET_CUST_RESP=$(curl -s -X GET "$BASE_URL/customers/$CUST_ID" \
  -H "Authorization: Bearer $API_KEY")
if echo "$GET_CUST_RESP" | grep -q "alice@example.com"; then
  pass "Fetched customer matches email: alice@example.com"
else
  fail "Failed to fetch customer: $GET_CUST_RESP"
fi

# 5. Create invoice
info "5. Creating invoice with line items (server-calculated totals)..."
# Item 1: 2 x 15000 = 30000 cents ($300.00)
# Item 2: 1 x 5000  = 5000 cents  ($50.00)
# Expected total: 35000 cents ($350.00)
CREATE_INV_RESP=$(curl -s -X POST "$BASE_URL/invoices" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"customer_id\": \"$CUST_ID\",
    \"currency\": \"USD\",
    \"due_date\": \"2026-12-31\",
    \"items\": [
      {\"description\": \"Software Consulting\", \"quantity\": 2, \"unit_amount_cents\": 15000},
      {\"description\": \"Setup Fee\", \"quantity\": 1, \"unit_amount_cents\": 5000}
    ]
  }")

INV_ID=$(echo "$CREATE_INV_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4 || true)
TOTAL_CENTS=$(echo "$CREATE_INV_RESP" | grep -o '"total_amount_cents":[0-9]*' | cut -d':' -f2 || true)
STATE=$(echo "$CREATE_INV_RESP" | grep -o '"state":"[^"]*' | cut -d'"' -f4 || true)

if [[ "$INV_ID" =~ ^inv_ ]] && [ "$TOTAL_CENTS" -eq 35000 ] && [ "$STATE" == "OPEN" ]; then
  pass "Invoice created: $INV_ID (Total: 35000 cents / \$350.00, State: OPEN)"
else
  fail "Failed to create invoice with expected totals: $CREATE_INV_RESP"
fi

# 6. Register webhook endpoint
info "6. Registering webhook endpoint..."
WEBHOOK_RESP=$(curl -s -X POST "$BASE_URL/webhook-endpoints" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/webhook"}')

WHE_ID=$(echo "$WEBHOOK_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4 || true)
SECRET=$(echo "$WEBHOOK_RESP" | grep -o '"secret":"[^"]*' | cut -d'"' -f4 || true)
if [[ "$WHE_ID" =~ ^whe_ ]] && [[ "$SECRET" =~ ^whsec_ ]]; then
  pass "Webhook endpoint registered: $WHE_ID with signing secret $SECRET"
else
  fail "Failed to register webhook endpoint: $WEBHOOK_RESP"
fi

# 7. Successful payment with Idempotency-Key
info "7. Paying invoice with tok_success and Idempotency-Key..."
PAY_KEY="smoke-test-key-$(date +%s)"
PAY_RESP=$(curl -s -X POST "$BASE_URL/invoices/$INV_ID/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: $PAY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"card_token": "tok_success"}')

PAY_STATUS=$(echo "$PAY_RESP" | grep -o '"status":"[^"]*' | cut -d'"' -f4 || true)
if [ "$PAY_STATUS" == "SUCCEEDED" ] || [ "$PAY_STATUS" == "succeeded" ]; then
  pass "Payment succeeded: status=$PAY_STATUS"
else
  fail "Payment failed: $PAY_RESP"
fi

# Verify invoice is now PAID
GET_INV_RESP=$(curl -s -X GET "$BASE_URL/invoices/$INV_ID" -H "Authorization: Bearer $API_KEY")
if echo "$GET_INV_RESP" | grep -q '"state":"PAID"'; then
  pass "Invoice state verified as PAID"
else
  fail "Invoice state expected to be PAID: $GET_INV_RESP"
fi

# 8. Idempotent replay: same key + same body
info "8. Replaying same payment with same Idempotency-Key..."
REPLAY_RESP=$(curl -s -X POST "$BASE_URL/invoices/$INV_ID/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: $PAY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"card_token": "tok_success"}')

if [ "$REPLAY_RESP" == "$PAY_RESP" ]; then
  pass "Idempotent replay returned identical response"
else
  fail "Idempotent replay mismatched: $REPLAY_RESP vs $PAY_RESP"
fi

# 9. Idempotency conflict: same key + different body
info "9. Reusing same Idempotency-Key with different card token..."
CONFLICT_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/invoices/$INV_ID/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: $PAY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"card_token": "tok_card_declined"}')

if [ "$CONFLICT_STATUS" -eq 409 ]; then
  pass "Reusing idempotency key with different body rejected with HTTP 409 Conflict"
else
  fail "Expected HTTP 409 for body mismatch, got $CONFLICT_STATUS"
fi

# 10. Already paid invoice: new key against paid invoice
info "10. Attempting payment against already PAID invoice..."
ALREADY_PAID_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/invoices/$INV_ID/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: new-key-$(date +%s)" \
  -H "Content-Type: application/json" \
  -d '{"card_token": "tok_success"}')

if [ "$ALREADY_PAID_STATUS" -eq 409 ]; then
  pass "Payment against already PAID invoice rejected with HTTP 409 Conflict"
else
  fail "Expected HTTP 409 for paid invoice, got $ALREADY_PAID_STATUS"
fi

# 11. Failed payment: tok_card_declined
info "11. Creating new invoice and testing tok_card_declined..."
INV2_RESP=$(curl -s -X POST "$BASE_URL/invoices" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"customer_id\": \"$CUST_ID\",
    \"currency\": \"USD\",
    \"due_date\": \"2026-12-31\",
    \"items\": [{\"description\": \"Item A\", \"quantity\": 1, \"unit_amount_cents\": 1000}]
  }")
INV2_ID=$(echo "$INV2_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4)

DECLINE_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/invoices/$INV2_ID/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: decline-key-$(date +%s)" \
  -H "Content-Type: application/json" \
  -d '{"card_token": "tok_card_declined"}')

if [ "$DECLINE_STATUS" -eq 402 ]; then
  pass "Card declined returned HTTP 402 Payment Required"
else
  fail "Expected HTTP 402 for declined card, got $DECLINE_STATUS"
fi

# Verify invoice remains OPEN
GET_INV2_RESP=$(curl -s -X GET "$BASE_URL/invoices/$INV2_ID" -H "Authorization: Bearer $API_KEY")
if echo "$GET_INV2_RESP" | grep -q '"state":"OPEN"'; then
  pass "Invoice correctly remains in OPEN state after decline"
else
  fail "Invoice expected to remain OPEN: $GET_INV2_RESP"
fi

# 12. Bounded timeout: tok_timeout returns 202 Accepted and UNKNOWN
info "12. Creating new invoice and testing tok_timeout (bounded timeout)..."
INV3_RESP=$(curl -s -X POST "$BASE_URL/invoices" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"customer_id\": \"$CUST_ID\",
    \"currency\": \"USD\",
    \"due_date\": \"2026-12-31\",
    \"items\": [{\"description\": \"Item B\", \"quantity\": 1, \"unit_amount_cents\": 2000}]
  }")
INV3_ID=$(echo "$INV3_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4)

TIMEOUT_START=$(date +%s)
TIMEOUT_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/invoices/$INV3_ID/pay" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: timeout-key-$(date +%s)" \
  -H "Content-Type: application/json" \
  -d '{"card_token": "tok_timeout"}')
TIMEOUT_DURATION=$(( $(date +%s) - TIMEOUT_START ))

if [ "$TIMEOUT_STATUS" -eq 202 ] && [ "$TIMEOUT_DURATION" -lt 6 ]; then
  pass "PSP timeout bounded successfully in ${TIMEOUT_DURATION}s with HTTP 202 Accepted"
else
  fail "Expected HTTP 202 in <6s, got HTTP $TIMEOUT_STATUS in ${TIMEOUT_DURATION}s"
fi

# 13. Void invoice
info "13. Creating and voiding an invoice..."
INV4_RESP=$(curl -s -X POST "$BASE_URL/invoices" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"customer_id\": \"$CUST_ID\",
    \"currency\": \"USD\",
    \"due_date\": \"2026-12-31\",
    \"items\": [{\"description\": \"Item C\", \"quantity\": 1, \"unit_amount_cents\": 3000}]
  }")
INV4_ID=$(echo "$INV4_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4)

VOID_RESP=$(curl -s -X POST "$BASE_URL/invoices/$INV4_ID/void" -H "Authorization: Bearer $API_KEY")
if echo "$VOID_RESP" | grep -q '"state":"VOID"'; then
  pass "Invoice voided successfully: state=VOID"
else
  fail "Failed to void invoice: $VOID_RESP"
fi

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN} All API Smoke Tests Passed Successfully!${NC}"
echo -e "${GREEN}========================================${NC}"
