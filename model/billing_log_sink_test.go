package model

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

// legacyLogV0110 mirrors the SQL-backed log schema shipped by v0.1.10. Keep
// this fixture constraint-accurate so upgrade tests exercise an existing table
// instead of silently validating only a fresh install.
type legacyLogV0110 struct {
	Id                int   `gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId            int   `gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64 `gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int   `gorm:"index:idx_created_at_type"`
	Content           string
	Username          string `gorm:"index;index:index_username_model_name,priority:2;default:''"`
	TokenName         string `gorm:"index;default:''"`
	ModelName         string `gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota             int    `gorm:"default:0"`
	PromptTokens      int    `gorm:"default:0"`
	CompletionTokens  int    `gorm:"default:0"`
	UseTime           int    `gorm:"default:0"`
	IsStream          bool
	ChannelId         int    `gorm:"index"`
	TokenId           int    `gorm:"default:0;index"`
	Group             string `gorm:"index"`
	Ip                string `gorm:"index;default:''"`
	RequestId         string `gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId string `gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other             string
}

func (legacyLogV0110) TableName() string {
	return "logs"
}

func frozenBillingLogFixture(t *testing.T, operationKey string) (string, string, int) {
	t.Helper()
	payload, payloadHash, protocol, err := freezeBillingLogPayload(operationKey, &Log{
		UserId:           91,
		CreatedAt:        1_800_100_000,
		Type:             LogTypeConsume,
		Content:          "terminal task billing settlement",
		Username:         "snapshot-user",
		TokenName:        "snapshot-token",
		ModelName:        "snapshot-model",
		Quota:            75,
		PromptTokens:     5,
		CompletionTokens: 7,
		ChannelId:        17,
		TokenId:          23,
		Group:            "snapshot-group",
		RequestId:        "request-visible-9001",
		Other:            `{"billing_operation":"` + operationKey + `"}`,
	})
	require.NoError(t, err)
	return payload, payloadHash, protocol
}

func TestFrozenBillingLogPayloadRoundTripRejectsTampering(t *testing.T) {
	operationKey := "task:9001:terminal:v1"
	payload, payloadHash, protocol := frozenBillingLogFixture(t, operationKey)

	entry, err := thawBillingLogPayload(operationKey, payload, payloadHash, protocol)
	require.NoError(t, err)
	assert.Equal(t, "request-visible-9001", entry.RequestId)
	assert.Equal(t, "snapshot-user", entry.Username)
	assert.Equal(t, "snapshot-token", entry.TokenName)
	assert.Equal(t, 75, entry.Quota)
	require.NotNil(t, entry.BillingOperationKey)
	assert.Equal(t, operationKey, *entry.BillingOperationKey)

	_, err = thawBillingLogPayload(operationKey, payload+" ", payloadHash, protocol)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
	_, err = thawBillingLogPayload(operationKey, payload, payloadHash, protocol+1)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
	_, err = thawBillingLogPayload(operationKey+":other", payload, payloadHash, protocol)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
}

func TestFrozenBillingLogPayloadKeepsRequestIDSeparateFromOperationReceipt(t *testing.T) {
	operationKey := "task:9001:request:v1"
	requestID := "req-real-client-visible"
	payload, payloadHash, protocol, err := freezeBillingLogPayload(operationKey, &Log{
		UserId: 1, CreatedAt: 1_800_100_000, Type: LogTypeConsume, Quota: 1, RequestId: requestID,
	})
	require.NoError(t, err)
	entry, err := thawBillingLogPayload(operationKey, payload, payloadHash, protocol)
	require.NoError(t, err)
	assert.Equal(t, requestID, entry.RequestId)
	require.NotNil(t, entry.BillingOperationKey)
	assert.Equal(t, operationKey, *entry.BillingOperationKey)
}

