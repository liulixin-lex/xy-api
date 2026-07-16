package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingDecisionCandidatesEndpointPaginatesAndReportsTruncation(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	candidates := make([]channelrouting.DecisionCandidate, channelrouting.MaxDecisionCandidates+2)
	for index := range candidates {
		candidates[index] = channelrouting.DecisionCandidate{
			PoolMemberID: index + 1, ChannelID: index + 101, Eligible: true, Score: float64(index),
		}
	}
	decisionID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: "candidate-controller", PoolID: 1, SnapshotRevision: 1,
		GroupName: "default", ModelName: "gpt-test", Candidates: candidates,
	})
	require.NoError(t, err)
	_, err = channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: decisionID}}
	ctx.Request = httptest.NewRequest(
		http.MethodGet, "/api/channel-routing/decisions/"+decisionID+"/candidates?limit=10", nil,
	)
	ListChannelRoutingDecisionCandidates(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := recorder.Body.String()
	assert.Contains(t, body, `"total":66`)
	assert.Contains(t, body, `"available":64`)
	assert.Contains(t, body, `"complete":false`)
	assert.Contains(t, body, `"next_cursor":10`)
	assert.Contains(t, body, `"truncation_reason":"non_replayable_candidate_payload_limit"`)
}

func TestChannelRoutingCostDetailEndpointReturnsSingleBoundedPricingPayload(t *testing.T) {
	withChannelRoutingControllerState(t, openChannelRoutingControllerDB(t))
	pricingSentinel := "full-pricing-detail"
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 12, BuiltAtUnix: 100,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 7, GroupName: "vip", Members: []channelrouting.PoolMemberSnapshot{{
				ID: 11, PoolID: 7, ChannelID: 101, ChannelName: "provider",
				Models: []channelrouting.ModelSnapshot{{
					ModelName: "gpt-test", CostKnown: true, Cost: 0.5,
					CostPricing: &model.RoutingNormalizedPricing{
						Currency: "USD", Unit: "request", BillingExpression: pricingSentinel,
					},
				}},
			}},
		}},
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "pool_id", Value: "7"}, {Key: "member_id", Value: "11"}}
	ctx.Request = httptest.NewRequest(
		http.MethodGet, "/api/channel-routing/costs/7/11?model=gpt-test", nil,
	)
	GetChannelRoutingCost(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), pricingSentinel)
	assert.Contains(t, recorder.Body.String(), `"snapshot_revision":12`)

	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, true, response["success"])
}
