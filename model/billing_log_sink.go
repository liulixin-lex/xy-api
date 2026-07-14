package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	billingLogPayloadProtocol         = 1
	billingLogSchemaReadyWaitEnv      = "BILLING_LOG_SCHEMA_READY_WAIT_SECONDS"
	billingLogSchemaReadyWaitDefault  = 60
	clickHouseVisibleLogsView         = "logs_visible_v1"
	billingLogOperationKeyIndex       = "uidx_logs_billing_operation_key"
	maxBillingLogOperationKeyBytes    = 64
	billingLogPayloadHashEncodedBytes = sha256.Size * 2
	// Frozen payloads are persisted in a cross-database TEXT column. Keep the
	// encoded document below MySQL's 64 KiB TEXT ceiling with room for storage
	// and driver differences.
	maxBillingLogPayloadBytes           = 60 << 10
	maxBillingLogContentBytes           = 8 << 10
	maxBillingLogOtherBytes             = 56 << 10
	maxBillingLogIndexedIdentityBytes   = 191
	maxBillingLogGroupBytes             = 64
	maxBillingLogIPBytes                = 128
	maxBillingLogUpstreamRequestIDBytes = 128
	maxBillingLogConflictsPerAuditPage  = 1000
	maxBillingLogConflictReceiptSamples = 32
)

var (
	ErrBillingLogPayloadInvalid  = errors.New("billing log payload is invalid")
	ErrBillingLogPayloadConflict = errors.New("billing log payload conflicts with the sink receipt")
)

// billingLogPayload is the immutable, protocol-versioned projection frozen in
// the authoritative main-database operation before any external log write.
type billingLogPayload struct {
	UserID            int    `json:"user_id"`
	CreatedAt         int64  `json:"created_at"`
	Type              int    `json:"type"`
	Content           string `json:"content"`
	Username          string `json:"username"`
	TokenName         string `json:"token_name"`
	ModelName         string `json:"model_name"`
	Quota             int    `json:"quota"`
	PromptTokens      int    `json:"prompt_tokens"`
	CompletionTokens  int    `json:"completion_tokens"`
	UseTime           int    `json:"use_time"`
	IsStream          bool   `json:"is_stream"`
	ChannelID         int    `json:"channel_id"`
	TokenID           int    `json:"token_id"`
	Group             string `json:"group"`
	IP                string `json:"ip"`
	RequestID         string `json:"request_id"`
	UpstreamRequestID string `json:"upstream_request_id"`
	Other             string `json:"other"`
}

func freezeBillingLogPayload(operationKey string, entry *Log) (string, string, int, error) {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" || len(operationKey) > maxBillingLogOperationKeyBytes || !utf8.ValidString(operationKey) || entry == nil {
		return "", "", 0, ErrBillingLogPayloadInvalid
	}
	payload := billingLogPayload{
		UserID:            entry.UserId,
		CreatedAt:         entry.CreatedAt,
		Type:              entry.Type,
		Content:           entry.Content,
		Username:          entry.Username,
		TokenName:         entry.TokenName,
		ModelName:         entry.ModelName,
		Quota:             entry.Quota,
		PromptTokens:      entry.PromptTokens,
		CompletionTokens:  entry.CompletionTokens,
		UseTime:           entry.UseTime,
		IsStream:          entry.IsStream,
		ChannelID:         entry.ChannelId,
		TokenID:           entry.TokenId,
		Group:             entry.Group,
		IP:                entry.Ip,
		RequestID:         entry.RequestId,
		UpstreamRequestID: entry.UpstreamRequestId,
		Other:             entry.Other,
	}
	if err := validateBillingLogPayloadFields(&payload); err != nil {
		return "", "", 0, err
	}
	payloadBytes, err := common.Marshal(payload)
	if err != nil {
		return "", "", 0, fmt.Errorf("%w: marshal payload", ErrBillingLogPayloadInvalid)
	}
	if len(payloadBytes) > maxBillingLogPayloadBytes || !utf8.Valid(payloadBytes) {
		return "", "", 0, ErrBillingLogPayloadInvalid
	}
	hash := billingLogPayloadHash(operationKey, billingLogPayloadProtocol, string(payloadBytes))
	return string(payloadBytes), hash, billingLogPayloadProtocol, nil
}