func TestFrozenBillingLogPayloadRejectsInvalidUTF8Fields(t *testing.T) {
	operationKey := "task:9001:utf8:v1"
	invalid := string([]byte{'a', 0xff, 'b'})
	tests := []struct {
		name  string
		entry *Log
	}{
		{name: "content", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, Content: invalid}},
		{name: "username", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, Username: invalid}},
		{name: "token name", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, TokenName: invalid}},
		{name: "model", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, ModelName: invalid}},
		{name: "group", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, Group: invalid}},
		{name: "ip", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, Ip: invalid}},
		{name: "upstream request id", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, UpstreamRequestId: invalid}},
		{name: "other", entry: &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume, Other: invalid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := freezeBillingLogPayload(operationKey, test.entry)
			assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
		})
	}
	_, _, _, err := freezeBillingLogPayload(invalid, &Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume})
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
}

func TestFrozenBillingLogPayloadEnforcesFieldAndEncodedSizeBoundaries(t *testing.T) {
	operationKey := "task:9001:size:v1"
	entry := &Log{
		UserId:    1,
		CreatedAt: 1,
		Type:      LogTypeConsume,
		Other:     strings.Repeat("x", maxBillingLogOtherBytes),
	}
	payload, _, _, err := freezeBillingLogPayload(operationKey, entry)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(payload), maxBillingLogPayloadBytes)

	entry.Other += "x"
	_, _, _, err = freezeBillingLogPayload(operationKey, entry)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)

	entry.Other = strings.Repeat("x", 52<<10)
	entry.Content = ""
	baseline, _, _, err := freezeBillingLogPayload(operationKey, entry)
	require.NoError(t, err)
	padding := maxBillingLogPayloadBytes - len(baseline)
	require.Positive(t, padding)
	require.LessOrEqual(t, padding, maxBillingLogContentBytes)
	entry.Content = strings.Repeat("x", padding)
	payload, payloadHash, protocol, err := freezeBillingLogPayload(operationKey, entry)
	require.NoError(t, err)
	assert.Len(t, payload, maxBillingLogPayloadBytes)
	_, err = thawBillingLogPayload(operationKey, payload, payloadHash, protocol)
	require.NoError(t, err)

	entry.Content += "x"
	_, _, _, err = freezeBillingLogPayload(operationKey, entry)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
}

func TestFrozenBillingLogPayloadRejectsValuesOutsideSinkStorageRange(t *testing.T) {
	operationKey := "task:9001:numeric-bounds:v1"
	entry := &Log{
		UserId: 1, CreatedAt: 1, Type: LogTypeConsume, Quota: common.MaxQuota,
		PromptTokens: math.MaxInt32, CompletionTokens: math.MaxInt32, UseTime: math.MaxInt32,
		ChannelId: math.MaxInt32, TokenId: math.MaxInt32,
	}
	_, _, _, err := freezeBillingLogPayload(operationKey, entry)
	require.NoError(t, err)

	entry.Type = LogTypeManage
	_, _, _, err = freezeBillingLogPayload(operationKey, entry)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
	entry.Type = LogTypeConsume

	if strconv.IntSize < 64 {
		return
	}
	overflow := int(math.MaxInt32) + 1
	tests := []struct {
		name   string
		mutate func(*Log)
	}{
		{name: "user id", mutate: func(log *Log) { log.UserId = overflow }},
		{name: "prompt tokens", mutate: func(log *Log) { log.PromptTokens = overflow }},
		{name: "completion tokens", mutate: func(log *Log) { log.CompletionTokens = overflow }},
		{name: "use time", mutate: func(log *Log) { log.UseTime = overflow }},
		{name: "channel id", mutate: func(log *Log) { log.ChannelId = overflow }},
		{name: "token id", mutate: func(log *Log) { log.TokenId = overflow }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := *entry
			test.mutate(&candidate)
			_, _, _, err := freezeBillingLogPayload(operationKey, &candidate)
			assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
		})
	}
}

