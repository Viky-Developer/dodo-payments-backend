package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// Recovery returns a Gin middleware that recovers from any panics, logs the stack trace
// securely on the server side, and returns a consistent JSON error response without leaking internals.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				reqID, _ := c.Get(RequestIDKey)
				log.Printf("[PANIC RECOVER] request_id=%v error=%v\nstack:\n%s", reqID, err, string(debug.Stack()))

				c.AbortWithStatusJSON(http.StatusInternalServerError, NewErrorResponse(
					"internal_error",
					"An unexpected error occurred",
				))
			}
		}()
		c.Next()
	}
}
