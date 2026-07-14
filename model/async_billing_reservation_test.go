package model

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAsyncBillingReservationTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/async-billing.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&User{}, &Token{}, &Channel{}, &AsyncBillingReservation{}, &AsyncBillingAttempt{}, &AsyncBillingManualResolution{},
		&TaskBillingOperation{}, &MidjourneyBillingOperation{}, &BillingStatsProjection{},
		&IdentityCacheSync{},
		&BillingLogProjection{}, &Task{}, &Midjourney{}, &Log{}, &QuotaData{},
		&UserSubscription{}, &SubscriptionBillingPeriod{},
	))
	previousDB := DB
	previousLogDB := LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	DB = db
	LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.RedisEnabled = previousRedisEnabled
	})
	return db
}

type legacyMidjourneySchemaForMigration struct {
	ID       int    `gorm:"column:id;primaryKey"`
	UserID   int    `gorm:"column:user_id"`
	MjID     string `gorm:"column:mj_id"`
	Status   string `gorm:"column:status"`
	Progress string `gorm:"column:progress"`
}

func (legacyMidjourneySchemaForMigration) TableName() string {
	return "midjourneys"
}

func asyncBillingFinalAudit(quotaRatio float64) AsyncBillingAcceptedAuditSnapshot {
	return AsyncBillingAcceptedAuditSnapshot{
		RequestID: "request-review-1", RequestPath: "/v1/videos", Action: "submit",
		OriginModelName: "suno-review", Group: "default", Content: "submit async task",
		OtherRatios: map[string]float64{"quality": quotaRatio},
	}
}

func createAuthorizedAsyncBillingReservation(
	t *testing.T,
	db *gorm.DB,
	authorizedAt time.Time,
	deadline time.Time,
) (*AsyncBillingReservation, *AsyncBillingAttempt) {
	t.Helper()
	const userID = 88101
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "async-review-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "sk-async-review", Name: "async-review",
		Status: common.TokenStatusEnabled, RemainQuota: 1000,
	}).Error)
	channel := Channel{
		Id: 77, Name: "async-review-channel", Key: "sk-async-review-channel",
		Status: common.ChannelStatusEnabled, Models: "suno-review", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	reservation, created, err := CreateAsyncBillingReservation(context.Background(), AsyncBillingReservationSpec{
		ReservationKey: "reservation-review-1", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_review_1", UserID: userID, TokenID: userID,
		BillingPreference: "wallet_only", Quota: 100,
	}, authorizedAt.Add(-time.Second))
	require.NoError(t, err)
	require.True(t, created)
	attempt, err := AuthorizeAsyncBillingAttempt(context.Background(), reservation.ID, AsyncBillingAttemptSpec{
		AttemptIndex: 0, ChannelID: 77, CredentialID: 9, ChannelVersion: channel.RoutingGeneration,
		SendDeadlineMs: deadline.UnixMilli(),
		AcceptanceIntent: &AsyncBillingAcceptanceIntentSpec{
			Task: &Task{
				Platform: constant.TaskPlatformSuno, Action: "submit", Status: TaskStatusNotStart,
			},
			Audit: AsyncBillingAcceptedAuditSnapshot{
				RequestID: "request-review-1", RequestPath: "/v1/videos", Action: "submit",
				OriginModelName: "suno-review", Group: "default", Content: "submit async task",
			},
		},
	}, authorizedAt)
	require.NoError(t, err)
	return reservation, attempt
}

func TestAcceptedProjectionCancellationBeforeCommitDoesNotScheduleRetry(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88100
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "projection-cancel-user", AffCode: "projection-cancel-user",
		Status: common.UserStatusEnabled, Quota: 900,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "sk-projection-cancel", Name: "projection-cancel",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100, ExpiredTime: -1,
	}).Error)
	now := time.Now()
	projectionKey := strings.Repeat("a", 64)
	reservation := AsyncBillingReservation{
		ReservationKey: "projection-cancel", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "projection_cancel",
		State: AsyncBillingReservationStateAccepted, UserID: userID, TokenID: userID,
		FundingSource: TaskBillingSourceWallet, InitialQuota: 100, CurrentQuota: 100, AcceptedQuota: 100,
		AcceptedProjectionKey: &projectionKey, AcceptedProjectionState: AsyncBillingAcceptedProjectionPending,
		AcceptedProjectionAttempts: 4, AcceptedProjectionNextRetryMs: 0,
		CacheSyncVersion: 7, CacheSyncedVersion: 6, CacheSyncPending: true,
		CacheSyncAttempts: 2, CacheSyncNextRetryMs: now.Add(time.Minute).UnixMilli(),
		CacheSyncLastError: "retained", CreatedTimeMs: now.Add(-time.Minute).UnixMilli(),
		UpdatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}
	require.NoError(t, db.Create(&reservation).Error)
	attempt := AsyncBillingAttempt{
		ReservationID: reservation.ID, AttemptIndex: 0, State: AsyncBillingAttemptStateAccepted,
		ChannelID: 77, AuthorizedMs: now.Add(-time.Minute).UnixMilli(), FailureCode: "retained",
	}
	require.NoError(t, db.Create(&attempt).Error)

	queryStarted := make(chan struct{})
	releaseQuery := make(chan struct{})
	var blockOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseQuery) }) }
	t.Cleanup(release)
	const callbackName = "test:block_accepted_projection_before_commit"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "async_billing_reservations" {
			return
		}
		blockOnce.Do(func() {
			close(queryStarted)
			select {
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			case <-releaseQuery:
				tx.AddError(context.Canceled)
			}
		})
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- ProcessAsyncBillingAcceptedProjection(ctx, reservation.ID, now)
	}()
	select {
	case <-queryStarted:
	case <-time.After(time.Second):
		t.Fatal("accepted projection did not reach the blocked transaction query")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("accepted projection did not stop after context cancellation")
	}
	release()
	require.NoError(t, db.Callback().Query().Remove(callbackName))

	var persisted AsyncBillingReservation
	require.NoError(t, db.First(&persisted, reservation.ID).Error)
	assert.Equal(t, AsyncBillingReservationStateAccepted, persisted.State)
	assert.Equal(t, 100, persisted.CurrentQuota)
	assert.Equal(t, AsyncBillingAcceptedProjectionPending, persisted.AcceptedProjectionState)
	assert.Equal(t, 4, persisted.AcceptedProjectionAttempts)
	assert.Zero(t, persisted.AcceptedProjectionNextRetryMs)
	assert.Equal(t, int64(7), persisted.CacheSyncVersion)
	assert.Equal(t, int64(6), persisted.CacheSyncedVersion)
	assert.True(t, persisted.CacheSyncPending)
	assert.Equal(t, 2, persisted.CacheSyncAttempts)
	assert.Equal(t, reservation.CacheSyncNextRetryMs, persisted.CacheSyncNextRetryMs)
	assert.Equal(t, "retained", persisted.CacheSyncLastError)
	var persistedAttempt AsyncBillingAttempt
	require.NoError(t, db.First(&persistedAttempt, attempt.ID).Error)
	assert.Equal(t, AsyncBillingAttemptStateAccepted, persistedAttempt.State)
	assert.Equal(t, "retained", persistedAttempt.FailureCode)
	var persistedUser User
	require.NoError(t, db.First(&persistedUser, userID).Error)
	assert.Equal(t, 900, persistedUser.Quota)
	var persistedToken Token
	require.NoError(t, db.First(&persistedToken, userID).Error)
	assert.Equal(t, 900, persistedToken.RemainQuota)
	assert.Equal(t, 100, persistedToken.UsedQuota)
}

func TestAsyncBillingManualRejectWaitsForSendDeadlineAndFreshEvidence(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	authorizedAt := time.Now()
	deadline := authorizedAt.Add(time.Minute)
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, authorizedAt, deadline)
	manualAt := authorizedAt.Add(10 * time.Second)
	review, err := MarkAsyncBillingReservationManualReview(
		context.Background(), reservation.ID, "ambiguous response", "", manualAt,
	)
	require.NoError(t, err)

	page, err := ListAsyncBillingManualReviewPage(context.Background(), 0, 10, true)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.False(t, page.Items[0].CanReject)
	assert.Contains(t, page.Items[0].Blockers, "submission_send_lease_active")

	decision := AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionRejected, ActorUserID: 9001,
		ExpectedVersion: review.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, review.ReviewVersion),
		UpstreamTaskID:  "provider-task-review-1", ProviderStatus: AsyncBillingProviderStatusNotFound,
		ProviderCheckedMs: manualAt.Add(time.Second).UnixMilli(), EvidenceReference: "provider-audit-1",
		Reason: "provider confirms no accepted task", DecisionKeyHash: strings.Repeat("a", 64),
		DecisionPayloadHash: strings.Repeat("b", 64),
	}
	_, err = ResolveAsyncBillingManualReview(context.Background(), decision, manualAt.Add(2*time.Second))
	assert.ErrorIs(t, err, ErrAsyncBillingManualDecisionBlocked)

	_, err = ResolveAsyncBillingManualReview(context.Background(), decision, deadline.Add(time.Second))
	assert.ErrorIs(t, err, ErrAsyncBillingManualDecisionPrecondition)

	decision.ProviderCheckedMs = deadline.Add(time.Second).UnixMilli()
	decision.DecisionPayloadHash = strings.Repeat("c", 64)
	result, err := ResolveAsyncBillingManualReview(context.Background(), decision, deadline.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateReleased, result.Reservation.State)
	assert.Zero(t, result.Reservation.CurrentQuota)

	var user User
	var token Token
	require.NoError(t, db.Unscoped().First(&user, reservation.UserID).Error)
	require.NoError(t, db.Unscoped().First(&token, reservation.TokenID).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestAsyncBillingManualRejectDoesNotDependOnAcceptanceIntent(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	authorizedAt := time.Now().Add(-2 * time.Minute)
	deadline := authorizedAt.Add(time.Minute)
	reservation, attempt := createAuthorizedAsyncBillingReservation(t, db, authorizedAt, deadline)
	manualAt := deadline.Add(time.Second)
	review, err := MarkAsyncBillingReservationManualReview(
		context.Background(), reservation.ID, "ambiguous response", "", manualAt,
	)
	require.NoError(t, err)
	require.NoError(t, db.Model(&AsyncBillingAttempt{}).Where("id = ?", attempt.ID).Updates(map[string]any{
		"intent_payload": []byte("corrupt"), "intent_payload_hash": strings.Repeat("0", 64),
	}).Error)

	page, err := ListAsyncBillingManualReviewPage(context.Background(), 0, 10, true)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.False(t, page.Items[0].CanAccept)
	assert.True(t, page.Items[0].CanReject)
	assert.Contains(t, page.Items[0].Blockers, "acceptance_intent_missing_or_invalid")

	result, err := ResolveAsyncBillingManualReview(context.Background(), AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionRejected, ActorUserID: 9001,
		ExpectedVersion: review.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, review.ReviewVersion),
		UpstreamTaskID:  "", ProviderStatus: AsyncBillingProviderStatusRejected,
		ProviderCheckedMs: manualAt.Add(time.Second).UnixMilli(), EvidenceReference: "provider-audit-2",
		Reason: "provider confirms rejection", DecisionKeyHash: strings.Repeat("d", 64),
		DecisionPayloadHash: strings.Repeat("e", 64),
	}, manualAt.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateReleased, result.Reservation.State)
}