func validateBillingLogPayloadFields(payload *billingLogPayload) error {
	if payload == nil || payload.UserID <= 0 || payload.CreatedAt <= 0 ||
		int64(payload.UserID) > math.MaxInt32 ||
		(payload.Type != LogTypeConsume && payload.Type != LogTypeRefund) ||
		payload.Quota < 0 || payload.Quota > common.MaxQuota ||
		payload.PromptTokens < 0 || int64(payload.PromptTokens) > math.MaxInt32 ||
		payload.CompletionTokens < 0 || int64(payload.CompletionTokens) > math.MaxInt32 ||
		payload.UseTime < 0 || int64(payload.UseTime) > math.MaxInt32 ||
		payload.ChannelID < 0 || int64(payload.ChannelID) > math.MaxInt32 ||
		payload.TokenID < 0 || int64(payload.TokenID) > math.MaxInt32 {
		return ErrBillingLogPayloadInvalid
	}
	fields := []struct {
		value    string
		maxBytes int
	}{
		{payload.Content, maxBillingLogContentBytes},
		{payload.Username, maxBillingLogIndexedIdentityBytes},
		{payload.TokenName, maxBillingLogIndexedIdentityBytes},
		{payload.ModelName, maxBillingLogIndexedIdentityBytes},
		{payload.Group, maxBillingLogGroupBytes},
		{payload.IP, maxBillingLogIPBytes},
		{payload.RequestID, maxBillingLogOperationKeyBytes},
		{payload.UpstreamRequestID, maxBillingLogUpstreamRequestIDBytes},
		{payload.Other, maxBillingLogOtherBytes},
	}
	for _, field := range fields {
		if len(field.value) > field.maxBytes || !utf8.ValidString(field.value) {
			return ErrBillingLogPayloadInvalid
		}
	}
	return nil
}

func billingLogPayloadHash(operationKey string, protocol int, payload string) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(strconv.Itoa(protocol)))
	_, _ = hasher.Write([]byte{'\n'})
	_, _ = hasher.Write([]byte(operationKey))
	_, _ = hasher.Write([]byte{'\n'})
	_, _ = hasher.Write([]byte(payload))
	return hex.EncodeToString(hasher.Sum(nil))
}

func thawBillingLogPayload(operationKey, payload, payloadHash string, protocol int) (*Log, error) {
	operationKey = strings.TrimSpace(operationKey)
	payloadHash = strings.ToLower(strings.TrimSpace(payloadHash))
	if protocol != billingLogPayloadProtocol || operationKey == "" || len(operationKey) > maxBillingLogOperationKeyBytes ||
		!utf8.ValidString(operationKey) || len(payload) > maxBillingLogPayloadBytes || !utf8.ValidString(payload) ||
		len(payloadHash) != billingLogPayloadHashEncodedBytes ||
		billingLogPayloadHash(operationKey, protocol, payload) != payloadHash {
		return nil, ErrBillingLogPayloadInvalid
	}
	var frozen billingLogPayload
	if err := common.UnmarshalJsonStr(payload, &frozen); err != nil {
		return nil, fmt.Errorf("%w: decode payload: %v", ErrBillingLogPayloadInvalid, err)
	}
	if err := validateBillingLogPayloadFields(&frozen); err != nil {
		return nil, err
	}
	key := operationKey
	return &Log{
		UserId:                 frozen.UserID,
		CreatedAt:              frozen.CreatedAt,
		Type:                   frozen.Type,
		Content:                frozen.Content,
		Username:               frozen.Username,
		TokenName:              frozen.TokenName,
		ModelName:              frozen.ModelName,
		Quota:                  frozen.Quota,
		PromptTokens:           frozen.PromptTokens,
		CompletionTokens:       frozen.CompletionTokens,
		UseTime:                frozen.UseTime,
		IsStream:               frozen.IsStream,
		ChannelId:              frozen.ChannelID,
		TokenId:                frozen.TokenID,
		Group:                  frozen.Group,
		Ip:                     frozen.IP,
		RequestId:              frozen.RequestID,
		UpstreamRequestId:      frozen.UpstreamRequestID,
		Other:                  frozen.Other,
		BillingOperationKey:    &key,
		BillingPayloadHash:     payloadHash,
		BillingPayloadProtocol: protocol,
	}, nil
}

func snapshotBillingLogNames(tx *gorm.DB, userID, tokenID int) (string, string, error) {
	if tx == nil || userID <= 0 {
		return "", "", ErrBillingLogPayloadInvalid
	}
	username := ""
	var user User
	if err := tx.Unscoped().Select("id", "username").Where("id = ?", userID).First(&user).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", err
		}
	} else {
		username = user.Username
	}
	tokenName := ""
	if tokenID > 0 {
		var token Token
		if err := tx.Unscoped().Select("id", "name").Where("id = ?", tokenID).First(&token).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return "", "", err
			}
		} else {
			tokenName = token.Name
		}
	}
	return username, tokenName, nil
}

