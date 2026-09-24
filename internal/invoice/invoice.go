// Package invoice manages invoice state and line items scoped to a business.
// Implementation begins in Phase 2.
package invoice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/Viky-Developer/dodo-payments-backend/internal/webhook"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier defines the queries required for invoice and line item operations.
type Querier interface {
	GetCustomerByID(ctx context.Context, arg generate.GetCustomerByIDParams) (generate.Customer, error)
	CreateInvoice(ctx context.Context, arg generate.CreateInvoiceParams) (generate.Invoice, error)
	GetInvoiceByID(ctx context.Context, arg generate.GetInvoiceByIDParams) (generate.Invoice, error)
	ListInvoicesByBusinessID(ctx context.Context, businessID int64) ([]generate.Invoice, error)
	UpdateInvoiceState(ctx context.Context, arg generate.UpdateInvoiceStateParams) (generate.Invoice, error)
	CreateInvoiceItem(ctx context.Context, arg generate.CreateInvoiceItemParams) (generate.InvoiceItem, error)
	ListInvoiceItemsByInvoiceID(ctx context.Context, invoiceID int64) ([]generate.InvoiceItem, error)
	ListInvoiceItemsByInvoiceIDs(ctx context.Context, dollar_1 []int64) ([]generate.InvoiceItem, error)
	ListActiveWebhookEndpointsByBusinessID(ctx context.Context, businessID int64) ([]generate.WebhookEndpoint, error)
	CreateWebhookDelivery(ctx context.Context, arg generate.CreateWebhookDeliveryParams) (generate.WebhookDelivery, error)
}

// Transactor handles running transactional operations.
type Transactor interface {
	WithTx(ctx context.Context, fn func(q Querier) error) error
}

// PgxTransactor implements Transactor using pgxpool.Pool and sqlc Queries.
type PgxTransactor struct {
	pool    *pgxpool.Pool
	queries *generate.Queries
}

// NewPgxTransactor creates a new PgxTransactor.
func NewPgxTransactor(pool *pgxpool.Pool, queries *generate.Queries) *PgxTransactor {
	return &PgxTransactor{
		pool:    pool,
		queries: queries,
	}
}

// WithTx executes the provided function within a pgx transaction.
func (pt *PgxTransactor) WithTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := pt.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	qTx := pt.queries.WithTx(tx)
	if err := fn(qTx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IDGenerator defines the snowflake ID generator interface.
type IDGenerator interface {
	NextID() int64
}

// PublicIDCodec defines the public ID encoding/decoding interface.
type PublicIDCodec interface {
	Encode(prefix string, id int64) (string, error)
	Decode(prefix string, publicID string) (int64, error)
}

// Handler handles HTTP requests for invoice resources.
type Handler struct {
	queries    Querier
	transactor Transactor
	idGen      IDGenerator
	codec      PublicIDCodec
}

// NewHandler constructs a new Handler.
func NewHandler(queries Querier, transactor Transactor, idGen IDGenerator, codec PublicIDCodec) *Handler {
	return &Handler{
		queries:    queries,
		transactor: transactor,
		idGen:      idGen,
		codec:      codec,
	}
}

// RegisterRoutes registers invoice endpoints on the provided router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	invoices := rg.Group("/invoices")
	{
		invoices.POST("", h.CreateInvoice)
		invoices.GET("", h.ListInvoices)
		invoices.GET("/:id", h.GetInvoice)
		invoices.POST("/:id/void", h.VoidInvoice)
	}
}

// ItemInput represents an individual line item in invoice creation.
type ItemInput struct {
	Description     string `json:"description"`
	Quantity        int32  `json:"quantity"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
}

// CreateInvoiceRequest represents the invoice creation request payload.
type CreateInvoiceRequest struct {
	CustomerID string      `json:"customer_id"`
	Currency   string      `json:"currency"`
	DueDate    string      `json:"due_date"`
	Items      []ItemInput `json:"items"`
	State      *string     `json:"state,omitempty"`
}

// ItemResponse represents a line item returned to the client.
type ItemResponse struct {
	ID              string `json:"id"`
	InvoiceID       string `json:"invoice_id"`
	Description     string `json:"description"`
	Quantity        int32  `json:"quantity"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
	LineTotalCents  int64  `json:"line_total_cents"`
}