func TestAsyncBillingManualAcceptRequiresUpstreamTaskID(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	authorizedAt := time.Now().Add(-2 * time.Minute)
	deadline := authorizedAt.Add(time.Minute)
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, authorizedAt, deadline)
	manualAt := deadline.Add(time.Second)
	review, err := MarkAsyncBillingReservationManualReview(
		context.Background(), reservation.ID, "ambiguous response", "", manualAt,
	)
	require.NoError(t, err)

	_, err = ResolveAsyncBillingManualReview(context.Background(), AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionAccepted, ActorUserID: 9001,
		ExpectedVersion: review.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, review.ReviewVersion),
		ProviderStatus:  AsyncBillingProviderStatusAccepted, ProviderCheckedMs: manualAt.Add(time.Second).UnixMilli(),
		EvidenceReference: "provider-audit-without-id", Reason: "provider confirms acceptance",
		DecisionKeyHash: strings.Repeat("f", 64), DecisionPayloadHash: strings.Repeat("0", 64),
	}, manualAt.Add(2*time.Second))
	assert.ErrorIs(t, err, ErrAsyncBillingManualDecisionInvalid)
}

func TestAsyncBillingManualResolutionIsIdempotentPerReviewVersion(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, now, now.Add(time.Minute))
	audit := asyncBillingFinalAudit(1)
	replay := AsyncBillingReplaySpec{
		StatusCode: 200, ContentType: "application/json", Body: []byte(`{"code":"success"}`),
	}
	handoff, err := MarkAsyncBillingAcceptedHandoffManualReview(
		context.Background(), reservation.ID, reservation.CurrentQuota, audit,
		"provider-multistage-1", replay, "persist accepted task failed", now.Add(time.Second),
	)
	require.NoError(t, err)
	handoffDecision := AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionAccepted, ActorUserID: 9001,
		ExpectedVersion: handoff.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, handoff.ReviewVersion),
		UpstreamTaskID:  "provider-multistage-1", ProviderStatus: AsyncBillingProviderStatusAccepted,
		ProviderCheckedMs: now.Add(2 * time.Second).UnixMilli(), EvidenceReference: "handoff-evidence",
		Reason: "confirm accepted handoff", DecisionKeyHash: strings.Repeat("3", 64),
		DecisionPayloadHash: strings.Repeat("4", 64),
	}
	accepted, err := ResolveAsyncBillingManualReview(context.Background(), handoffDecision, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Equal(t, AsyncBillingReservationStateAccepted, accepted.Reservation.State)

	require.NoError(t, db.Create(&TaskBillingOperation{
		TaskID: accepted.Reservation.TaskID, ReservationID: reservation.ID,
		OperationKey: "terminal-overage-multistage", TerminalStatus: TaskStatusSuccess,
		Kind: TaskBillingOperationKindSettle, State: TaskBillingOperationStateManualReview,
		UserID: reservation.UserID, ChannelID: 77, BillingSource: TaskBillingSourceWallet,
		TokenID: reservation.TokenID, PreConsumedQuota: reservation.CurrentQuota,
		TargetQuota: 150, QuotaDelta: 50, CreatedTimeMs: now.Add(4 * time.Second).UnixMilli(),
		UpdatedTimeMs: now.Add(4 * time.Second).UnixMilli(), LogState: TaskBillingOperationLogNotRequired,
	}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		err := CompleteAsyncBillingReservationTx(
			tx, reservation.ID, reservation.UserID, reservation.CurrentQuota, 150, now.Add(4*time.Second),
		)
		require.ErrorIs(t, err, ErrAsyncBillingTerminalOverage)
		return nil
	}))
	var terminalReview AsyncBillingReservation
	require.NoError(t, db.First(&terminalReview, reservation.ID).Error)
	require.Equal(t, AsyncBillingReviewKindTerminalOverage, terminalReview.ManualReviewKind)
	require.Greater(t, terminalReview.ReviewVersion, handoff.ReviewVersion)

	terminalDecision := AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionRejected, ActorUserID: 9001,
		ExpectedVersion: terminalReview.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, terminalReview.ReviewVersion),
		UpstreamTaskID:  "provider-multistage-1", ProviderStatus: AsyncBillingProviderStatusTerminalVerified,
		ProviderCheckedMs: now.Add(5 * time.Second).UnixMilli(), EvidenceReference: "terminal-evidence",
		Reason: "write off terminal overage", DecisionKeyHash: handoffDecision.DecisionKeyHash,
		DecisionPayloadHash: strings.Repeat("6", 64),
	}
	_, err = ResolveAsyncBillingManualReview(context.Background(), terminalDecision, now.Add(6*time.Second))
	assert.ErrorIs(t, err, ErrAsyncBillingIdempotencyConflict)
	terminalDecision.DecisionKeyHash = strings.Repeat("5", 64)
	terminal, err := ResolveAsyncBillingManualReview(context.Background(), terminalDecision, now.Add(6*time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateTerminal, terminal.Reservation.State)
	var writtenOffOperation TaskBillingOperation
	require.NoError(t, db.Where("reservation_id = ?", reservation.ID).First(&writtenOffOperation).Error)
	assert.Equal(t, TaskBillingOperationKindNoop, writtenOffOperation.Kind)
	assert.Equal(t, reservation.CurrentQuota, writtenOffOperation.TargetQuota)
	assert.Zero(t, writtenOffOperation.QuotaDelta)
	assert.Equal(t, TaskBillingOperationLogNotRequired, writtenOffOperation.LogState)

	var receipts []AsyncBillingManualResolution
	require.NoError(t, db.Where("reservation_id = ?", reservation.ID).
		Order("expected_version asc").Find(&receipts).Error)
	require.Len(t, receipts, 2)
	assert.Equal(t, []int64{handoff.ReviewVersion, terminalReview.ReviewVersion},
		[]int64{receipts[0].ExpectedVersion, receipts[1].ExpectedVersion})

	retry, err := ResolveAsyncBillingManualReview(context.Background(), handoffDecision, now.Add(7*time.Second))
	require.NoError(t, err)
	assert.Equal(t, handoff.ReviewVersion, retry.Resolution.ExpectedVersion)
}

