package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
)

type mockQuerier struct {
	endpoints  map[int64]generate.WebhookEndpoint
	deliveries map[int64]generate.WebhookDelivery
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		endpoints:  make(map[int64]generate.WebhookEndpoint),
		deliveries: make(map[int64]generate.WebhookDelivery),
	}
}

func (m *mockQuerier) CreateWebhookEndpoint(ctx context.Context, arg generate.CreateWebhookEndpointParams) (generate.WebhookEndpoint, error) {
	ep := generate.WebhookEndpoint{
		ID:         arg.ID,
		BusinessID: arg.BusinessID,
		Url:        arg.Url,
		Secret:     arg.Secret,
		IsActive:   true,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	m.endpoints[ep.ID] = ep
	return ep, nil
}

func (m *mockQuerier) GetWebhookEndpointByID(ctx context.Context, arg generate.GetWebhookEndpointByIDParams) (generate.WebhookEndpoint, error) {
	ep, ok := m.endpoints[arg.ID]
	if !ok || ep.BusinessID != arg.BusinessID {
		return generate.WebhookEndpoint{}, http.ErrNoCookie // any not-found err
	}
	return ep, nil
}

func (m *mockQuerier) ListWebhookEndpointsByBusinessID(ctx context.Context, businessID int64) ([]generate.WebhookEndpoint, error) {
	var res []generate.WebhookEndpoint
	for _, ep := range m.endpoints {
		if ep.BusinessID == businessID {
			res = append(res, ep)
		}
	}
	return res, nil
}

func (m *mockQuerier) ListActiveWebhookEndpointsByBusinessID(ctx context.Context, businessID int64) ([]generate.WebhookEndpoint, error) {
	var res []generate.WebhookEndpoint
	for _, ep := range m.endpoints {
		if ep.BusinessID == businessID && ep.IsActive {
			res = append(res, ep)
		}
	}
	return res, nil
}

func (m *mockQuerier) CreateWebhookDelivery(ctx context.Context, arg generate.CreateWebhookDeliveryParams) (generate.WebhookDelivery, error) {
	d := generate.WebhookDelivery{
		ID:                arg.ID,
		WebhookEndpointID: arg.WebhookEndpointID,
		InvoiceID:         arg.InvoiceID,
		EventType:         arg.EventType,
		Payload:           arg.Payload,
		Status:            generate.WebhookDeliveryStatusEnumPENDING,
		AttemptCount:      0,
		NextAttemptAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CreatedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	m.deliveries[d.ID] = d
	return d, nil
}

func (m *mockQuerier) ClaimDueWebhookDeliveries(ctx context.Context, arg generate.ClaimDueWebhookDeliveriesParams) ([]generate.ClaimDueWebhookDeliveriesRow, error) {
	var rows []generate.ClaimDueWebhookDeliveriesRow
	for id, d := range m.deliveries {
		if d.Status == generate.WebhookDeliveryStatusEnumPENDING || d.Status == generate.WebhookDeliveryStatusEnumPROCESSING {
			ep := m.endpoints[d.WebhookEndpointID]
			d.Status = generate.WebhookDeliveryStatusEnumPROCESSING
			d.AttemptCount++
			d.NextAttemptAt = arg.NextAttemptAt
			m.deliveries[id] = d
			rows = append(rows, generate.ClaimDueWebhookDeliveriesRow{
				ID:                d.ID,
				WebhookEndpointID: d.WebhookEndpointID,
				InvoiceID:         d.InvoiceID,
				EventType:         d.EventType,
				Payload:           d.Payload,
				Status:            d.Status,
				AttemptCount:      d.AttemptCount,
				NextAttemptAt:     d.NextAttemptAt,
				CreatedAt:         d.CreatedAt,
				EndpointUrl:       ep.Url,
				EndpointSecret:    ep.Secret,
			})
			if len(rows) >= int(arg.Limit) {
				break
			}
		}
	}
	return rows, nil
}

func (m *mockQuerier) MarkWebhookDeliveryDelivered(ctx context.Context, id int64) (generate.WebhookDelivery, error) {
	d := m.deliveries[id]
	d.Status = generate.WebhookDeliveryStatusEnumDELIVERED
	d.DeliveredAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	m.deliveries[id] = d
	return d, nil
}

func (m *mockQuerier) MarkWebhookDeliveryRetry(ctx context.Context, arg generate.MarkWebhookDeliveryRetryParams) (generate.WebhookDelivery, error) {
	d := m.deliveries[arg.ID]
	d.Status = generate.WebhookDeliveryStatusEnumPENDING
	d.NextAttemptAt = arg.NextAttemptAt
	d.LastError = arg.LastError
	m.deliveries[arg.ID] = d
	return d, nil
}

func (m *mockQuerier) MarkWebhookDeliveryExhausted(ctx context.Context, arg generate.MarkWebhookDeliveryExhaustedParams) (generate.WebhookDelivery, error) {
	d := m.deliveries[arg.ID]
	d.Status = generate.WebhookDeliveryStatusEnumEXHAUSTED
	d.LastError = arg.LastError
	m.deliveries[arg.ID] = d
	return d, nil
}

type dummyIDGen struct {
	current int64
}

func (d *dummyIDGen) NextID() int64 {
	d.current++
	return d.current
}

func TestWebhookSigning(t *testing.T) {
	secret := "whsec_testsecret123"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	payload := []byte(`{"event":"INVOICE.PAID","data":{"id":"inv_123"}}`)

	sig := ComputeSignature(secret, timestamp, payload)
	if sig == "" {
		t.Fatalf("expected signature to be non-empty")
	}

	if !VerifySignature(secret, timestamp, payload, sig) {
		t.Fatalf("expected signature to be valid")
	}

	// Tampered payload
	if VerifySignature(secret, timestamp, []byte(`{"event":"INVOICE.PAID","data":{"id":"inv_999"}}`), sig) {
		t.Fatalf("expected signature to be invalid for tampered payload")
	}

	// Tampered timestamp
	if VerifySignature(secret, timestamp+"1", payload, sig) {
		t.Fatalf("expected signature to be invalid for tampered timestamp")
	}

	// Tampered secret
	if VerifySignature("whsec_wrongsecret", timestamp, payload, sig) {
		t.Fatalf("expected signature to be invalid for wrong secret")
	}
}

func TestWebhookEndpointRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := newMockQuerier()
	ids := &dummyIDGen{}
	codec := publicid.New("test-secret")
	h := NewHandler(q, ids, codec)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.BusinessIDContextKey, int64(42))
		c.Next()
	})
	h.RegisterRoutes(r.Group(""))

	// 1. Valid endpoint registration
	reqBody, _ := json.Marshal(CreateEndpointRequest{URL: "https://example.com/webhook"})
	req := httptest.NewRequest(http.MethodPost, "/webhook-endpoints", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp WebhookEndpointResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID == "" || resp.Secret == "" || resp.URL != "https://example.com/webhook" || !resp.IsActive {
		t.Fatalf("unexpected endpoint response: %+v", resp)
	}

	// 2. Invalid URL
	badReqBody, _ := json.Marshal(CreateEndpointRequest{URL: "not-a-valid-url"})
	badReq := httptest.NewRequest(http.MethodPost, "/webhook-endpoints", bytes.NewReader(badReqBody))
	badReq.Header.Set("Content-Type", "application/json")
	badW := httptest.NewRecorder()
	r.ServeHTTP(badW, badReq)

	if badW.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid url, got %d", badW.Code)
	}

	// 3. List endpoints
	listReq := httptest.NewRequest(http.MethodGet, "/webhook-endpoints", nil)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("expected 200 for list, got %d", listW.Code)
	}
	var listResp []WebhookEndpointResponse
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil || len(listResp) != 1 {
		t.Fatalf("expected 1 endpoint in list, got %d", len(listResp))
	}

	// 4. Get endpoint by ID
	getReq := httptest.NewRequest(http.MethodGet, "/webhook-endpoints/"+resp.ID, nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 for get by id, got %d", getW.Code)
	}
}

