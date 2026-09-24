// Package auth handles API key authentication and multi-tenant business scoping.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/jackc/pgx/v5"
)

// BootstrapQuerier defines the query subset needed to seed demo business and key.
type BootstrapQuerier interface {
	GetBusinessByName(ctx context.Context, name string) (generate.Business, error)
	CreateBusiness(ctx context.Context, arg generate.CreateBusinessParams) (generate.Business, error)
	GetApiKeyByPrefix(ctx context.Context, keyPrefix string) (generate.ApiKey, error)
	CreateApiKey(ctx context.Context, arg generate.CreateApiKeyParams) (generate.ApiKey, error)
}

const (
	DefaultDemoBusinessName = "Dodo Demo Business"
	DefaultDemoKeyPrefix    = "dp_test_demo"
)

// BootstrapResult contains information about the bootstrapped demo entity and API key.
type BootstrapResult struct {
	BusinessID int64
	KeyPrefix  string
	FullApiKey string
}

// IDGenerator provides unique 64-bit integer IDs.
type IDGenerator interface {
	NextID() int64
}

// BootstrapDemoData idempotently creates the demo business and associated demo API key.
func BootstrapDemoData(ctx context.Context, q BootstrapQuerier, idGen IDGenerator) (*BootstrapResult, error) {
	businessName := os.Getenv("DEMO_BUSINESS_NAME")
	if businessName == "" {
		businessName = DefaultDemoBusinessName
	}

	keyPrefix := os.Getenv("DEMO_API_KEY_PREFIX")
	if keyPrefix == "" {
		keyPrefix = DefaultDemoKeyPrefix
	}

	secret := os.Getenv("DEMO_API_KEY_SECRET")
	var generatedSecret string
	if secret == "" {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return nil, fmt.Errorf("failed to generate random secret: %w", err)
		}
		secret = hex.EncodeToString(secretBytes)
		generatedSecret = secret
	}

	// 1. Resolve demo business
	biz, err := q.GetBusinessByName(ctx, businessName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			bizID := idGen.NextID()
			biz, err = q.CreateBusiness(ctx, generate.CreateBusinessParams{
				ID:   bizID,
				Name: businessName,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create demo business: %w", err)
			}
			slog.Info("bootstrapped demo business", "business_id", biz.ID, "name", biz.Name)
		} else {
			return nil, fmt.Errorf("failed to check demo business: %w", err)
		}
	} else {
		slog.Info("found existing demo business", "business_id", biz.ID, "name", biz.Name)
	}

	// 2. Resolve demo API key
	apiKey, err := q.GetApiKeyByPrefix(ctx, keyPrefix)
	var fullKey string
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			keyID := idGen.NextID()
			secretHash := HashSecret(secret)
			apiKey, err = q.CreateApiKey(ctx, generate.CreateApiKeyParams{
				ID:         keyID,
				BusinessID: biz.ID,
				KeyPrefix:  keyPrefix,
				SecretHash: secretHash,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create demo api key: %w", err)
			}
			fullKey = fmt.Sprintf("%s_%s", keyPrefix, secret)
			slog.Info("bootstrapped demo api key", "key_prefix", keyPrefix, "api_key", fullKey)
		} else {
			return nil, fmt.Errorf("failed to check demo api key: %w", err)
		}
	} else {
		slog.Info("found existing demo api key", "key_prefix", apiKey.KeyPrefix)
		if generatedSecret == "" {
			fullKey = fmt.Sprintf("%s_%s", keyPrefix, secret)
		}
	}

	return &BootstrapResult{
		BusinessID: biz.ID,
		KeyPrefix:  keyPrefix,
		FullApiKey: fullKey,
	}, nil
}