func TestTerminalUsageManualReviewPreservesChargeAndResolvesIdempotently(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	const userID = 88171
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "terminal-usage-review", AffCode: "terminal-usage-review",
		Status: common.UserStatusEnabled, Quota: 900,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "sk-terminal-usage-review", Name: "terminal-usage-review",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100, ExpiredTime: -1,
	}).Error)
	reservation := AsyncBillingReservation{
		ID: 88171, ReservationKey: "terminal-usage-review", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_terminal_usage_review",
		State: AsyncBillingReservationStateAccepted, UserID: userID, TokenID: userID,
		FundingSource: TaskBillingSourceWallet, InitialQuota: 100, CurrentQuota: 100, AcceptedQuota: 100,
		UpstreamTaskID: "provider-terminal-usage-review",
		CreatedTimeMs:  now.Add(-time.Minute).UnixMilli(), UpdatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}
	require.NoError(t, db.Create(&reservation).Error)
	task := Task{
		TaskID: reservation.PublicTaskID, Platform: constant.TaskPlatformSuno,
		UserId: userID, ChannelId: 77, Status: TaskStatusSuccess, Progress: "100%",
		SubmitTime: now.Add(-time.Minute).Unix(), FinishTime: now.Unix(), Data: json.RawMessage(`{"usage":"unknown"}`),
		PrivateData: TaskPrivateData{
			BillingProtocolVersion:    TaskBillingProtocolVersion,
			AsyncBillingReservationID: reservation.ID, BillingSource: TaskBillingSourceWallet, TokenId: userID,
		},
	}
	require.NoError(t, task.IsolateV2BillingFromLegacyPollers(100))
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", reservation.ID).
		Update("task_id", task.ID).Error)

	review, err := MarkAsyncBillingTerminalUsageManualReview(
		context.Background(), reservation.ID, "terminal usage cannot be verified", now.Add(time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateManualReview, review.State)
	assert.Equal(t, AsyncBillingReviewKindTerminalUsage, review.ManualReviewKind)
	assert.Equal(t, 100, review.CurrentQuota)
	replayedReview, err := MarkAsyncBillingTerminalUsageManualReview(
		context.Background(), reservation.ID, "terminal usage cannot be verified", now.Add(2*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, review.ReviewVersion, replayedReview.ReviewVersion)

	page, err := ListAsyncBillingManualReviewPage(context.Background(), 0, 10, true)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].CanAccept)
	assert.False(t, page.Items[0].CanReject)

	decision := AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionAccepted, ActorUserID: 9001,
		ExpectedVersion: review.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, review.ReviewVersion),
		UpstreamTaskID:  reservation.UpstreamTaskID, ProviderStatus: AsyncBillingProviderStatusTerminalVerified,
		ProviderCheckedMs: now.Add(3 * time.Second).UnixMilli(), EvidenceReference: "terminal-usage-audit",
		Reason:          "preserve reserved charge after terminal verification",
		DecisionKeyHash: strings.Repeat("7", 64), DecisionPayloadHash: strings.Repeat("8", 64),
	}
	resolved, err := ResolveAsyncBillingManualReview(context.Background(), decision, now.Add(4*time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateTerminal, resolved.Reservation.State)
	assert.Equal(t, 100, resolved.Reservation.CurrentQuota)
	var operation TaskBillingOperation
	require.NoError(t, db.Where("reservation_id = ?", reservation.ID).First(&operation).Error)
	assert.Equal(t, TaskBillingOperationStateCompleted, operation.State)
	assert.Equal(t, TaskBillingOperationKindNoop, operation.Kind)
	assert.Equal(t, 100, operation.TargetQuota)
	assert.Equal(t, asyncTerminalPayloadProtocol, operation.TerminalPayloadProtocol)

	retry, err := ResolveAsyncBillingManualReview(context.Background(), decision, now.Add(5*time.Second))
	require.NoError(t, err)
	assert.Equal(t, resolved.Resolution.ID, retry.Resolution.ID)
	var operationCount int64
	var resolutionCount int64
	require.NoError(t, db.Model(&TaskBillingOperation{}).Where("reservation_id = ?", reservation.ID).Count(&operationCount).Error)
	require.NoError(t, db.Model(&AsyncBillingManualResolution{}).Where("reservation_id = ?", reservation.ID).Count(&resolutionCount).Error)
	assert.Equal(t, int64(1), operationCount)
	assert.Equal(t, int64(1), resolutionCount)
	var user User
	var token Token
	require.NoError(t, db.First(&user, userID).Error)
	require.NoError(t, db.First(&token, userID).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
}

func TestAsyncBillingReplayRequiresAcceptedOrTerminalReservation(t *testing.T) {
	normalized, hash, err := normalizeAsyncBillingReplaySpec(AsyncBillingReplaySpec{
		StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`),
	})
	require.NoError(t, err)
	for _, kind := range []string{AsyncBillingKindTask, AsyncBillingKindMidjourney} {
		for _, state := range []string{AsyncBillingReservationStateAccepted, AsyncBillingReservationStateTerminal} {
			t.Run(kind+"_"+state, func(t *testing.T) {
				replay, replayErr := GetAsyncBillingReplayResponse(&AsyncBillingReservation{
					Kind: kind, State: state, ReplayReady: true, ReplayProtocol: AsyncBillingReplayProtocol,
					ReplayStatusCode: normalized.StatusCode, ReplayContentType: normalized.ContentType,
					ReplayHeadersJSON: normalized.HeadersJSON, ReplayBody: normalized.Body, ReplayHash: hash,
				})
				require.NoError(t, replayErr)
				assert.Equal(t, normalized.Body, replay.Body)
			})
		}
		t.Run(kind+"_manual_review", func(t *testing.T) {
			_, replayErr := GetAsyncBillingReplayResponse(&AsyncBillingReservation{
				Kind: kind, State: AsyncBillingReservationStateManualReview,
				ReplayReady: true, ReplayProtocol: AsyncBillingReplayProtocol,
				ReplayStatusCode: normalized.StatusCode, ReplayContentType: normalized.ContentType,
				ReplayHeadersJSON: normalized.HeadersJSON, ReplayBody: normalized.Body, ReplayHash: hash,
			})
			assert.ErrorIs(t, replayErr, ErrAsyncBillingReplayUnavailable)
		})
	}
}

func TestAsyncBillingReplayValidationRejectsUnsafeMetadataAndSurvivesPersistence(t *testing.T) {
	for _, spec := range []AsyncBillingReplaySpec{
		{StatusCode: 199, ContentType: "application/json", Body: []byte(`{}`)},
		{StatusCode: 300, ContentType: "application/json", Body: []byte(`{}`)},
		{StatusCode: 500, ContentType: "application/json", Body: []byte(`{}`)},
		{StatusCode: 200, ContentType: "application/json\r\nX-Test: injected", Body: []byte(`{}`)},
		{StatusCode: 200, ContentType: "not a media type", Body: []byte(`{}`)},
		{StatusCode: 200, ContentType: "application/json", HeadersJSON: `{"Authorization":["secret"]}`, Body: []byte(`{}`)},
		{StatusCode: 200, ContentType: "application/json", HeadersJSON: `{"X-Test":["bad\r\nvalue"]}`, Body: []byte(`{}`)},
	} {
		_, _, err := normalizeAsyncBillingReplaySpec(spec)
		assert.ErrorIs(t, err, ErrAsyncBillingReservationInvariant)
	}
	boundary := bytes.Repeat([]byte{'x'}, 3<<20)
	_, _, err := normalizeAsyncBillingReplaySpec(AsyncBillingReplaySpec{
		StatusCode: http.StatusOK, ContentType: "application/octet-stream", Body: boundary,
	})
	require.NoError(t, err)
	_, _, err = normalizeAsyncBillingReplaySpec(AsyncBillingReplaySpec{
		StatusCode: http.StatusOK, ContentType: "application/octet-stream", Body: append(boundary, 'x'),
	})
	assert.ErrorIs(t, err, ErrAsyncBillingReservationInvariant)

	db := setupAsyncBillingReservationTest(t)
	normalized, hash, err := normalizeAsyncBillingReplaySpec(AsyncBillingReplaySpec{
		StatusCode: 202, ContentType: "application/json; charset=utf-8",
		HeadersJSON: `{"Content-Type":["application/json"],"X-Request-Id":["request-1"]}`,
		Body:        []byte(`{"id":"task_replay_persisted"}`),
	})
	require.NoError(t, err)
	require.NoError(t, db.Create(&AsyncBillingReservation{
		ReservationKey: "replay-persisted", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_replay_persisted", State: AsyncBillingReservationStateAccepted,
		UserID: 1, FundingSource: TaskBillingSourceWallet, ReplayProtocol: AsyncBillingReplayProtocol,
		ReplayReady: true, ReplayStatusCode: normalized.StatusCode, ReplayContentType: normalized.ContentType,
		ReplayHeadersJSON: normalized.HeadersJSON, ReplayBody: normalized.Body, ReplayHash: hash,
		CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
	}).Error)
	var persisted AsyncBillingReservation
	require.NoError(t, db.Where("reservation_key = ?", "replay-persisted").First(&persisted).Error)
	replay, err := GetAsyncBillingReplayResponse(&persisted)
	require.NoError(t, err)
	assert.Equal(t, normalized.HeadersJSON, replay.HeadersJSON)
	assert.NotContains(t, replay.HeadersJSON, "Content-Type")
	assert.Equal(t, normalized.Body, replay.Body)
}

func TestAsyncBillingReplayStripsRepresentationHeadersAndReadsLegacyHash(t *testing.T) {
	raw := AsyncBillingReplaySpec{
		StatusCode: 202, ContentType: "application/json; charset=utf-8",
		HeadersJSON: `{"Content-Encoding":["gzip"],"Content-Length":["999"],"Content-Type":["text/plain"],"X-Trace":["trace-1"]}`,
		Body:        []byte(`{"id":"task_public"}`),
	}
	normalized, _, err := normalizeAsyncBillingReplaySpec(raw)
	require.NoError(t, err)
	assert.JSONEq(t, `{"X-Trace":["trace-1"]}`, normalized.HeadersJSON)

	replay, err := GetAsyncBillingReplayResponse(&AsyncBillingReservation{
		State: AsyncBillingReservationStateAccepted, ReplayReady: true,
		ReplayProtocol: AsyncBillingReplayProtocol, ReplayStatusCode: raw.StatusCode,
		ReplayContentType: raw.ContentType, ReplayHeadersJSON: raw.HeadersJSON,
		ReplayBody: raw.Body, ReplayHash: legacyAsyncBillingReplayHash(raw),
	})
	require.NoError(t, err)
	assert.Equal(t, normalized.HeadersJSON, replay.HeadersJSON)
	assert.Equal(t, raw.Body, replay.Body)
}

func TestAcceptedHandoffFallbackFreezesAttemptAuditQuotaIdentityAndReplay(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, now, now.Add(time.Minute))
	replay := AsyncBillingReplaySpec{
		StatusCode: 202, ContentType: "application/json", Body: []byte(`{"id":"task_review_1"}`),
	}
	review, err := MarkAsyncBillingAcceptedHandoffManualReviewFromAttempt(
		context.Background(), reservation.ID, 125, "provider-fallback-1", replay,
		"final audit construction failed", now.Add(time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateManualReview, review.State)
	assert.Equal(t, AsyncBillingReviewKindAcceptedHandoff, review.ManualReviewKind)
	assert.Equal(t, 125, review.ReviewTargetQuota)
	assert.Equal(t, "provider-fallback-1", review.UpstreamTaskID)
	assert.True(t, review.ReplayReady)
	assert.Equal(t, replay.Body, review.ReplayBody)
	audit, err := thawAsyncBillingReviewAudit(review)
	require.NoError(t, err)
	assert.Equal(t, "suno-review", audit.OriginModelName)
	assert.Equal(t, "post_submit_final_fallback", audit.AdminInfo["billing_context_phase"])
}

func TestAcceptedHandoffCapabilityFailsClosedForOutOfRangeTargetQuota(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, now, now.Add(time.Minute))
	review, err := MarkAsyncBillingAcceptedHandoffManualReview(
		context.Background(), reservation.ID, reservation.CurrentQuota, asyncBillingFinalAudit(1),
		"provider-corrupt-target", AsyncBillingReplaySpec{
			StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`),
		}, "persist failed", now.Add(time.Second),
	)
	require.NoError(t, err)
	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", reservation.ID).
		UpdateColumn("review_target_quota", common.MaxQuota+1).Error)

	page, err := ListAsyncBillingManualReviewPage(context.Background(), 0, 10, true)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.False(t, page.Items[0].CanAccept)
	assert.Contains(t, page.Items[0].Blockers, "accepted_handoff_context_missing_or_invalid")

	_, err = ResolveAsyncBillingManualReview(context.Background(), AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionAccepted, ActorUserID: 9001,
		ExpectedVersion: review.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, review.ReviewVersion),
		UpstreamTaskID:  "provider-corrupt-target", ProviderStatus: AsyncBillingProviderStatusAccepted,
		ProviderCheckedMs: now.Add(2 * time.Second).UnixMilli(), EvidenceReference: "corrupt-target-evidence",
		Reason: "confirm handoff", DecisionKeyHash: strings.Repeat("7", 64),
		DecisionPayloadHash: strings.Repeat("8", 64),
	}, now.Add(3*time.Second))
	assert.ErrorIs(t, err, ErrAsyncBillingManualDecisionPrecondition)
}

