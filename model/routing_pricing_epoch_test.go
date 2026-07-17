package model

import (
	"errors"
	"maps"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestRoutingPricingVersionAdvancesOnlyWithCommittedPricingState(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&Option{}, &RoutingPricingVersion{}))
	require.NoError(t, db.Create(&Option{Key: "ModelRatio", Value: `{}`}).Error)
	require.NoError(t, EnsureRoutingPricingVersion(db))

	initial, err := GetRoutingPricingVersionDBContext(t.Context(), db)
	require.NoError(t, err)
	assert.Zero(t, initial.Epoch)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&Option{Key: "ModelRatio", Value: `{}`}).Error)
		_, reconcileErr := advanceRoutingPricingVersionTx(tx, common.GetTimestamp())
		return reconcileErr
	}))
	unchanged, err := GetRoutingPricingVersionDBContext(t.Context(), db)
	require.NoError(t, err)
	assert.Equal(t, initial.Epoch, unchanged.Epoch)
	assert.Equal(t, initial.StateHash, unchanged.StateHash)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&Option{}).Where("key = ?", "ModelRatio").
			Update("value", `{"routing-epoch-test":1}`).Error)
		_, reconcileErr := advanceRoutingPricingVersionTx(tx, common.GetTimestamp())
		return reconcileErr
	}))
	advanced, err := GetRoutingPricingVersionDBContext(t.Context(), db)
	require.NoError(t, err)
	assert.Equal(t, initial.Epoch+1, advanced.Epoch)
	assert.NotEqual(t, initial.StateHash, advanced.StateHash)

	rollbackErr := errors.New("force pricing transaction rollback")
	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&Option{}).Where("key = ?", "ModelRatio").
			Update("value", `{"routing-epoch-test":2}`).Error)
		_, reconcileErr := advanceRoutingPricingVersionTx(tx, common.GetTimestamp())
		require.NoError(t, reconcileErr)
		return rollbackErr
	})
	assert.ErrorIs(t, err, rollbackErr)
	afterRollback, err := GetRoutingPricingVersionDBContext(t.Context(), db)
	require.NoError(t, err)
	assert.Equal(t, advanced.Epoch, afterRollback.Epoch)
	assert.Equal(t, advanced.StateHash, afterRollback.StateHash)
	var option Option
	require.NoError(t, db.Where("key = ?", "ModelRatio").First(&option).Error)
	assert.Equal(t, `{"routing-epoch-test":1}`, option.Value)
}

func TestRoutingPricingOptionClassificationIsExplicit(t *testing.T) {
	assert.True(t, routingPricingOptionsChanged(map[string]string{"ModelPrice": `{}`}))
	assert.True(t, routingPricingOptionsChanged(map[string]string{"billing_setting.billing_mode": `{}`}))
	assert.True(t, routingPricingOptionsChanged(map[string]string{"tool_price_setting.example": `{}`}))
	assert.False(t, routingPricingOptionsChanged(map[string]string{"SystemName": "example"}))
}

func TestUpdateOptionsBulkPublishesCommittedRoutingPricingVersion(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&Option{}, &RoutingPricingVersion{}))
	require.NoError(t, EnsureRoutingPricingVersion(db))

	previousPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices)) })
	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	published := make(chan RoutingPricingVersion, 1)
	restorePublisher := RegisterRoutingPricingChangePublisher(func(version RoutingPricingVersion) {
		published <- version
	})
	t.Cleanup(restorePublisher)

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"ModelPrice": `{"routing-pricing-event-test":0.5}`,
	}))
	select {
	case version := <-published:
		assert.Equal(t, int64(1), version.Epoch)
		assert.Len(t, version.StateHash, 64)
		stored, err := GetRoutingPricingVersionDBContext(t.Context(), db)
		require.NoError(t, err)
		assert.Equal(t, stored, version)
	default:
		t.Fatal("committed pricing update did not publish its pricing version")
	}
}
