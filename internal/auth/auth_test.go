package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/idgen"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type mockAuthenticator struct {
	keys map[string]generate.ApiKey
}

func (m *mockAuthenticator) GetApiKeyByPrefix(ctx context.Context, keyPrefix string) (generate.ApiKey, error) {
	k, exists := m.keys[keyPrefix]
	if !exists {
		return generate.ApiKey{}, pgx.ErrNoRows
	}
	return k, nil
}

type mockBootstrapQuerier struct {
	businesses map[string]generate.Business
	keys       map[string]generate.ApiKey
}

func (m *mockBootstrapQuerier) GetBusinessByName(ctx context.Context, name string) (generate.Business, error) {
	b, exists := m.businesses[name]
	if !exists {
		return generate.Business{}, pgx.ErrNoRows
	}
	return b, nil
}

func (m *mockBootstrapQuerier) CreateBusiness(ctx context.Context, arg generate.CreateBusinessParams) (generate.Business, error) {
	b := generate.Business{
		ID:        arg.ID,
		Name:      arg.Name,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	m.businesses[arg.Name] = b
	return b, nil
}

func (m *mockBootstrapQuerier) GetApiKeyByPrefix(ctx context.Context, keyPrefix string) (generate.ApiKey, error) {
	k, exists := m.keys[keyPrefix]
	if !exists {
		return generate.ApiKey{}, pgx.ErrNoRows
	}
	return k, nil
}

func (m *mockBootstrapQuerier) CreateApiKey(ctx context.Context, arg generate.CreateApiKeyParams) (generate.ApiKey, error) {
	k := generate.ApiKey{
		ID:         arg.ID,
		BusinessID: arg.BusinessID,
		KeyPrefix:  arg.KeyPrefix,
		SecretHash: arg.SecretHash,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	m.keys[arg.KeyPrefix] = k
	return k, nil
}

func TestParseAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		wantPrefix string
		wantSecret string
		wantErr    bool
	}{
		{
			name:       "valid standard token",
			token:      "dp_test_demo_secret123",
			wantPrefix: "dp_test_demo",
			wantSecret: "secret123",
			wantErr:    false,
		},
		{
			name:       "valid prefix with multiple underscores",
			token:      "dp_test_live_app_key_mysecret",
			wantPrefix: "dp_test_live_app_key",
			wantSecret: "mysecret",
			wantErr:    false,
		},
		{
			name:    "empty token",
			token:   "",
			wantErr: true,
		},
		{
			name:    "no underscore",
			token:   "nounderscore",
			wantErr: true,
		},
		{
			name:    "trailing underscore",
			token:   "prefix_",
			wantErr: true,
		},
		{
			name:    "leading underscore only",
			token:   "_secret",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, s, err := ParseAPIKey(tt.token)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p != tt.wantPrefix {
				t.Errorf("got prefix %q, want %q", p, tt.wantPrefix)
			}
			if s != tt.wantSecret {
				t.Errorf("got secret %q, want %q", s, tt.wantSecret)
			}
		})
	}
}

func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	secret := "8dd9fd99e88d862190a460c6848178af9789e133d530b481316751f1c9d8403a"
	secretHash := HashSecret(secret)
	prefix := "dp_test_demo"

	mockAuth := &mockAuthenticator{
		keys: map[string]generate.ApiKey{
			prefix: {
				ID:         1001,
				BusinessID: 2001,
				KeyPrefix:  prefix,
				SecretHash: secretHash,
			},
			"dp_test_revoked": {
				ID:         1002,
				BusinessID: 2001,
				KeyPrefix:  "dp_test_revoked",
				SecretHash: secretHash,
				RevokedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		},
	}

	setupRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(Middleware(mockAuth))
		r.GET("/protected", func(c *gin.Context) {
			bizID := c.GetInt64(BusinessIDContextKey)
			if bizID == 0 {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"business_id": bizID})
		})
		return r
	}

	router := setupRouter()

	t.Run("success valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s_%s", prefix, secret))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]int64
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp["business_id"] != 2001 {
			t.Errorf("expected business_id 2001, got %d", resp["business_id"])
		}
	})

	t.Run("missing authorization header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}

		var errResp middleware.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if errResp.Error.Code != "unauthorized" {
			t.Errorf("expected error code unauthorized, got %s", errResp.Error.Code)
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s_wrongsecret", prefix))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})

	t.Run("revoked key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer dp_test_revoked_%s", secret))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})

	t.Run("unknown key prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer dp_test_unknown_secret")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})
}

func TestBootstrapDemoData(t *testing.T) {
	mockQ := &mockBootstrapQuerier{
		businesses: make(map[string]generate.Business),
		keys:       make(map[string]generate.ApiKey),
	}

	idGen := idgen.NewSnowflake(1)

	ctx := context.Background()

	// 1. Initial bootstrap should create business and key
	res1, err := BootstrapDemoData(ctx, mockQ, idGen)
	if err != nil {
		t.Fatalf("bootstrap 1 failed: %v", err)
	}
	if res1.BusinessID == 0 {
		t.Errorf("expected non-zero business ID")
	}
	if res1.KeyPrefix != DefaultDemoKeyPrefix {
		t.Errorf("expected prefix %s, got %s", DefaultDemoKeyPrefix, res1.KeyPrefix)
	}

	// 2. Second bootstrap should reuse existing business and key
	res2, err := BootstrapDemoData(ctx, mockQ, idGen)
	if err != nil {
		t.Fatalf("bootstrap 2 failed: %v", err)
	}
	if res2.BusinessID != res1.BusinessID {
		t.Errorf("expected reused business ID %d, got %d", res1.BusinessID, res2.BusinessID)
	}
}