func TestFindAcceptedTerminalDriftsSkipsCorruptHeadReservation(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	require.NoError(t, db.Create(&AsyncBillingReservation{
		ID: 9201, ReservationKey: "drift-missing-head", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "drift-missing-head", State: AsyncBillingReservationStateAccepted,
		UserID: 9201, FundingSource: TaskBillingSourceWallet, TaskID: 999999,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}).Error)
	task := &Task{
		TaskID: "drift-valid-tail", UserId: 9202, Status: TaskStatusSuccess, Progress: "100%",
		PrivateData: TaskPrivateData{
			BillingProtocolVersion: TaskBillingProtocolVersion, AsyncBillingReservationID: 9202,
		},
	}
	require.NoError(t, task.IsolateV2BillingFromLegacyPollers(10))
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.Create(&AsyncBillingReservation{
		ID: 9202, ReservationKey: "drift-valid-tail", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "drift-valid-tail", State: AsyncBillingReservationStateAccepted,
		UserID: 9202, FundingSource: TaskBillingSourceWallet, TaskID: task.ID, CurrentQuota: 10, AcceptedQuota: 10,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}).Error)

	drifts, err := FindAcceptedAsyncBillingTerminalDrifts(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, drifts, 1)
	assert.Equal(t, int64(9202), drifts[0].Reservation.ID)
}

func TestCleanupExpiredAsyncBillingReceiptsRotatesPastProtectedHead(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	expired := time.Now().Add(-31 * 24 * time.Hour)
	for _, reservation := range []AsyncBillingReservation{
		{
			ID: 9301, ReservationKey: "retention-protected-head", ProtocolVersion: TaskBillingProtocolVersion,
			Kind: AsyncBillingKindTask, PublicTaskID: "retention-protected-head", State: AsyncBillingReservationStateReleased,
			UserID: 9301, FundingSource: TaskBillingSourceWallet, CacheSyncPending: true,
			TerminalTimeMs: expired.UnixMilli(), CreatedTimeMs: expired.UnixMilli(), UpdatedTimeMs: expired.UnixMilli(),
		},
		{
			ID: 9302, ReservationKey: "retention-deletable-tail", ProtocolVersion: TaskBillingProtocolVersion,
			Kind: AsyncBillingKindTask, PublicTaskID: "retention-deletable-tail", State: AsyncBillingReservationStateReleased,
			UserID: 9302, FundingSource: TaskBillingSourceWallet,
			TerminalTimeMs: expired.UnixMilli(), CreatedTimeMs: expired.UnixMilli(), UpdatedTimeMs: expired.UnixMilli(),
		},
	} {
		require.NoError(t, db.Create(&reservation).Error)
	}

	page, err := CleanupExpiredAsyncBillingReceiptsPage(
		context.Background(), time.Now().Add(-30*24*time.Hour), 0, 1,
	)
	require.NoError(t, err)
	assert.Zero(t, page.Deleted)
	assert.Equal(t, 1, page.Scanned)
	assert.False(t, page.Done)
	assert.Equal(t, int64(9301), page.NextID)
	page, err = CleanupExpiredAsyncBillingReceiptsPage(
		context.Background(), time.Now().Add(-30*24*time.Hour), page.NextID, 1,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Deleted)
	assert.Equal(t, 1, page.Scanned)
	assert.False(t, page.Done)
	assert.Equal(t, int64(9302), page.NextID)
	page, err = CleanupExpiredAsyncBillingReceiptsPage(
		context.Background(), time.Now().Add(-30*24*time.Hour), page.NextID, 1,
	)
	require.NoError(t, err)
	assert.Zero(t, page.Deleted)
	assert.Zero(t, page.Scanned)
	assert.True(t, page.Done)
	assert.Zero(t, page.NextID)

	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", 9301).
		Update("cache_sync_pending", false).Error)
	page, err = CleanupExpiredAsyncBillingReceiptsPage(
		context.Background(), time.Now().Add(-30*24*time.Hour), page.NextID, 1,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Deleted)
	assert.Equal(t, 1, page.Scanned)
	assert.False(t, page.Done)
	assert.Equal(t, int64(9301), page.NextID)
	page, err = CleanupExpiredAsyncBillingReceiptsPage(
		context.Background(), time.Now().Add(-30*24*time.Hour), page.NextID, 1,
	)
	require.NoError(t, err)
	assert.Zero(t, page.Deleted)
	assert.Zero(t, page.Scanned)
	assert.True(t, page.Done)
	assert.Zero(t, page.NextID)
	var count int64
	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", 9301).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", 9302).Count(&count).Error)
	assert.Zero(t, count)
}

func createCompletedStatsProjectionForCleanup(
	t *testing.T,
	db *gorm.DB,
	kind string,
	referenceID int64,
	operationKey string,
	quotaDelta int,
	requestDelta int,
) BillingStatsProjection {
	t.Helper()
	projection := BillingStatsProjection{
		OperationKey: operationKey, ProtocolVersion: BillingStatsProjectionProtocol,
		Kind: kind, ReferenceID: referenceID, UserID: 1, ChannelID: 1,
		QuotaDelta: quotaDelta, RequestDelta: requestDelta, State: BillingStatsProjectionStateCompleted,
		CreatedTimeMs:   time.Now().Add(-400 * 24 * time.Hour).UnixMilli(),
		UpdatedTimeMs:   time.Now().Add(-400 * 24 * time.Hour).UnixMilli(),
		CompletedTimeMs: time.Now().Add(-400 * 24 * time.Hour).UnixMilli(),
	}
	require.NoError(t, db.Create(&projection).Error)
	return projection
}

func createCompletedLogProjectionForCleanup(
	t *testing.T,
	db *gorm.DB,
	kind string,
	referenceID int64,
	operationKey string,
) BillingLogProjection {
	t.Helper()
	projection := BillingLogProjection{
		OperationKey: operationKey, ProtocolVersion: BillingLogProjectionProtocol,
		Kind: kind, ReferenceID: referenceID, Required: false,
		Disposition: BillingLogProjectionDispositionNotRequired,
		State:       BillingLogProjectionStateCompleted, Outcome: BillingLogProjectionOutcomeNotRequired,
		CreatedTimeMs:   time.Now().Add(-400 * 24 * time.Hour).UnixMilli(),
		UpdatedTimeMs:   time.Now().Add(-400 * 24 * time.Hour).UnixMilli(),
		CompletedTimeMs: time.Now().Add(-400 * 24 * time.Hour).UnixMilli(),
	}
	require.NoError(t, db.Create(&projection).Error)
	return projection
}

func TestCleanupExpiredAsyncBillingReceiptsRequiresExactProjectionEvidence(t *testing.T) {
	for _, kind := range []string{AsyncBillingKindTask, AsyncBillingKindMidjourney} {
		t.Run(kind, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			now := time.Now()
			reservationID := int64(9340)
			if kind == AsyncBillingKindMidjourney {
				reservationID++
			}
			acceptedKey := fmt.Sprintf("async:%d:accepted:v1", reservationID)
			reservation := AsyncBillingReservation{
				ID: reservationID, ReservationKey: fmt.Sprintf("cleanup-exact-%s", kind),
				ProtocolVersion: TaskBillingProtocolVersion, Kind: kind,
				PublicTaskID: fmt.Sprintf("cleanup_exact_%s", kind), State: AsyncBillingReservationStateTerminal,
				UserID: 1, FundingSource: TaskBillingSourceWallet, InitialQuota: 100,
				CurrentQuota: 110, AcceptedQuota: 100, AcceptedProjectionKey: &acceptedKey,
				AcceptedProjectionState: AsyncBillingAcceptedProjectionCompleted,
				AcceptedProjectionQuota: 100, AcceptedProjectionRequestDelta: 1,
				TerminalTimeMs: now.Add(-366 * 24 * time.Hour).UnixMilli(),
				CreatedTimeMs:  now.Add(-400 * 24 * time.Hour).UnixMilli(),
				UpdatedTimeMs:  now.Add(-366 * 24 * time.Hour).UnixMilli(),
			}
			createCompletedStatsProjectionForCleanup(
				t, db, BillingStatsProjectionKindAccepted, reservationID, acceptedKey, 100, 1,
			)
			createCompletedLogProjectionForCleanup(
				t, db, BillingLogProjectionKindAccepted, reservationID, acceptedKey,
			)

			mismatchedKey := fmt.Sprintf("%s-terminal-mismatch", kind)
			correctKey := fmt.Sprintf("%s-terminal-correct", kind)
			var projectionToRepair any
			if kind == AsyncBillingKindTask {
				task := Task{
					TaskID: "cleanup-exact-task", UserId: 1, ChannelId: 1,
					Status: TaskStatusSuccess, Progress: "100%",
					PrivateData: TaskPrivateData{
						BillingProtocolVersion:    TaskBillingProtocolVersion,
						AsyncBillingReservationID: reservationID,
					},
				}
				require.NoError(t, task.IsolateV2BillingFromLegacyPollers(110))
				require.NoError(t, db.Create(&task).Error)
				reservation.TaskID = task.ID
				require.NoError(t, db.Create(&reservation).Error)
				operation := TaskBillingOperation{
					TaskID: task.ID, ReservationID: reservationID, OperationKey: correctKey,
					TerminalStatus: TaskStatusSuccess, Kind: TaskBillingOperationKindSettle,
					State: TaskBillingOperationStateCompleted, UserID: 1, ChannelID: 1,
					BillingSource: TaskBillingSourceWallet, PreConsumedQuota: 100,
					TargetQuota: 110, QuotaDelta: 10, LogState: TaskBillingOperationLogNotRequired,
				}
				require.NoError(t, db.Create(&operation).Error)
				stats := createCompletedStatsProjectionForCleanup(
					t, db, BillingStatsProjectionKindTaskTerminal, operation.ID, mismatchedKey, 10, 0,
				)
				createCompletedLogProjectionForCleanup(
					t, db, BillingLogProjectionKindTaskTerminal, operation.ID, correctKey,
				)
				projectionToRepair = &stats
			} else {
				task := Midjourney{
					MjId: "cleanup-exact-mj", UserId: 1, ChannelId: 1,
					Status: "FAILURE", Progress: "100%", BillingProtocolVersion: TaskBillingProtocolVersion,
					AsyncBillingReservationID: reservationID,
				}
				require.NoError(t, db.Create(&task).Error)
				reservation.MidjourneyID = task.Id
				reservation.CurrentQuota = 0
				require.NoError(t, db.Create(&reservation).Error)
				operation := MidjourneyBillingOperation{
					MidjourneyID: task.Id, ReservationID: reservationID, OperationKey: correctKey,
					TerminalStatus: "FAILURE", Kind: TaskBillingOperationKindRefund,
					State: TaskBillingOperationStateCompleted, UserID: 1, ChannelID: 1,
					BillingSource: TaskBillingSourceWallet, RefundQuota: 100,
					LogState: TaskBillingOperationLogNotRequired,
				}
				require.NoError(t, db.Create(&operation).Error)
				logProjection := createCompletedLogProjectionForCleanup(
					t, db, BillingLogProjectionKindMidjourneyTerminal, int64(operation.ID), mismatchedKey,
				)
				projectionToRepair = &logProjection
			}

			page, err := CleanupExpiredAsyncBillingReceiptsPage(
				context.Background(), now.Add(-365*24*time.Hour), 0, 10,
			)
			require.NoError(t, err)
			assert.Zero(t, page.Deleted)
			if projection, ok := projectionToRepair.(*BillingStatsProjection); ok {
				require.NoError(t, db.Model(&BillingStatsProjection{}).Where("id = ?", projection.ID).
					Update("operation_key", correctKey).Error)
			} else {
				projection := projectionToRepair.(*BillingLogProjection)
				require.NoError(t, db.Model(&BillingLogProjection{}).Where("id = ?", projection.ID).
					Update("operation_key", correctKey).Error)
			}
			page, err = CleanupExpiredAsyncBillingReceiptsPage(
				context.Background(), now.Add(-365*24*time.Hour), 0, 10,
			)
			require.NoError(t, err)
			assert.Equal(t, int64(1), page.Deleted)
			assert.ErrorIs(t, db.First(&AsyncBillingReservation{}, reservationID).Error, gorm.ErrRecordNotFound)
		})
	}
}

