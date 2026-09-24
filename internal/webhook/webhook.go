// Package webhook handles webhook endpoint registration, outbox delivery, and signing.
// Implementation begins in Phase 5.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IDGenerator generates unique Snowflake 64-bit IDs.
type IDGenerator interface {
	NextID() int64
}

// PublicIDCodec encodes and decodes public IDs with prefixes.
type PublicIDCodec interface {
	Encode(prefix string, id int64) (string, error)
	Decode(prefix string, publicID string) (int64, error)
}

// Querier defines database query access for webhook operations.
type Querier interface {
	CreateWebhookEndpoint(ctx context.Context, arg generate.CreateWebhookEndpointParams) (generate.WebhookEndpoint, error)
	GetWebhookEndpointByID(ctx context.Context, arg generate.GetWebhookEndpointByIDParams) (generate.WebhookEndpoint, error)
	ListWebhookEndpointsByBusinessID(ctx context.Context, businessID int64) ([]generate.WebhookEndpoint, error)
	ListActiveWebhookEndpointsByBusinessID(ctx context.Context, businessID int64) ([]generate.WebhookEndpoint, error)
	CreateWebhookDelivery(ctx context.Context, arg generate.CreateWebhookDeliveryParams) (generate.WebhookDelivery, error)
	ClaimDueWebhookDeliveries(ctx context.Context, arg generate.ClaimDueWebhookDeliveriesParams) ([]generate.ClaimDueWebhookDeliveriesRow, error)
	MarkWebhookDeliveryDelivered(ctx context.Context, id int64) (generate.WebhookDelivery, error)
	MarkWebhookDeliveryRetry(ctx context.Context, arg generate.MarkWebhookDeliveryRetryParams) (generate.WebhookDelivery, error)
	MarkWebhookDeliveryExhausted(ctx context.Context, arg generate.MarkWebhookDeliveryExhaustedParams) (generate.WebhookDelivery, error)
}

// Handler handles HTTP requests for webhook endpoints.
type Handler struct {
	queries Querier
	idGen   IDGenerator
	codec   PublicIDCodec
}

// NewHandler creates a new webhook endpoint Handler.
func NewHandler(queries Querier, idGen IDGenerator, codec PublicIDCodec) *Handler {
	return &Handler{
		queries: queries,
		idGen:   idGen,
		codec:   codec,
	}
}

// RegisterRoutes registers webhook endpoints on the router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	group := rg.Group("/webhook-endpoints")
	{
		group.POST("", h.CreateEndpoint)
		group.GET("", h.ListEndpoints)
		group.GET("/:id", h.GetEndpoint)
	}
}

// CreateEndpointRequest represents the payload to register a webhook destination.
type CreateEndpointRequest struct {
	URL string `json:"url"`
}