type billingLogSinkReceipt struct {
	BillingPayloadHash     string `gorm:"column:billing_payload_hash"`
	BillingPayloadProtocol int    `gorm:"column:billing_payload_protocol"`
}

type BillingLogSinkConflict struct {
	OperationKey     string `json:"operation_key" gorm:"column:operation_key"`
	Receipts         string `json:"receipts" gorm:"column:receipts"`
	DistinctReceipts int64  `json:"distinct_receipts" gorm:"column:distinct_receipts"`
	PhysicalRows     int64  `json:"physical_rows" gorm:"column:physical_rows"`
}

type BillingLogSinkConflictPage struct {
	Conflicts        []BillingLogSinkConflict
	NextOperationKey string
	HasMore          bool
}

// BillingLogSinkAuditWindowEnd uses the log database clock so rolling nodes
// with small wall-clock skew agree on a closed audit window.
func BillingLogSinkAuditWindowEnd(ctx context.Context) (time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return time.Now().Add(time.Second).Truncate(time.Second), nil
	}
	if LOG_DB == nil {
		return time.Time{}, errors.New("billing log database is unavailable")
	}
	var unixSeconds int64
	if err := LOG_DB.WithContext(ctx).Raw("SELECT toUnixTimestamp(now()) + 1").Scan(&unixSeconds).Error; err != nil {
		return time.Time{}, err
	}
	if unixSeconds <= 0 {
		return time.Time{}, errors.New("billing log database returned an invalid audit clock")
	}
	return time.Unix(unixSeconds, 0), nil
}

// AuditBillingLogSinkConflictsPage catches the narrow ClickHouse race where
// one writer acknowledges before a conflicting append becomes visible. The
// time window is fixed while operation-key pagination advances, and the outer
// aggregation still includes every historical raw receipt for each candidate.
func AuditBillingLogSinkConflictsPage(
	ctx context.Context,
	insertedAfter time.Time,
	insertedBefore time.Time,
	afterOperationKey string,
	limit int,
) (BillingLogSinkConflictPage, error) {
	page := BillingLogSinkConflictPage{}
	if ctx == nil {
		ctx = context.Background()
	}
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return page, nil
	}
	if LOG_DB == nil {
		return page, errors.New("billing log database is unavailable")
	}
	afterOperationKey = strings.TrimSpace(afterOperationKey)
	if insertedAfter.IsZero() || insertedBefore.IsZero() || !insertedAfter.Before(insertedBefore) ||
		len(afterOperationKey) > maxBillingLogOperationKeyBytes || !utf8.ValidString(afterOperationKey) {
		return page, errors.New("billing log conflict audit window is invalid")
	}
	if limit <= 0 || limit > maxBillingLogConflictsPerAuditPage {
		limit = maxBillingLogConflictsPerAuditPage
	}
	var conflicts []BillingLogSinkConflict
	err := LOG_DB.WithContext(ctx).Raw(`
SELECT
	assumeNotNull(billing_operation_key) AS operation_key,
	arrayStringConcat(arraySort(groupUniqArray(?)(concat('v', toString(billing_payload_protocol), ':', billing_payload_hash))), ',') AS receipts,
	uniqExact(tuple(billing_payload_protocol, billing_payload_hash)) AS distinct_receipts,
	count() AS physical_rows
FROM logs
WHERE assumeNotNull(billing_operation_key) > ?
	AND assumeNotNull(billing_operation_key) IN
(
	SELECT assumeNotNull(billing_operation_key)
	FROM logs
	WHERE billing_operation_key IS NOT NULL
		AND billing_sink_written_at >= ? AND billing_sink_written_at < ?
	GROUP BY billing_operation_key
)
GROUP BY billing_operation_key
HAVING uniqExact(tuple(billing_payload_protocol, billing_payload_hash)) > 1
ORDER BY operation_key
LIMIT ?`, maxBillingLogConflictReceiptSamples, afterOperationKey, insertedAfter.Unix(),
		insertedBefore.Unix(), limit+1).Scan(&conflicts).Error
	if err != nil {
		return page, err
	}
	page.HasMore = len(conflicts) > limit
	if page.HasMore {
		conflicts = conflicts[:limit]
	}
	page.Conflicts = conflicts
	if page.HasMore && len(conflicts) > 0 {
		page.NextOperationKey = conflicts[len(conflicts)-1].OperationKey
	}
	for _, conflict := range conflicts {
		logger.LogWarn(ctx, fmt.Sprintf(
			"billing log raw receipt conflict: operation=%s receipts=%s distinct_receipts=%d physical_rows=%d",
			conflict.OperationKey, conflict.Receipts, conflict.DistinctReceipts, conflict.PhysicalRows,
		))
	}
	return page, nil
}

