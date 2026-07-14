package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatefulGuardTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/channel-stateful.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &Ability{}, &Task{}, &Midjourney{}, &AsyncBillingReservation{}, &AsyncBillingAttempt{},
		&TaskBillingOperation{}, &MidjourneyBillingOperation{}, &BillingStatsProjection{},
		&RoutingCredentialRef{}, &RoutingChannelHealthState{},
	))
	previousDB := DB
	previousType := common.MainDatabaseType()
	DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.LogDatabaseType())
	t.Cleanup(func() {
		DB = previousDB
		common.SetDatabaseTypes(previousType, common.LogDatabaseType())
	})
	return db
}

func TestChannelDeletePathsRejectStatefulTaskReferences(t *testing.T) {
	db := setupChannelStatefulGuardTest(t)
	channel := Channel{
		Id: 901, Name: "stateful-delete", Key: "key-a", Status: common.ChannelStatusManuallyDisabled,
		Models: "video-model", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Task{
		TaskID: "task_channel_delete", UserId: 1, ChannelId: channel.Id, Status: TaskStatusInProgress,
		PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingLegacyProtocolVersion},
	}).Error)

	assert.ErrorIs(t, (&Channel{Id: channel.Id}).Delete(), ErrChannelHasStatefulReferences)
	assert.ErrorIs(t, BatchDeleteChannels([]int{channel.Id}), ErrChannelHasStatefulReferences)
	_, err := DeleteDisabledChannel()
	assert.ErrorIs(t, err, ErrChannelHasStatefulReferences)
	require.NoError(t, db.First(&Channel{}, channel.Id).Error)
}

func TestHistoricalTerminalTasksWithClosedBillingDoNotLockChannel(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, *gorm.DB, *Channel)
	}{
		{name: "protocol zero terminal", create: func(t *testing.T, db *gorm.DB, channel *Channel) {
			require.NoError(t, db.Create(&Task{
				TaskID: "historical-protocol-zero", UserId: 1, ChannelId: channel.Id,
				Status: TaskStatusSuccess, Progress: "100%",
			}).Error)
		}},
		{name: "durable terminal with closed operation", create: func(t *testing.T, db *gorm.DB, channel *Channel) {
			task := Task{
				TaskID: "historical-closed-operation", UserId: 1, ChannelId: channel.Id,
				Status: TaskStatusSuccess, Progress: "100%",
				PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingLegacyProtocolVersion},
			}
			require.NoError(t, db.Create(&task).Error)
			require.NoError(t, db.Create(&TaskBillingOperation{
				TaskID: task.ID, OperationKey: "task:historical:closed", TerminalStatus: TaskStatusSuccess,
				Kind: TaskBillingOperationKindNoop, State: TaskBillingOperationStateCompleted,
				UserID: 1, ChannelID: channel.Id, BillingSource: TaskBillingSourceWallet,
				LogState: TaskBillingOperationLogNotRequired,
			}).Error)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupChannelStatefulGuardTest(t)
			channel := Channel{
				Id: 905, Name: "historical-terminal", Key: "key-a", Status: common.ChannelStatusManuallyDisabled,
				Models: "video-model", Group: "default",
			}
			require.NoError(t, db.Create(&channel).Error)
			test.create(t, db, &channel)
			require.NoError(t, (&Channel{Id: channel.Id}).Delete())
			assert.ErrorIs(t, db.First(&Channel{}, channel.Id).Error, gorm.ErrRecordNotFound)
		})
	}
}