// WebhookEndpointResponse represents the public JSON representation of a webhook endpoint.
type WebhookEndpointResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Secret    string `json:"secret"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random secret: %w", err)
	}
	return "whsec_" + hex.EncodeToString(b), nil
}

func toEndpointResponse(ep generate.WebhookEndpoint, codec PublicIDCodec) (WebhookEndpointResponse, error) {
	pubID, err := codec.Encode(publicid.PrefixWebhookEndpoint, ep.ID)
	if err != nil {
		return WebhookEndpointResponse{}, err
	}
	return WebhookEndpointResponse{
		ID:        pubID,
		URL:       ep.Url,
		Secret:    ep.Secret,
		IsActive:  ep.IsActive,
		CreatedAt: ep.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt: ep.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}, nil
}

// CreateEndpoint registers a new webhook endpoint URL for the authenticated business.
func (h *Handler) CreateEndpoint(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	var req CreateEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "Invalid request body"))
		return
	}

	trimmedURL := strings.TrimSpace(req.URL)
	if trimmedURL == "" {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("invalid_url", "url is required"))
		return
	}

	parsedURL, err := url.ParseRequestURI(trimmedURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("invalid_url", "url must be a valid http or https URL"))
		return
	}

	secret, err := generateSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to generate signing secret"))
		return
	}

	endpointID := h.idGen.NextID()
	ep, err := h.queries.CreateWebhookEndpoint(c.Request.Context(), generate.CreateWebhookEndpointParams{
		ID:         endpointID,
		BusinessID: businessID,
		Url:        trimmedURL,
		Secret:     secret,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to save webhook endpoint"))
		return
	}

	resp, err := toEndpointResponse(ep, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode public ID"))
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// ListEndpoints lists all webhook endpoints scoped to the authenticated business.
func (h *Handler) ListEndpoints(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	endpoints, err := h.queries.ListWebhookEndpointsByBusinessID(c.Request.Context(), businessID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to fetch webhook endpoints"))
		return
	}

	respList := make([]WebhookEndpointResponse, 0, len(endpoints))
	for _, ep := range endpoints {
		resp, err := toEndpointResponse(ep, h.codec)
		if err != nil {
			c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode public ID"))
			return
		}
		respList = append(respList, resp)
	}

	c.JSON(http.StatusOK, respList)
}

// GetEndpoint retrieves a specific webhook endpoint by public ID.
func (h *Handler) GetEndpoint(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	idStr := c.Param("id")

	epID, err := h.codec.Decode(publicid.PrefixWebhookEndpoint, idStr)
	if err != nil {
		c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Webhook endpoint not found"))
		return
	}

	ep, err := h.queries.GetWebhookEndpointByID(c.Request.Context(), generate.GetWebhookEndpointByIDParams{
		ID:         epID,
		BusinessID: businessID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Webhook endpoint not found"))
			return
		}
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to retrieve webhook endpoint"))
		return
	}

	resp, err := toEndpointResponse(ep, h.codec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to encode public ID"))
		return
	}

	c.JSON(http.StatusOK, resp)
}

// ComputeSignature calculates the HMAC-SHA256 signature over timestamp + "." + payload.
func ComputeSignature(secret string, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks whether the provided signature matches the computed HMAC-SHA256.
func VerifySignature(secret string, timestamp string, payload []byte, signature string) bool {
	expected := ComputeSignature(secret, timestamp, payload)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// Standard retry schedule: immediate, +1m, +5m, +30m, +2h, +12h (6 attempts total).
var retryIntervals = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	12 * time.Hour,
}

// Worker processes pending and lease-expired webhook deliveries in the background.
type Worker struct {
	pool          *pgxpool.Pool
	queries       Querier
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int32
	client        *http.Client
}

// NewWorker creates a new webhook delivery background worker.
func NewWorker(pool *pgxpool.Pool, queries Querier, pollInterval, leaseDuration time.Duration, batchSize int32, client *http.Client) *Worker {
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	if batchSize <= 0 {
		batchSize = 10
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Worker{
		pool:          pool,
		queries:       queries,
		pollInterval:  pollInterval,
		leaseDuration: leaseDuration,
		batchSize:     batchSize,
		client:        client,
	}
}

// Start launches the background worker loop until the context is canceled.
func (w *Worker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.ProcessBatch(ctx)
		}
	}
}

// ProcessBatch claims and delivers one batch of due webhook rows.
func (w *Worker) ProcessBatch(ctx context.Context) int {
	leaseExpiry := time.Now().Add(w.leaseDuration)
	deliveries, err := w.queries.ClaimDueWebhookDeliveries(ctx, generate.ClaimDueWebhookDeliveriesParams{
		Limit:         w.batchSize,
		NextAttemptAt: pgtype.Timestamptz{Time: leaseExpiry, Valid: true},
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("[Webhook Worker] Error claiming deliveries: %v", err)
		}
		return 0
	}

	for _, d := range deliveries {
		w.deliverOne(ctx, d)
	}

	return len(deliveries)
}

func (w *Worker) deliverOne(ctx context.Context, d generate.ClaimDueWebhookDeliveriesRow) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ComputeSignature(d.EndpointSecret, timestamp, d.Payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.EndpointUrl, bytes.NewReader(d.Payload))
	if err != nil {
		w.handleFailure(ctx, d, fmt.Sprintf("failed to create HTTP request: %v", err))
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Timestamp", timestamp)
	req.Header.Set("X-Webhook-Signature", sig)

	resp, err := w.client.Do(req)
	if err != nil {
		w.handleFailure(ctx, d, fmt.Sprintf("transport error: %v", err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.handleFailure(ctx, d, fmt.Sprintf("HTTP status %d", resp.StatusCode))
		return
	}

	// Successful delivery
	if _, err := w.queries.MarkWebhookDeliveryDelivered(ctx, d.ID); err != nil {
		log.Printf("[Webhook Worker] Failed to mark delivery %d delivered: %v", d.ID, err)
	}
}

func (w *Worker) handleFailure(ctx context.Context, d generate.ClaimDueWebhookDeliveriesRow, errMsg string) {
	// AttemptCount was already incremented during claim.
	// Attempts: 1 (immediate), 2 (+1m), 3 (+5m), 4 (+30m), 5 (+2h), 6 (+12h).
	// If attempt 6 fails, status = EXHAUSTED.
	if d.AttemptCount >= 6 {
		_, err := w.queries.MarkWebhookDeliveryExhausted(ctx, generate.MarkWebhookDeliveryExhaustedParams{
			ID:        d.ID,
			LastError: pgtype.Text{String: errMsg, Valid: true},
		})
		if err != nil {
			log.Printf("[Webhook Worker] Failed to mark delivery %d exhausted: %v", d.ID, err)
		}
		return
	}

	intervalIndex := int(d.AttemptCount) - 1
	if intervalIndex < 0 {
		intervalIndex = 0
	}
	if intervalIndex >= len(retryIntervals) {
		intervalIndex = len(retryIntervals) - 1
	}

	delay := retryIntervals[intervalIndex]
	nextAttempt := time.Now().Add(delay)

	_, err := w.queries.MarkWebhookDeliveryRetry(ctx, generate.MarkWebhookDeliveryRetryParams{
		ID:            d.ID,
		NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true},
		LastError:     pgtype.Text{String: errMsg, Valid: true},
	})
	if err != nil {
		log.Printf("[Webhook Worker] Failed to reschedule delivery %d: %v", d.ID, err)
	}
}

// OutboxQueuer defines the database queries needed to queue transactional outbox deliveries.
type OutboxQueuer interface {
	ListActiveWebhookEndpointsByBusinessID(ctx context.Context, businessID int64) ([]generate.WebhookEndpoint, error)
	CreateWebhookDelivery(ctx context.Context, arg generate.CreateWebhookDeliveryParams) (generate.WebhookDelivery, error)
}

// QueueDeliveries inserts webhook_deliveries records for all active endpoints of the business.
func QueueDeliveries(ctx context.Context, q OutboxQueuer, idGen IDGenerator, businessID, invoiceID int64, eventType generate.WebhookEventTypeEnum, payload []byte) error {
	endpoints, err := q.ListActiveWebhookEndpointsByBusinessID(ctx, businessID)
	if err != nil {
		return fmt.Errorf("failed to query active webhook endpoints: %w", err)
	}

	for _, ep := range endpoints {
		_, err := q.CreateWebhookDelivery(ctx, generate.CreateWebhookDeliveryParams{
			ID:                idGen.NextID(),
			WebhookEndpointID: ep.ID,
			InvoiceID:         invoiceID,
			EventType:         eventType,
			Payload:           payload,
		})
		if err != nil {
			return fmt.Errorf("failed to insert webhook delivery: %w", err)
		}
	}

	return nil
}

// WebhookEventData represents the standard event data envelope.
type WebhookEventData struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// BuildPayload serializes an event and its data payload into JSON.
func BuildPayload(eventType generate.WebhookEventTypeEnum, data any) ([]byte, error) {
	return json.Marshal(WebhookEventData{
		Event: string(eventType),
		Data:  data,
	})
}
