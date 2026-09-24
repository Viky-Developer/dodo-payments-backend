// Package customer handles customer resource management scoped to a business.
// Implementation begins in Phase 2.
package customer

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Querier defines the database access methods needed for customer operations.
type Querier interface {
	CreateCustomer(ctx context.Context, arg generate.CreateCustomerParams) (generate.Customer, error)
	GetCustomerByID(ctx context.Context, arg generate.GetCustomerByIDParams) (generate.Customer, error)
	ListCustomersByBusinessID(ctx context.Context, businessID int64) ([]generate.Customer, error)
}

// IDGenerator defines the ID generator interface.
type IDGenerator interface {
	NextID() int64
}

// PublicIDCodec defines the interface for encoding/decoding public IDs.
type PublicIDCodec interface {
	Encode(prefix string, id int64) (string, error)
	Decode(prefix string, publicID string) (int64, error)
}

// Handler handles HTTP requests for customer resources.
type Handler struct {
	queries Querier
	idGen   IDGenerator
	codec   PublicIDCodec
}

// NewHandler constructs a new customer Handler.
func NewHandler(queries Querier, idGen IDGenerator, codec PublicIDCodec) *Handler {
	return &Handler{
		queries: queries,
		idGen:   idGen,
		codec:   codec,
	}
}

// RegisterRoutes registers customer endpoints on the provided router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	customers := rg.Group("/customers")
	{
		customers.POST("", h.CreateCustomer)
		customers.GET("", h.ListCustomers)
		customers.GET("/:id", h.GetCustomer)
	}
}

// CreateCustomerRequest represents the payload for creating a customer.
type CreateCustomerRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// CustomerResponse represents the public representation of a customer.
type CustomerResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func isValidEmail(email string) bool {
	if len(email) == 0 || len(email) > 320 {
		return false
	}
	addr, err := mail.ParseAddress(email)
	return err == nil && addr.Address == email && strings.Contains(email, "@")
}

func toCustomerResponse(c generate.Customer, codec PublicIDCodec) (CustomerResponse, error) {

	pubID, err := codec.Encode(publicid.PrefixCustomer, c.ID)
	if err != nil {
		return CustomerResponse{}, err
	}
	return CustomerResponse{
		ID:        pubID,
		Name:      c.Name,
		Email:     c.Email,
		CreatedAt: c.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}, nil
}

// CreateCustomer handles POST /customers.
func (h *Handler) CreateCustomer(c *gin.Context) {

	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	var req CreateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invalid request body"))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Customer name is required"))
		return
	}
	if len(name) > 255 {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Customer name cannot exceed 255 characters"))
		return
	}

	email := strings.TrimSpace(req.Email)
	if !isValidEmail(email) {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "A valid email address is required"))
		return
	}

	custID := h.idGen.NextID()
	customer, err := h.queries.CreateCustomer(c.Request.Context(), generate.CreateCustomerParams{
		ID:         custID,
		BusinessID: businessID,
		Name:       name,
		Email:      email,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to create customer"))
		return
	}

	resp, err := toCustomerResponse(customer, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode customer ID"))
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// ListCustomers handles GET /customers.
func (h *Handler) ListCustomers(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	customers, err := h.queries.ListCustomersByBusinessID(c.Request.Context(), businessID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve customers"))
		return
	}

	resps := make([]CustomerResponse, 0, len(customers))
	for _, cust := range customers {
		resp, err := toCustomerResponse(cust, h.codec)
		if err != nil {
			c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode customer ID"))
			return
		}
		resps = append(resps, resp)
	}

	c.JSON(http.StatusOK, resps)
}

// GetCustomer handles GET /customers/:id.
func (h *Handler) GetCustomer(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	if businessID == 0 {
		c.JSON(http.StatusUnauthorized, middleware.NewErrorResponse("unauthorized", "Authentication required"))
		return
	}

	rawID := c.Param("id")
	decodedID, err := h.codec.Decode(publicid.PrefixCustomer, rawID)
	if err != nil {
		c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Customer not found"))
		return
	}

	customer, err := h.queries.GetCustomerByID(c.Request.Context(), generate.GetCustomerByIDParams{
		ID:         decodedID,
		BusinessID: businessID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Customer not found"))
			return
		}
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve customer"))
		return
	}

	resp, err := toCustomerResponse(customer, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode customer ID"))
		return
	}

	c.JSON(http.StatusOK, resp)
}
