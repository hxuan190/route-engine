package middlewares

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hxuan190/route-engine/internal/metrics"
)

func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		c.Next()

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())

		metrics.HTTPRequests.WithLabelValues(c.Request.Method, path, status).Inc()
		metrics.HTTPDuration.WithLabelValues(c.Request.Method, path).Observe(duration)
	}
}
