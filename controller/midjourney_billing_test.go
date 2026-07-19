package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupMidjourneyControllerBillingDB(t *testing.T) *gorm.DB {
	t.Helper()
	service.InitHttpClient()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.Option{},
		&model.User{},
		&model.Token{},
		&model.Channel{},
		&model.Midjourney{},
		&model.BillingReservation{},
		&model.QuotaLedgerEntry{},
		&model.SubscriptionPreConsumeRecord{},
		&model.Log{},
	))

	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(originalMainType, originalLogType)
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
	return db
}

func TestMidjourneyPollingScopesSharedUpstreamIDByChannel(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	sharedID := "shared-cross-channel-id"
	providerServer := func(status, reason string) *httptest.Server {
		payload, err := common.Marshal([]dto.MidjourneyDto{{
			MjId:       sharedID,
			Status:     status,
			Progress:   "100%",
			FailReason: reason,
		}})
		require.NoError(t, err)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
		}))
	}
	failureServer := providerServer(model.MidjourneyStatusFailure, "channel one failure")
	successServer := providerServer(model.MidjourneyStatusSuccess, "")
	t.Cleanup(failureServer.Close)
	t.Cleanup(successServer.Close)

	type fixture struct {
		userID    int
		tokenID   int
		channelID int
		requestID string
		baseURL   string
	}
	fixtures := []fixture{
		{userID: 981011, tokenID: 982011, channelID: 983011, requestID: "mj-cross-channel-fail", baseURL: failureServer.URL},
		{userID: 981012, tokenID: 982012, channelID: 983012, requestID: "mj-cross-channel-success", baseURL: successServer.URL},
	}
	tasks := make([]*model.Midjourney, 0, len(fixtures))
	for _, item := range fixtures {
		user := &model.User{Id: item.userID, Username: item.requestID, AffCode: item.requestID, Quota: 100, Status: common.UserStatusEnabled}
		token := &model.Token{Id: item.tokenID, UserId: item.userID, Key: item.requestID, Name: item.requestID, Status: common.TokenStatusEnabled, RemainQuota: 100}
		baseURL := item.baseURL
		channel := &model.Channel{Id: item.channelID, Name: item.requestID, Key: "mj-secret", Status: common.ChannelStatusEnabled, BaseURL: &baseURL}
		require.NoError(t, db.Create(user).Error)
		require.NoError(t, db.Create(token).Error)
		require.NoError(t, db.Create(channel).Error)
		_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
			RequestId: item.requestID, UserId: item.userID, TokenId: item.tokenID,
			FundingSource: model.BillingFundingWallet, Quota: 20,
		}, token.Key)
		require.NoError(t, err)
		claimed, err := model.ClaimMidjourneyBillingReservation(item.requestID)
		require.NoError(t, err)
		require.True(t, claimed)
		task := &model.Midjourney{
			UserId: item.userID, MjId: sharedID, Action: "IMAGINE", SubmitTime: time.Now().UnixMilli(),
			ChannelId: item.channelID, Quota: 20, Progress: "20%",
			PrivateData: model.TaskPrivateData{BillingSource: model.BillingFundingWallet, TokenId: item.tokenID, BillingRequestId: item.requestID},
		}
		require.NoError(t, model.CreateMidjourneyWithBillingReservation(task, item.requestID, 20, token.Key))
		tasks = append(tasks, task)
	}

	summary := runMidjourneyTaskUpdateOnce(context.Background(), nil)
	assert.Equal(t, 2, summary.ChannelsScanned)

	var failedTask, succeededTask model.Midjourney
	require.NoError(t, db.First(&failedTask, tasks[0].Id).Error)
	require.NoError(t, db.First(&succeededTask, tasks[1].Id).Error)
	assert.Equal(t, model.MidjourneyStatusFailure, failedTask.Status)
	assert.Equal(t, model.MidjourneyStatusSuccess, succeededTask.Status)
	failedReservation, err := model.GetBillingReservation(fixtures[0].requestID)
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusRefunded, failedReservation.Status)
	succeededReservation, err := model.GetBillingReservation(fixtures[1].requestID)
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusSettled, succeededReservation.Status)
}