// InvoiceResponse represents the full invoice returned to the client.
type InvoiceResponse struct {
	ID               string         `json:"id"`
	CustomerID       string         `json:"customer_id"`
	TotalAmountCents int64          `json:"total_amount_cents"`
	Currency         string         `json:"currency"`
	State            string         `json:"state"`
	DueDate          string         `json:"due_date"`
	Items            []ItemResponse `json:"items"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

func toItemResponses(items []generate.InvoiceItem, codec PublicIDCodec) ([]ItemResponse, error) {
	resps := make([]ItemResponse, len(items))
	for i, itm := range items {
		pubID, err := codec.Encode(publicid.PrefixInvoiceItem, itm.ID)
		if err != nil {
			return nil, err
		}
		invPubID, err := codec.Encode(publicid.PrefixInvoice, itm.InvoiceID)
		if err != nil {
			return nil, err
		}
		lineTotal := int64(itm.Quantity) * itm.UnitAmountCents
		resps[i] = ItemResponse{
			ID:              pubID,
			InvoiceID:       invPubID,
			Description:     itm.Description,
			Quantity:        itm.Quantity,
			UnitAmountCents: itm.UnitAmountCents,
			LineTotalCents:  lineTotal,
		}
	}
	return resps, nil
}

func toInvoiceResponse(inv generate.Invoice, items []generate.InvoiceItem, codec PublicIDCodec) (InvoiceResponse, error) {
	invPubID, err := codec.Encode(publicid.PrefixInvoice, inv.ID)
	if err != nil {
		return InvoiceResponse{}, err
	}
	custPubID, err := codec.Encode(publicid.PrefixCustomer, inv.CustomerID)
	if err != nil {
		return InvoiceResponse{}, err
	}

	itemResps, err := toItemResponses(items, codec)
	if err != nil {
		return InvoiceResponse{}, err
	}

	dueDateStr := ""
	if inv.DueDate.Valid {
		dueDateStr = inv.DueDate.Time.Format("2006-01-02")
	}

	return InvoiceResponse{
		ID:               invPubID,
		CustomerID:       custPubID,
		TotalAmountCents: inv.TotalAmountCents,
		Currency:         inv.Currency,
		State:            string(inv.State),
		DueDate:          dueDateStr,
		Items:            itemResps,
		CreatedAt:        inv.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:        inv.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}, nil
}

// CreateInvoice handles POST /invoices.
func (h *Handler) CreateInvoice(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	var req CreateInvoiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invalid request body"))
		return
	}

	// 1. Currency validation (USD only)
	if strings.ToUpper(strings.TrimSpace(req.Currency)) != "USD" {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Currency must be USD"))
		return
	}

	// 2. Due date validation (YYYY-MM-DD)
	dueDate, err := time.Parse("2006-01-02", strings.TrimSpace(req.DueDate))
	if err != nil {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invalid due_date format, expected YYYY-MM-DD"))
		return
	}

	// 3. Line items validation and server-side calculation
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invoice must contain at least one line item"))
		return
	}

	var totalAmountCents int64
	for i, itm := range req.Items {
		desc := strings.TrimSpace(itm.Description)
		if desc == "" {
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", fmt.Sprintf("Item %d: description is required", i+1)))
			return
		}
		if itm.Quantity <= 0 {
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", fmt.Sprintf("Item %d: quantity must be positive", i+1)))
			return
		}
		if itm.UnitAmountCents < 0 {
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", fmt.Sprintf("Item %d: unit_amount_cents must be non-negative", i+1)))
			return
		}

		lineTotal := int64(itm.Quantity) * itm.UnitAmountCents
		if itm.UnitAmountCents > 0 && lineTotal/itm.UnitAmountCents != int64(itm.Quantity) {
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", fmt.Sprintf("Item %d: amount calculation overflow", i+1)))
			return
		}

		newTotal := totalAmountCents + lineTotal
		if newTotal < totalAmountCents {
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invoice total amount overflow"))
			return
		}
		totalAmountCents = newTotal
	}

	// 4. Customer validation & multi-tenant business ownership verification
	custID, err := h.codec.Decode(publicid.PrefixCustomer, strings.TrimSpace(req.CustomerID))
	if err != nil {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invalid customer_id format"))
		return
	}

	_, err = h.queries.GetCustomerByID(c.Request.Context(), generate.GetCustomerByIDParams{
		ID:         custID,
		BusinessID: businessID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Customer not found or belongs to another business"))
			return
		}
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to verify customer"))
		return
	}

	// 5. Initial state validation
	initialState := generate.InvoiceStateEnumOPEN
	if req.State != nil {
		s := strings.ToUpper(strings.TrimSpace(*req.State))
		switch s {
		case "DRAFT":
			initialState = generate.InvoiceStateEnumDRAFT
		case "OPEN":
			initialState = generate.InvoiceStateEnumOPEN
		default:
			c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Initial invoice state must be DRAFT or OPEN"))
			return
		}
	}

	// 6. Transactional creation of invoice and line items
	invoiceID := h.idGen.NextID()
	var createdInvoice generate.Invoice
	createdItems := make([]generate.InvoiceItem, len(req.Items))

	err = h.transactor.WithTx(c.Request.Context(), func(q Querier) error {
		inv, createErr := q.CreateInvoice(c.Request.Context(), generate.CreateInvoiceParams{
			ID:               invoiceID,
			BusinessID:       businessID,
			CustomerID:       custID,
			TotalAmountCents: totalAmountCents,
			Currency:         "USD",
			State:            initialState,
			DueDate:          pgtype.Date{Time: dueDate, Valid: true},
		})
		if createErr != nil {
			return createErr
		}
		createdInvoice = inv

		for idx, itm := range req.Items {
			itemID := h.idGen.NextID()
			item, itemErr := q.CreateInvoiceItem(c.Request.Context(), generate.CreateInvoiceItemParams{
				ID:              itemID,
				InvoiceID:       invoiceID,
				Description:     strings.TrimSpace(itm.Description),
				Quantity:        itm.Quantity,
				UnitAmountCents: itm.UnitAmountCents,
			})
			if itemErr != nil {
				return itemErr
			}
			createdItems[idx] = item
		}

		// Queue INVOICE.CREATED webhook delivery atomically
		pubInvID, _ := h.codec.Encode(publicid.PrefixInvoice, invoiceID)
		payload, _ := webhook.BuildPayload(generate.WebhookEventTypeEnumINVOICECREATED, map[string]any{
			"id":                 pubInvID,
			"customer_id":        req.CustomerID,
			"total_amount_cents": totalAmountCents,
			"currency":           "USD",
			"state":              string(initialState),
			"due_date":           dueDate.Format("2006-01-02"),
		})
		_ = webhook.QueueDeliveries(c.Request.Context(), q, h.idGen, businessID, invoiceID, generate.WebhookEventTypeEnumINVOICECREATED, payload)

		return nil
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to create invoice"))
		return
	}

	resp, err := toInvoiceResponse(createdInvoice, createdItems, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode invoice response"))
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// ListInvoices handles GET /invoices.
func (h *Handler) ListInvoices(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	invoices, err := h.queries.ListInvoicesByBusinessID(c.Request.Context(), businessID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve invoices"))
		return
	}

	if len(invoices) == 0 {
		c.JSON(http.StatusOK, []InvoiceResponse{})
		return
	}

	invIDs := make([]int64, len(invoices))
	for i, inv := range invoices {
		invIDs[i] = inv.ID
	}

	items, err := h.queries.ListInvoiceItemsByInvoiceIDs(c.Request.Context(), invIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve invoice items"))
		return
	}

	itemsByInvoiceID := make(map[int64][]generate.InvoiceItem)
	for _, itm := range items {
		itemsByInvoiceID[itm.InvoiceID] = append(itemsByInvoiceID[itm.InvoiceID], itm)
	}

	resps := make([]InvoiceResponse, len(invoices))
	for i, inv := range invoices {
		resp, err := toInvoiceResponse(inv, itemsByInvoiceID[inv.ID], h.codec)
		if err != nil {
			c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode invoice response"))
			return
		}
		resps[i] = resp
	}

	c.JSON(http.StatusOK, resps)
}

// GetInvoice handles GET /invoices/:id.
func (h *Handler) GetInvoice(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	rawID := c.Param("id")
	decodedID, err := h.codec.Decode(publicid.PrefixInvoice, rawID)
	if err != nil {
		c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Invoice not found"))
		return
	}

	inv, err := h.queries.GetInvoiceByID(c.Request.Context(), generate.GetInvoiceByIDParams{
		ID:         decodedID,
		BusinessID: businessID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Invoice not found"))
			return
		}
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve invoice"))
		return
	}

	items, err := h.queries.ListInvoiceItemsByInvoiceID(c.Request.Context(), decodedID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve invoice items"))
		return
	}

	resp, err := toInvoiceResponse(inv, items, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode invoice response"))
		return
	}

	c.JSON(http.StatusOK, resp)
}

// VoidInvoice handles POST /invoices/:id/void.
func (h *Handler) VoidInvoice(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	rawID := c.Param("id")
	decodedID, err := h.codec.Decode(publicid.PrefixInvoice, rawID)
	if err != nil {
		c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Invoice not found"))
		return
	}

	inv, err := h.queries.GetInvoiceByID(c.Request.Context(), generate.GetInvoiceByIDParams{
		ID:         decodedID,
		BusinessID: businessID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Invoice not found"))
			return
		}
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve invoice"))
		return
	}

	// State machine transition validation
	switch inv.State {
	case generate.InvoiceStateEnumPAID:
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("invalid_state_transition", "Cannot void a paid invoice"))
		return
	case generate.InvoiceStateEnumUNCOLLECTIBLE:
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("invalid_state_transition", "Cannot void an uncollectible invoice"))
		return
	case generate.InvoiceStateEnumVOID:
		// Idempotent void - invoice is already VOID
	case generate.InvoiceStateEnumDRAFT, generate.InvoiceStateEnumOPEN:
		updated, updateErr := h.queries.UpdateInvoiceState(c.Request.Context(), generate.UpdateInvoiceStateParams{
			ID:         decodedID,
			BusinessID: businessID,
			State:      generate.InvoiceStateEnumVOID,
		})
		if updateErr != nil {
			c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to void invoice"))
			return
		}
		inv = updated
	}

	items, err := h.queries.ListInvoiceItemsByInvoiceID(c.Request.Context(), decodedID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve invoice items"))
		return
	}

	resp, err := toInvoiceResponse(inv, items, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode invoice response"))
		return
	}

	c.JSON(http.StatusOK, resp)
}