func TestThawBillingLogPayloadRevalidatesFrozenFields(t *testing.T) {
	operationKey := "task:9001:revalidate:v1"
	frozen := billingLogPayload{
		UserID:    1,
		CreatedAt: 1,
		Type:      LogTypeConsume,
		Content:   strings.Repeat("x", maxBillingLogContentBytes+1),
		RequestID: operationKey,
	}
	payloadBytes, err := common.Marshal(frozen)
	require.NoError(t, err)
	payload := string(payloadBytes)
	payloadHash := billingLogPayloadHash(operationKey, billingLogPayloadProtocol, payload)

	_, err = thawBillingLogPayload(operationKey, payload, payloadHash, billingLogPayloadProtocol)
	assert.ErrorIs(t, err, ErrBillingLogPayloadInvalid)
}

func TestSQLBillingLogSinkConcurrentRetriesCreateOneReceipt(t *testing.T) {
	truncateTables(t)
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	operationKey := "task:9002:terminal:v1"
	payload, payloadHash, protocol := frozenBillingLogFixture(t, operationKey)

	const workers = 16
	errCh := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errCh <- writeFrozenBillingLog(context.Background(), operationKey, payload, payloadHash, protocol)
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", operationKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var stored Log
	require.NoError(t, LOG_DB.Where("billing_operation_key = ?", operationKey).First(&stored).Error)
	assert.Equal(t, payloadHash, stored.BillingPayloadHash)
	assert.Equal(t, protocol, stored.BillingPayloadProtocol)
}

func TestSQLBillingLogSinkAckLossReplayUsesExistingReceipt(t *testing.T) {
	truncateTables(t)
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	operationKey := "midjourney:9003:refund:v1"
	payload, payloadHash, protocol := frozenBillingLogFixture(t, operationKey)

	// The first call represents a committed sink write whose main-db ack was
	// lost. The replay must verify the immutable receipt and remain a no-op.
	require.NoError(t, writeFrozenBillingLog(context.Background(), operationKey, payload, payloadHash, protocol))
	require.NoError(t, writeFrozenBillingLog(context.Background(), operationKey, payload, payloadHash, protocol))

	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", operationKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestSQLBillingLogSinkRejectsDifferentHashWithoutOverwrite(t *testing.T) {
	truncateTables(t)
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	operationKey := "task:9004:terminal:v1"
	payload, payloadHash, protocol := frozenBillingLogFixture(t, operationKey)
	conflictingKey := operationKey
	require.NoError(t, LOG_DB.Create(&Log{
		CreatedAt:              1_800_100_001,
		RequestId:              operationKey,
		BillingOperationKey:    &conflictingKey,
		BillingPayloadHash:     fmt.Sprintf("%064x", 1),
		BillingPayloadProtocol: protocol,
	}).Error)

	err := writeFrozenBillingLog(context.Background(), operationKey, payload, payloadHash, protocol)
	assert.ErrorIs(t, err, ErrBillingLogPayloadConflict)
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", operationKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var stored Log
	require.NoError(t, LOG_DB.Where("billing_operation_key = ?", operationKey).First(&stored).Error)
	assert.NotEqual(t, payloadHash, stored.BillingPayloadHash)
}

func TestBillingLogSchemaReadinessRequiresUniqueReceiptIndex(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	ready, err := billingLogSchemaReady(LOG_DB)
	require.NoError(t, err)
	assert.True(t, ready)
	require.NoError(t, waitBillingLogSchemaReady(context.Background(), LOG_DB, 0))
}

