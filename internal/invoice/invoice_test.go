package invoice

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

type mockInvoiceQuerier struct {
	customers map[int64]generate.Customer
	invoices  map[int64]generate.Invoice
	items     map[int64][]generate.InvoiceItem
}

func newMockInvoiceQuerier() *mockInvoiceQuerier {
	return &mockInvoiceQuerier{
		customers: make(map[int64]generate.Customer),
		invoices:  make(map[int64]generate.Invoice),
		items:     make(map[int64][]generate.InvoiceItem),
	}
}

func (m *mockInvoiceQuerier) GetCustomerByID(ctx context.Context, arg generate.GetCustomerByIDParams) (generate.Customer, error) {
	c, exists := m.customers[arg.ID]
	if !exists || c.BusinessID != arg.BusinessID {
		return generate.Customer{}, pgx.ErrNoRows
	}
	return c, nil
}

func (m *mockInvoiceQuerier) CreateInvoice(ctx context.Context, arg generate.CreateInvoiceParams) (generate.Invoice, error) {
	inv := generate.Invoice{
		ID:               arg.ID,
		BusinessID:       arg.BusinessID,
		CustomerID:       arg.CustomerID,
		TotalAmountCents: arg.TotalAmountCents,
		Currency:         arg.Currency,
		State:            arg.State,
		DueDate:          arg.DueDate,
		CreatedAt:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		UpdatedAt:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	m.invoices[arg.ID] = inv
	return inv, nil
}

func (m *mockInvoiceQuerier) GetInvoiceByID(ctx context.Context, arg generate.GetInvoiceByIDParams) (generate.Invoice, error) {
	inv, exists := m.invoices[arg.ID]
	if !exists || inv.BusinessID != arg.BusinessID {
		return generate.Invoice{}, pgx.ErrNoRows
	}
	return inv, nil
}

func (m *mockInvoiceQuerier) ListInvoicesByBusinessID(ctx context.Context, businessID int64) ([]generate.Invoice, error) {
	var list []generate.Invoice
	for _, inv := range m.invoices {
		if inv.BusinessID == businessID {
			list = append(list, inv)
		}
	}
	return list, nil
}

func (m *mockInvoiceQuerier) UpdateInvoiceState(ctx context.Context, arg generate.UpdateInvoiceStateParams) (generate.Invoice, error) {
	inv, exists := m.invoices[arg.ID]
	if !exists || inv.BusinessID != arg.BusinessID {
		return generate.Invoice{}, pgx.ErrNoRows
	}
	inv.State = arg.State
	inv.UpdatedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	m.invoices[arg.ID] = inv
	return inv, nil
}

func (m *mockInvoiceQuerier) CreateInvoiceItem(ctx context.Context, arg generate.CreateInvoiceItemParams) (generate.InvoiceItem, error) {
	item := generate.InvoiceItem{
		ID:              arg.ID,
		InvoiceID:       arg.InvoiceID,
		Description:     arg.Description,
		Quantity:        arg.Quantity,
		UnitAmountCents: arg.UnitAmountCents,
		CreatedAt:       pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	m.items[arg.InvoiceID] = append(m.items[arg.InvoiceID], item)
	return item, nil
}

func (m *mockInvoiceQuerier) ListInvoiceItemsByInvoiceID(ctx context.Context, invoiceID int64) ([]generate.InvoiceItem, error) {
	return m.items[invoiceID], nil
}

func (m *mockInvoiceQuerier) ListInvoiceItemsByInvoiceIDs(ctx context.Context, ids []int64) ([]generate.InvoiceItem, error) {
	var list []generate.InvoiceItem
	for _, id := range ids {
		list = append(list, m.items[id]...)
	}
	return list, nil
}

type mockTransactor struct {
	q Querier
}

func (m *mockTransactor) WithTx(ctx context.Context, fn func(q Querier) error) error {
	return fn(m.q)
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

func TestInvoiceEndpoints(t *testing.T) {
	q := newMockInvoiceQuerier()
	tx := &mockTransactor{q: q}
	idGen := &mockIDGen{id: 8000}
	codec := publicid.New("test_invoice_secret_for_codec")
	handler := NewHandler(q, tx, idGen, codec)

	biz1 := int64(10)
	biz2 := int64(20)

	custID1 := int64(101)
	q.customers[custID1] = generate.Customer{
		ID:         custID1,
		BusinessID: biz1,
		Name:       "Customer 1",
		Email:      "c1@example.com",
	}
	custPubID1, _ := codec.Encode(publicid.PrefixCustomer, custID1)

	custID2 := int64(201)
	q.customers[custID2] = generate.Customer{
		ID:         custID2,
		BusinessID: biz2,
		Name:       "Customer 2",
		Email:      "c2@example.com",
	}
	custPubID2, _ := codec.Encode(publicid.PrefixCustomer, custID2)

	routerBiz1 := setupTestRouter(handler, biz1)
	routerBiz2 := setupTestRouter(handler, biz2)

	var createdInvPubID string

	t.Run("create invoice success with calculated totals", func(t *testing.T) {
		body := fmt.Sprintf(`{
			"customer_id": "%s",
			"currency": "USD",
			"due_date": "2026-10-15",
			"items": [
				{"description": "Consulting Hour", "quantity": 2, "unit_amount_cents": 15000},
				{"description": "Setup Fee", "quantity": 1, "unit_amount_cents": 5000}
			]
		}`, custPubID1)

		req := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp InvoiceResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		// Total should be 2 * 15000 + 1 * 5000 = 35000
		if resp.TotalAmountCents != 35000 {
			t.Errorf("expected total 35000 cents, got %d", resp.TotalAmountCents)
		}
		if resp.State != "OPEN" {
			t.Errorf("expected state OPEN, got %s", resp.State)
		}
		if resp.DueDate != "2026-10-15" {
			t.Errorf("expected due_date 2026-10-15, got %s", resp.DueDate)
		}
		if len(resp.Items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(resp.Items))
		}
		if resp.Items[0].LineTotalCents != 30000 || resp.Items[1].LineTotalCents != 5000 {
			t.Errorf("unexpected item line totals: %+v", resp.Items)
		}
		createdInvPubID = resp.ID
	})

	t.Run("create invoice reject non-USD currency", func(t *testing.T) {
		body := fmt.Sprintf(`{
			"customer_id": "%s",
			"currency": "EUR",
			"due_date": "2026-10-15",
			"items": [{"description": "Item", "quantity": 1, "unit_amount_cents": 100}]
		}`, custPubID1)

		req := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for non-USD, got %d", w.Code)
		}
	})

	t.Run("create invoice reject cross-tenant customer", func(t *testing.T) {
		// Biz1 tries to create invoice for Biz2's customer
		body := fmt.Sprintf(`{
			"customer_id": "%s",
			"currency": "USD",
			"due_date": "2026-10-15",
			"items": [{"description": "Item", "quantity": 1, "unit_amount_cents": 100}]
		}`, custPubID2)

		req := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for cross-tenant customer, got %d", w.Code)
		}
	})

	t.Run("create invoice reject non-positive quantity", func(t *testing.T) {
		body := fmt.Sprintf(`{
			"customer_id": "%s",
			"currency": "USD",
			"due_date": "2026-10-15",
			"items": [{"description": "Item", "quantity": 0, "unit_amount_cents": 100}]
		}`, custPubID1)

		req := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for zero quantity, got %d", w.Code)
		}
	})

	t.Run("create invoice with draft state", func(t *testing.T) {
		body := fmt.Sprintf(`{
			"customer_id": "%s",
			"currency": "USD",
			"due_date": "2026-10-15",
			"state": "DRAFT",
			"items": [{"description": "Draft Item", "quantity": 1, "unit_amount_cents": 2000}]
		}`, custPubID1)

		req := httptest.NewRequest(http.MethodPost, "/invoices", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d", w.Code)
		}

		var resp InvoiceResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.State != "DRAFT" {
			t.Errorf("expected state DRAFT, got %s", resp.State)
		}
	})

	t.Run("get invoice success and tenant isolation", func(t *testing.T) {
		// Biz1 can read its invoice
		req1 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%s", createdInvPubID), nil)
		w1 := httptest.NewRecorder()
		routerBiz1.ServeHTTP(w1, req1)
		if w1.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w1.Code)
		}

		// Biz2 cannot read Biz1's invoice
		req2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%s", createdInvPubID), nil)
		w2 := httptest.NewRecorder()
		routerBiz2.ServeHTTP(w2, req2)
		if w2.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for different business, got %d", w2.Code)
		}
	})

	t.Run("void invoice success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/invoices/%s/void", createdInvPubID), nil)
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp InvoiceResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.State != "VOID" {
			t.Errorf("expected state VOID, got %s", resp.State)
		}

		// Subsequent void should be idempotent
		wAgain := httptest.NewRecorder()
		reqAgain := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/invoices/%s/void", createdInvPubID), nil)
		routerBiz1.ServeHTTP(wAgain, reqAgain)
		if wAgain.Code != http.StatusOK {
			t.Fatalf("expected 200 on idempotent void, got %d", wAgain.Code)
		}
	})

	t.Run("void paid invoice reject", func(t *testing.T) {
		paidInvID := int64(9999)
		q.invoices[paidInvID] = generate.Invoice{
			ID:               paidInvID,
			BusinessID:       biz1,
			CustomerID:       custID1,
			TotalAmountCents: 1000,
			Currency:         "USD",
			State:            generate.InvoiceStateEnumPAID,
			CreatedAt:        pgtype.Timestamptz{Time: time.Now(), Valid: true},
			UpdatedAt:        pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}
		paidPubID, _ := codec.Encode(publicid.PrefixInvoice, paidInvID)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/invoices/%s/void", paidPubID), nil)
		w := httptest.NewRecorder()

		routerBiz1.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for voiding paid invoice, got %d", w.Code)
		}

		var errResp middleware.ErrorResponse
		json.Unmarshal(w.Body.Bytes(), &errResp)
		if errResp.Error.Code != "invalid_state_transition" {
			t.Errorf("expected invalid_state_transition, got %s", errResp.Error.Code)
		}
	})
}