func TestAsyncBillingRetentionKeepsProjectionsUntilTaskAndMidjourneySourcesExpire(t *testing.T) {
	tests := []struct {
		name          string
		kind          string
		terminalState string
		operationKind string
		currentQuota  int
		quotaDelta    int
		refundQuota   int
	}{
		{name: "task refund", kind: AsyncBillingKindTask, terminalState: string(TaskStatusFailure), operationKind: TaskBillingOperationKindRefund, currentQuota: 60, quotaDelta: -40},
		{name: "task noop", kind: AsyncBillingKindTask, terminalState: string(TaskStatusSuccess), operationKind: TaskBillingOperationKindNoop, currentQuota: 100},
		{name: "midjourney refund", kind: AsyncBillingKindMidjourney, terminalState: "FAILURE", operationKind: TaskBillingOperationKindRefund, currentQuota: 0, refundQuota: 100},
		{name: "midjourney noop", kind: AsyncBillingKindMidjourney, terminalState: "SUCCESS", operationKind: TaskBillingOperationKindNoop, currentQuota: 100},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			now := time.Now()
			reservationID := int64(9360 + index)
			acceptedKey := fmt.Sprintf("async:%d:accepted:v1", reservationID)
			reservation := AsyncBillingReservation{
				ID: reservationID, ReservationKey: fmt.Sprintf("retention-order-%d", reservationID),
				ProtocolVersion: TaskBillingProtocolVersion, Kind: test.kind,
				PublicTaskID: fmt.Sprintf("retention_order_%d", reservationID),
				State:        AsyncBillingReservationStateTerminal, UserID: 1, FundingSource: TaskBillingSourceWallet,
				InitialQuota: 100, AcceptedQuota: 100, CurrentQuota: test.currentQuota,
				AcceptedProjectionKey: &acceptedKey, AcceptedProjectionState: AsyncBillingAcceptedProjectionCompleted,
				AcceptedProjectionQuota: 100, AcceptedProjectionRequestDelta: 1,
				TerminalTimeMs: now.Add(-366 * 24 * time.Hour).UnixMilli(),
				CreatedTimeMs:  now.Add(-400 * 24 * time.Hour).UnixMilli(), UpdatedTimeMs: now.Add(-366 * 24 * time.Hour).UnixMilli(),
			}
			createCompletedStatsProjectionForCleanup(
				t, db, BillingStatsProjectionKindAccepted, reservationID, acceptedKey, 100, 1,
			)
			createCompletedLogProjectionForCleanup(
				t, db, BillingLogProjectionKindAccepted, reservationID, acceptedKey,
			)
			terminalKey := fmt.Sprintf("%s:%d:terminal:v2", test.kind, reservationID)
			if test.kind == AsyncBillingKindTask {
				task := Task{
					TaskID: reservation.PublicTaskID, UserId: 1, ChannelId: 1,
					Status: TaskStatus(test.terminalState), Progress: "100%",
					PrivateData: TaskPrivateData{
						BillingProtocolVersion:    TaskBillingProtocolVersion,
						AsyncBillingReservationID: reservationID,
					},
				}
				require.NoError(t, task.IsolateV2BillingFromLegacyPollers(test.currentQuota))
				require.NoError(t, db.Create(&task).Error)
				reservation.TaskID = task.ID
				require.NoError(t, db.Create(&reservation).Error)
				operation := TaskBillingOperation{
					TaskID: task.ID, ReservationID: reservationID, OperationKey: terminalKey,
					TerminalStatus: task.Status, Kind: test.operationKind, State: TaskBillingOperationStateCompleted,
					UserID: 1, ChannelID: 1, BillingSource: TaskBillingSourceWallet,
					PreConsumedQuota: 100, TargetQuota: test.currentQuota, QuotaDelta: test.quotaDelta,
					LogState: TaskBillingOperationLogNotRequired,
				}
				require.NoError(t, db.Create(&operation).Error)
				if test.quotaDelta != 0 {
					createCompletedLogProjectionForCleanup(
						t, db, BillingLogProjectionKindTaskTerminal, operation.ID, terminalKey,
					)
				}
			} else {
				task := Midjourney{
					MjId: reservation.PublicTaskID, UserId: 1, ChannelId: 1,
					Status: test.terminalState, Progress: "100%", BillingProtocolVersion: TaskBillingProtocolVersion,
					AsyncBillingReservationID: reservationID,
				}
				require.NoError(t, task.IsolateV2BillingFromLegacyPollers(test.currentQuota))
				require.NoError(t, db.Create(&task).Error)
				reservation.MidjourneyID = task.Id
				require.NoError(t, db.Create(&reservation).Error)
				operation := MidjourneyBillingOperation{
					MidjourneyID: task.Id, ReservationID: reservationID, OperationKey: terminalKey,
					TerminalStatus: task.Status, Kind: test.operationKind, State: TaskBillingOperationStateCompleted,
					UserID: 1, ChannelID: 1, BillingSource: TaskBillingSourceWallet,
					RefundQuota: test.refundQuota, LogState: TaskBillingOperationLogNotRequired,
				}
				require.NoError(t, db.Create(&operation).Error)
				if test.refundQuota > 0 {
					createCompletedLogProjectionForCleanup(
						t, db, BillingLogProjectionKindMidjourneyTerminal, operation.ID, terminalKey,
					)
				}
			}

			statsDeleted, err := CleanupExpiredBillingStatsProjections(context.Background(), now, 100)
			require.NoError(t, err)
			logsDeleted, err := CleanupExpiredBillingLogProjections(context.Background(), now, 100)
			require.NoError(t, err)
			assert.Zero(t, statsDeleted)
			assert.Zero(t, logsDeleted)
			page, err := CleanupExpiredAsyncBillingReceiptsPage(
				context.Background(), now.Add(-365*24*time.Hour), 0, 100,
			)
			require.NoError(t, err)
			assert.Equal(t, int64(1), page.Deleted)
			statsDeleted, err = CleanupExpiredBillingStatsProjections(context.Background(), now, 100)
			require.NoError(t, err)
			logsDeleted, err = CleanupExpiredBillingLogProjections(context.Background(), now, 100)
			require.NoError(t, err)
			assert.Positive(t, statsDeleted)
			assert.Positive(t, logsDeleted)
			assert.ErrorIs(t, db.First(&AsyncBillingReservation{}, reservationID).Error, gorm.ErrRecordNotFound)
		})
	}
}

func TestFindStaleAmbiguousAsyncBillingReservationsStartsAfterSendDeadline(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	authorizedAt := time.Now()
	deadline := authorizedAt.Add(time.Minute)
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, authorizedAt, deadline)

	ids, err := FindStaleAmbiguousAsyncBillingReservationIDs(deadline.Add(15*time.Minute-time.Millisecond), 15*time.Minute, 10)
	require.NoError(t, err)
	assert.NotContains(t, ids, reservation.ID)

	ids, err = FindStaleAmbiguousAsyncBillingReservationIDs(deadline.Add(15*time.Minute), 15*time.Minute, 10)
	require.NoError(t, err)
	assert.Contains(t, ids, reservation.ID)
}

func TestAuthorizeAsyncBillingAttemptFailsClosedUntilCacheFenceIsDurable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88151
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "cache-fence-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "sk-cache-fence", Name: "cache-fence",
		Status: common.TokenStatusEnabled, RemainQuota: 1000,
	}).Error)
	channel := Channel{
		Id: 78, Name: "cache-fence-channel", Key: "sk-cache-fence-channel",
		Status: common.ChannelStatusEnabled, Models: "suno-review", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	now := time.Now()
	reservation, created, err := CreateAsyncBillingReservation(context.Background(), AsyncBillingReservationSpec{
		ReservationKey: "reservation-cache-fence", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_cache_fence", UserID: userID, TokenID: userID,
		BillingPreference: "wallet_only", Quota: 100,
	}, now)
	require.NoError(t, err)
	require.True(t, created)

	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", reservation.ID).Updates(map[string]any{
		"cache_sync_version": 2, "cache_synced_version": 1, "cache_sync_pending": false,
	}).Error)
	spec := AsyncBillingAttemptSpec{
		AttemptIndex: 0, ChannelID: channel.Id, ChannelVersion: channel.RoutingGeneration,
		SendDeadlineMs: now.Add(time.Minute).UnixMilli(),
		AcceptanceIntent: &AsyncBillingAcceptanceIntentSpec{
			Task: &Task{Platform: constant.TaskPlatformSuno, Action: "submit", Status: TaskStatusNotStart},
			Audit: AsyncBillingAcceptedAuditSnapshot{
				RequestID: "request-cache-fence", RequestPath: "/v1/videos", Action: "submit",
				OriginModelName: "suno-review", Group: "default", Content: "submit async task",
			},
		},
	}
	_, err = AuthorizeAsyncBillingAttempt(context.Background(), reservation.ID, spec, now)
	assert.ErrorIs(t, err, ErrAsyncBillingCacheFencePending)
	var attempts int64
	require.NoError(t, db.Model(&AsyncBillingAttempt{}).Where("reservation_id = ?", reservation.ID).Count(&attempts).Error)
	assert.Zero(t, attempts)

	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", reservation.ID).Updates(map[string]any{
		"cache_sync_pending": true, "cache_sync_next_retry_ms": 0,
	}).Error)
	attempt, err := AuthorizeAsyncBillingAttempt(context.Background(), reservation.ID, spec, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingAttemptStateAuthorized, attempt.State)
	var persisted AsyncBillingReservation
	require.NoError(t, db.First(&persisted, reservation.ID).Error)
	assert.False(t, persisted.CacheSyncPending)
	assert.Equal(t, persisted.CacheSyncVersion, persisted.CacheSyncedVersion)
}

