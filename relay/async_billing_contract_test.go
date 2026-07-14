package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBindAsyncBillingClientIdentityCanonicalizesQueryWithoutWeakeningConflictDetection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bind := func(rawURL string) *relaycommon.RelayInfo {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, rawURL, nil)
		ctx.Request.Header.Set("Idempotency-Key", "query-contract-0001")
		info := &relaycommon.RelayInfo{
			UserId: 17, TokenId: 29, TaskRelayInfo: &relaycommon.TaskRelayInfo{},
		}
		err := bindAsyncBillingClientIdentity(ctx, info, model.AsyncBillingKindTask, "submit", map[string]any{
			"prompt": "stable",
		})
		require.NoError(t, err)
		return info
	}

	first := bind("/v1/videos?quality=hd&size=1280x720")
	reordered := bind("/v1/videos?size=1280x720&quality=hd")
	changed := bind("/v1/videos?quality=standard&size=1280x720")

	assert.Equal(t, first.AsyncBillingClientScope, reordered.AsyncBillingClientScope)
	assert.Equal(t, first.AsyncBillingClientKeyHash, reordered.AsyncBillingClientKeyHash)
	assert.Equal(t, first.AsyncBillingClientPayloadHash, reordered.AsyncBillingClientPayloadHash)
	assert.LessOrEqual(t, len(first.AsyncBillingClientScope), 191)

	assert.Equal(t, first.AsyncBillingClientScope, changed.AsyncBillingClientScope)
	assert.Equal(t, first.AsyncBillingClientKeyHash, changed.AsyncBillingClientKeyHash)
	assert.NotEqual(t, first.AsyncBillingClientPayloadHash, changed.AsyncBillingClientPayloadHash)
}

func TestClassifyAsyncSubmissionHTTPStatus(t *testing.T) {
	tests := []struct {
		status int
		want   asyncSubmissionOutcome
	}{
		{http.StatusOK, asyncSubmissionOutcomeAccepted},
		{http.StatusCreated, asyncSubmissionOutcomeAccepted},
		{http.StatusAccepted, asyncSubmissionOutcomeAccepted},
		{http.StatusNoContent, asyncSubmissionOutcomeAccepted},
		{http.StatusBadRequest, asyncSubmissionOutcomeRejected},
		{http.StatusUnauthorized, asyncSubmissionOutcomeRejected},
		{http.StatusTooManyRequests, asyncSubmissionOutcomeRejected},
		{http.StatusRequestTimeout, asyncSubmissionOutcomeAmbiguous},
		{http.StatusConflict, asyncSubmissionOutcomeAmbiguous},
		{http.StatusTooEarly, asyncSubmissionOutcomeAmbiguous},
		{460, asyncSubmissionOutcomeAmbiguous},
		{499, asyncSubmissionOutcomeAmbiguous},
		{http.StatusTemporaryRedirect, asyncSubmissionOutcomeAmbiguous},
		{http.StatusInternalServerError, asyncSubmissionOutcomeAmbiguous},
		{http.StatusBadGateway, asyncSubmissionOutcomeAmbiguous},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			assert.Equal(t, test.want, classifyAsyncSubmissionHTTPStatus(test.status))
		})
	}
}

func TestAsyncBillingIdempotentReplayTreatsManualReviewAsInProgress(t *testing.T) {
	for _, kind := range []string{model.AsyncBillingKindTask, model.AsyncBillingKindMidjourney} {
		t.Run(kind, func(t *testing.T) {
			_, err := asyncBillingIdempotentReplay(&model.AsyncBillingReservation{
				Kind: kind, State: model.AsyncBillingReservationStateManualReview, ReplayReady: true,
			})
			assert.ErrorIs(t, err, model.ErrAsyncBillingRequestInProgress)
		})
	}
}