func TestMigrateLOGDBUpgradesV0110SQLiteLogs(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&legacyLogV0110{}))
	require.NoError(t, db.Create(&legacyLogV0110{
		UserId: 17, CreatedAt: 1_720_000_000, Type: LogTypeManage,
		Content: "v0.1.10 retained log", RequestId: "legacy-log-request",
	}).Error)

	require.NoError(t, migrateLOGDB())
	require.NoError(t, migrateLOGDB(), "v0.1.10 log upgrade must be restart-safe")

	var retained Log
	require.NoError(t, db.Where("request_id = ?", "legacy-log-request").First(&retained).Error)
	assert.Equal(t, "v0.1.10 retained log", retained.Content)
	assert.Nil(t, retained.BillingOperationKey)
	assert.Empty(t, retained.BillingPayloadHash)
	assert.Zero(t, retained.BillingPayloadProtocol)
	assert.Zero(t, retained.BillingSinkWrittenAt)
	ready, err := billingLogSchemaReady(db)
	require.NoError(t, err)
	assert.True(t, ready)

	receiptKey := "task:legacy-upgrade:terminal:v1"
	require.NoError(t, db.Create(&Log{CreatedAt: 1_720_000_001, BillingOperationKey: &receiptKey}).Error)
	err = db.Create(&Log{CreatedAt: 1_720_000_002, BillingOperationKey: &receiptKey}).Error
	assert.Error(t, err, "the post-upgrade receipt index must reject duplicate non-NULL keys")
	require.NoError(t, db.Create(&Log{CreatedAt: 1_720_000_003}).Error)
	require.NoError(t, db.Create(&Log{CreatedAt: 1_720_000_004}).Error)
}

func TestBillingLogSchemaReadinessRejectsNonUniqueOrNonNullableReceipt(t *testing.T) {
	previousLogType := common.LogDatabaseType()
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetLogDatabaseType(previousLogType) })

	tests := []struct {
		name        string
		columnSQL   string
		indexSQL    string
		expectReady bool
	}{
		{
			name:      "non unique",
			columnSQL: "billing_operation_key varchar(191)",
			indexSQL:  "CREATE INDEX " + billingLogOperationKeyIndex + " ON logs(billing_operation_key)",
		},
		{
			name:      "not nullable",
			columnSQL: "billing_operation_key varchar(191) NOT NULL",
			indexSQL:  "CREATE UNIQUE INDEX " + billingLogOperationKeyIndex + " ON logs(billing_operation_key)",
		},
		{
			name:      "unique wrong column",
			columnSQL: "billing_operation_key varchar(191)",
			indexSQL:  "CREATE UNIQUE INDEX " + billingLogOperationKeyIndex + " ON logs(billing_payload_hash)",
		},
		{
			name:      "partial unique",
			columnSQL: "billing_operation_key varchar(191)",
			indexSQL: "CREATE UNIQUE INDEX " + billingLogOperationKeyIndex +
				" ON logs(billing_operation_key) WHERE billing_operation_key LIKE 'async:%'",
		},
		{
			name:        "unique nullable",
			columnSQL:   "billing_operation_key varchar(191)",
			indexSQL:    "CREATE UNIQUE INDEX " + billingLogOperationKeyIndex + " ON logs(billing_operation_key)",
			expectReady: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(t.TempDir()+"/schema.db"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.Exec(`CREATE TABLE logs (
				`+test.columnSQL+`,
				billing_payload_hash varchar(64),
				billing_payload_protocol integer,
				billing_sink_written_at bigint
			)`).Error)
			require.NoError(t, db.Exec(test.indexSQL).Error)
			ready, err := billingLogSchemaReady(db)
			require.NoError(t, err)
			assert.Equal(t, test.expectReady, ready)
		})
	}
}