func TestAuthorizeAsyncBillingAttemptDoesNotSendWhenRedisInvalidationFails(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88152
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "cache-failure-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "sk-cache-failure", Name: "cache-failure",
		Status: common.TokenStatusEnabled, RemainQuota: 1000,
	}).Error)
	channel := Channel{
		Id: 79, Name: "cache-failure-channel", Key: "sk-cache-failure-channel",
		Status: common.ChannelStatusEnabled, Models: "suno-review", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	now := time.Now()
	reservation, created, err := CreateAsyncBillingReservation(context.Background(), AsyncBillingReservationSpec{
		ReservationKey: "reservation-cache-failure", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_cache_failure", UserID: userID, TokenID: userID,
		BillingPreference: "wallet_only", Quota: 100,
	}, now)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", reservation.ID).Updates(map[string]any{
		"cache_sync_version": 2, "cache_synced_version": 1, "cache_sync_pending": true,
	}).Error)

	previousRDB := common.RDB
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousRDB
	})
	spec := AsyncBillingAttemptSpec{
		AttemptIndex: 0, ChannelID: channel.Id, ChannelVersion: channel.RoutingGeneration,
		SendDeadlineMs: now.Add(time.Minute).UnixMilli(),
		AcceptanceIntent: &AsyncBillingAcceptanceIntentSpec{
			Task: &Task{Platform: constant.TaskPlatformSuno, Action: "submit", Status: TaskStatusNotStart},
			Audit: AsyncBillingAcceptedAuditSnapshot{
				RequestID: "request-cache-failure", RequestPath: "/v1/videos", Action: "submit",
				OriginModelName: "suno-review", Group: "default", Content: "submit async task",
			},
		},
	}
	_, err = AuthorizeAsyncBillingAttempt(context.Background(), reservation.ID, spec, now)
	assert.ErrorIs(t, err, ErrAsyncBillingCacheFencePending)
	var attempts int64
	require.NoError(t, db.Model(&AsyncBillingAttempt{}).Where("reservation_id = ?", reservation.ID).Count(&attempts).Error)
	assert.Zero(t, attempts)
	var persisted AsyncBillingReservation
	require.NoError(t, db.First(&persisted, reservation.ID).Error)
	assert.True(t, persisted.CacheSyncPending)
	assert.Less(t, persisted.CacheSyncedVersion, persisted.CacheSyncVersion)

	common.RedisEnabled = false
	attempt, err := AuthorizeAsyncBillingAttempt(context.Background(), reservation.ID, spec, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingAttemptStateAuthorized, attempt.State)
}

func TestHardDeleteUserFailsClosedWhileAsyncBillingReceiptExists(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	authorizedAt := time.Now().Add(-time.Minute)
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, authorizedAt, authorizedAt.Add(30*time.Second))

	err := HardDeleteUserById(reservation.UserID)
	assert.ErrorIs(t, err, ErrAsyncBillingOutstandingReferences)

	var user User
	require.NoError(t, db.Unscoped().First(&user, reservation.UserID).Error)
	assert.Equal(t, reservation.UserID, user.Id)
}

func TestHardDeleteSubscriptionFailsClosedWhileAsyncBillingReceiptExists(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88201
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "subscription-delete-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	subscription := UserSubscription{
		UserId: userID, PlanId: 7, AmountTotal: 1000, AmountUsed: 100,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(), Status: "active",
	}
	require.NoError(t, db.Create(&subscription).Error)
	require.NoError(t, db.Create(&AsyncBillingReservation{
		ReservationKey: "subscription-delete-reference", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_subscription_delete", State: AsyncBillingReservationStateTerminal,
		UserID: userID, FundingSource: TaskBillingSourceSubscription, SubscriptionID: subscription.Id,
		InitialQuota: 100, CurrentQuota: 100, AcceptedQuota: 100,
		CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
	}).Error)

	_, err := AdminDeleteUserSubscription(subscription.Id)
	assert.ErrorIs(t, err, ErrAsyncBillingOutstandingReferences)

	var persisted UserSubscription
	require.NoError(t, db.First(&persisted, subscription.Id).Error)
	assert.Equal(t, subscription.Id, persisted.Id)
}

func TestHardDeleteUserFailsClosedBeforeLegacyBillingOperationMaterializes(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, *gorm.DB, int)
	}{
		{name: "task non terminal", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Task{
				TaskID: "legacy-task-running", UserId: userID, Status: TaskStatusInProgress,
				PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingLegacyProtocolVersion, BillingSource: TaskBillingSourceWallet},
			}).Error)
		}},
		{name: "historical version zero task non terminal", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Task{
				TaskID: "historical-v0-task-running", UserId: userID, Status: TaskStatusInProgress,
				PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingHistoricalProtocolVersion, BillingSource: TaskBillingSourceWallet},
			}).Error)
		}},
		{name: "task terminal without operation", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Task{
				TaskID: "legacy-task-terminal", UserId: userID, Status: TaskStatusFailure, Progress: "100%",
				PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingLegacyProtocolVersion, BillingSource: TaskBillingSourceWallet},
			}).Error)
		}},
		{name: "midjourney non terminal", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Midjourney{
				MjId: "legacy-mj-running", UserId: userID, Status: "IN_PROGRESS",
				BillingProtocolVersion: TaskBillingLegacyProtocolVersion, BillingSource: TaskBillingSourceWallet,
			}).Error)
		}},
		{name: "historical version zero midjourney non terminal", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Midjourney{
				MjId: "historical-v0-mj-running", UserId: userID, Status: "IN_PROGRESS",
				BillingProtocolVersion: TaskBillingHistoricalProtocolVersion, BillingSource: TaskBillingSourceWallet,
			}).Error)
		}},
		{name: "midjourney terminal without operation", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Midjourney{
				MjId: "legacy-mj-terminal", UserId: userID, Status: "FAILURE", Progress: "100%",
				BillingProtocolVersion: TaskBillingLegacyProtocolVersion, BillingSource: TaskBillingSourceWallet,
			}).Error)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			userID := 88210
			require.NoError(t, db.Create(&User{
				Id: userID, Username: "legacy-delete-user", Status: common.UserStatusEnabled, Quota: 1000,
			}).Error)
			test.create(t, db, userID)

			err := HardDeleteUserById(userID)
			assert.ErrorIs(t, err, ErrAsyncBillingOutstandingReferences)
			var user User
			require.NoError(t, db.Unscoped().First(&user, userID).Error)
		})
	}
}

func TestHistoricalVersionZeroTerminalDoesNotRemainOutstanding(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, *gorm.DB, int)
	}{
		{name: "task", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Task{
				TaskID: "historical-v0-task-terminal", UserId: userID,
				Status: TaskStatusFailure, Progress: "100%",
				PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingHistoricalProtocolVersion},
			}).Error)
		}},
		{name: "midjourney", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Midjourney{
				MjId: "historical-v0-mj-terminal", UserId: userID,
				Status: "FAILURE", Progress: "100%",
				BillingProtocolVersion: TaskBillingHistoricalProtocolVersion,
			}).Error)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			const userID = 88211
			require.NoError(t, db.Create(&User{
				Id: userID, Username: "historical-v0-terminal-user", Status: common.UserStatusEnabled, Quota: 1000,
			}).Error)
			test.create(t, db, userID)

			outstanding, err := hasUnmaterializedLegacyBillingReferenceTx(db, userID, 0)
			require.NoError(t, err)
			assert.False(t, outstanding)
		})
	}
}

func TestSubscriptionReferenceFailsClosedForHistoricalUnknownFunding(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, *gorm.DB, int)
	}{
		{name: "task", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Task{
				TaskID: "historical-v0-task-unknown-funding", UserId: userID, Status: TaskStatusInProgress,
				PrivateData: TaskPrivateData{BillingProtocolVersion: TaskBillingHistoricalProtocolVersion},
			}).Error)
		}},
		{name: "midjourney", create: func(t *testing.T, db *gorm.DB, userID int) {
			require.NoError(t, db.Create(&Midjourney{
				MjId: "historical-v0-mj-unknown-funding", UserId: userID,
				Status: "IN_PROGRESS", Progress: "50%",
				BillingProtocolVersion: TaskBillingHistoricalProtocolVersion,
			}).Error)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			const userID = 88212
			test.create(t, db, userID)

			outstanding, err := hasUnmaterializedLegacyBillingReferenceTx(db, userID, 991)
			require.NoError(t, err)
			assert.True(t, outstanding)
		})
	}
}

func TestLegacyMidjourneyNullProtocolMigrationRemainsOutstanding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-midjourney.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyMidjourneySchemaForMigration{}))
	require.NoError(t, db.Create(&legacyMidjourneySchemaForMigration{
		ID: 1, UserID: 88213, MjID: "legacy-null-protocol", Status: "IN_PROGRESS", Progress: "50%",
	}).Error)
	require.NoError(t, db.AutoMigrate(&Midjourney{}, &MidjourneyBillingOperation{}))

	var protocol sql.NullInt64
	require.NoError(t, db.Raw("SELECT billing_protocol_version FROM midjourneys WHERE id = ?", 1).Scan(&protocol).Error)
	require.False(t, protocol.Valid)
	var migrated Midjourney
	require.NoError(t, db.First(&migrated, 1).Error)
	assert.Equal(t, TaskBillingHistoricalProtocolVersion, migrated.BillingProtocolVersion)

	for _, subscriptionID := range []int{0, 992} {
		outstanding, err := hasUnmaterializedLegacyBillingReferenceTx(db, 88213, subscriptionID)
		require.NoError(t, err)
		assert.True(t, outstanding)
	}

	require.NoError(t, db.Exec(
		"INSERT INTO midjourneys (id, user_id, mj_id, status, progress) VALUES (?, ?, ?, NULL, NULL)",
		2, 88214, "legacy-null-terminal-state",
	).Error)
	for _, subscriptionID := range []int{0, 993} {
		outstanding, err := hasUnmaterializedLegacyBillingReferenceTx(db, 88214, subscriptionID)
		require.NoError(t, err)
		assert.True(t, outstanding, "NULL terminal markers must fail closed")
	}
}

