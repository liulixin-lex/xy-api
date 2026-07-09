package service

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

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
