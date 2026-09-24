package middleware

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger returns a structured request logging middleware.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		reqID := GetRequestID(c)

		if raw != "" {
			path = path + "?" + raw
		}

		log.Printf("[HTTP] status=%d method=%s path=%s client_ip=%s latency=%v request_id=%s",
			statusCode, method, path, clientIP, latency, reqID,
		)
	}
}