func TestSQLBillingLogSinkExternalDatabaseCompatibility(t *testing.T) {
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
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			if db.Migrator().HasTable(&Log{}) {
				t.Skip("refusing to use a non-empty external log database")
			}
			t.Cleanup(func() { _ = db.Migrator().DropTable(&Log{}) })
			require.NoError(t, db.AutoMigrate(&legacyLogV0110{}))
			require.NoError(t, db.Create(&legacyLogV0110{
				UserId: 18, CreatedAt: 1_720_000_000, Type: LogTypeManage,
				Content: "v0.1.10 external retained log", RequestId: "legacy-external-log",
			}).Error)
			previousLogDB := LOG_DB
			previousLogType := common.LogDatabaseType()
			LOG_DB = db
			common.SetLogDatabaseType(test.dbType)
			t.Cleanup(func() {
				LOG_DB = previousLogDB
				common.SetLogDatabaseType(previousLogType)
			})
			require.NoError(t, migrateLOGDB())
			require.NoError(t, migrateLOGDB(), "billing log migration must be idempotent")
			var retained Log
			require.NoError(t, db.Where("request_id = ?", "legacy-external-log").First(&retained).Error)
			assert.Equal(t, "v0.1.10 external retained log", retained.Content)
			assert.Nil(t, retained.BillingOperationKey)

			// Multiple ordinary logs keep NULL receipt keys and must not collide.
			require.NoError(t, db.Create(&Log{CreatedAt: 1_800_200_000, RequestId: "ordinary-a"}).Error)
			require.NoError(t, db.Create(&Log{CreatedAt: 1_800_200_001, RequestId: "ordinary-b"}).Error)

			operationKey := "task:external:terminal:v1"
			payload, payloadHash, protocol := frozenBillingLogFixture(t, operationKey)
			const workers = 8
			errCh := make(chan error, workers)
			var wait sync.WaitGroup
			for index := 0; index < workers; index++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					errCh <- writeFrozenBillingLog(context.Background(), operationKey, payload, payloadHash, protocol)
				}()
			}
			wait.Wait()
			close(errCh)
			for err := range errCh {
				require.NoError(t, err)
			}
			var billingCount int64
			require.NoError(t, db.Model(&Log{}).Where("billing_operation_key = ?", operationKey).Count(&billingCount).Error)
			assert.Equal(t, int64(1), billingCount)
			var ordinaryCount int64
			require.NoError(t, db.Model(&Log{}).Where("billing_operation_key IS NULL").Count(&ordinaryCount).Error)
			assert.Equal(t, int64(3), ordinaryCount)
		})
	}
}

