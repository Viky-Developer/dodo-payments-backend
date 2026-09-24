// Package payment handles the payment endpoint, idempotency engine, and concurrency.
package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/Viky-Developer/dodo-payments-backend/internal/psp"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/Viky-Developer/dodo-payments-backend/internal/webhook"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IDGenerator interface{ NextID() int64 }
type PublicIDCodec interface {
	Encode(string, int64) (string, error)
	Decode(string, string) (int64, error)
}
type Charger interface {
	Charge(context.Context, psp.ChargeRequest) (psp.ChargeResult, error)
}

type Handler struct {
	pool  *pgxpool.Pool
	ids   IDGenerator
	codec PublicIDCodec
	psp   Charger
}

func NewHandler(pool *pgxpool.Pool, ids IDGenerator, codec PublicIDCodec, charger Charger) *Handler {
	return &Handler{pool: pool, ids: ids, codec: codec, psp: charger}
}

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) { rg.POST("/invoices/:id/pay", h.PayInvoice) }

type payRequest struct {
	CardToken string `json:"card_token"`
}
type payResponse struct {
	PaymentAttemptID string `json:"payment_attempt_id"`
	InvoiceID        string `json:"invoice_id"`
	Status           string `json:"status"`
	InvoiceState     string `json:"invoice_state"`
	PSPRef           string `json:"psp_ref,omitempty"`
	FailureCode      string `json:"failure_code,omitempty"`
	FailureReason    string `json:"failure_reason,omitempty"`
}

type claim struct {
	idem    generate.IdempotencyKey
	attempt generate.PaymentAttempt
	invoice generate.Invoice
	replay  bool
}

func fingerprint(invoiceID int64, token string) string {
	sum := sha256.Sum256([]byte("invoice_id=" + strconv.FormatInt(invoiceID, 10) + "\ncard_token=" + token))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) claim(ctx context.Context, businessID, invoiceID int64, key, hash string) (claim, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return claim{}, err
	}
	defer tx.Rollback(ctx)
	q := generate.New(tx)
	existing, err := q.GetIdempotencyKey(ctx, generate.GetIdempotencyKeyParams{BusinessID: businessID, IdempotencyKey: key})
	if err == nil {
		if existing.RequestHash != hash {
			return claim{}, errHashConflict
		}
		if existing.Status == generate.IdempotencyStatusEnumCOMPLETED {
			if err := tx.Commit(ctx); err != nil {
				return claim{}, err
			}
			return claim{idem: existing, replay: true}, nil
		}
		return claim{}, errInProgress
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return claim{}, err
	}
	inv, err := q.GetInvoiceForPayment(ctx, generate.GetInvoiceForPaymentParams{ID: invoiceID, BusinessID: businessID})
	if err != nil {
		return claim{}, err
	}
	if inv.State != generate.InvoiceStateEnumOPEN {
		return claim{}, errNotPayable
	}
	idem, err := q.CreateIdempotencyKey(ctx, generate.CreateIdempotencyKeyParams{ID: h.ids.NextID(), BusinessID: businessID, IdempotencyKey: key, RequestHash: hash})
	if err != nil {
		return claim{}, err
	}
	attempt, err := q.CreatePaymentAttempt(ctx, generate.CreatePaymentAttemptParams{ID: h.ids.NextID(), InvoiceID: invoiceID, IdempotencyKeyID: idem.ID, PspRequestID: h.ids.NextID()})
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return claim{}, errInProgress
		}
		return claim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return claim{}, err
	}
	return claim{idem: idem, attempt: attempt, invoice: inv}, nil
}

var (
	errHashConflict = errors.New("idempotency hash conflict")
	errInProgress   = errors.New("payment in progress")
	errNotPayable   = errors.New("invoice not payable")
)

