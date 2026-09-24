package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestEngine() *gin.Engine {
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger())
	r.Use(middleware.Recovery())
	return r
}

func TestRequestIDMiddleware(t *testing.T) {
	r := setupTestEngine()
	r.GET("/ping", func(c *gin.Context) {
		reqID := middleware.GetRequestID(c)
		c.JSON(http.StatusOK, gin.H{"request_id": reqID})
	})

	t.Run("generates request ID when none provided", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/ping", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}
		headerID := w.Header().Get(middleware.RequestIDHeader)
		if headerID == "" {
			t.Fatal("expected X-Request-ID header to be set")
		}

		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if body["request_id"] != headerID {
			t.Fatalf("expected body request_id %q to match header %q", body["request_id"], headerID)
		}
	})

	t.Run("preserves existing request ID", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/ping", nil)
		customID := "custom-req-12345"
		req.Header.Set(middleware.RequestIDHeader, customID)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}
		headerID := w.Header().Get(middleware.RequestIDHeader)
		if headerID != customID {
			t.Fatalf("expected header %q, got %q", customID, headerID)
		}
	})
}

func TestRecoveryMiddleware(t *testing.T) {
	r := setupTestEngine()
	r.GET("/panic", func(c *gin.Context) {
		panic("unexpected nil pointer or failure")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/panic", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", w.Code)
	}

	var resp middleware.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal JSON error response: %v", err)
	}

	if resp.Error.Code != "internal_error" {
		t.Fatalf("expected error code 'internal_error', got %q", resp.Error.Code)
	}
	if resp.Error.Message != "An unexpected error occurred" {
		t.Fatalf("expected generic error message, got %q", resp.Error.Message)
	}
}
