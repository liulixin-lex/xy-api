package model

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/types"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Channel struct {
	Id                 int     `json:"id"`
	RoutingIdentity    string  `json:"-" gorm:"column:routing_identity;type:varchar(32);index"`
	RoutingGeneration  string  `json:"-" gorm:"column:routing_generation;type:varchar(32)"`
	Type               int     `json:"type" gorm:"default:0"`
	Key                string  `json:"key" gorm:"not null"`
	OpenAIOrganization *string `json:"openai_organization"`
	TestModel          *string `json:"test_model"`
	Status             int     `json:"status" gorm:"default:1"`
	Name               string  `json:"name" gorm:"index"`
	Weight             *uint   `json:"weight" gorm:"default:0"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint"`
	TestTime           int64   `json:"test_time" gorm:"bigint"`
	ResponseTime       int     `json:"response_time"` // in milliseconds
	BaseURL            *string `json:"base_url" gorm:"column:base_url;default:''"`
	Other              string  `json:"other"`
	Balance            float64 `json:"balance"` // in USD
	BalanceUpdatedTime int64   `json:"balance_updated_time" gorm:"bigint"`
	Models             string  `json:"models"`
	Group              string  `json:"group" gorm:"type:varchar(64);default:'default'"`
	UsedQuota          int64   `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping       *string `json:"model_mapping" gorm:"type:text"`
	//MaxInputTokens     *int    `json:"max_input_tokens" gorm:"default:0"`
	StatusCodeMapping *string `json:"status_code_mapping" gorm:"type:varchar(1024);default:''"`
	Priority          *int64  `json:"priority" gorm:"bigint;default:0"`
	AutoBan           *int    `json:"auto_ban" gorm:"default:1"`
	OtherInfo         string  `json:"other_info"`
	Tag               *string `json:"tag" gorm:"index"`
	Setting           *string `json:"setting" gorm:"type:text"` // 渠道额外设置
	ParamOverride     *string `json:"param_override" gorm:"type:text"`
	HeaderOverride    *string `json:"header_override" gorm:"type:text"`
	Remark            *string `json:"remark" gorm:"type:varchar(255)" validate:"max=255"`
	// add after v0.8.5
	ChannelInfo ChannelInfo `json:"channel_info" gorm:"type:json"`

	OtherSettings string `json:"settings" gorm:"column:settings"` // 其他设置，存储azure版本等不需要检索的信息，详见dto.ChannelOtherSettings

	// cache info
	Keys []string `json:"-" gorm:"-"`
}

var ErrChannelHasStatefulReferences = errors.New("channel has stateful task references; disable it instead of changing its upstream identity")

func (channel *Channel) BeforeCreate(_ *gorm.DB) error {
	if !validRoutingIdentity(channel.RoutingIdentity) {
		channel.RoutingIdentity = common.GetUUID()
	}
	if !validRoutingIdentity(channel.RoutingGeneration) {
		channel.RoutingGeneration = common.GetUUID()
	}
	return nil
}

func (channel *Channel) AfterCreate(tx *gorm.DB) error {
	if channel == nil {
		return ErrRoutingChannelConfigurationInvalid
	}
	if tx.Migrator().HasTable(&RoutingChannelLifecycle{}) {
		if err := ensureRoutingChannelLifecycleTx(
			tx, *channel, RoutingChannelLifecycleReasonCreated, common.GetTimestamp(),
		); err != nil {
			return err
		}
	}
	return createDefaultRoutingChannelConfigurationTx(tx, *channel)
}

func EnsureChannelRoutingGenerations(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is required")
	}
	const batchSize = 200
	for {
		var channels []Channel
		if err := db.Select("id", "routing_identity", "routing_generation").
			Where("routing_identity IS NULL OR routing_identity = ? OR routing_generation IS NULL OR routing_generation = ?", "", "").
			Order("id asc").Limit(batchSize).Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for i := range channels {
				updates := map[string]any{}
				if !validRoutingIdentity(channels[i].RoutingIdentity) {
					updates["routing_identity"] = common.GetUUID()
				}
				if !validRoutingIdentity(channels[i].RoutingGeneration) {
					updates["routing_generation"] = common.GetUUID()
				}
				if len(updates) == 0 {
					continue
				}
				result := tx.Model(&Channel{}).Where("id = ?", channels[i].Id).Updates(updates)
				if result.Error != nil {
					return result.Error
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
}

func channelKeyMultisetContains(candidate, required []string) bool {
	counts := make(map[string]int, len(candidate))
	for _, key := range candidate {
		counts[key]++
	}
	for _, key := range required {
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func HasStatefulChannelReferencesTx(tx *gorm.DB, channelID int) (bool, error) {
	if tx == nil || channelID <= 0 {
		return false, errors.New("channel reference check is invalid")
	}
	hasReference := func(modelValue any, where string, args ...any) (bool, error) {
		if !tx.Migrator().HasTable(modelValue) {
			return false, nil
		}
		var count int64
		if err := tx.Model(modelValue).Where(where, args...).Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		return count > 0, nil
	}
	checks := []struct {
		model any
		where string
		args  []any
	}{
		{&AsyncBillingAttempt{}, "channel_id = ? AND state = ?", []any{
			channelID, AsyncBillingAttemptStateAuthorized,
		}},
		{&AsyncBillingReservation{}, "accepted_projection_channel_id = ? AND accepted_projection_state IN ?", []any{
			channelID, []string{AsyncBillingAcceptedProjectionPending, AsyncBillingAcceptedProjectionLogPending},
		}},
		{&TaskBillingOperation{}, "channel_id = ? AND (state != ? OR log_state NOT IN ?)", []any{
			channelID, TaskBillingOperationStateCompleted,
			[]string{TaskBillingOperationLogWritten, TaskBillingOperationLogNotRequired},
		}},
		{&MidjourneyBillingOperation{}, "channel_id = ? AND (state != ? OR log_state NOT IN ?)", []any{
			channelID, TaskBillingOperationStateCompleted,
			[]string{TaskBillingOperationLogWritten, TaskBillingOperationLogNotRequired},
		}},
		{&BillingStatsProjection{}, "channel_id = ? AND state IN ?", []any{channelID, []string{
			BillingStatsProjectionStatePending, BillingStatsProjectionStateRunning, BillingStatsProjectionStateFailed,
		}}},
	}
	for _, check := range checks {
		exists, err := hasReference(check.model, check.where, check.args...)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	if exists, err := hasReference(&Task{}, "channel_id = ? AND status NOT IN ?", channelID,
		[]TaskStatus{TaskStatusSuccess, TaskStatusFailure}); err != nil || exists {
		return exists, err
	}
	if tx.Migrator().HasTable(&Task{}) {
		const batchSize = 256
		var cursor int64
		for {
			var tasks []Task
			query := tx.Where("channel_id = ? AND id > ? AND status IN ?", channelID, cursor,
				[]TaskStatus{TaskStatusSuccess, TaskStatusFailure})
			if tx.Migrator().HasTable(&TaskBillingOperation{}) {
				query = query.Where("NOT EXISTS (SELECT 1 FROM task_billing_operations WHERE "+
					"task_billing_operations.task_id = tasks.id AND task_billing_operations.state = ? AND "+
					"task_billing_operations.log_state IN ?)", TaskBillingOperationStateCompleted,
					[]string{TaskBillingOperationLogWritten, TaskBillingOperationLogNotRequired})
			}
			if err := query.Order("id asc").Limit(batchSize).Find(&tasks).Error; err != nil {
				return false, err
			}
			for index := range tasks {
				version := tasks[index].PrivateData.BillingProtocolVersion
				if version != TaskBillingHistoricalProtocolVersion && SupportsDurableTaskBillingProtocol(version) {
					return true, nil
				}
			}
			if len(tasks) < batchSize {
				break
			}
			cursor = tasks[len(tasks)-1].ID
		}
	}
	if exists, err := hasReference(&Midjourney{}, "channel_id = ? AND (progress != ? OR status NOT IN ?)",
		channelID, "100%", []string{"SUCCESS", "FAILURE"}); err != nil || exists {
		return exists, err
	}
	if tx.Migrator().HasTable(&Midjourney{}) {
		query := tx.Model(&Midjourney{}).Where(
			"channel_id = ? AND progress = ? AND status IN ? AND billing_protocol_version BETWEEN ? AND ?",
			channelID, "100%", []string{"SUCCESS", "FAILURE"},
			TaskBillingLegacyProtocolVersion, TaskBillingProtocolVersion,
		)
		if tx.Migrator().HasTable(&MidjourneyBillingOperation{}) {
			query = query.Where("NOT EXISTS (SELECT 1 FROM midjourney_billing_operations WHERE "+
				"midjourney_billing_operations.midjourney_id = midjourneys.id AND "+
				"midjourney_billing_operations.state = ? AND midjourney_billing_operations.log_state IN ?)",
				TaskBillingOperationStateCompleted,
				[]string{TaskBillingOperationLogWritten, TaskBillingOperationLogNotRequired})
		}
		var count int64
		if err := query.Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func EnsureNoStatefulChannelReferencesTx(tx *gorm.DB, channelID int) error {
	hasReferences, err := HasStatefulChannelReferencesTx(tx, channelID)
	if err != nil {
		return err
	}
	if hasReferences {
		return ErrChannelHasStatefulReferences
	}
	return nil
}

type ChannelInfo struct {
	IsMultiKey             bool                  `json:"is_multi_key"`                        // 是否多Key模式
	MultiKeySize           int                   `json:"multi_key_size"`                      // 多Key模式下的Key数量
	MultiKeyStatusList     map[int]int           `json:"multi_key_status_list"`               // key状态列表，key index -> status
	MultiKeyDisabledReason map[int]string        `json:"multi_key_disabled_reason,omitempty"` // key禁用原因列表，key index -> reason
	MultiKeyDisabledTime   map[int]int64         `json:"multi_key_disabled_time,omitempty"`   // key禁用时间列表，key index -> time
	MultiKeyPollingIndex   int                   `json:"multi_key_polling_index"`             // 多Key模式下轮询的key索引
	MultiKeyMode           constant.MultiKeyMode `json:"multi_key_mode"`
}

func (info *ChannelInfo) RemapMultiKeyState(oldKeys []string, newKeys []string) {
	oldKeyCounts := make(map[string]int, len(oldKeys))
	oldKeyIndexes := make(map[string]int, len(oldKeys))
	for index, key := range oldKeys {
		oldKeyCounts[key]++
		oldKeyIndexes[key] = index
	}

	newKeyCounts := make(map[string]int, len(newKeys))
	for _, key := range newKeys {
		newKeyCounts[key]++
	}

	newStatusList := make(map[int]int)
	newDisabledReason := make(map[int]string)
	newDisabledTime := make(map[int]int64)
	for newIndex, key := range newKeys {
		if oldKeyCounts[key] != 1 || newKeyCounts[key] != 1 {
			continue
		}

		oldIndex := oldKeyIndexes[key]
		status, ok := info.MultiKeyStatusList[oldIndex]
		if !ok || (status != common.ChannelStatusManuallyDisabled && status != common.ChannelStatusAutoDisabled) {
			continue
		}
		newStatusList[newIndex] = status
		if reason, ok := info.MultiKeyDisabledReason[oldIndex]; ok {
			newDisabledReason[newIndex] = reason
		}
		if disabledTime, ok := info.MultiKeyDisabledTime[oldIndex]; ok {
			newDisabledTime[newIndex] = disabledTime
		}
	}

	info.MultiKeySize = len(newKeys)
	info.MultiKeyStatusList = newStatusList
	info.MultiKeyDisabledReason = newDisabledReason
	info.MultiKeyDisabledTime = newDisabledTime
	info.MultiKeyPollingIndex = 0
}

type ChannelSortOptions struct {
	SortBy    string
	SortOrder string
	IDSort    bool
}

var channelSortColumns = map[string]string{
	"id":            "id",
	"name":          "name",
	"priority":      "priority",
	"balance":       "balance",
	"response_time": "response_time",
	"test_time":     "test_time",
}

func NewChannelSortOptions(sortBy string, sortOrder string, idSort bool) ChannelSortOptions {
	normalizedSortBy := strings.ToLower(strings.TrimSpace(sortBy))
	normalizedSortOrder := strings.ToLower(strings.TrimSpace(sortOrder))
	if _, ok := channelSortColumns[normalizedSortBy]; !ok {
		normalizedSortBy = ""
		normalizedSortOrder = ""
	} else if normalizedSortOrder != "asc" {
		normalizedSortOrder = "desc"
	}

	return ChannelSortOptions{
		SortBy:    normalizedSortBy,
		SortOrder: normalizedSortOrder,
		IDSort:    idSort,
	}
}

func (options ChannelSortOptions) Apply(query *gorm.DB) *gorm.DB {
	if columnName, ok := channelSortColumns[options.SortBy]; ok {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: columnName},
			Desc:   options.SortOrder != "asc",
		})
	}
	if options.IDSort {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: "id"},
			Desc:   true,
		})
	}
	return query.Order(clause.OrderByColumn{
		Column: clause.Column{Name: "priority"},
		Desc:   true,
	})
}

func resolveChannelSortOptions(idSort bool, sortOptions []ChannelSortOptions) ChannelSortOptions {
	if len(sortOptions) == 0 {
		return NewChannelSortOptions("", "", idSort)
	}
	options := sortOptions[0]
	options.IDSort = options.IDSort || idSort
	return options
}

func NormalizeChannelGroupFilter(group string) string {
	group = strings.TrimSpace(group)
	if group == "" || strings.EqualFold(group, "all") || strings.EqualFold(group, "null") {
		return ""
	}
	return group
}

func channelGroupFilterCondition() string {
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		return `CONCAT(',', ` + commonGroupCol + `, ',') LIKE ? ESCAPE '!'`
	}
	return `(',' || ` + commonGroupCol + ` || ',') LIKE ? ESCAPE '!'`
}

func channelGroupFilterPattern(group string) string {
	group = strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	).Replace(group)
	return "%," + group + ",%"
}

func ApplyChannelGroupFilter(query *gorm.DB, group string) *gorm.DB {
	group = NormalizeChannelGroupFilter(group)
	if group == "" {
		return query
	}
	return query.Where(channelGroupFilterCondition(), channelGroupFilterPattern(group))
}

// Value implements driver.Valuer interface
func (c ChannelInfo) Value() (driver.Value, error) {
	return common.Marshal(&c)
}

// Scan implements sql.Scanner interface
func (c *ChannelInfo) Scan(value interface{}) error {
	bytesValue, _ := value.([]byte)
	return common.Unmarshal(bytesValue, c)
}

type LegacyRoutingStateEligibility struct {
	channelID   int
	apiKeyIndex int
	supported   bool
}

func (eligibility LegacyRoutingStateEligibility) Supported() bool {
	return eligibility.supported &&
		eligibility.channelID > 0 &&
		eligibility.apiKeyIndex == RoutingMetricSingleKeyIndex
}

func ResolveLegacyRoutingStateEligibility(channelID int, apiKeyIndex int) (LegacyRoutingStateEligibility, error) {
	return ResolveLegacyRoutingStateEligibilityContext(context.Background(), channelID, apiKeyIndex)
}

func ResolveLegacyRoutingStateEligibilityContext(ctx context.Context, channelID int, apiKeyIndex int) (LegacyRoutingStateEligibility, error) {
	unsupported := LegacyRoutingStateEligibility{}
	if channelID <= 0 || apiKeyIndex != RoutingMetricSingleKeyIndex {
		return unsupported, nil
	}
	memoryCacheEnabled := common.MemoryCacheEnabled
	var info *ChannelInfo
	var err error
	if memoryCacheEnabled {
		info, err = CacheGetChannelInfo(channelID)
	} else {
		var channel Channel
		err = DB.WithContext(ctx).Select("channel_info").First(&channel, "id = ?", channelID).Error
		info = &channel.ChannelInfo
	}
	if err != nil {
		if memoryCacheEnabled || errors.Is(err, gorm.ErrRecordNotFound) {
			return unsupported, nil
		}
		return unsupported, err
	}
	if info == nil || info.IsMultiKey {
		return unsupported, nil
	}
	return LegacyRoutingStateEligibility{
		channelID:   channelID,
		apiKeyIndex: apiKeyIndex,
		supported:   true,
	}, nil
}

func SupportsLegacyRoutingState(channelID int, apiKeyIndex int) bool {
	eligibility, err := ResolveLegacyRoutingStateEligibility(channelID, apiKeyIndex)
	return err == nil && eligibility.Supported()
}

func (channel *Channel) GetKeys() []string {
	if channel.Key == "" {
		return []string{}
	}
	if len(channel.Keys) > 0 {
		return channel.Keys
	}
	trimmed := strings.TrimSpace(channel.Key)
	// If the key starts with '[', try to parse it as a JSON array (e.g., for Vertex AI scenarios)
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := common.Unmarshal([]byte(trimmed), &arr); err == nil {
			res := make([]string, len(arr))
			for i, v := range arr {
				res[i] = string(v)
			}
			return res
		}
	}
	// Otherwise, fall back to splitting by newline
	keys := strings.Split(strings.Trim(channel.Key, "\n"), "\n")
	return keys
}

func (channel *Channel) GetNextEnabledKey() (string, int, *types.NewAPIError) {
	return channel.GetNextEnabledKeyFiltered(nil)
}

func (channel *Channel) GetNextEnabledKeyFiltered(allowIndex func(index int) bool) (string, int, *types.NewAPIError) {
	// If not in multi-key mode, return the original key string directly.
	if !channel.ChannelInfo.IsMultiKey {
		return channel.Key, 0, nil
	}

	// Obtain all keys (split by \n)
	keys := channel.GetKeys()
	if len(keys) == 0 {
		// No keys available, return error, should disable the channel
		return "", 0, types.NewError(errors.New("no keys available"), types.ErrorCodeChannelNoAvailableKey)
	}

	lock := GetChannelPollingLock(channel.Id)
	lock.Lock()
	defer lock.Unlock()

	statusList := channel.ChannelInfo.MultiKeyStatusList
	// helper to get key status, default to enabled when missing
	getStatus := func(idx int) int {
		if statusList == nil {
			return common.ChannelStatusEnabled
		}
		if status, ok := statusList[idx]; ok {
			return status
		}
		return common.ChannelStatusEnabled
	}

	// Collect indexes of enabled keys
	enabledIdx := make([]int, 0, len(keys))
	for i := range keys {
		if getStatus(i) == common.ChannelStatusEnabled && (allowIndex == nil || allowIndex(i)) {
			enabledIdx = append(enabledIdx, i)
		}
	}
	// If no specific status list or none enabled, return an explicit error so caller can
	// properly handle a channel with no available keys (e.g. mark channel disabled).
	// Returning the first key here caused requests to keep using an already-disabled key.
	if len(enabledIdx) == 0 {
		return "", 0, types.NewError(errors.New("no enabled keys"), types.ErrorCodeChannelNoAvailableKey)
	}

	switch channel.ChannelInfo.MultiKeyMode {
	case constant.MultiKeyModeRandom:
		// Randomly pick one enabled key
		selectedIdx := enabledIdx[rand.Intn(len(enabledIdx))]
		return keys[selectedIdx], selectedIdx, nil
	case constant.MultiKeyModePolling:
		// Use channel-specific lock to ensure thread-safe polling

		channelInfo, err := CacheGetChannelInfo(channel.Id)
		if err != nil {
			return "", 0, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		defer func() {
			if common.DebugEnabled {
				logger.LogDebug(nil, "channel %d polling index: %d", channel.Id, channel.ChannelInfo.MultiKeyPollingIndex)
			}
			if !common.MemoryCacheEnabled {
				_ = channel.SaveChannelInfo()
			} else {
				// CacheUpdateChannel(channel)
			}
		}()
		// Start from the saved polling index and look for the next enabled key
		start := channelInfo.MultiKeyPollingIndex
		if start < 0 || start >= len(keys) {
			start = 0
		}
		for i := 0; i < len(keys); i++ {
			idx := (start + i) % len(keys)
			if getStatus(idx) == common.ChannelStatusEnabled && (allowIndex == nil || allowIndex(idx)) {
				// update polling index for next call (point to the next position)
				channel.ChannelInfo.MultiKeyPollingIndex = (idx + 1) % len(keys)
				return keys[idx], idx, nil
			}
		}
		// Fallback – should not happen, but return first enabled key
		return keys[enabledIdx[0]], enabledIdx[0], nil
	default:
		// Unknown mode, default to first enabled key (or original key string)
		return keys[enabledIdx[0]], enabledIdx[0], nil
	}
}

func (channel *Channel) SaveChannelInfo() error {
	return DB.Model(channel).Update("channel_info", channel.ChannelInfo).Error
}

func (channel *Channel) GetModels() []string {
	if channel.Models == "" {
		return []string{}
	}
	return strings.Split(strings.Trim(channel.Models, ","), ",")
}

func (channel *Channel) GetGroups() []string {
	if channel.Group == "" {
		return []string{}
	}
	groups := strings.Split(strings.Trim(channel.Group, ","), ",")
	for i, group := range groups {
		groups[i] = strings.TrimSpace(group)
	}
	return groups
}

func (channel *Channel) GetOtherInfo() map[string]interface{} {
	otherInfo := make(map[string]interface{})
	if channel.OtherInfo != "" {
		err := common.Unmarshal([]byte(channel.OtherInfo), &otherInfo)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal other info: channel_id=%d, tag=%s, name=%s, error=%v", channel.Id, channel.GetTag(), channel.Name, err))
		}
	}
	return otherInfo
}

func (channel *Channel) SetOtherInfo(otherInfo map[string]interface{}) {
	otherInfoBytes, err := common.Marshal(otherInfo)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal other info: channel_id=%d, tag=%s, name=%s, error=%v", channel.Id, channel.GetTag(), channel.Name, err))
		return
	}
	channel.OtherInfo = string(otherInfoBytes)
}

func (channel *Channel) GetTag() string {
	if channel.Tag == nil {
		return ""
	}
	return *channel.Tag
}

func (channel *Channel) SetTag(tag string) {
	channel.Tag = &tag
}

func (channel *Channel) GetAutoBan() bool {
	if channel.AutoBan == nil {
		return false
	}
	return *channel.AutoBan == 1
}

func (channel *Channel) Save() error {
	err := DB.Save(channel).Error
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

func (channel *Channel) SaveWithoutKey() error {
	if channel.Id == 0 {
		return errors.New("channel ID is 0")
	}
	err := DB.Omit("key").Save(channel).Error
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

func GetAllChannels(startIdx int, num int, selectAll bool, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	var err error
	order := resolveChannelSortOptions(idSort, sortOptions)
	if selectAll {
		err = order.Apply(DB).Find(&channels).Error
	} else {
		err = order.Apply(DB).Limit(num).Offset(startIdx).Omit("key").Find(&channels).Error
	}
	return channels, err
}

func GetChannelsByTag(tag string, idSort bool, selectAll bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	order := resolveChannelSortOptions(idSort, sortOptions)
	query := order.Apply(DB.Where("tag = ?", tag))
	if !selectAll {
		query = query.Omit("key")
	}
	err := query.Find(&channels).Error
	return channels, err
}

func SearchChannels(keyword string, group string, model string, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	modelsCol := "`models`"

	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		modelsCol = `"models"`
	}

	baseURLCol := "`base_url`"
	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		baseURLCol = `"base_url"`
	}

	order := resolveChannelSortOptions(idSort, sortOptions)

	// 构造基础查询
	baseQuery := DB.Model(&Channel{}).Omit("key")

	// 构造WHERE子句
	whereClause := "(id = ? OR name LIKE ? OR " + commonKeyCol + " = ? OR " + baseURLCol + " LIKE ?) AND " + modelsCol + " LIKE ?"
	args := []any{common.String2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%", "%" + model + "%"}
	baseQuery = ApplyChannelGroupFilter(baseQuery.Where(whereClause, args...), group)

	// 执行查询
	err := order.Apply(baseQuery).Find(&channels).Error
	if err != nil {
		return nil, err
	}
	return channels, nil
}

func GetChannelById(id int, selectAll bool) (*Channel, error) {
	channel := &Channel{Id: id}
	var err error = nil
	if selectAll {
		err = DB.First(channel, "id = ?", id).Error
	} else {
		err = DB.Omit("key").First(channel, "id = ?", id).Error
	}
	if err != nil {
		return nil, err
	}
	return channel, nil
}

func BatchInsertChannels(channels []Channel) error {
	if len(channels) == 0 {
		return nil
	}
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for _, chunk := range lo.Chunk(channels, 50) {
		if err := tx.Create(&chunk).Error; err != nil {
			tx.Rollback()
			return err
		}
		for _, channel_ := range chunk {
			if err := channel_.AddAbilities(tx); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	err := tx.Commit().Error
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

func BatchDeleteChannels(ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	// 使用事务 分批删除channel表和abilities表
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	var channels []Channel
	if err := lockForUpdate(tx).Where("id IN ?", ids).Order("id asc").Find(&channels).Error; err != nil {
		tx.Rollback()
		return err
	}
	for index := range channels {
		if err := EnsureNoStatefulChannelReferencesTx(tx, channels[index].Id); err != nil {
			tx.Rollback()
			return err
		}
	}
	now := common.GetTimestamp()
	for index := range channels {
		if tx.Migrator().HasTable(&RoutingChannelLifecycle{}) {
			if err := retireRoutingChannelLifecycleTx(
				tx, channels[index], RoutingChannelLifecycleReasonDeleted, now,
			); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := retireRoutingChannelGenerationStateTx(tx, channels[index], now); err != nil {
			tx.Rollback()
			return err
		}
	}
	for _, chunk := range lo.Chunk(ids, 200) {
		if tx.Migrator().HasTable(&RoutingChannelConfiguration{}) {
			if err := tx.Where("channel_id in (?)", chunk).Delete(&RoutingChannelConfiguration{}).Error; err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Where("id in (?)", chunk).Delete(&Channel{}).Error; err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Where("channel_id in (?)", chunk).Delete(&Ability{}).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	err := tx.Commit().Error
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

func (channel *Channel) GetPriority() int64 {
	if channel.Priority == nil {
		return 0
	}
	return *channel.Priority
}

func (channel *Channel) GetWeight() int {
	if channel.Weight == nil {
		return 0
	}
	return int(*channel.Weight)
}

func (channel *Channel) GetBaseURL() string {
	if channel.BaseURL == nil {
		return ""
	}
	url := *channel.BaseURL
	if url == "" {
		url = constant.ChannelBaseURLs[channel.Type]
	}
	return url
}

func (channel *Channel) GetModelMapping() string {
	if channel.ModelMapping == nil {
		return ""
	}
	return *channel.ModelMapping
}

func (channel *Channel) GetStatusCodeMapping() string {
	if channel.StatusCodeMapping == nil {
		return ""
	}
	return *channel.StatusCodeMapping
}

func (channel *Channel) Insert() error {
	if channel == nil {
		return errors.New("channel is nil")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(channel).Error; err != nil {
			return err
		}
		return channel.AddAbilities(tx)
	})
	if err != nil {
		return err
	}
	NotifyRoutingTopologyChanged()
	return nil
}

func (channel *Channel) Update() error {
	_, err := channel.UpdateWithCredentialChange()
	return err
}

func (channel *Channel) UpdateWithCredentialChange() (bool, error) {
	// If this is a multi-key channel, recalculate MultiKeySize based on the current key list to avoid inconsistency after editing keys
	if channel.ChannelInfo.IsMultiKey {
		keyStr := channel.Key
		if keyStr == "" {
			// If key is not provided, read the existing key from the database
			if existing, err := GetChannelById(channel.Id, true); err == nil {
				keyStr = existing.Key
			}
		}
		keys := (&Channel{Key: keyStr}).GetKeys()
		channel.ChannelInfo.MultiKeySize = len(keys)
		// Clean up status data that exceeds the new key count to prevent index out of range
		if channel.ChannelInfo.MultiKeyStatusList != nil {
			for idx := range channel.ChannelInfo.MultiKeyStatusList {
				if idx < 0 || idx >= channel.ChannelInfo.MultiKeySize {
					delete(channel.ChannelInfo.MultiKeyStatusList, idx)
				}
			}
		}
		if channel.ChannelInfo.MultiKeyDisabledReason != nil {
			for idx := range channel.ChannelInfo.MultiKeyDisabledReason {
				if idx < 0 || idx >= channel.ChannelInfo.MultiKeySize {
					delete(channel.ChannelInfo.MultiKeyDisabledReason, idx)
				}
			}
		}
		if channel.ChannelInfo.MultiKeyDisabledTime != nil {
			for idx := range channel.ChannelInfo.MultiKeyDisabledTime {
				if idx < 0 || idx >= channel.ChannelInfo.MultiKeySize {
					delete(channel.ChannelInfo.MultiKeyDisabledTime, idx)
				}
			}
		}
		if channel.ChannelInfo.MultiKeyPollingIndex < 0 || channel.ChannelInfo.MultiKeyPollingIndex >= channel.ChannelInfo.MultiKeySize {
			channel.ChannelInfo.MultiKeyPollingIndex = 0
		}
	}
	credentialChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Channel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", channel.Id).First(&current).Error; err != nil {
			return err
		}
		effectiveKey := channel.Key
		if effectiveKey == "" {
			effectiveKey = current.Key
		}
		credentialChanged = effectiveKey != current.Key
		endpointChanged := channel.Type != current.Type ||
			!reflect.DeepEqual(channel.OpenAIOrganization, current.OpenAIOrganization) ||
			!reflect.DeepEqual(channel.BaseURL, current.BaseURL) ||
			channel.Other != current.Other || channel.OtherInfo != current.OtherInfo ||
			!reflect.DeepEqual(channel.Setting, current.Setting) ||
			!reflect.DeepEqual(channel.ParamOverride, current.ParamOverride) ||
			!reflect.DeepEqual(channel.HeaderOverride, current.HeaderOverride) ||
			channel.OtherSettings != current.OtherSettings
		credentialModeChanged := channel.ChannelInfo.IsMultiKey != current.ChannelInfo.IsMultiKey ||
			channel.ChannelInfo.MultiKeyMode != current.ChannelInfo.MultiKeyMode
		keysRemoved := credentialChanged && !channelKeyMultisetContains(
			(&Channel{Key: effectiveKey}).GetKeys(), current.GetKeys(),
		)
		lifecycleChanged := endpointChanged
		if lifecycleChanged || credentialChanged || credentialModeChanged {
			hasReferences, err := HasStatefulChannelReferencesTx(tx, channel.Id)
			if err != nil {
				return err
			}
			if hasReferences && (endpointChanged || credentialModeChanged || keysRemoved) {
				return ErrChannelHasStatefulReferences
			}
		}
		channel.RoutingIdentity = current.RoutingIdentity
		channel.RoutingGeneration = current.RoutingGeneration
		if lifecycleChanged {
			channel.RoutingGeneration = common.GetUUID()
		}
		if err := tx.Model(channel).Updates(channel).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		if lifecycleChanged {
			if tx.Migrator().HasTable(&RoutingChannelLifecycle{}) {
				if err := rotateRoutingChannelLifecycleTx(
					tx, current, *channel, RoutingChannelLifecycleReasonUpstreamChanged, now,
				); err != nil {
					return err
				}
			}
			if err := retireRoutingChannelGenerationStateTx(tx, current, now); err != nil {
				return err
			}
			if tx.Migrator().HasTable(&RoutingChannelConfiguration{}) {
				if err := rotateRoutingChannelConfigurationGenerationTx(tx, current, *channel, now); err != nil {
					return err
				}
			}
		}
		if credentialChanged {
			if tx.Migrator().HasTable(&RoutingChannelHealthState{}) {
				if err := tx.Model(&RoutingChannelHealthState{}).Where("channel_id = ?", channel.Id).
					Updates(map[string]any{
						"auth_failure": false, "auth_failure_reason": "", "auth_failure_until": int64(0), "updated_time": now,
					}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.First(channel, "id = ?", channel.Id).Error; err != nil {
			return err
		}
		return channel.UpdateAbilities(tx)
	})
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return credentialChanged, err
}

func RotateSingleChannelCredentialContinuity(
	ctx context.Context,
	channelID int,
	expectedGeneration string,
	expectedKey string,
	newKey string,
) (*Channel, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	expectedGeneration = strings.TrimSpace(expectedGeneration)
	if channelID <= 0 || expectedGeneration == "" || expectedKey == "" || newKey == "" || expectedKey == newKey {
		return nil, errors.New("channel credential continuity rotation is invalid")
	}
	var rotated Channel
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", channelID).First(&rotated).Error; err != nil {
			return err
		}
		if rotated.ChannelInfo.IsMultiKey || rotated.RoutingGeneration != expectedGeneration || rotated.Key != expectedKey {
			return ErrChannelHasStatefulReferences
		}
		hasReferences, err := HasStatefulChannelReferencesTx(tx, channelID)
		if err != nil {
			return err
		}
		if tx.Migrator().HasTable(&RoutingCredentialRef{}) {
			oldFingerprint, fingerprintErr := RoutingCredentialFingerprint(channelID, expectedGeneration, expectedKey)
			if fingerprintErr != nil {
				if hasReferences {
					return fingerprintErr
				}
			} else {
				newFingerprint, fingerprintErr := RoutingCredentialFingerprint(channelID, expectedGeneration, newKey)
				if fingerprintErr != nil {
					return fingerprintErr
				}
				var ref RoutingCredentialRef
				refErr := lockForUpdate(tx).Where(
					"channel_id = ? AND channel_generation = ? AND fingerprint = ? AND active = ?",
					channelID, expectedGeneration, oldFingerprint, true,
				).First(&ref).Error
				if errors.Is(refErr, gorm.ErrRecordNotFound) {
					if hasReferences {
						return ErrChannelHasStatefulReferences
					}
				} else if refErr != nil {
					return refErr
				} else {
					now := common.GetTimestamp()
					updated := tx.Model(&RoutingCredentialRef{}).Where(
						"id = ? AND fingerprint = ? AND channel_generation = ? AND active = ?",
						ref.ID, oldFingerprint, expectedGeneration, true,
					).Updates(map[string]any{
						"fingerprint": newFingerprint, "updated_time": now,
					})
					if updated.Error != nil {
						return updated.Error
					}
					if updated.RowsAffected != 1 {
						return ErrChannelHasStatefulReferences
					}
				}
			}
		} else if hasReferences {
			return ErrChannelHasStatefulReferences
		}
		updated := tx.Model(&Channel{}).
			Where("id = ? AND routing_generation = ?", channelID, expectedGeneration).
			Where(map[string]any{"key": expectedKey}).
			Update("key", newKey)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrChannelHasStatefulReferences
		}
		if tx.Migrator().HasTable(&RoutingChannelHealthState{}) {
			if err := tx.Model(&RoutingChannelHealthState{}).Where("channel_id = ?", channelID).
				Updates(map[string]any{
					"auth_failure": false, "auth_failure_reason": "", "auth_failure_until": int64(0),
					"updated_time": common.GetTimestamp(),
				}).Error; err != nil {
				return err
			}
		}
		rotated.Key = newKey
		return nil
	})
	if err != nil {
		return nil, err
	}
	NotifyRoutingTopologyChanged()
	return &rotated, nil
}

func (channel *Channel) UpdateResponseTime(responseTime int64) {
	err := DB.Model(channel).Select("response_time", "test_time").Updates(Channel{
		TestTime:     common.GetTimestamp(),
		ResponseTime: int(responseTime),
	}).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update response time: channel_id=%d, error=%v", channel.Id, err))
	}
}

func (channel *Channel) UpdateBalance(balance float64) {
	err := DB.Model(channel).Select("balance_updated_time", "balance").Updates(Channel{
		BalanceUpdatedTime: common.GetTimestamp(),
		Balance:            balance,
	}).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update balance: channel_id=%d, error=%v", channel.Id, err))
	}
}

func (channel *Channel) Delete() error {
	if channel == nil || channel.Id <= 0 {
		return errors.New("channel ID is 0")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Channel
		if err := lockForUpdate(tx).Where("id = ?", channel.Id).First(&current).Error; err != nil {
			return err
		}
		if err := EnsureNoStatefulChannelReferencesTx(tx, current.Id); err != nil {
			return err
		}
		now := common.GetTimestamp()
		if tx.Migrator().HasTable(&RoutingChannelLifecycle{}) {
			if err := retireRoutingChannelLifecycleTx(
				tx, current, RoutingChannelLifecycleReasonDeleted, now,
			); err != nil {
				return err
			}
		}
		if err := retireRoutingChannelGenerationStateTx(tx, current, now); err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", current.Id).Delete(&Ability{}).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&RoutingChannelConfiguration{}) {
			if err := tx.Where("channel_id = ?", current.Id).Delete(&RoutingChannelConfiguration{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&current).Error
	})
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

var channelStatusLock sync.Mutex

// channelPollingLocks stores locks for each channel.id to ensure thread-safe polling
var channelPollingLocks sync.Map

// GetChannelPollingLock returns or creates a mutex for the given channel ID
func GetChannelPollingLock(channelId int) *sync.Mutex {
	if lock, exists := channelPollingLocks.Load(channelId); exists {
		return lock.(*sync.Mutex)
	}
	// Create new lock for this channel
	newLock := &sync.Mutex{}
	actual, _ := channelPollingLocks.LoadOrStore(channelId, newLock)
	return actual.(*sync.Mutex)
}

// CleanupChannelPollingLocks removes locks for channels that no longer exist
// This is optional and can be called periodically to prevent memory leaks
func CleanupChannelPollingLocks() {
	var activeChannelIds []int
	DB.Model(&Channel{}).Pluck("id", &activeChannelIds)

	activeChannelSet := make(map[int]bool)
	for _, id := range activeChannelIds {
		activeChannelSet[id] = true
	}

	channelPollingLocks.Range(func(key, value interface{}) bool {
		channelId := key.(int)
		if !activeChannelSet[channelId] {
			channelPollingLocks.Delete(channelId)
		}
		return true
	})
}

func handlerMultiKeyUpdate(channel *Channel, usingKey string, status int, reason string) {
	keys := channel.GetKeys()
	if len(keys) == 0 {
		channel.Status = status
	} else {
		keyIndex := -1
		for i, key := range keys {
			if key == usingKey {
				keyIndex = i
				break
			}
		}
		if keyIndex < 0 {
			if usingKey != "" {
				common.SysLog(fmt.Sprintf("failed to update multi-key status: channel_id=%d, using key not found", channel.Id))
				return
			}
			channel.Status = status
			info := channel.GetOtherInfo()
			info["status_reason"] = reason
			info["status_time"] = common.GetTimestamp()
			channel.SetOtherInfo(info)
			return
		}
		if channel.ChannelInfo.MultiKeyStatusList == nil {
			channel.ChannelInfo.MultiKeyStatusList = make(map[int]int)
		}
		if status == common.ChannelStatusEnabled {
			delete(channel.ChannelInfo.MultiKeyStatusList, keyIndex)
			delete(channel.ChannelInfo.MultiKeyDisabledReason, keyIndex)
			delete(channel.ChannelInfo.MultiKeyDisabledTime, keyIndex)
		} else {
			channel.ChannelInfo.MultiKeyStatusList[keyIndex] = status
			if channel.ChannelInfo.MultiKeyDisabledReason == nil {
				channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string)
			}
			if channel.ChannelInfo.MultiKeyDisabledTime == nil {
				channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64)
			}
			channel.ChannelInfo.MultiKeyDisabledReason[keyIndex] = reason
			channel.ChannelInfo.MultiKeyDisabledTime[keyIndex] = common.GetTimestamp()
		}
		if !hasEnabledMultiKey(keys, channel.ChannelInfo.MultiKeyStatusList) {
			channel.Status = common.ChannelStatusAutoDisabled
			info := channel.GetOtherInfo()
			info["status_reason"] = "All keys are disabled"
			info["status_time"] = common.GetTimestamp()
			channel.SetOtherInfo(info)
		} else if status == common.ChannelStatusEnabled {
			channel.Status = common.ChannelStatusEnabled
		}
	}
}

func hasEnabledMultiKey(keys []string, statusList map[int]int) bool {
	for i := range keys {
		if statusList == nil {
			return true
		}
		status, ok := statusList[i]
		if !ok || status == common.ChannelStatusEnabled {
			return true
		}
	}
	return false
}

func UpdateChannelStatus(channelId int, usingKey string, status int, reason string) bool {
	if common.MemoryCacheEnabled {
		channelStatusLock.Lock()
		defer channelStatusLock.Unlock()

		channelCache, _ := CacheGetChannel(channelId)
		if channelCache == nil {
			return false
		}
		if channelCache.ChannelInfo.IsMultiKey {
			// Use per-channel lock to prevent concurrent map read/write with GetNextEnabledKey
			beforeStatus := channelCache.Status
			pollingLock := GetChannelPollingLock(channelId)
			pollingLock.Lock()
			// 如果是多Key模式，更新缓存中的状态
			handlerMultiKeyUpdate(channelCache, usingKey, status, reason)
			pollingLock.Unlock()
			if beforeStatus != channelCache.Status {
				CacheUpdateChannelStatus(channelId, channelCache.Status)
			}
			//CacheUpdateChannel(channelCache)
			//return true
		} else {
			// 如果缓存渠道存在，且状态已是目标状态，直接返回
			if channelCache.Status == status {
				return false
			}
			CacheUpdateChannelStatus(channelId, status)
		}
	}

	shouldUpdateAbilities := false
	defer func() {
		if shouldUpdateAbilities {
			err := UpdateAbilityStatus(channelId, status == common.ChannelStatusEnabled)
			if err != nil {
				common.SysLog(fmt.Sprintf("failed to update ability status: channel_id=%d, error=%v", channelId, err))
			}
		}
	}()
	channel, err := GetChannelById(channelId, true)
	if err != nil {
		return false
	} else {
		if channel.Status == status {
			return false
		}

		if channel.ChannelInfo.IsMultiKey {
			beforeStatus := channel.Status
			// Protect map writes with the same per-channel lock used by readers
			pollingLock := GetChannelPollingLock(channelId)
			pollingLock.Lock()
			handlerMultiKeyUpdate(channel, usingKey, status, reason)
			pollingLock.Unlock()
			if beforeStatus != channel.Status {
				shouldUpdateAbilities = true
			}
		} else {
			info := channel.GetOtherInfo()
			info["status_reason"] = reason
			info["status_time"] = common.GetTimestamp()
			channel.SetOtherInfo(info)
			channel.Status = status
			shouldUpdateAbilities = true
		}
		err = channel.SaveWithoutKey()
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to update channel status: channel_id=%d, status=%d, error=%v", channel.Id, status, err))
			return false
		}
	}
	return true
}

func EnableChannelByTag(tag string) error {
	err := DB.Model(&Channel{}).Where("tag = ?", tag).Update("status", common.ChannelStatusEnabled).Error
	if err != nil {
		return err
	}
	err = UpdateAbilityStatusByTag(tag, true)
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

func DisableChannelByTag(tag string) error {
	err := DB.Model(&Channel{}).Where("tag = ?", tag).Update("status", common.ChannelStatusManuallyDisabled).Error
	if err != nil {
		return err
	}
	err = UpdateAbilityStatusByTag(tag, false)
	if err == nil {
		NotifyRoutingTopologyChanged()
	}
	return err
}

func EditChannelByTag(tag string, newTag *string, modelMapping *string, models *string, group *string, priority *int64, weight *uint, paramOverride *string, headerOverride *string) error {
	updateData := Channel{}
	shouldReCreateAbilities := false
	updatedTag := tag
	// 如果 newTag 不为空且不等于 tag，则更新 tag
	if newTag != nil && *newTag != tag {
		updateData.Tag = newTag
		updatedTag = *newTag
	}
	if modelMapping != nil {
		updateData.ModelMapping = modelMapping
	}
	if models != nil && *models != "" {
		shouldReCreateAbilities = true
		updateData.Models = *models
	}
	if group != nil && *group != "" {
		shouldReCreateAbilities = true
		updateData.Group = *group
	}
	if priority != nil {
		updateData.Priority = priority
	}
	if weight != nil {
		updateData.Weight = weight
	}
	if paramOverride != nil {
		updateData.ParamOverride = paramOverride
	}
	if headerOverride != nil {
		updateData.HeaderOverride = headerOverride
	}

	statefulEgressChange := paramOverride != nil || headerOverride != nil
	err := DB.Transaction(func(tx *gorm.DB) error {
		if statefulEgressChange {
			var channels []Channel
			if err := lockForUpdate(tx).Where("tag = ?", tag).Order("id asc").Find(&channels).Error; err != nil {
				return err
			}
			for index := range channels {
				changed := (paramOverride != nil && !reflect.DeepEqual(channels[index].ParamOverride, paramOverride)) ||
					(headerOverride != nil && !reflect.DeepEqual(channels[index].HeaderOverride, headerOverride))
				if !changed {
					continue
				}
				if err := EnsureNoStatefulChannelReferencesTx(tx, channels[index].Id); err != nil {
					return err
				}
				next := channels[index]
				next.RoutingGeneration = common.GetUUID()
				if paramOverride != nil {
					next.ParamOverride = paramOverride
				}
				if headerOverride != nil {
					next.HeaderOverride = headerOverride
				}
				if err := tx.Model(&Channel{}).Where("id = ?", channels[index].Id).
					Update("routing_generation", next.RoutingGeneration).Error; err != nil {
					return err
				}
				now := common.GetTimestamp()
				if tx.Migrator().HasTable(&RoutingChannelLifecycle{}) {
					if err := rotateRoutingChannelLifecycleTx(
						tx, channels[index], next, RoutingChannelLifecycleReasonUpstreamChanged, now,
					); err != nil {
						return err
					}
				}
				if err := retireRoutingChannelGenerationStateTx(tx, channels[index], now); err != nil {
					return err
				}
				if tx.Migrator().HasTable(&RoutingChannelConfiguration{}) {
					if err := rotateRoutingChannelConfigurationGenerationTx(
						tx, channels[index], next, now,
					); err != nil {
						return err
					}
				}
			}
		}
		return tx.Model(&Channel{}).Where("tag = ?", tag).Updates(updateData).Error
	})
	if err != nil {
		return err
	}
	if shouldReCreateAbilities {
		channels, err := GetChannelsByTag(updatedTag, false, false)
		if err == nil {
			for _, channel := range channels {
				err = channel.UpdateAbilities(nil)
				if err != nil {
					common.SysLog(fmt.Sprintf("failed to update abilities: channel_id=%d, tag=%s, error=%v", channel.Id, channel.GetTag(), err))
				}
			}
		}
	} else {
		err := UpdateAbilityByTag(tag, newTag, priority, weight)
		if err != nil {
			return err
		}
	}
	NotifyRoutingTopologyChanged()
	return nil
}

func UpdateChannelUsedQuota(id int, quota int) {
	if common.BatchUpdateEnabled {
		addNewRecord(BatchUpdateTypeChannelUsedQuota, id, quota)
		return
	}
	updateChannelUsedQuota(id, quota)
}

func updateChannelUsedQuota(id int, quota int) {
	err := DB.Model(&Channel{}).Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota)).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update channel used quota: channel_id=%d, delta_quota=%d, error=%v", id, quota, err))
	}
}

func DeleteChannelByStatus(status int64) (int64, error) {
	return deleteChannelsByStatuses([]int64{status})
}

func DeleteDisabledChannel() (int64, error) {
	return deleteChannelsByStatuses([]int64{common.ChannelStatusAutoDisabled, common.ChannelStatusManuallyDisabled})
}

func deleteChannelsByStatuses(statuses []int64) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	var deleted int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channels []Channel
		if err := lockForUpdate(tx).Where("status IN ?", statuses).Order("id asc").Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		ids := make([]int, 0, len(channels))
		now := common.GetTimestamp()
		for index := range channels {
			if err := EnsureNoStatefulChannelReferencesTx(tx, channels[index].Id); err != nil {
				return err
			}
			if tx.Migrator().HasTable(&RoutingChannelLifecycle{}) {
				if err := retireRoutingChannelLifecycleTx(
					tx, channels[index], RoutingChannelLifecycleReasonDeleted, now,
				); err != nil {
					return err
				}
			}
			if err := retireRoutingChannelGenerationStateTx(tx, channels[index], now); err != nil {
				return err
			}
			ids = append(ids, channels[index].Id)
		}
		for _, chunk := range lo.Chunk(ids, 200) {
			if err := tx.Where("channel_id IN ?", chunk).Delete(&Ability{}).Error; err != nil {
				return err
			}
			if tx.Migrator().HasTable(&RoutingChannelConfiguration{}) {
				if err := tx.Where("channel_id IN ?", chunk).Delete(&RoutingChannelConfiguration{}).Error; err != nil {
					return err
				}
			}
			result := tx.Where("id IN ?", chunk).Delete(&Channel{})
			if result.Error != nil {
				return result.Error
			}
			deleted += result.RowsAffected
		}
		return nil
	})
	if err == nil && deleted > 0 {
		NotifyRoutingTopologyChanged()
	}
	return deleted, err
}

func GetPaginatedTags(offset int, limit int) ([]*string, error) {
	return GetPaginatedChannelTags(DB.Model(&Channel{}), offset, limit)
}

func GetPaginatedChannelTags(query *gorm.DB, offset int, limit int) ([]*string, error) {
	var tags []*string
	err := query.
		Select("DISTINCT tag").
		Where("tag is not null AND tag != ''").
		Order(clause.OrderByColumn{Column: clause.Column{Name: "tag"}}).
		Offset(offset).
		Limit(limit).
		Find(&tags).Error
	return tags, err
}

func SearchTags(keyword string, group string, model string, idSort bool) ([]*string, error) {
	var tags []*string
	modelsCol := "`models`"

	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		modelsCol = `"models"`
	}

	baseURLCol := "`base_url`"
	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		baseURLCol = `"base_url"`
	}

	order := "priority desc"
	if idSort {
		order = "id desc"
	}

	// 构造基础查询
	baseQuery := DB.Model(&Channel{}).Omit("key")

	// 构造WHERE子句
	whereClause := "(id = ? OR name LIKE ? OR " + commonKeyCol + " = ? OR " + baseURLCol + " LIKE ?) AND " + modelsCol + " LIKE ?"
	args := []any{common.String2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%", "%" + model + "%"}
	baseQuery = ApplyChannelGroupFilter(baseQuery.Where(whereClause, args...), group)

	subQuery := baseQuery.
		Select("tag").
		Where("tag != ''").
		Order(order)

	err := DB.Table("(?) as sub", subQuery).
		Select("DISTINCT tag").
		Find(&tags).Error

	if err != nil {
		return nil, err
	}

	return tags, nil
}

func (channel *Channel) ValidateSettings() error {
	channelParams := &dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		err := common.Unmarshal([]byte(*channel.Setting), channelParams)
		if err != nil {
			return err
		}
	}
	channelOtherSettings := &dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		err := common.UnmarshalJsonStr(channel.OtherSettings, channelOtherSettings)
		if err != nil {
			return err
		}
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if channelOtherSettings.AdvancedCustom == nil {
			return fmt.Errorf("advanced_custom is required")
		}
	}
	if channelOtherSettings.AdvancedCustom != nil {
		if err := channelOtherSettings.AdvancedCustom.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) GetSetting() dto.ChannelSettings {
	setting := dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		err := common.Unmarshal([]byte(*channel.Setting), &setting)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal setting: channel_id=%d, error=%v", channel.Id, err))
			channel.Setting = nil // 清空设置以避免后续错误
			_ = channel.Save()    // 保存修改
		}
	}
	return setting
}

func (channel *Channel) SetSetting(setting dto.ChannelSettings) {
	settingBytes, err := common.Marshal(setting)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal setting: channel_id=%d, error=%v", channel.Id, err))
		return
	}
	channel.Setting = common.GetPointer[string](string(settingBytes))
}

func (channel *Channel) GetOtherSettings() dto.ChannelOtherSettings {
	setting := dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		err := common.UnmarshalJsonStr(channel.OtherSettings, &setting)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal setting: channel_id=%d, error=%v", channel.Id, err))
			channel.OtherSettings = "{}" // 清空设置以避免后续错误
			_ = channel.Save()           // 保存修改
		}
	}
	return setting
}

func (channel *Channel) SetOtherSettings(setting dto.ChannelOtherSettings) {
	settingBytes, err := common.Marshal(setting)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal setting: channel_id=%d, error=%v", channel.Id, err))
		return
	}
	channel.OtherSettings = string(settingBytes)
}

func (channel *Channel) GetParamOverride() map[string]interface{} {
	paramOverride := make(map[string]interface{})
	if channel.ParamOverride != nil && *channel.ParamOverride != "" {
		err := common.Unmarshal([]byte(*channel.ParamOverride), &paramOverride)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal param override: channel_id=%d, error=%v", channel.Id, err))
		}
	}
	return paramOverride
}

func (channel *Channel) GetHeaderOverride() map[string]interface{} {
	headerOverride := make(map[string]interface{})
	if channel.HeaderOverride != nil && *channel.HeaderOverride != "" {
		err := common.Unmarshal([]byte(*channel.HeaderOverride), &headerOverride)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal header override: channel_id=%d, error=%v", channel.Id, err))
		}
	}
	return headerOverride
}

func GetChannelsByIds(ids []int) ([]*Channel, error) {
	var channels []*Channel
	err := DB.Where("id in (?)", ids).Find(&channels).Error
	return channels, err
}

func BatchSetChannelTag(ids []int, tag *string) error {
	// 开启事务
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	// 更新标签
	err := tx.Model(&Channel{}).Where("id in (?)", ids).Update("tag", tag).Error
	if err != nil {
		tx.Rollback()
		return err
	}

	// update ability status
	channels, err := GetChannelsByIds(ids)
	if err != nil {
		tx.Rollback()
		return err
	}

	for _, channel := range channels {
		err = channel.UpdateAbilities(tx)
		if err != nil {
			tx.Rollback()
			return err
		}
	}

	// 提交事务
	return tx.Commit().Error
}

// CountAllChannels returns total channels in DB
func CountAllChannels() (int64, error) {
	var total int64
	err := DB.Model(&Channel{}).Count(&total).Error
	return total, err
}

// CountAllTags returns number of non-empty distinct tags
func CountAllTags() (int64, error) {
	return CountChannelTags(DB.Model(&Channel{}))
}

func CountChannelTags(query *gorm.DB) (int64, error) {
	var total int64
	err := query.Where("tag is not null AND tag != ''").Distinct("tag").Count(&total).Error
	return total, err
}

// Get channels of specified type with pagination
func GetChannelsByType(startIdx int, num int, idSort bool, channelType int) ([]*Channel, error) {
	var channels []*Channel
	order := "priority desc"
	if idSort {
		order = "id desc"
	}
	err := DB.Where("type = ?", channelType).Order(order).Limit(num).Offset(startIdx).Omit("key").Find(&channels).Error
	return channels, err
}

// Count channels of specific type
func CountChannelsByType(channelType int) (int64, error) {
	var count int64
	err := DB.Model(&Channel{}).Where("type = ?", channelType).Count(&count).Error
	return count, err
}

// Return map[type]count for all channels
func CountChannelsGroupByType() (map[int64]int64, error) {
	type result struct {
		Type  int64 `gorm:"column:type"`
		Count int64 `gorm:"column:count"`
	}
	var results []result
	err := DB.Model(&Channel{}).Select("type, count(*) as count").Group("type").Find(&results).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]int64)
	for _, r := range results {
		counts[r.Type] = r.Count
	}
	return counts, nil
}