func TestWorkerDeliveryAndRetry(t *testing.T) {
	q := newMockQuerier()
	ids := &dummyIDGen{}

	// Register mock server as target
	var receivedSig, receivedTimestamp string
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Webhook-Signature")
		receivedTimestamp = r.Header.Get("X-Webhook-Timestamp")
		receivedBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ep, err := q.CreateWebhookEndpoint(context.Background(), generate.CreateWebhookEndpointParams{
		ID:         ids.NextID(),
		BusinessID: 10,
		Url:        server.URL,
		Secret:     "whsec_secret_key_12345",
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"event":"INVOICE.CREATED","data":{"id":"inv_test"}}`)
	err = QueueDeliveries(context.Background(), q, ids, 10, 1001, generate.WebhookEventTypeEnumINVOICECREATED, payload)
	if err != nil {
		t.Fatal(err)
	}

	worker := NewWorker(nil, q, 10*time.Millisecond, 10*time.Second, 10, server.Client())

	processed := worker.ProcessBatch(context.Background())
	if processed != 1 {
		t.Fatalf("expected 1 delivery processed, got %d", processed)
	}

	// Verify receiver got valid signature
	if !VerifySignature(ep.Secret, receivedTimestamp, receivedBody, receivedSig) {
		t.Fatalf("receiver signature validation failed")
	}

	// Verify delivery is marked DELIVERED
	for _, d := range q.deliveries {
		if d.Status != generate.WebhookDeliveryStatusEnumDELIVERED {
			t.Fatalf("expected delivery status DELIVERED, got %s", d.Status)
		}
	}
}

func TestWorkerExhaustionAfterSixAttempts(t *testing.T) {
	q := newMockQuerier()
	ids := &dummyIDGen{}

	// Failing server (500 Internal Server Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := q.CreateWebhookEndpoint(context.Background(), generate.CreateWebhookEndpointParams{
		ID:         ids.NextID(),
		BusinessID: 20,
		Url:        server.URL,
		Secret:     "whsec_secret_test",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = QueueDeliveries(context.Background(), q, ids, 20, 2002, generate.WebhookEventTypeEnumINVOICEPAYMENTFAILED, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	worker := NewWorker(nil, q, 10*time.Millisecond, 10*time.Second, 10, server.Client())

	// Run attempts 1 to 5: should be PENDING with backoff
	for i := 1; i <= 5; i++ {
		worker.ProcessBatch(context.Background())
		for _, d := range q.deliveries {
			if d.Status != generate.WebhookDeliveryStatusEnumPENDING {
				t.Fatalf("attempt %d: expected status PENDING, got %s", i, d.Status)
			}
			if d.AttemptCount != int32(i) {
				t.Fatalf("attempt %d: expected attempt_count %d, got %d", i, i, d.AttemptCount)
			}
		}
	}

	// Run attempt 6: should become EXHAUSTED
	worker.ProcessBatch(context.Background())
	for _, d := range q.deliveries {
		if d.Status != generate.WebhookDeliveryStatusEnumEXHAUSTED {
			t.Fatalf("expected status EXHAUSTED, got %s", d.Status)
		}
		if d.AttemptCount != 6 {
			t.Fatalf("expected attempt_count 6, got %d", d.AttemptCount)
		}
	}
}