func TestMidjourneyPollDefersMissingUpstreamIdWithoutRefund(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)

	user := &model.User{Id: 981001, Username: "mj-poll-user", Quota: 100, Status: common.UserStatusEnabled}
	token := &model.Token{Id: 982001, UserId: user.Id, Key: "mj-poll-token", Name: "mj-poll-token", Status: common.TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(token).Error)
	_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     "mj-poll-missing-id",
		UserId:        user.Id,
		TokenId:       token.Id,
		FundingSource: model.BillingFundingWallet,
		Quota:         40,
	}, token.Key)
	require.NoError(t, err)
	task := &model.Midjourney{
		UserId:     user.Id,
		Action:     "IMAGINE",
		SubmitTime: time.Now().UnixMilli(),
		ChannelId:  1,
		Quota:      40,
		Progress:   "0%",
		PrivateData: model.TaskPrivateData{
			BillingSource:    model.BillingFundingWallet,
			TokenId:          token.Id,
			BillingRequestId: "mj-poll-missing-id",
		},
	}
	require.NoError(t, model.CreateMidjourneyWithBillingReservation(task, "mj-poll-missing-id", 40, token.Key))

	summary := runMidjourneyTaskUpdateOnce(context.Background(), nil)
	assert.Equal(t, 1, summary.UnfinishedTasks)
	assert.Equal(t, 1, summary.NullTasksDeferred)
	assert.Equal(t, 0, summary.ChannelsScanned)

	var currentTask model.Midjourney
	require.NoError(t, db.Where("id = ?", task.Id).First(&currentTask).Error)
	assert.Empty(t, currentTask.Status)
	assert.Equal(t, "0%", currentTask.Progress)
	reservation, err := model.GetBillingReservation("mj-poll-missing-id")
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusReserved, reservation.Status)
	var currentUser model.User
	var currentToken model.Token
	require.NoError(t, db.Select("quota").Where("id = ?", user.Id).First(&currentUser).Error)
	require.NoError(t, db.Select("remain_quota", "used_quota").Where("id = ?", token.Id).First(&currentToken).Error)
	assert.Equal(t, 60, currentUser.Quota)
	assert.Equal(t, 60, currentToken.RemainQuota)
	assert.Equal(t, 40, currentToken.UsedQuota)
}

func TestMidjourneyProviderResultFinalizesEveryLocalRowSharingUpstreamId(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	user := &model.User{Id: 981002, Username: "mj-duplicate-user", Quota: 100, Status: common.UserStatusEnabled}
	token := &model.Token{Id: 982002, UserId: user.Id, Key: "mj-duplicate-token", Name: "mj-duplicate-token", Status: common.TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(token).Error)

	tasks := make([]*model.Midjourney, 0, 2)
	for _, requestId := range []string{"mj-duplicate-a", "mj-duplicate-b"} {
		_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
			RequestId:     requestId,
			UserId:        user.Id,
			TokenId:       token.Id,
			FundingSource: model.BillingFundingWallet,
			Quota:         20,
		}, token.Key)
		require.NoError(t, err)
		task := &model.Midjourney{
			UserId:     user.Id,
			MjId:       "shared-upstream-id",
			Action:     "IMAGINE",
			SubmitTime: time.Now().UnixMilli(),
			ChannelId:  1,
			Quota:      20,
			Progress:   "20%",
			PrivateData: model.TaskPrivateData{
				BillingSource:    model.BillingFundingWallet,
				TokenId:          token.Id,
				BillingRequestId: requestId,
			},
		}
		require.NoError(t, model.CreateMidjourneyWithBillingReservation(task, requestId, 20, token.Key))
		tasks = append(tasks, task)
	}

	providerFailure := dto.MidjourneyDto{
		MjId:       "shared-upstream-id",
		Status:     model.MidjourneyStatusFailure,
		Progress:   "100%",
		FailReason: "provider rejected task",
	}
	for _, task := range tasks {
		updateMidjourneyTaskFromProvider(context.Background(), task, providerFailure)
		updateMidjourneyTaskFromProvider(context.Background(), task, providerFailure)
	}

	var currentUser model.User
	var currentToken model.Token
	require.NoError(t, db.Select("quota").Where("id = ?", user.Id).First(&currentUser).Error)
	require.NoError(t, db.Select("remain_quota", "used_quota").Where("id = ?", token.Id).First(&currentToken).Error)
	assert.Equal(t, 100, currentUser.Quota)
	assert.Equal(t, 100, currentToken.RemainQuota)
	assert.Equal(t, 0, currentToken.UsedQuota)
	for _, requestId := range []string{"mj-duplicate-a", "mj-duplicate-b"} {
		reservation, err := model.GetBillingReservation(requestId)
		require.NoError(t, err)
		assert.Equal(t, model.BillingReservationStatusRefunded, reservation.Status)
		var refunds int64
		require.NoError(t, db.Model(&model.QuotaLedgerEntry{}).
			Where("request_id = ? AND phase = ?", requestId, model.QuotaLedgerPhaseRefund).
			Count(&refunds).Error)
		assert.EqualValues(t, 1, refunds)
	}
}
