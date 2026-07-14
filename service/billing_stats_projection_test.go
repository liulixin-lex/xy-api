package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProcessNextBillingStatsProjectionDrainsDurableIntent(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.BillingStatsProjection{}, &model.QuotaData{}))
	require.NoError(t, model.DB.Exec("DELETE FROM billing_stats_projections").Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Exec("DELETE FROM billing_stats_projections").Error) })
	now := time.Unix(1_800_400_000, 0)
	require.NoError(t, model.DB.Create(&model.User{
		Id: 9951, Username: "stats-worker-user", AffCode: "stats-worker-aff", Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 9951, Name: "stats-worker-channel", Key: "stats-worker-key", Status: common.ChannelStatusEnabled,
	}).Error)
	var projection *model.BillingStatsProjection
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		projection, _, err = model.CreateBillingStatsProjectionTx(tx, model.BillingStatsProjectionSpec{
			OperationKey: "async:9951:accepted:v1", Kind: model.BillingStatsProjectionKindAccepted,
			ReferenceID: 9951, UserID: 9951, ChannelID: 9951, QuotaDelta: 15, RequestDelta: 1,
		}, now)
		return err
	}))

	completed, processed, err := processNextBillingStatsProjectionAt(
		context.Background(), "stats-worker", now, time.Minute,
	)
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, projection.ID, completed.ID)
	assert.Equal(t, model.BillingStatsProjectionStateCompleted, completed.State)
	var user model.User
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, 9951).Error)
	require.NoError(t, model.DB.First(&channel, 9951).Error)
	assert.Equal(t, 15, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, int64(15), channel.UsedQuota)

	_, processed, err = processNextBillingStatsProjectionAt(
		context.Background(), "stats-worker", now.Add(time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.False(t, processed)
}
