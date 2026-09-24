package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Viky-Developer/dodo-payments-backend/internal/psp"
)

func TestPaymentHandlerTokens(t *testing.T) {
	tests := []struct {
		token, status, code string
		httpStatus          int
	}{
		{"tok_success", "succeeded", "", http.StatusOK},
		{"tok_insufficient_funds", "failed", "insufficient_funds", http.StatusOK},
		{"tok_card_declined", "failed", "card_declined", http.StatusOK},
		{"tok_network_error", "", "", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			body, _ := json.Marshal(psp.ChargeRequest{Token: tt.token, PSPRequestID: 42})
			req := httptest.NewRequest(http.MethodPost, "/payments", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			paymentHandler(rec, req)
			if rec.Code != tt.httpStatus {
				t.Fatalf("got HTTP %d", rec.Code)
			}
			if tt.httpStatus == http.StatusOK {
				var got psp.ChargeResult
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Status != tt.status || got.FailureCode != tt.code {
					t.Fatalf("unexpected result %+v", got)
				}
			}
		})
	}
}
