package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/psp"
)

var callCount atomic.Int64

func paymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	callCount.Add(1)
	var req psp.ChargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch req.Token {
	case "tok_success":
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(psp.ChargeResult{Status: "succeeded", PSPRef: fmt.Sprintf("psp_%d", req.PSPRequestID)})
	case "tok_insufficient_funds":
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(psp.ChargeResult{Status: "failed", FailureCode: "insufficient_funds"})
	case "tok_card_declined":
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(psp.ChargeResult{Status: "failed", FailureCode: "card_declined"})
	case "tok_timeout":
		time.Sleep(30 * time.Second)
		_ = json.NewEncoder(w).Encode(psp.ChargeResult{Status: "succeeded", PSPRef: fmt.Sprintf("psp_%d", req.PSPRequestID)})
	case "tok_network_error":
		http.Error(w, "simulated network error", http.StatusInternalServerError)
	default:
		http.Error(w, "unknown token", http.StatusBadRequest)
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/payments", paymentHandler)
	mux.HandleFunc("/calls", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"count": callCount.Load()})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	log.Printf("mock PSP listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
