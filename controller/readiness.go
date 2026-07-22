package controller

import (
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const readinessProbeCacheTTL = time.Second

var readinessProbeCache struct {
	sync.Mutex
	checkedAt time.Time
	ready     bool
}

// GetReadiness is an infrastructure probe, separate from the public status
// endpoint. A node remains alive for diagnostics while being removed from
// traffic whenever its database or payment-cluster runtime is unsafe.
func GetReadiness(c *gin.Context) {
	readinessProbeCache.Lock()
	now := time.Now()
	if readinessProbeCache.checkedAt.IsZero() || now.Sub(readinessProbeCache.checkedAt) >= readinessProbeCacheTTL {
		readinessProbeCache.ready = model.PingDB() == nil && service.EnsurePaymentClusterReady() == nil
		readinessProbeCache.checkedAt = now
	}
	ready := readinessProbeCache.ready
	readinessProbeCache.Unlock()

	if !ready {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"status":  "not_ready",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"status":  "ready",
	})
}
