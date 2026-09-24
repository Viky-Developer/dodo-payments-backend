package payment

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/gin-gonic/gin"
)

type dummyIDs struct{}

func (d *dummyIDs) NextID() int64 { return 1001 }

func TestFingerprintDeterminism(t *testing.T) {
	fp1 := fingerprint(12345, "tok_success")
	fp2 := fingerprint(12345, "tok_success")
	if fp1 != fp2 {
		t.Fatalf("expected identical fingerprints, got %s and %s", fp1, fp2)
	}

	fpDiffToken := fingerprint(12345, "tok_card_declined")
	if fp1 == fpDiffToken {
		t.Fatalf("expected different fingerprints for different tokens")
	}

	fpDiffInvoice := fingerprint(99999, "tok_success")
	if fp1 == fpDiffInvoice {
		t.Fatalf("expected different fingerprints for different invoices")
	}
}

func TestPayInvoiceValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	codec := publicid.New("test-secret")
	h := NewHandler(nil, &dummyIDs{}, codec, nil)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.BusinessIDContextKey, int64(100))
		c.Next()
	})
	h.RegisterRoutes(r.Group(""))

	// 1. Missing Idempotency-Key
	req := httptest.NewRequest(http.MethodPost, "/invoices/inv_123/pay", bytes.NewBufferString(`{"card_token":"tok_success"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing Idempotency-Key, got %d", w.Code)
	}

	// 2. Invalid invoice ID format
	req = httptest.NewRequest(http.MethodPost, "/invoices/bad_id/pay", bytes.NewBufferString(`{"card_token":"tok_success"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-1")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for invalid invoice ID, got %d", w.Code)
	}

	// 3. Missing card_token in body
	validInv, err := codec.Encode(publicid.PrefixInvoice, 12345)
	if err != nil {
		t.Fatalf("failed to encode invoice id: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/invoices/"+validInv+"/pay", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-1")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing card_token, got %d", w.Code)
	}
}
