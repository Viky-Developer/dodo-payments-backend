package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func getBaseURL() string {
	url := os.Getenv("API_BASE_URL")
	if url == "" {
		url = "http://localhost:8080"
	}
	return url
}

func getAPIKey() string {
	key := os.Getenv("API_KEY")
	if key == "" {
		key = "dp_test_demo_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	}
	return key
}

// TestLiveAPILifecycle executes a comprehensive end-to-end verification against a running server.
// If the server is not reachable, it gracefully skips so offline unit tests still pass.
func TestLiveAPILifecycle(t *testing.T) {
	baseURL := getBaseURL()
	apiKey := getAPIKey()

	client := &http.Client{Timeout: 10 * time.Second}

	// 1. Health check to test reachability
	healthResp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Skipf("Live API not reachable at %s, skipping e2e live test: %v", baseURL, err)
		return
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health check returned status %d", healthResp.StatusCode)
	}

	// 2. Unauthenticated request must return 401
	unauthReq, _ := http.NewRequest(http.MethodGet, baseURL+"/customers", nil)
	unauthResp, err := client.Do(unauthReq)
	if err != nil {
		t.Fatal(err)
	}
	defer unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing auth, got %d", unauthResp.StatusCode)
	}

	// 3. Create customer
	custBody, _ := json.Marshal(map[string]string{
		"name":  "Alice Test",
		"email": fmt.Sprintf("alice-%d@example.com", time.Now().UnixNano()),
	})
	custReq, _ := http.NewRequest(http.MethodPost, baseURL+"/customers", bytes.NewReader(custBody))
	custReq.Header.Set("Authorization", "Bearer "+apiKey)
	custReq.Header.Set("Content-Type", "application/json")
	custResp, err := client.Do(custReq)
	if err != nil {
		t.Fatal(err)
	}
	defer custResp.Body.Close()
	if custResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for customer create, got %d", custResp.StatusCode)
	}

	var createdCustomer struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(custResp.Body).Decode(&createdCustomer); err != nil {
		t.Fatal(err)
	}

	// 4. Create invoice with server-calculated line item totals
	invBody, _ := json.Marshal(map[string]interface{}{
		"customer_id": createdCustomer.ID,
		"currency":    "USD",
		"due_date":    "2026-12-31",
		"items": []map[string]interface{}{
			{"description": "Consulting", "quantity": 2, "unit_amount_cents": 15000},
			{"description": "Setup Fee", "quantity": 1, "unit_amount_cents": 5000},
		},
	})
	invReq, _ := http.NewRequest(http.MethodPost, baseURL+"/invoices", bytes.NewReader(invBody))
	invReq.Header.Set("Authorization", "Bearer "+apiKey)
	invReq.Header.Set("Content-Type", "application/json")
	invResp, err := client.Do(invReq)
	if err != nil {
		t.Fatal(err)
	}
	defer invResp.Body.Close()
	if invResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for invoice create, got %d", invResp.StatusCode)
	}

	var createdInvoice struct {
		ID               string `json:"id"`
		TotalAmountCents int64  `json:"total_amount_cents"`
		State            string `json:"state"`
	}
	if err := json.NewDecoder(invResp.Body).Decode(&createdInvoice); err != nil {
		t.Fatal(err)
	}
	if createdInvoice.TotalAmountCents != 35000 {
		t.Fatalf("expected total 35000 cents, got %d", createdInvoice.TotalAmountCents)
	}
	if createdInvoice.State != "OPEN" {
		t.Fatalf("expected state OPEN, got %s", createdInvoice.State)
	}

	// 5. Register webhook endpoint
	wheBody, _ := json.Marshal(map[string]string{
		"url": "https://example.com/e2e-webhook",
	})
	wheReq, _ := http.NewRequest(http.MethodPost, baseURL+"/webhook-endpoints", bytes.NewReader(wheBody))
	wheReq.Header.Set("Authorization", "Bearer "+apiKey)
	wheReq.Header.Set("Content-Type", "application/json")
	wheResp, err := client.Do(wheReq)
	if err != nil {
		t.Fatal(err)
	}
	defer wheResp.Body.Close()
	if wheResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for webhook endpoint create, got %d", wheResp.StatusCode)
	}

	// 6. Pay invoice with tok_success and Idempotency-Key
	idempKey := fmt.Sprintf("e2e-key-%d", time.Now().UnixNano())
	payBody, _ := json.Marshal(map[string]string{"card_token": "tok_success"})
	payReq, _ := http.NewRequest(http.MethodPost, baseURL+"/invoices/"+createdInvoice.ID+"/pay", bytes.NewReader(payBody))
	payReq.Header.Set("Authorization", "Bearer "+apiKey)
	payReq.Header.Set("Idempotency-Key", idempKey)
	payReq.Header.Set("Content-Type", "application/json")
	payResp, err := client.Do(payReq)
	if err != nil {
		t.Fatal(err)
	}
	defer payResp.Body.Close()
	if payResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for payment success, got %d", payResp.StatusCode)
	}

	var paymentResult struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(payResp.Body).Decode(&paymentResult); err != nil {
		t.Fatal(err)
	}
	if paymentResult.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s", paymentResult.Status)
	}

	// 7. Replay same request with same Idempotency-Key
	replayReq, _ := http.NewRequest(http.MethodPost, baseURL+"/invoices/"+createdInvoice.ID+"/pay", bytes.NewReader(payBody))
	replayReq.Header.Set("Authorization", "Bearer "+apiKey)
	replayReq.Header.Set("Idempotency-Key", idempKey)
	replayReq.Header.Set("Content-Type", "application/json")
	replayResp, err := client.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	defer replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for idempotent replay, got %d", replayResp.StatusCode)
	}

	// 8. Body mismatch with same Idempotency-Key -> 409 Conflict
	conflictBody, _ := json.Marshal(map[string]string{"card_token": "tok_card_declined"})
	conflictReq, _ := http.NewRequest(http.MethodPost, baseURL+"/invoices/"+createdInvoice.ID+"/pay", bytes.NewReader(conflictBody))
	conflictReq.Header.Set("Authorization", "Bearer "+apiKey)
	conflictReq.Header.Set("Idempotency-Key", idempKey)
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictResp, err := client.Do(conflictReq)
	if err != nil {
		t.Fatal(err)
	}
	defer conflictResp.Body.Close()
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for body mismatch, got %d", conflictResp.StatusCode)
	}
}