func TestLegacyMidjourneyNullProtocolMigrationExternalDatabasesRemainOutstanding(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql57", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres96", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openAsyncBillingSchemaExternalTestDB(t, test.dbType, dsn)
			if db.Migrator().HasTable(&Midjourney{}) || db.Migrator().HasTable(&MidjourneyBillingOperation{}) {
				t.Skip("refusing to run against external database because Midjourney billing tables already exist")
			}
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&MidjourneyBillingOperation{})
				_ = db.Migrator().DropTable(&Midjourney{})
			})

			previousType := common.MainDatabaseType()
			common.SetMainDatabaseType(test.dbType)
			t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
			require.NoError(t, db.AutoMigrate(&legacyMidjourneySchemaForMigration{}))
			require.NoError(t, db.Exec(
				"INSERT INTO midjourneys (id, user_id, mj_id, status, progress) VALUES (?, ?, ?, NULL, NULL)",
				1, 88215, "legacy-null-protocol-external",
			).Error)
			require.NoError(t, db.AutoMigrate(&Midjourney{}, &MidjourneyBillingOperation{}))

			var protocol sql.NullInt64
			require.NoError(t, db.Raw(
				"SELECT billing_protocol_version FROM midjourneys WHERE id = ?", 1,
			).Scan(&protocol).Error)
			require.False(t, protocol.Valid)
			var migrated Midjourney
			require.NoError(t, db.First(&migrated, 1).Error)
			assert.Equal(t, TaskBillingHistoricalProtocolVersion, migrated.BillingProtocolVersion)
			for _, subscriptionID := range []int{0, 994} {
				outstanding, err := hasUnmaterializedLegacyBillingReferenceTx(db, 88215, subscriptionID)
				require.NoError(t, err)
				assert.True(t, outstanding, "NULL protocol and terminal markers must fail closed")
			}
		})
	}
}

func TestHardDeleteSubscriptionFailsClosedBeforeLegacyBillingOperationMaterializes(t *testing.T) {
	for _, kind := range []string{"task", "midjourney"} {
		t.Run(kind, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			const userID = 88220
			require.NoError(t, db.Create(&User{
				Id: userID, Username: "legacy-sub-delete-user", Status: common.UserStatusEnabled, Quota: 1000,
			}).Error)
			subscription := UserSubscription{
				Id: 88220, UserId: userID, PlanId: 7, AmountTotal: 1000, AmountUsed: 100,
				StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(), Status: "active",
			}
			require.NoError(t, db.Create(&subscription).Error)
			if kind == "task" {
				require.NoError(t, db.Create(&Task{
					TaskID: "legacy-sub-task", UserId: userID, Status: TaskStatusInProgress,
					PrivateData: TaskPrivateData{
						BillingProtocolVersion: TaskBillingLegacyProtocolVersion,
						BillingSource:          TaskBillingSourceSubscription, SubscriptionId: subscription.Id,
					},
				}).Error)
			} else {
				require.NoError(t, db.Create(&Midjourney{
					MjId: "legacy-sub-mj", UserId: userID, Status: "IN_PROGRESS",
					BillingProtocolVersion: TaskBillingLegacyProtocolVersion,
					BillingSource:          TaskBillingSourceSubscription, SubscriptionId: subscription.Id,
				}).Error)
			}

			_, err := AdminDeleteUserSubscription(subscription.Id)
			assert.ErrorIs(t, err, ErrAsyncBillingOutstandingReferences)
			var persisted UserSubscription
			require.NoError(t, db.First(&persisted, subscription.Id).Error)
		})
	}
}

func TestAsyncBillingReservationRejectsNewSoftDeletedPrincipalButReplaysExistingReceipt(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88301
	now := time.Now()
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "deleted-principal-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "deleted-principal-token", Name: "deleted-principal",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}).Error)
	spec := AsyncBillingReservationSpec{
		ReservationKey: "deleted-principal-existing", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_deleted_principal", UserID: userID, TokenID: userID,
		BillingPreference: "wallet_only", Quota: 100,
	}
	reservation, created, err := CreateAsyncBillingReservation(context.Background(), spec, now)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, DeleteTokenById(userID, userID))

	replayed, created, err := CreateAsyncBillingReservation(context.Background(), spec, now.Add(time.Second))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, reservation.ID, replayed.ID)

	spec.ReservationKey = "deleted-principal-new"
	spec.PublicTaskID = "task_deleted_principal_new"
	_, _, err = CreateAsyncBillingReservation(context.Background(), spec, now.Add(2*time.Second))
	assert.ErrorIs(t, err, ErrTokenInvalid)

	require.NoError(t, db.Unscoped().Model(&User{}).Where("id = ?", userID).Update("deleted_at", now).Error)
	spec.TokenID = 0
	spec.IsPlayground = true
	spec.ReservationKey = "deleted-user-new"
	spec.PublicTaskID = "task_deleted_user_new"
	_, _, err = CreateAsyncBillingReservation(context.Background(), spec, now.Add(3*time.Second))
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestSingleTokenDeleteKeepsOutstandingWalletRefundSettleable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88302
	now := time.Now()
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "single-delete-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "single-delete-token", Name: "single-delete",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}).Error)
	reservation, created, err := CreateAsyncBillingReservation(context.Background(), AsyncBillingReservationSpec{
		ReservationKey: "single-delete-reservation", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_single_delete", UserID: userID, TokenID: userID,
		BillingPreference: "wallet_only", Quota: 100,
	}, now)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, DeleteTokenById(userID, userID))
	_, err = ReleaseAsyncBillingReservation(context.Background(), reservation.ID, now.Add(time.Second))
	require.NoError(t, err)

	var user User
	var token Token
	require.NoError(t, db.Unscoped().First(&user, userID).Error)
	require.NoError(t, db.Unscoped().First(&token, userID).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.True(t, token.DeletedAt.Valid)
}

func TestBatchTokenDeleteKeepsOutstandingSubscriptionRefundSettleable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88303
	now := time.Now()
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "batch-delete-user", Status: common.UserStatusEnabled, Quota: 1000,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: userID, UserId: userID, Key: "batch-delete-token", Name: "batch-delete",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100, ExpiredTime: -1,
	}).Error)
	subscription := UserSubscription{
		Id: 88303, UserId: userID, PlanId: 1, AmountTotal: 1000, AmountUsed: 100,
		BillingPeriodSequence: 1, StartTime: now.Add(-time.Hour).Unix(), EndTime: now.Add(time.Hour).Unix(), Status: "active",
	}
	require.NoError(t, db.Create(&subscription).Error)
	period := SubscriptionBillingPeriod{
		SubscriptionID: subscription.Id, UserID: userID, PeriodSequence: 1,
		PeriodStart: subscription.StartTime, PeriodEnd: subscription.EndTime,
		AmountTotal: 1000, AmountUsed: 100, CreatedTime: now.Unix(), UpdatedTime: now.Unix(),
	}
	require.NoError(t, db.Create(&period).Error)
	reservation := AsyncBillingReservation{
		ReservationKey: "batch-delete-reservation", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_batch_delete", State: AsyncBillingReservationStateAccepted,
		UserID: userID, TokenID: userID, FundingSource: TaskBillingSourceSubscription,
		SubscriptionID: subscription.Id, SubscriptionPeriodID: period.ID,
		InitialQuota: 100, CurrentQuota: 100, AcceptedQuota: 100,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, db.Create(&reservation).Error)
	deleted, err := BatchDeleteTokens([]int{userID}, userID)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return CompleteAsyncBillingReservationTx(tx, reservation.ID, userID, 100, 50, now.Add(time.Second))
	}))

	var token Token
	require.NoError(t, db.Unscoped().First(&token, userID).Error)
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&period, period.ID).Error)
	assert.Equal(t, 950, token.RemainQuota)
	assert.Equal(t, 50, token.UsedQuota)
	assert.True(t, token.DeletedAt.Valid)
	assert.Equal(t, int64(50), subscription.AmountUsed)
	assert.Equal(t, int64(50), period.AmountUsed)
}

