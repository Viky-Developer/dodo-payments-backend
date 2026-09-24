// Package psp contains the mock PSP HTTP client with bounded timeouts.
package psp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChargeRequest struct {
	Token        string `json:"token"`
	AmountCents  int64  `json:"amount_cents"`
	Currency     string `json:"currency"`
	PSPRequestID int64  `json:"psp_request_id"`
}

type ChargeResult struct {
	Status      string `json:"status"`
	PSPRef      string `json:"psp_ref,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *Client) Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ChargeResult{}, fmt.Errorf("encode PSP request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/payments", bytes.NewReader(body))
	if err != nil {
		return ChargeResult{}, fmt.Errorf("create PSP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return ChargeResult{}, fmt.Errorf("PSP transport failure: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ChargeResult{}, fmt.Errorf("PSP returned HTTP %d", resp.StatusCode)
	}
	var result ChargeResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return ChargeResult{}, fmt.Errorf("decode PSP response: %w", err)
	}
	if result.Status != "succeeded" && result.Status != "failed" {
		return ChargeResult{}, fmt.Errorf("invalid PSP status %q", result.Status)
	}
	return result, nil
}