func TestAcceptedAttemptWithClosedTerminalEvidenceDoesNotLockChannel(t *testing.T) {
	db := setupChannelStatefulGuardTest(t)
	channel := Channel{
		Id: 906, Name: "accepted-attempt-closed", Key: "key-a", Status: common.ChannelStatusManuallyDisabled,
		Models: "video-model", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	task := Task{
		TaskID: "accepted-attempt-terminal", UserId: 1, ChannelId: channel.Id,
		Status: TaskStatusSuccess, Progress: "100%",
		PrivateData: TaskPrivateData{
			BillingProtocolVersion: TaskBillingProtocolVersion, AsyncBillingReservationID: 906,
		},
	}
	require.NoError(t, task.IsolateV2BillingFromLegacyPollers(0))
	require.NoError(t, db.Create(&task).Error)
	key := "async:906:accepted:v1"
	reservation := AsyncBillingReservation{
		ID:             906,
		ReservationKey: "accepted-attempt-closed", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_accepted_attempt_closed",
		State: AsyncBillingReservationStateTerminal, UserID: 1, FundingSource: TaskBillingSourceWallet,
		TaskID: task.ID, AcceptedProjectionKey: &key,
		AcceptedProjectionState:     AsyncBillingAcceptedProjectionCompleted,
		AcceptedProjectionChannelID: channel.Id,
	}
	require.NoError(t, db.Create(&reservation).Error)
	require.NoError(t, db.Create(&AsyncBillingAttempt{
		ReservationID: reservation.ID, AttemptIndex: 0, State: AsyncBillingAttemptStateAccepted,
		ChannelID: channel.Id, ChannelVersion: channel.RoutingGeneration,
	}).Error)
	require.NoError(t, db.Create(&TaskBillingOperation{
		TaskID: task.ID, ReservationID: reservation.ID, OperationKey: "task:accepted-attempt:closed",
		TerminalStatus: TaskStatusSuccess, Kind: TaskBillingOperationKindNoop,
		State: TaskBillingOperationStateCompleted, UserID: 1, ChannelID: channel.Id,
		BillingSource: TaskBillingSourceWallet, LogState: TaskBillingOperationLogNotRequired,
	}).Error)

	require.NoError(t, (&Channel{Id: channel.Id}).Delete())
	assert.ErrorIs(t, db.First(&Channel{}, channel.Id).Error, gorm.ErrRecordNotFound)
}

func TestChannelIdentityUpdateAllowsKeyReorderAndAppendButBlocksRemovalOrEndpointChange(t *testing.T) {
	db := setupChannelStatefulGuardTest(t)
	baseURL := "https://provider-a.example"
	channel := Channel{
		Id: 902, Name: "stateful-update", Key: "key-a\nkey-b", Status: common.ChannelStatusEnabled,
		Models: "video-model", Group: "default", BaseURL: &baseURL,
		ChannelInfo: ChannelInfo{IsMultiKey: true},
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Midjourney{
		MjId: "task_channel_update", UserId: 1, ChannelId: channel.Id, Status: "IN_PROGRESS",
		BillingProtocolVersion: TaskBillingLegacyProtocolVersion,
	}).Error)
	originalGeneration := channel.RoutingGeneration

	channel.Key = "key-b\nkey-a\nkey-c"
	require.NoError(t, channel.Update())
	assert.Equal(t, originalGeneration, channel.RoutingGeneration)

	channel.Key = "key-b\nkey-c"
	assert.ErrorIs(t, channel.Update(), ErrChannelHasStatefulReferences)
	providerB := "https://provider-b.example"
	channel.Key = ""
	channel.BaseURL = &providerB
	assert.ErrorIs(t, channel.Update(), ErrChannelHasStatefulReferences)

	var persisted Channel
	require.NoError(t, db.First(&persisted, channel.Id).Error)
	assert.Equal(t, "key-b\nkey-a\nkey-c", persisted.Key)
	assert.Equal(t, baseURL, persisted.GetBaseURL())
	assert.Equal(t, originalGeneration, persisted.RoutingGeneration)
}

func TestChannelIdentityUpdateAdvancesGenerationWithoutStatefulReferences(t *testing.T) {
	db := setupChannelStatefulGuardTest(t)
	baseURL := "https://provider-a.example"
	channel := Channel{
		Id: 903, Name: "stateless-update", Key: "key-a", Status: common.ChannelStatusEnabled,
		Models: "video-model", Group: "default", BaseURL: &baseURL,
	}
	require.NoError(t, db.Create(&channel).Error)
	originalGeneration := channel.RoutingGeneration
	providerB := "https://provider-b.example"
	channel.BaseURL = &providerB
	require.NoError(t, channel.Update())
	assert.NotEqual(t, originalGeneration, channel.RoutingGeneration)
	assert.Equal(t, providerB, channel.GetBaseURL())
}

func TestSingleChannelCredentialContinuityRotationPreservesReferencedCredentialID(t *testing.T) {
	db := setupChannelStatefulGuardTest(t)
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stateful-channel-continuity-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	channel := Channel{
		Id: 904, Name: "continuity-update", Key: "oauth-old", Status: common.ChannelStatusEnabled,
		Models: "video-model", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	oldFingerprint, err := RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, channel.Key)
	require.NoError(t, err)
	credential := RoutingCredentialRef{
		ID: 9041, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: oldFingerprint, FingerprintVersion: RoutingCredentialFingerprintVersion,
		Active: true, CurrentOccurrences: 1,
	}
	require.NoError(t, db.Create(&credential).Error)
	require.NoError(t, db.Create(&Task{
		TaskID: "task_credential_continuity", UserId: 1, ChannelId: channel.Id, Status: TaskStatusInProgress,
		PrivateData: TaskPrivateData{
			BillingProtocolVersion: TaskBillingLegacyProtocolVersion, RoutingCredentialID: credential.ID,
		},
	}).Error)

	rotated, err := RotateSingleChannelCredentialContinuity(
		context.Background(), channel.Id, channel.RoutingGeneration, channel.Key, "oauth-new",
	)
	require.NoError(t, err)
	assert.Equal(t, channel.RoutingGeneration, rotated.RoutingGeneration)
	assert.Equal(t, "oauth-new", rotated.Key)
	require.NoError(t, db.First(&credential, credential.ID).Error)
	newFingerprint, err := RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "oauth-new")
	require.NoError(t, err)
	assert.Equal(t, newFingerprint, credential.Fingerprint)
	assert.True(t, credential.Active)
	assert.Equal(t, 1, credential.CurrentOccurrences)
}
