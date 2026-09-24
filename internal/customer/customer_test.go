package customer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type mockQuerier struct {
	customers map[int64]generate.Customer
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		customers: make(map[int64]generate.Customer),
	}
}

func (m *mockQuerier) CreateCustomer(ctx context.Context, arg generate.CreateCustomerParams) (generate.Customer, error) {
	c := generate.Customer{
		ID:         arg.ID,
		BusinessID: arg.BusinessID,
		Name:       arg.Name,
		Email:      arg.Email,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		UpdatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	m.customers[arg.ID] = c
	return c, nil
}

func (m *mockQuerier) GetCustomerByID(ctx context.Context, arg generate.GetCustomerByIDParams) (generate.Customer, error) {
	c, exists := m.customers[arg.ID]
	if !exists || c.BusinessID != arg.BusinessID {
		return generate.Customer{}, pgx.ErrNoRows
	}
	return c, nil
}

func (m *mockQuerier) ListCustomersByBusinessID(ctx context.Context, businessID int64) ([]generate.Customer, error) {
	var list []generate.Customer
	for _, c := range m.customers {
		if c.BusinessID == businessID {
			list = append(list, c)
		}
	}
	return list, nil
}

type mockIDGen struct {
	id int64
}

func (m *mockIDGen) NextID() int64 {
	m.id++
	return m.id
}

func setupTestRouter(h *Handler, businessID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.BusinessIDContextKey, businessID)
		c.Next()
	})
	h.RegisterRoutes(r.Group(""))
	return r
}

func TestCustomerEndpoints(t *testing.T) {
	q := newMockQuerier()
	idGen := &mockIDGen{id: 5000}
	codec := publicid.New("test_secret_for_public_id")
	handler := NewHandler(q, idGen, codec)

	biz1 := int64(100)
	biz2 := int64(200)

	routerBiz1 := setupTestRouter(handler, biz1)
	routerBiz2 := setupTestRouter(handler, biz2)

	var createdID string

	t.Run("create customer success", func(t *testing.T) {
		body := `{"name": "Alice Wonderland", "email": "alice@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp CustomerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if resp.Name != "Alice Wonderland" || resp.Email != "alice@example.com" {
			t.Errorf("unexpected customer data: %+v", resp)
		}
		if len(resp.ID) < 5 || resp.ID[:4] != "cus_" {
			t.Errorf("expected cus_ prefix on ID, got %s", resp.ID)
		}
		createdID = resp.ID
	})

	t.Run("create customer invalid email", func(t *testing.T) {
		body := `{"name": "Alice", "email": "not-an-email"}`
		req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", w.Code)
		}

		var errResp middleware.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if errResp.Error.Code != "bad_request" {
			t.Errorf("expected bad_request, got %s", errResp.Error.Code)
		}
	})

	t.Run("create customer empty name", func(t *testing.T) {
		body := `{"name": "", "email": "test@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})

	t.Run("get customer success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/customers/%s", createdID), nil)
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp CustomerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.ID != createdID {
			t.Errorf("expected %s, got %s", createdID, resp.ID)
		}
	})

	t.Run("get customer tenant isolation", func(t *testing.T) {
		// Biz2 attempts to access Biz1's customer
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/customers/%s", createdID), nil)
		w := httptest.NewRecorder()

		routerBiz2.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for different business, got %d", w.Code)
		}
	})

	t.Run("get customer invalid prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/customers/inv_123456", nil)
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for invalid prefix, got %d", w.Code)
		}
	})

	t.Run("list customers scoped", func(t *testing.T) {
		req1 := httptest.NewRequest(http.MethodGet, "/customers", nil)
		w1 := httptest.NewRecorder()
		routerBiz1.ServeHTTP(w1, req1)
		if w1.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w1.Code)
		}
		var list1 []CustomerResponse
		json.Unmarshal(w1.Body.Bytes(), &list1)
		if len(list1) != 1 {
			t.Errorf("expected 1 customer for biz1, got %d", len(list1))
		}

		req2 := httptest.NewRequest(http.MethodGet, "/customers", nil)
		w2 := httptest.NewRecorder()
		routerBiz2.ServeHTTP(w2, req2)
		if w2.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w2.Code)
		}
		var list2 []CustomerResponse
		json.Unmarshal(w2.Body.Bytes(), &list2)
		if len(list2) != 0 {
			t.Errorf("expected 0 customers for biz2, got %d", len(list2))
		}
	})
}
