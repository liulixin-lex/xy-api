package service

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingSessionReserveWorksAfterTrustedPreConsume(t *testing.T) {
	truncate(t)
	seedUser(t, 9911, 5000)
	info := &relaycommon.RelayInfo{UserId: 9911, IsPlayground: true}
	session := &BillingSession{
		relayInfo: info,
		funding:   &WalletFunding{userId: 9911},
		trusted:   true,
	}

	err := session.Reserve(1500)

	require.NoError(t, err)
	assert.Equal(t, 1500, session.GetPreConsumedQuota())
	assert.Equal(t, 3500, getUserQuota(t, 9911))
	assert.Equal(t, 1500, info.FinalPreConsumedQuota)
}

func TestBillingSessionWalletPreConsumeMapsAtomicQuotaFailures(t *testing.T) {
	truncate(t)
	tests := []struct {
		name          string
		id            int
		userQuota     int
		tokenQuota    int
		expectedCode  types.ErrorCode
		expectedCause error
	}{
		{
			name: "wallet exhausted after stale precheck", id: 9912,
			userQuota: 50, tokenQuota: 100,
			expectedCode: types.ErrorCodeInsufficientUserQuota, expectedCause: model.ErrUserQuotaInsufficient,
		},
		{
			name: "finite token exhausted after stale precheck", id: 9913,
			userQuota: 100, tokenQuota: 50,
			expectedCode: types.ErrorCodePreConsumeTokenQuotaFailed, expectedCause: model.ErrTokenQuotaInsufficient,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, model.DB.Create(&model.User{
				Id: test.id, Username: fmt.Sprintf("atomic-session-user-%d", test.id),
				AffCode: fmt.Sprintf("atomic-session-aff-%d", test.id),
				Quota:   test.userQuota, Status: common.UserStatusEnabled,
			}).Error)
			key := fmt.Sprintf("sk-atomic-session-%d", test.id)
			seedToken(t, test.id, test.id, key, test.tokenQuota)
			info := &relaycommon.RelayInfo{UserId: test.id, TokenId: test.id, TokenKey: key}
			session := &BillingSession{relayInfo: info, funding: &WalletFunding{userId: test.id}}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

			apiErr := session.preConsume(ctx, 80)
			require.NotNil(t, apiErr)
			assert.Equal(t, test.expectedCode, apiErr.GetErrorCode())
			assert.ErrorIs(t, apiErr, test.expectedCause)
			assert.Equal(t, test.userQuota, getUserQuota(t, test.id))
			assert.Equal(t, test.tokenQuota, getTokenRemainQuota(t, test.id))
			assert.Zero(t, getTokenUsedQuota(t, test.id))
			assert.Zero(t, session.tokenConsumed)
			assert.Zero(t, session.preConsumedQuota)
		})
	}
}

func TestBillingSessionReserveMapsAtomicQuotaFailures(t *testing.T) {
	truncate(t)
	tests := []struct {
		name          string
		id            int
		userQuota     int
		tokenQuota    int
		expectedCode  types.ErrorCode
		expectedCause error
	}{
		{
			name: "wallet exhausted during reserve", id: 9914,
			userQuota: 50, tokenQuota: 100,
			expectedCode: types.ErrorCodeInsufficientUserQuota, expectedCause: model.ErrUserQuotaInsufficient,
		},
		{
			name: "finite token exhausted during reserve", id: 9915,
			userQuota: 100, tokenQuota: 50,
			expectedCode: types.ErrorCodePreConsumeTokenQuotaFailed, expectedCause: model.ErrTokenQuotaInsufficient,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, model.DB.Create(&model.User{
				Id: test.id, Username: fmt.Sprintf("atomic-reserve-user-%d", test.id),
				AffCode: fmt.Sprintf("atomic-reserve-aff-%d", test.id),
				Quota:   test.userQuota, Status: common.UserStatusEnabled,
			}).Error)
			key := fmt.Sprintf("sk-atomic-reserve-%d", test.id)
			seedToken(t, test.id, test.id, key, test.tokenQuota)
			session := &BillingSession{
				relayInfo: &relaycommon.RelayInfo{UserId: test.id, TokenId: test.id, TokenKey: key},
				funding:   &WalletFunding{userId: test.id},
			}

			err := session.Reserve(80)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, test.expectedCode, apiErr.GetErrorCode())
			assert.ErrorIs(t, err, test.expectedCause)
			assert.Equal(t, test.userQuota, getUserQuota(t, test.id))
			assert.Equal(t, test.tokenQuota, getTokenRemainQuota(t, test.id))
			assert.Zero(t, getTokenUsedQuota(t, test.id))
			assert.Zero(t, session.preConsumedQuota)
		})
	}
}

func TestBillingSessionRejectsNegativeAmountsWithoutCreditingQuota(t *testing.T) {
	truncate(t)
	seedUser(t, 9916, 100)
	seedToken(t, 9916, 9916, "sk-atomic-negative", 100)
	info := &relaycommon.RelayInfo{UserId: 9916, TokenId: 9916, TokenKey: "sk-atomic-negative"}
	session := &BillingSession{relayInfo: info, funding: &WalletFunding{userId: 9916}}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	require.Error(t, session.Reserve(-1))
	require.Error(t, session.Settle(-1))
	apiErr := session.preConsume(ctx, -1)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
	_, apiErr = NewBillingSession(ctx, info, -1)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
	apiErr = PreConsumeQuota(ctx, -1, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
	assert.Equal(t, 100, getUserQuota(t, 9916))
	assert.Equal(t, 100, getTokenRemainQuota(t, 9916))
	assert.Zero(t, getTokenUsedQuota(t, 9916))
	assert.Zero(t, session.preConsumedQuota)
}