func AuditBillingLogSinkConflicts(ctx context.Context, insertedAfter time.Time) ([]BillingLogSinkConflict, error) {
	if insertedAfter.IsZero() {
		insertedAfter = time.Now().Add(-24 * time.Hour)
	}
	insertedBefore, err := BillingLogSinkAuditWindowEnd(ctx)
	if err != nil {
		return nil, err
	}
	page, err := AuditBillingLogSinkConflictsPage(
		ctx, insertedAfter, insertedBefore, "", maxBillingLogConflictsPerAuditPage,
	)
	return page.Conflicts, err
}

func BillingLogSinkConflictAuditEnabled() bool {
	return LOG_DB != nil && common.UsingLogDatabase(common.DatabaseTypeClickHouse)
}

func writeFrozenBillingLog(ctx context.Context, operationKey, payload, payloadHash string, protocol int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if LOG_DB == nil {
		return errors.New("billing log database is unavailable")
	}
	entry, err := thawBillingLogPayload(operationKey, payload, payloadHash, protocol)
	if err != nil {
		return err
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		windowEnd, err := BillingLogSinkAuditWindowEnd(ctx)
		if err != nil {
			return err
		}
		entry.BillingSinkWrittenAt = windowEnd.Add(-time.Second).Unix()
		return writeFrozenBillingLogClickHouse(ctx, entry, operationKey, payloadHash, protocol)
	}
	entry.BillingSinkWrittenAt = time.Now().Unix()

	result := LOG_DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "billing_operation_key"}},
		DoNothing: true,
	}).Create(entry)
	if result.Error != nil {
		return result.Error
	}
	return verifyBillingLogSinkReceipt(ctx, operationKey, payloadHash, protocol)
}

func writeFrozenBillingLogClickHouse(
	ctx context.Context,
	entry *Log,
	operationKey string,
	payloadHash string,
	protocol int,
) error {
	receipts, err := getBillingLogSinkReceipts(ctx, operationKey)
	if err != nil {
		return err
	}
	if len(receipts) > 0 {
		return validateBillingLogSinkReceipts(ctx, operationKey, receipts, payloadHash, protocol)
	}
	if err := LOG_DB.WithContext(ctx).Create(entry).Error; err != nil {
		return err
	}
	// MergeTree cannot enforce a unique key. Re-read after append so a racing
	// conflicting writer is never acknowledged; identical retries are collapsed
	// by logs_visible_v1 for all user-visible reads and statistics.
	receipts, err = getBillingLogSinkReceipts(ctx, operationKey)
	if err != nil {
		return err
	}
	return validateBillingLogSinkReceipts(ctx, operationKey, receipts, payloadHash, protocol)
}

func verifyBillingLogSinkReceipt(ctx context.Context, operationKey, payloadHash string, protocol int) error {
	receipts, err := getBillingLogSinkReceipts(ctx, operationKey)
	if err != nil {
		return err
	}
	return validateBillingLogSinkReceipts(ctx, operationKey, receipts, payloadHash, protocol)
}

func getBillingLogSinkReceipts(ctx context.Context, operationKey string) ([]billingLogSinkReceipt, error) {
	var receipts []billingLogSinkReceipt
	err := LOG_DB.WithContext(ctx).Table("logs").
		Select("billing_payload_hash, billing_payload_protocol").
		Where("billing_operation_key = ?", operationKey).
		Group("billing_payload_hash, billing_payload_protocol").
		Find(&receipts).Error
	return receipts, err
}

func validateBillingLogSinkReceipts(
	ctx context.Context,
	operationKey string,
	receipts []billingLogSinkReceipt,
	payloadHash string,
	protocol int,
) error {
	if len(receipts) == 1 && strings.EqualFold(receipts[0].BillingPayloadHash, payloadHash) &&
		receipts[0].BillingPayloadProtocol == protocol {
		return nil
	}
	stored := make([]string, 0, len(receipts))
	for _, receipt := range receipts {
		stored = append(stored, fmt.Sprintf("v%d:%s", receipt.BillingPayloadProtocol, receipt.BillingPayloadHash))
	}
	err := fmt.Errorf("%w: operation=%s expected=v%d:%s stored=%s",
		ErrBillingLogPayloadConflict, operationKey, protocol, payloadHash, strings.Join(stored, ","))
	logger.LogWarn(ctx, err.Error())
	return err
}

