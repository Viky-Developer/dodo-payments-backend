package psp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientCharge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payments" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(ChargeResult{Status: "succeeded", PSPRef: "psp_test"})
	}))
	defer server.Close()
	result, err := NewClient(server.URL, time.Second).Charge(context.Background(), ChargeRequest{Token: "tok_success", AmountCents: 100, Currency: "USD", PSPRequestID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.PSPRef != "psp_test" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientTimeoutIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer server.Close()
	_, err := NewClient(server.URL, 10*time.Millisecond).Charge(context.Background(), ChargeRequest{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestClientRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "failure", http.StatusInternalServerError) }))
	defer server.Close()
	_, err := NewClient(server.URL, time.Second).Charge(context.Background(), ChargeRequest{})
	if err == nil {
		t.Fatal("expected transport ambiguity error")
	}
}