func TestClickHouseBillingLogSinkVisibleExactlyOnce(t *testing.T) {
	dsn := os.Getenv("ROUTING_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("ROUTING_TEST_CLICKHOUSE_DSN is not set")
	}
	db, err := gorm.Open(clickhouse.Open(normalizeClickHouseDSN(dsn)), &gorm.Config{PrepareStmt: false})
	require.NoError(t, err)
	var existing int64
	require.NoError(t, db.Raw(`SELECT count() FROM system.tables
WHERE database = currentDatabase() AND name IN ('logs', ?)`, clickHouseVisibleLogsView).Scan(&existing).Error)
	if existing != 0 {
		t.Skip("refusing to use a ClickHouse database that already contains billing log tables")
	}

	previousLogDB := LOG_DB
	previousLogType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeClickHouse)
	t.Cleanup(func() {
		_ = db.Exec("DROP VIEW IF EXISTS " + clickHouseVisibleLogsView).Error
		_ = db.Exec("DROP TABLE IF EXISTS logs SYNC").Error
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
	})
	require.NoError(t, migrateClickHouseLogDB())
	require.NoError(t, db.Create(&Log{CreatedAt: 1_800_099_998, Type: LogTypeManage, RequestId: "ordinary-a"}).Error)
	require.NoError(t, db.Create(&Log{CreatedAt: 1_800_099_999, Type: LogTypeManage, RequestId: "ordinary-b"}).Error)

	operationKey := "task:clickhouse:terminal:v1"
	payload, payloadHash, protocol := frozenBillingLogFixture(t, operationKey)
	entry, err := thawBillingLogPayload(operationKey, payload, payloadHash, protocol)
	require.NoError(t, err)
	entry.BillingSinkWrittenAt = time.Now().Unix()
	require.NoError(t, db.Create(entry).Error)
	entry.Id = 0
	require.NoError(t, db.Create(entry).Error)

	var physicalCount int64
	require.NoError(t, db.Table("logs").Where("billing_operation_key = ?", operationKey).Count(&physicalCount).Error)
	assert.Equal(t, int64(2), physicalCount)
	var retainedKeys []string
	require.NoError(t, db.Table("logs").Distinct("billing_operation_key").
		Where("billing_operation_key IN ?", []string{operationKey, "missing-operation"}).
		Pluck("billing_operation_key", &retainedKeys).Error)
	assert.Equal(t, []string{operationKey}, retainedKeys)
	var visibleCount int64
	require.NoError(t, db.Table(clickHouseVisibleLogsView).Where("billing_operation_key = ?", operationKey).Count(&visibleCount).Error)
	assert.Equal(t, int64(1), visibleCount)
	require.NoError(t, writeFrozenBillingLog(context.Background(), operationKey, payload, payloadHash, protocol))
	conflicts, err := AuditBillingLogSinkConflicts(context.Background(), time.Now().Add(-time.Minute))
	require.NoError(t, err)
	assert.Empty(t, conflicts)

	allLogs, total, err := GetAllLogs(
		LogTypeUnknown, 0, 0, "", "", "", 0, 20, 0, "", "request-visible-9001", "",
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, allLogs, 1)
	userLogs, userTotal, err := GetUserLogs(91, LogTypeUnknown, 0, 0, "", "", 0, 20, "", "request-visible-9001", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), userTotal)
	require.Len(t, userLogs, 1)
	tokenLogs, err := GetLogByTokenId(23)
	require.NoError(t, err)
	require.Len(t, tokenLogs, 1)
	stat, err := SumUsedQuota(LogTypeUnknown, 0, 0, "snapshot-model", "snapshot-user", "snapshot-token", 17, "snapshot-group")
	require.NoError(t, err)
	assert.Equal(t, 75, stat.Quota)
	assert.Equal(t, 12, SumUsedToken(LogTypeUnknown, 0, 0, "snapshot-model", "snapshot-user", "snapshot-token"))

	conflict := *entry
	conflict.BillingPayloadHash = fmt.Sprintf("%064x", 2)
	require.NoError(t, db.Create(&conflict).Error)
	err = verifyBillingLogSinkReceipt(context.Background(), operationKey, payloadHash, protocol)
	assert.ErrorIs(t, err, ErrBillingLogPayloadConflict)
	conflicts, err = AuditBillingLogSinkConflicts(context.Background(), time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, operationKey, conflicts[0].OperationKey)
	assert.Equal(t, int64(3), conflicts[0].PhysicalRows)
	assert.Contains(t, conflicts[0].Receipts, payloadHash)
	require.NoError(t, db.Table(clickHouseVisibleLogsView).Where("billing_operation_key = ?", operationKey).Count(&visibleCount).Error)
	assert.Zero(t, visibleCount, "conflicting receipts must be quarantined from visible billing logs")
	allLogs, total, err = GetAllLogs(LogTypeUnknown, 0, 0, "", "", "", 0, 20, 0, "", "request-visible-9001", "")
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, allLogs)

	secondKey := "task:clickhouse:terminal:v2"
	secondA := *entry
	secondA.Id = 0
	secondA.RequestId = "request-visible-9002"
	secondA.BillingOperationKey = &secondKey
	secondA.BillingPayloadHash = fmt.Sprintf("%064x", 3)
	secondB := secondA
	secondB.BillingPayloadHash = fmt.Sprintf("%064x", 4)
	require.NoError(t, db.Create(&secondA).Error)
	require.NoError(t, db.Create(&secondB).Error)
	auditThrough, err := BillingLogSinkAuditWindowEnd(context.Background())
	require.NoError(t, err)
	page, err := AuditBillingLogSinkConflictsPage(
		context.Background(), time.Now().Add(-time.Minute), auditThrough, "", 1,
	)
	require.NoError(t, err)
	require.Len(t, page.Conflicts, 1)
	assert.True(t, page.HasMore)
	assert.Equal(t, operationKey, page.NextOperationKey)
	assert.Equal(t, int64(2), page.Conflicts[0].DistinctReceipts)
	page, err = AuditBillingLogSinkConflictsPage(
		context.Background(), time.Now().Add(-time.Minute), auditThrough, page.NextOperationKey, 1,
	)
	require.NoError(t, err)
	require.Len(t, page.Conflicts, 1)
	assert.Equal(t, secondKey, page.Conflicts[0].OperationKey)
	assert.False(t, page.HasMore)
	assert.Empty(t, page.NextOperationKey)
}