func billingLogReadTable() string {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return clickHouseVisibleLogsView
	}
	return "logs"
}

func billingLogReadQuery() *gorm.DB {
	return LOG_DB.Table(billingLogReadTable() + " AS logs")
}

func billingLogSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, errors.New("billing log database is unavailable")
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		var columns []struct {
			Name string `gorm:"column:name"`
			Type string `gorm:"column:type"`
		}
		if err := db.Raw(`SELECT name, type FROM system.columns
WHERE database = currentDatabase() AND table = 'logs'
			AND name IN ('billing_operation_key', 'billing_payload_hash', 'billing_payload_protocol', 'billing_sink_written_at')`).Scan(&columns).Error; err != nil {
			return false, err
		}
		expectedTypes := map[string]string{
			"billing_operation_key":    "Nullable(String)",
			"billing_payload_hash":     "String",
			"billing_payload_protocol": "UInt16",
			"billing_sink_written_at":  "Int64",
		}
		for _, column := range columns {
			if expectedTypes[column.Name] == column.Type {
				delete(expectedTypes, column.Name)
			}
		}
		if len(expectedTypes) != 0 {
			return false, nil
		}
		var viewSQL string
		if err := db.Raw("SHOW CREATE TABLE " + clickHouseVisibleLogsView).Scan(&viewSQL).Error; err != nil {
			return false, err
		}
		viewSQL = strings.NewReplacer("`", "", `"`, "").Replace(strings.ToLower(viewSQL))
		viewSQL = strings.Join(strings.Fields(viewSQL), " ")
		return strings.Contains(viewSQL, "limit 1 by billing_operation_key") &&
			strings.Contains(viewSQL, "having uniqexact(") &&
			strings.Contains(viewSQL, "billing_payload_protocol") &&
			strings.Contains(viewSQL, "billing_payload_hash") &&
			strings.Contains(viewSQL, ") = 1"), nil
	}
	if !db.Migrator().HasColumn(&Log{}, "BillingOperationKey") ||
		!db.Migrator().HasColumn(&Log{}, "BillingPayloadHash") ||
		!db.Migrator().HasColumn(&Log{}, "BillingPayloadProtocol") ||
		!db.Migrator().HasColumn(&Log{}, "BillingSinkWrittenAt") {
		return false, nil
	}
	return sqlBillingLogReceiptSchemaReady(db)
}

func sqlBillingLogReceiptSchemaReady(db *gorm.DB) (bool, error) {
	var nullableCount int64
	var uniqueIndexCount int64
	switch common.LogDatabaseType() {
	case common.DatabaseTypeSQLite:
		if err := db.Raw(`SELECT count(*) FROM pragma_table_info('logs')
WHERE name = 'billing_operation_key' AND "notnull" = 0`).Scan(&nullableCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(*) FROM pragma_index_list('logs')
WHERE name = ? AND "unique" = 1`, billingLogOperationKeyIndex).Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypeMySQL:
		if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = 'logs'
	AND column_name = 'billing_operation_key' AND is_nullable = 'YES'`).Scan(&nullableCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(DISTINCT index_name) FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = 'logs'
	AND index_name = ? AND non_unique = 0`, billingLogOperationKeyIndex).Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypePostgreSQL:
		if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'logs'
	AND column_name = 'billing_operation_key' AND is_nullable = 'YES'`).Scan(&nullableCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(*) FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = 'logs'
	AND indexname = ? AND upper(indexdef) LIKE 'CREATE UNIQUE INDEX%'`, billingLogOperationKeyIndex).Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported billing log database type %q", common.LogDatabaseType())
	}
	indexReady, err := billingUniqueReceiptIndexReady(
		db, common.LogDatabaseType(), "logs", billingLogOperationKeyIndex, "billing_operation_key",
	)
	if err != nil {
		return false, err
	}
	return nullableCount == 1 && uniqueIndexCount == 1 && indexReady, nil
}

func waitBillingLogSchemaReady(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout < 0 {
		return errors.New("billing log schema wait timeout must be non-negative")
	}
	deadline := time.Now().Add(timeout)
	for {
		ready, err := billingLogSchemaReady(db)
		if err == nil && ready {
			return nil
		}
		if timeout == 0 || !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("billing log schema readiness check failed: %w", err)
			}
			return errors.New("billing log schema is not ready")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
