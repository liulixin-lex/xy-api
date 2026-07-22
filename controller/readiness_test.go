package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetReadinessSeparatesTrafficReadinessFromLiveness(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "readiness-payment-secret-key-0000000001")
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))
	require.NoError(t, service.ReportCurrentSystemInstance())
	readinessProbeCache.Lock()
	readinessProbeCache.checkedAt = time.Time{}
	readinessProbeCache.Unlock()

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, "/api/readiness", nil)
		GetReadiness(context)
		return recorder
	}

	ready := request()
	assert.Equal(t, http.StatusOK, ready.Code)
	assert.JSONEq(t, `{"success":true,"status":"ready"}`, ready.Body.String())

	require.NoError(t, db.Exec("DELETE FROM system_instances").Error)
	readinessProbeCache.Lock()
	readinessProbeCache.checkedAt = time.Time{}
	readinessProbeCache.Unlock()
	notReady := request()
	assert.Equal(t, http.StatusServiceUnavailable, notReady.Code)
	assert.JSONEq(t, `{"success":false,"status":"not_ready"}`, notReady.Body.String())
	assert.NotContains(t, notReady.Body.String(), "secret")
	assert.NotContains(t, notReady.Body.String(), "payment")
}
