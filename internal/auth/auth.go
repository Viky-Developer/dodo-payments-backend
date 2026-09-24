// Package auth handles API key authentication and multi-tenant business scoping.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

const (
	BusinessIDContextKey = "business_id"
	ApiKeyIDContextKey   = "api_key_id"
)

// ParseAPIKey parses a raw API key token into keyPrefix and secret.
// The key format is dp_test_<key_prefix>_<secret>. The last underscore separates prefix and secret.
func ParseAPIKey(token string) (keyPrefix string, secret string, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", errors.New("empty api key")
	}
	lastUnderscore := strings.LastIndex(token, "_")
	if lastUnderscore <= 0 || lastUnderscore >= len(token)-1 {
		return "", "", errors.New("invalid api key format")
	}
	keyPrefix = token[:lastUnderscore]
	secret = token[lastUnderscore+1:]
	if keyPrefix == "" || secret == "" {
		return "", "", errors.New("invalid api key format")
	}
	return keyPrefix, secret, nil
}

// HashSecret computes the SHA-256 hex digest of an API key secret.
func HashSecret(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(hash[:])
}

// Authenticator defines the query operations needed by the auth middleware.
type Authenticator interface {
	GetApiKeyByPrefix(ctx context.Context, keyPrefix string) (generate.ApiKey, error)
}

// Middleware creates a Gin middleware that authenticates requests using Bearer API keys.
func Middleware(q Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.NewErrorResponse(
				"unauthorized",
				"Missing Authorization header",
			))
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.NewErrorResponse(
				"unauthorized",
				"Invalid Authorization header format, expected 'Bearer <api_key>'",
			))
			return
		}

		rawKey := strings.TrimSpace(parts[1])
		keyPrefix, secret, err := ParseAPIKey(rawKey)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.NewErrorResponse(
				"unauthorized",
				"Invalid API key format",
			))
			return
		}

		apiKey, err := q.GetApiKeyByPrefix(c.Request.Context(), keyPrefix)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.NewErrorResponse(
				"unauthorized",
				"Invalid API key",
			))
			return
		}

		if apiKey.RevokedAt.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.NewErrorResponse(
				"unauthorized",
				"API key has been revoked",
			))
			return
		}

		presentedHash := HashSecret(secret)
		if subtle.ConstantTimeCompare([]byte(apiKey.SecretHash), []byte(presentedHash)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.NewErrorResponse(
				"unauthorized",
				"Invalid API key",
			))
			return
		}

		c.Set(BusinessIDContextKey, int64(apiKey.BusinessID))
		c.Set(ApiKeyIDContextKey, int64(apiKey.ID))
		c.Next()
	}
}
