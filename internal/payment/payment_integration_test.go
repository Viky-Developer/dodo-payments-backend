package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/idgen"
	"github.com/Viky-Developer/dodo-payments-backend/internal/psp"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type countingCharger struct {
	calls  atomic.Int32
	result psp.ChargeResult
	err    error
	delay  time.Duration
}

func (c *countingCharger) Charge(context.Context, psp.ChargeRequest) (psp.ChargeResult, error) {
	c.calls.Add(1)
	time.Sleep(c.delay)
	return c.result, c.err
}

func paymentFixture(t *testing.T, charger Charger) (*gin.Engine, *pgxpool.Pool, string, *idgen.SnowflakeGenerator) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ids := idgen.NewSnowflake(77)
	codec := publicid.New("payment-test-secret")
	businessID := ids.NextID()
	customerID := ids.NextID()
	invoiceID := ids.NextID()
	_, err = pool.Exec(context.Background(), `INSERT INTO businesses(id,name) VALUES($1,$2)`, businessID, "payment test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `INSERT INTO customers(id,business_id,name,email) VALUES($1,$2,'payer','payer@example.com')`, customerID, businessID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `INSERT INTO invoices(id,business_id,customer_id,total_amount_cents,currency,state,due_date) VALUES($1,$2,$3,1000,'USD','OPEN',CURRENT_DATE)`, invoiceID, businessID, customerID)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(auth.BusinessIDContextKey, businessID); c.Next() })
	NewHandler(pool, ids, codec, charger).RegisterRoutes(r.Group(""))
	publicInvoice, err := codec.Encode(publicid.PrefixInvoice, invoiceID)
	if err != nil {
		t.Fatal(err)
	}
	return r, pool, publicInvoice, ids
}

func pay(t *testing.T, r http.Handler, invoice, key, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"card_token": token})
	req := httptest.NewRequest(http.MethodPost, "/invoices/"+invoice+"/pay", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestConcurrentPaymentChargesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	charger := &countingCharger{result: psp.ChargeResult{Status: "succeeded", PSPRef: "ref"}, delay: 50 * time.Millisecond}
	r, pool, invoice, _ := paymentFixture(t, charger)
	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes <- pay(t, r, invoice, "key-"+formatTestInt(i), "tok_success").Code }(i)
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == http.StatusOK {
			success++
		}
	}
	if success != 1 || charger.calls.Load() != 1 {
		t.Fatalf("success=%d calls=%d", success, charger.calls.Load())
	}
	var state string
	if err := pool.QueryRow(context.Background(), `SELECT state FROM invoices ORDER BY created_at DESC LIMIT 1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "PAID" {
		t.Fatalf("state=%s", state)
	}
}

func TestIdempotentReplayAndConflict(t *testing.T) {
	charger := &countingCharger{result: psp.ChargeResult{Status: "failed", FailureCode: "card_declined"}}
	r, _, invoice, _ := paymentFixture(t, charger)
	first := pay(t, r, invoice, "same-key", "tok_card_declined")
	second := pay(t, r, invoice, "same-key", "tok_card_declined")
	if first.Code != http.StatusPaymentRequired || second.Code != first.Code || first.Body.String() != second.Body.String() || charger.calls.Load() != 1 {
		t.Fatalf("first=%d second=%d calls=%d", first.Code, second.Code, charger.calls.Load())
	}
	if got := pay(t, r, invoice, "same-key", "tok_success"); got.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d", got.Code)
	}
}

func TestAmbiguousOutcomeIsPersistedAndReplayed(t *testing.T) {
	charger := &countingCharger{err: errors.New("timeout")}
	r, _, invoice, _ := paymentFixture(t, charger)
	first := pay(t, r, invoice, "timeout-key", "tok_timeout")
	second := pay(t, r, invoice, "timeout-key", "tok_timeout")
	if first.Code != http.StatusAccepted || second.Code != first.Code || charger.calls.Load() != 1 {
		t.Fatalf("first=%d second=%d calls=%d", first.Code, second.Code, charger.calls.Load())
	}
	if got := pay(t, r, invoice, "new-key", "tok_success"); got.Code != http.StatusConflict {
		t.Fatalf("new key status=%d", got.Code)
	}
}

func formatTestInt(v int) string {
	const digits = "0123456789"
	if v < 10 {
		return string(digits[v])
	}
	return "many"
}