func (h *Handler) finalize(ctx context.Context, c claim, result psp.ChargeResult, pspErr error, businessID int64) (int, []byte, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	q := generate.New(tx)
	attemptID, _ := h.codec.Encode(publicid.PrefixPaymentAttempt, c.attempt.ID)
	invoiceID, _ := h.codec.Encode(publicid.PrefixInvoice, c.invoice.ID)
	status := http.StatusOK
	resp := payResponse{PaymentAttemptID: attemptID, InvoiceID: invoiceID, InvoiceState: string(c.invoice.State)}
	if pspErr != nil {
		_, err = q.MarkPaymentAttemptUnknown(ctx, generate.MarkPaymentAttemptUnknownParams{ID: c.attempt.ID, FailureReason: pgtype.Text{String: "ambiguous_transport_error", Valid: true}})
		if err != nil {
			return 0, nil, err
		}
		status = http.StatusAccepted
		resp.Status = "UNKNOWN"
		resp.FailureReason = "ambiguous_transport_error"
	} else if result.Status == "failed" {
		_, err = q.MarkPaymentAttemptFailed(ctx, generate.MarkPaymentAttemptFailedParams{ID: c.attempt.ID, FailureCode: pgtype.Text{String: result.FailureCode, Valid: true}, FailureReason: pgtype.Text{String: "payment_declined", Valid: true}})
		if err != nil {
			return 0, nil, err
		}
		status = http.StatusPaymentRequired
		resp.Status = "FAILED"
		resp.FailureCode = result.FailureCode

		// Queue INVOICE.PAYMENT_FAILED webhook atomically
		payload, _ := webhook.BuildPayload(generate.WebhookEventTypeEnumINVOICEPAYMENTFAILED, map[string]any{
			"invoice_id":         invoiceID,
			"payment_attempt_id": attemptID,
			"failure_code":       result.FailureCode,
			"status":             "FAILED",
		})
		_ = webhook.QueueDeliveries(ctx, q, h.ids, businessID, c.invoice.ID, generate.WebhookEventTypeEnumINVOICEPAYMENTFAILED, payload)
	} else {
		_, err = q.MarkPaymentAttemptSucceeded(ctx, generate.MarkPaymentAttemptSucceededParams{ID: c.attempt.ID, PspRef: pgtype.Text{String: result.PSPRef, Valid: true}})
		if err != nil {
			return 0, nil, err
		}
		if _, err = q.MarkInvoicePaid(ctx, generate.MarkInvoicePaidParams{ID: c.invoice.ID, BusinessID: businessID}); err != nil {
			return 0, nil, err
		}
		resp.Status = "SUCCEEDED"
		resp.InvoiceState = "PAID"
		resp.PSPRef = result.PSPRef

		// Queue INVOICE.PAID webhook atomically
		payload, _ := webhook.BuildPayload(generate.WebhookEventTypeEnumINVOICEPAID, map[string]any{
			"invoice_id":         invoiceID,
			"payment_attempt_id": attemptID,
			"psp_ref":            result.PSPRef,
			"status":             "PAID",
		})
		_ = webhook.QueueDeliveries(ctx, q, h.ids, businessID, c.invoice.ID, generate.WebhookEventTypeEnumINVOICEPAID, payload)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return 0, nil, err
	}
	_, err = q.CompleteIdempotencyKey(ctx, generate.CompleteIdempotencyKeyParams{ID: c.idem.ID, ResponseStatus: pgtype.Int4{Int32: int32(status), Valid: true}, ResponseBody: body})
	if err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return status, body, nil
}

func (h *Handler) PayInvoice(c *gin.Context) {
	businessID := c.GetInt64(auth.BusinessIDContextKey)
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("invalid_idempotency_key", "Idempotency-Key header is required"))
		return
	}
	invoiceID, err := h.codec.Decode(publicid.PrefixInvoice, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Invoice not found"))
		return
	}
	var req payRequest
	if err = c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CardToken) == "" {
		c.JSON(http.StatusBadRequest, middleware.NewErrorResponse("bad_request", "card_token is required"))
		return
	}
	req.CardToken = strings.TrimSpace(req.CardToken)
	cl, err := h.claim(c.Request.Context(), businessID, invoiceID, key, fingerprint(invoiceID, req.CardToken))
	if err != nil {
		switch {
		case errors.Is(err, errHashConflict):
			c.JSON(http.StatusConflict, middleware.NewErrorResponse("idempotency_conflict", "Idempotency key was already used with a different request"))
		case errors.Is(err, errInProgress):
			c.JSON(http.StatusConflict, middleware.NewErrorResponse("payment_in_progress", "A payment is already processing for this invoice"))
		case errors.Is(err, errNotPayable):
			c.JSON(http.StatusConflict, middleware.NewErrorResponse("invoice_not_payable", "Invoice is not open for payment"))
		case errors.Is(err, pgx.ErrNoRows):
			c.JSON(http.StatusNotFound, middleware.NewErrorResponse("not_found", "Invoice not found"))
		default:
			c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to claim payment"))
		}
		return
	}
	if cl.replay {
		c.Data(int(cl.idem.ResponseStatus.Int32), "application/json", cl.idem.ResponseBody)
		return
	}
	result, pspErr := h.psp.Charge(c.Request.Context(), psp.ChargeRequest{Token: req.CardToken, AmountCents: cl.invoice.TotalAmountCents, Currency: cl.invoice.Currency, PSPRequestID: cl.attempt.PspRequestID})
	status, body, err := h.finalize(c.Request.Context(), cl, result, pspErr, businessID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, middleware.NewErrorResponse("internal_error", "Failed to finalize payment"))
		return
	}
	c.Data(status, "application/json", body)
}