func TestAcceptedProjectionCompletesWithAuditedMissingChannelOutcome(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	const userID = 88304
	now := time.Now()
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "missing-channel-user", Status: common.UserStatusEnabled,
	}).Error)
	key := "async:88304:accepted:v1"
	payload, payloadHash, protocol, err := freezeBillingLogPayload(key, &Log{
		UserId: userID, CreatedAt: now.Unix(), Type: LogTypeConsume,
		Content: "accepted projection", ModelName: "video-model", Quota: 25,
		ChannelId: 999999, Group: "default", RequestId: "missing-channel-request",
	})
	require.NoError(t, err)
	reservation := AsyncBillingReservation{
		ReservationKey: "missing-channel-reservation", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "task_missing_channel", State: AsyncBillingReservationStateAccepted,
		UserID: userID, FundingSource: TaskBillingSourceWallet,
		AcceptedProjectionKey: &key, AcceptedProjectionState: AsyncBillingAcceptedProjectionPending,
		AcceptedProjectionChannelID: 999999, AcceptedProjectionQuota: 25, AcceptedProjectionRequestDelta: 1,
		AcceptedProjectionLogProtocol: protocol, AcceptedProjectionLogHash: payloadHash,
		AcceptedProjectionLogPayload: payload, CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, db.Create(&reservation).Error)
	require.NoError(t, ProcessAsyncBillingAcceptedProjection(context.Background(), reservation.ID, now))
	require.NoError(t, ProcessAsyncBillingAcceptedProjection(context.Background(), reservation.ID, now.Add(time.Second)))

	require.NoError(t, db.First(&reservation, reservation.ID).Error)
	assert.Equal(t, AsyncBillingAcceptedProjectionCompleted, reservation.AcceptedProjectionState)
	assert.Equal(t, BillingUsageOutcomeApplied, reservation.AcceptedProjectionUserOutcome)
	assert.Equal(t, BillingUsageOutcomeSkippedMissing, reservation.AcceptedProjectionChannelOutcome)
	assert.Contains(t, reservation.AcceptedProjectionWarning, "channel=skipped_missing")
	var user User
	require.NoError(t, db.First(&user, userID).Error)
	assert.Equal(t, 25, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

type legacyAcceptedProjectionFixture struct {
	reservation AsyncBillingReservation
	stats       BillingStatsProjection
	userID      int
}

func createLegacyAcceptedProjectionFixture(
	t *testing.T,
	db *gorm.DB,
	userID int,
	statsQuota int,
	createLog bool,
	createStats bool,
) legacyAcceptedProjectionFixture {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Create(&User{
		Id: userID, Username: fmt.Sprintf("legacy-projection-%d", userID), Status: common.UserStatusEnabled,
	}).Error)
	channel := Channel{
		Id: userID, Name: fmt.Sprintf("legacy-projection-%d", userID), Key: fmt.Sprintf("sk-%d", userID),
		Status: common.ChannelStatusEnabled, Models: "video-model", Group: "default",
	}
	require.NoError(t, db.Create(&channel).Error)
	key := fmt.Sprintf("async:%d:accepted:v1", userID)
	entry := &Log{
		UserId: userID, CreatedAt: now.Unix(), Type: LogTypeConsume,
		Content: "legacy accepted projection", ModelName: "video-model", Quota: 25,
		ChannelId: channel.Id, Group: "default", RequestId: fmt.Sprintf("request-%d", userID),
	}
	payload, payloadHash, protocol, err := freezeBillingLogPayload(key, entry)
	require.NoError(t, err)
	fixture := legacyAcceptedProjectionFixture{
		userID: userID,
		reservation: AsyncBillingReservation{
			ID: int64(userID), ReservationKey: fmt.Sprintf("legacy-projection-%d", userID),
			ProtocolVersion: TaskBillingProtocolVersion, Kind: AsyncBillingKindTask,
			PublicTaskID: fmt.Sprintf("task_legacy_projection_%d", userID), State: AsyncBillingReservationStateAccepted,
			UserID: userID, FundingSource: TaskBillingSourceWallet,
			AcceptedProjectionKey: &key, AcceptedProjectionState: AsyncBillingAcceptedProjectionPending,
			AcceptedProjectionChannelID: channel.Id, AcceptedProjectionQuota: 25, AcceptedProjectionRequestDelta: 1,
			AcceptedProjectionLogProtocol: protocol, AcceptedProjectionLogHash: payloadHash,
			AcceptedProjectionLogPayload: payload, CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
		},
	}
	require.NoError(t, db.Create(&fixture.reservation).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if createLog {
			if _, _, err := CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
				OperationKey: key, Kind: BillingLogProjectionKindAccepted,
				ReferenceID: fixture.reservation.ID, Required: true, Entry: entry,
			}, now); err != nil {
				return err
			}
		}
		if createStats {
			projection, _, err := CreateBillingStatsProjectionTx(tx, BillingStatsProjectionSpec{
				OperationKey: key, Kind: BillingStatsProjectionKindAccepted,
				ReferenceID: fixture.reservation.ID, UserID: userID, ChannelID: channel.Id,
				QuotaDelta: statsQuota, RequestDelta: 1,
			}, now)
			if err != nil {
				return err
			}
			fixture.stats = *projection
		}
		return nil
	}))
	return fixture
}

func TestLegacyAcceptedProjectionIsSupersededWithoutDoubleCounting(t *testing.T) {
	for _, durableFirst := range []bool{true, false} {
		name := "legacy_guard_first"
		if durableFirst {
			name = "durable_stats_first"
		}
		t.Run(name, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			userID := 88310
			if durableFirst {
				userID++
			}
			fixture := createLegacyAcceptedProjectionFixture(t, db, userID, 25, true, true)
			completeStats := func(at time.Time) {
				t.Helper()
				_, claimed, err := ClaimBillingStatsProjection(
					context.Background(), fixture.stats.ID, name, at, time.Minute,
				)
				require.NoError(t, err)
				require.True(t, claimed)
				_, err = CompleteClaimedBillingStatsProjection(context.Background(), fixture.stats.ID, name, at)
				require.NoError(t, err)
			}
			now := time.Now()
			if durableFirst {
				completeStats(now)
			}
			require.NoError(t, ProcessAsyncBillingAcceptedProjection(
				context.Background(), fixture.reservation.ID, now.Add(time.Second),
			))
			if !durableFirst {
				completeStats(now.Add(2 * time.Second))
			}

			var reservation AsyncBillingReservation
			var user User
			require.NoError(t, db.First(&reservation, fixture.reservation.ID).Error)
			require.NoError(t, db.First(&user, fixture.userID).Error)
			assert.Equal(t, AsyncBillingAcceptedProjectionCompleted, reservation.AcceptedProjectionState)
			assert.Contains(t, reservation.AcceptedProjectionWarning, "superseded by durable receipts")
			assert.Equal(t, 25, user.UsedQuota)
			assert.Equal(t, 1, user.RequestCount)
		})
	}
}

func TestLegacyAcceptedProjectionFailsClosedOnIncompleteOrConflictingDurableReceipts(t *testing.T) {
	for _, test := range []struct {
		name        string
		userID      int
		statsQuota  int
		createLog   bool
		createStats bool
	}{
		{name: "missing log receipt", userID: 88312, statsQuota: 25, createStats: true},
		{name: "conflicting stats receipt", userID: 88313, statsQuota: 26, createLog: true, createStats: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupAsyncBillingReservationTest(t)
			fixture := createLegacyAcceptedProjectionFixture(
				t, db, test.userID, test.statsQuota, test.createLog, test.createStats,
			)
			err := ProcessAsyncBillingAcceptedProjection(context.Background(), fixture.reservation.ID, time.Now())
			assert.ErrorIs(t, err, ErrAsyncBillingReservationInvariant)

			var reservation AsyncBillingReservation
			var user User
			require.NoError(t, db.First(&reservation, fixture.reservation.ID).Error)
			require.NoError(t, db.First(&user, fixture.userID).Error)
			assert.Equal(t, AsyncBillingAcceptedProjectionPending, reservation.AcceptedProjectionState)
			assert.Zero(t, user.UsedQuota)
			assert.Zero(t, user.RequestCount)
		})
	}
}

func TestAcceptAsyncBillingReservationAtomicallyChargesSubmitAdjustment(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, now, now.Add(time.Minute))
	task := &Task{Platform: constant.TaskPlatformSuno, Action: "submit", Status: TaskStatusNotStart}
	accepted, err := AcceptAsyncTaskReservation(
		context.Background(), reservation.ID, 0, task, "provider-adjusted-1", 150,
		asyncBillingFinalAudit(1.5), AsyncBillingReplaySpec{
			StatusCode: 200, ContentType: "application/json", Body: []byte(`{"code":"success"}`),
		}, now.Add(time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, 150, accepted.CurrentQuota)
	assert.Equal(t, 150, accepted.AcceptedQuota)
	assert.Zero(t, task.Quota)
	assert.Equal(t, 150, task.DurableQuota)

	var user User
	var token Token
	require.NoError(t, db.First(&user, reservation.UserID).Error)
	require.NoError(t, db.First(&token, reservation.TokenID).Error)
	assert.Equal(t, 850, user.Quota)
	assert.Equal(t, 850, token.RemainQuota)
	assert.Equal(t, 150, token.UsedQuota)

	var stats BillingStatsProjection
	require.NoError(t, db.Where("reference_id = ? AND kind = ?", reservation.ID, BillingStatsProjectionKindAccepted).First(&stats).Error)
	assert.Equal(t, 150, stats.QuotaDelta)
}

func TestAcceptanceOverageManualWriteoffPreservesFinalAuditAndCharge(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	reservation, _ := createAuthorizedAsyncBillingReservation(t, db, now, now.Add(time.Minute))
	require.NoError(t, db.Model(&User{}).Where("id = ?", reservation.UserID).Update("quota", 0).Error)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", reservation.TokenID).Update("remain_quota", 0).Error)
	task := &Task{Platform: constant.TaskPlatformSuno, Action: "submit", Status: TaskStatusNotStart}
	finalAudit := asyncBillingFinalAudit(1.5)
	_, err := AcceptAsyncTaskReservation(
		context.Background(), reservation.ID, 0, task, "provider-overage-1", 150, finalAudit,
		AsyncBillingReplaySpec{StatusCode: 200, ContentType: "application/json", Body: []byte(`{}`)}, now.Add(time.Second),
	)
	assert.ErrorIs(t, err, ErrAsyncBillingInsufficientQuota)
	review, err := MarkAsyncBillingAcceptanceOverageManualReview(
		context.Background(), reservation.ID, 150, finalAudit, "provider-overage-1", now.Add(2*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReviewKindAcceptanceOverage, review.ManualReviewKind)
	assert.Equal(t, 150, review.ReviewTargetQuota)

	result, err := ResolveAsyncBillingManualReview(context.Background(), AsyncBillingManualDecisionSpec{
		ReservationID: reservation.ID, Action: AsyncBillingManualResolutionRejected, ActorUserID: 9001,
		ExpectedVersion: review.ReviewVersion,
		ExpectedETag:    AsyncBillingManualReviewETag(reservation.ID, review.ReviewVersion),
		UpstreamTaskID:  "provider-overage-1", ProviderStatus: AsyncBillingProviderStatusAccepted,
		ProviderCheckedMs: now.Add(3 * time.Second).UnixMilli(), EvidenceReference: "provider-overage-audit",
		Reason:          "approve reserved amount and write off submit adjustment",
		DecisionKeyHash: strings.Repeat("1", 64), DecisionPayloadHash: strings.Repeat("2", 64),
	}, now.Add(4*time.Second))
	require.NoError(t, err)
	assert.Equal(t, AsyncBillingReservationStateAccepted, result.Reservation.State)
	assert.Equal(t, 100, result.Reservation.CurrentQuota)

	var persisted Task
	require.NoError(t, db.First(&persisted, result.Reservation.TaskID).Error)
	assert.Zero(t, persisted.Quota)
	assert.Equal(t, 100, persisted.DurableQuota)
	assert.Equal(t, 100, persisted.EffectiveBillingQuota())
	require.NotNil(t, persisted.PrivateData.BillingAudit)
	assert.Equal(t, 1.5, persisted.PrivateData.BillingAudit.OtherRatios["quality"])
	assert.Equal(t, "acceptance_overage_writeoff", persisted.PrivateData.BillingAudit.AdminInfo["billing_context_phase"])

	var stats BillingStatsProjection
	require.NoError(t, db.Where("reference_id = ? AND kind = ?", reservation.ID, BillingStatsProjectionKindAccepted).First(&stats).Error)
	assert.Equal(t, 100, stats.QuotaDelta)
}
