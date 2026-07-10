package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/go-redis/redis/v8"
	"golang.org/x/sync/singleflight"
)

const (
	routingSub2APILockTTL              = 30 * time.Second
	routingSub2APIDefaultUnlockTimeout = 2 * time.Second
	routingSub2APITokenTTLBuffer       = 60 * time.Second
	routingSub2APIDefaultMaxJWTEntries = 4_096
)

type routingSub2APIEnvelope struct {
	Code    int             `json:"code"`
	Success *bool           `json:"success,omitempty"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type routingSub2APIGroup struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Group            string  `json:"group"`
	RateMultiplier   float64 `json:"rate_multiplier"`
	Ratio            float64 `json:"ratio"`
	ClaudeCodeOnly   bool    `json:"claude_code_only"`
	ServesClaudeCode bool    `json:"serves_claude_code"`
}

type routingSub2APIChannel struct {
	Model            string   `json:"model"`
	ModelName        string   `json:"model_name"`
	Name             string   `json:"name"`
	Models           []string `json:"models"`
	Group            string   `json:"group"`
	Groups           []string `json:"groups"`
	ClaudeCodeOnly   bool     `json:"claude_code_only"`
	ServesClaudeCode bool     `json:"serves_claude_code"`
	BillingMode      string   `json:"billing_mode"`

	InputPrice      float64 `json:"input_price"`
	OutputPrice     float64 `json:"output_price"`
	CachePrice      float64 `json:"cache_price"`
	PerRequestPrice float64 `json:"per_request_price"`
	ImagePrice      float64 `json:"image_price"`
	Price           float64 `json:"price"`
	Rate            float64 `json:"rate"`
	Ratio           float64 `json:"ratio"`
	Input           float64 `json:"input"`
	Output          float64 `json:"output"`
	Cache           float64 `json:"cache"`
	PerRequest      float64 `json:"per_request"`
	Image           float64 `json:"image"`
}

type routingSub2APIJWTCacheEntry struct {
	Ciphertext string
	ExpiresAt  int64
}

// RoutingSub2APIJWTCacheStats reports this process's local JWT cache only.
// Redis-backed JWT cache activity is intentionally excluded.
type RoutingSub2APIJWTCacheStats struct {
	Entries     int
	Expirations int64
	Evictions   int64
}

var routingSub2APIJWTCache = struct {
	sync.Mutex
	values      map[int]routingSub2APIJWTCacheEntry
	expirations int64
	evictions   int64
}{
	values: map[int]routingSub2APIJWTCacheEntry{},
}

var routingSub2APIMaxJWTEntries = routingSub2APIDefaultMaxJWTEntries
var routingSub2APIUnlockTimeout = routingSub2APIDefaultUnlockTimeout
var routingSub2APILoginCoordinator = struct {
	sync.RWMutex
	group      *singleflight.Group
	generation uint64
}{
	group: &singleflight.Group{},
}

func fetchRoutingSub2APICostSnapshots(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) ([]model.RoutingCostSnapshot, error) {
	jwt, err := routingSub2APIJWT(ctx, binding, credentials)
	if err != nil {
		return nil, err
	}

	if err = fetchRoutingSub2APIBalance(ctx, binding, credentials, jwt); err != nil && routingUpstreamAuthError(err) {
		return nil, err
	}

	groupsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/available", jwt, nil)
	if err != nil {
		return nil, err
	}
	groups := parseRoutingSub2APIGroups(groupsRaw)
	groupInfo, groupFound := groups[binding.UpstreamGroup]
	groupRatio := routingSub2APIGroupRatio(groupInfo)
	if groupRatio <= 0 {
		groupRatio = 1
	}

	ratesRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/rates", jwt, nil)
	if err != nil {
		return nil, err
	}
	if ratio, ok := parseRoutingSub2APIRates(ratesRaw)[binding.UpstreamGroup]; ok && ratio > 0 {
		groupRatio = ratio
	}

	channelsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/channels/available", jwt, nil)
	if err != nil {
		return nil, err
	}
	channels := parseRoutingSub2APIChannels(channelsRaw)

	now := common.GetTimestamp()
	snapshots := make([]model.RoutingCostSnapshot, 0, len(channels))
	modelNameMap := routingModelReverseMapping(binding.ChannelID)
	for _, channel := range channels {
		if !routingSub2APIChannelServesBinding(channel, binding) {
			continue
		}
		for _, modelName := range routingSub2APIChannelModels(channel) {
			if localName, ok := modelNameMap[modelName]; ok {
				modelName = localName
			}
			snapshot := routingSub2APIChannelSnapshot(binding.ChannelID, modelName, groupRatio, groupFound, channel, now)
			snapshots = append(snapshots, snapshot)
		}
	}
	clearRoutingAuthFailure(binding.ChannelID)
	return snapshots, nil
}

func routingSub2APIJWT(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (string, error) {
	if token := strings.TrimSpace(credentials.Sub2APIToken); token != "" {
		return token, nil
	}
	if token, ok := getRoutingSub2APICachedJWT(ctx, binding.ChannelID); ok {
		return token, nil
	}

	routingSub2APILoginCoordinator.RLock()
	loginGroup := routingSub2APILoginCoordinator.group
	loginGeneration := routingSub2APILoginCoordinator.generation
	resultChannel := loginGroup.DoChan(strconv.Itoa(binding.ChannelID), func() (any, error) {
		sharedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routingSub2APILockTTL)
		defer cancel()

		if token, ok := getRoutingSub2APICachedJWT(sharedCtx, binding.ChannelID); ok {
			return token, nil
		}

		unlockRedis, lockErr := acquireRoutingSub2APIRedisLock(sharedCtx, binding.ChannelID)
		if lockErr != nil {
			return "", lockErr
		}
		if unlockRedis != nil {
			defer unlockRedis()
		}
		if token, ok := getRoutingSub2APICachedJWT(sharedCtx, binding.ChannelID); ok {
			return token, nil
		}

		token, ttl, loginErr := loginRoutingSub2API(sharedCtx, binding, credentials)
		if loginErr != nil {
			return "", loginErr
		}
		routingSub2APILoginCoordinator.RLock()
		if loginGeneration == routingSub2APILoginCoordinator.generation {
			setRoutingSub2APICachedJWT(sharedCtx, binding.ChannelID, token, ttl)
		}
		routingSub2APILoginCoordinator.RUnlock()
		return token, nil
	})
	routingSub2APILoginCoordinator.RUnlock()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultChannel:
		if result.Err != nil {
			return "", result.Err
		}
		token, ok := result.Val.(string)
		if !ok {
			return "", fmt.Errorf("sub2api login returned unexpected result type %T", result.Val)
		}
		return token, nil
	}
}

func acquireRoutingSub2APIRedisLock(ctx context.Context, channelID int) (func(), error) {
	if !common.RedisEnabled || common.RDB == nil {
		return nil, nil
	}
	lockKey := routingSub2APIRedisLockKey(channelID)
	lockOwner := common.GetRandomString(32)
	deadline := time.Now().Add(routingSub2APILockTTL)
	for {
		acquired, err := common.RDB.SetNX(ctx, lockKey, lockOwner, routingSub2APILockTTL).Result()
		if err != nil {
			return nil, fmt.Errorf("sub2api login lock failed: %w", err)
		}
		if acquired {
			return func() {
				releaseRoutingSub2APIRedisLock(channelID, lockOwner)
			}, nil
		}
		if _, ok := getRoutingSub2APICachedJWT(ctx, channelID); ok {
			return func() {}, nil
		}
		if time.Now().After(deadline) {
			return nil, routingAuthErrorf("sub2api login lock timed out")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func releaseRoutingSub2APIRedisLock(channelID int, lockOwner string) {
	ctx, cancel := context.WithTimeout(context.Background(), routingSub2APIUnlockTimeout)
	defer cancel()

	script := redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`)
	if err := script.Run(ctx, common.RDB, []string{routingSub2APIRedisLockKey(channelID)}, lockOwner).Err(); err != nil {
		common.SysError(fmt.Sprintf("sub2api login lock release failed: channel_id=%d err=%v", channelID, err))
	}
}

func loginRoutingSub2API(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (string, time.Duration, error) {
	email := strings.TrimSpace(credentials.Sub2APIEmail)
	if email == "" || credentials.Sub2APIPassword == "" {
		markRoutingAuthFailure(binding.ChannelID)
		return "", 0, routingAuthErrorf("sub2api email and password are required")
	}
	body, err := common.Marshal(map[string]string{
		"email":    email,
		"password": credentials.Sub2APIPassword,
	})
	if err != nil {
		return "", 0, err
	}
	data, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodPost, "/api/v1/auth/login", "", body)
	if err != nil {
		return "", 0, err
	}

	var response struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		JWT         string `json:"jwt"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err = common.Unmarshal(data, &response); err != nil {
		var token string
		if strErr := common.Unmarshal(data, &token); strErr != nil {
			return "", 0, err
		}
		response.Token = token
	}
	token := strings.TrimSpace(response.Token)
	if token == "" {
		token = strings.TrimSpace(response.AccessToken)
	}
	if token == "" {
		token = strings.TrimSpace(response.JWT)
	}
	if token == "" {
		markRoutingAuthFailure(binding.ChannelID)
		return "", 0, routingAuthErrorf("sub2api login did not return a token")
	}
	clearRoutingAuthFailure(binding.ChannelID)
	ttl := time.Duration(response.ExpiresIn) * time.Second
	if ttl <= routingSub2APITokenTTLBuffer {
		ttl = time.Hour
	} else {
		ttl -= routingSub2APITokenTTLBuffer
	}
	return token, ttl, nil
}

func routingSub2APIRequest(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials, method string, path string, bearer string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(binding.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(bearer); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: time.Duration(defaultTimeoutSeconds) * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		markRoutingAuthFailure(binding.ChannelID)
		return nil, routingAuthErrorf("sub2api endpoint %s returned %s", path, response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sub2api endpoint %s returned %s", path, response.Status)
	}

	var envelope routingSub2APIEnvelope
	if err = common.DecodeJson(io.LimitReader(response.Body, maxRatioConfigBytes), &envelope); err != nil {
		return nil, err
	}
	if (envelope.Success != nil && !*envelope.Success) || envelope.Code != 0 {
		message := envelope.Message
		if strings.TrimSpace(message) == "" {
			message = "sub2api endpoint returned code != 0"
		}
		authFailure := routingSub2APIEnvelopeAuthFailure(envelope)
		message = routingCleanCredentialErrorMessage(message, credentials)
		if authFailure {
			markRoutingAuthFailure(binding.ChannelID)
			return nil, routingAuthErrorf("%s", message)
		}
		return nil, fmt.Errorf("%s", message)
	}
	return envelope.Data, nil
}

func routingSub2APIEnvelopeAuthFailure(envelope routingSub2APIEnvelope) bool {
	if envelope.Code == http.StatusUnauthorized || envelope.Code == http.StatusForbidden {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(envelope.Message))
	if normalized == "" {
		return false
	}
	authMarkers := []string{
		"unauthorized",
		"unauthorised",
		"forbidden",
		"invalid token",
		"expired token",
		"token invalid",
		"token expired",
		"missing token",
		"token missing",
		"access token",
		"jwt",
		"bearer",
		"credential",
		"authentication",
		"authorization",
		"not authorized",
		"not authorised",
		"password",
		"login",
		"permission denied",
	}
	for _, marker := range authMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func fetchRoutingSub2APIBalance(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials, jwt string) error {
	token := strings.TrimSpace(credentials.GatewayAPIKey)
	if token == "" {
		token = strings.TrimSpace(jwt)
	}
	if token == "" {
		return nil
	}
	raw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/v1/usage", token, nil)
	if err != nil {
		return err
	}
	if balance, ok := parseRoutingSub2APIBalance(raw); ok {
		now := common.GetTimestamp()
		routinghotcache.SetBalance(binding.ChannelID, routinghotcache.BalanceSnapshot{
			Known:       true,
			Balance:     balance,
			UpdatedUnix: now,
		})
		if err = model.UpsertRoutingChannelBalance(binding.ChannelID, balance, now); err != nil {
			common.SysError(fmt.Sprintf("persist routing balance failed: channel_id=%d err=%v", binding.ChannelID, err))
		}
	}
	return nil
}

func parseRoutingSub2APIBalance(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var data map[string]float64
	if err := common.Unmarshal(raw, &data); err == nil {
		for _, key := range []string{"balance", "remaining", "remaining_balance", "available_balance"} {
			if value, ok := data[key]; ok && !math.IsNaN(value) && !math.IsInf(value, 0) {
				return value, true
			}
		}
		quota, hasQuota := data["quota"]
		usedQuota, hasUsedQuota := data["used_quota"]
		if hasQuota && hasUsedQuota {
			return (quota - usedQuota) / common.QuotaPerUnit, true
		}
	}
	var wrapper struct {
		Usage map[string]float64 `json:"usage"`
	}
	if err := common.Unmarshal(raw, &wrapper); err == nil && wrapper.Usage != nil {
		data, encodedErr := common.Marshal(wrapper.Usage)
		if encodedErr == nil {
			return parseRoutingSub2APIBalance(data)
		}
	}
	return 0, false
}

func parseRoutingSub2APIGroups(raw json.RawMessage) map[string]routingSub2APIGroup {
	groups := map[string]routingSub2APIGroup{}
	parseRoutingSub2APIGroupArray(raw, groups)
	if len(groups) > 0 {
		return groups
	}

	var wrapper struct {
		Groups json.RawMessage `json:"groups"`
		Items  json.RawMessage `json:"items"`
		List   json.RawMessage `json:"list"`
	}
	if err := common.Unmarshal(raw, &wrapper); err == nil {
		for _, nested := range []json.RawMessage{wrapper.Groups, wrapper.Items, wrapper.List} {
			parseRoutingSub2APIGroupArray(nested, groups)
			if len(groups) > 0 {
				return groups
			}
		}
	}

	var ratios map[string]float64
	if err := common.Unmarshal(raw, &ratios); err == nil {
		for group, ratio := range ratios {
			groups[group] = routingSub2APIGroup{ID: group, RateMultiplier: ratio}
		}
	}
	return groups
}

func parseRoutingSub2APIGroupArray(raw json.RawMessage, groups map[string]routingSub2APIGroup) {
	if len(raw) == 0 {
		return
	}
	var items []routingSub2APIGroup
	if err := common.Unmarshal(raw, &items); err != nil {
		return
	}
	for _, item := range items {
		key := routingSub2APIGroupName(item)
		if key == "" {
			continue
		}
		groups[key] = item
	}
}

func parseRoutingSub2APIRates(raw json.RawMessage) map[string]float64 {
	rates := map[string]float64{}
	if len(raw) == 0 {
		return rates
	}
	if err := common.Unmarshal(raw, &rates); err == nil && len(rates) > 0 {
		return rates
	}
	var wrapper struct {
		Rates  json.RawMessage `json:"rates"`
		Groups json.RawMessage `json:"groups"`
	}
	if err := common.Unmarshal(raw, &wrapper); err == nil {
		for _, nested := range []json.RawMessage{wrapper.Rates, wrapper.Groups} {
			if err = common.Unmarshal(nested, &rates); err == nil && len(rates) > 0 {
				return rates
			}
		}
	}
	var items []routingSub2APIGroup
	if err := common.Unmarshal(raw, &items); err == nil {
		for _, item := range items {
			key := routingSub2APIGroupName(item)
			if key != "" {
				rates[key] = routingSub2APIGroupRatio(item)
			}
		}
	}
	return rates
}

func parseRoutingSub2APIChannels(raw json.RawMessage) []routingSub2APIChannel {
	if len(raw) == 0 {
		return nil
	}
	var channels []routingSub2APIChannel
	if err := common.Unmarshal(raw, &channels); err == nil {
		return channels
	}
	var wrapper struct {
		Channels json.RawMessage `json:"channels"`
		Models   json.RawMessage `json:"models"`
		Items    json.RawMessage `json:"items"`
		List     json.RawMessage `json:"list"`
	}
	if err := common.Unmarshal(raw, &wrapper); err == nil {
		for _, nested := range []json.RawMessage{wrapper.Channels, wrapper.Models, wrapper.Items, wrapper.List} {
			if err = common.Unmarshal(nested, &channels); err == nil && len(channels) > 0 {
				return channels
			}
		}
	}
	return channels
}

func routingSub2APIGroupName(group routingSub2APIGroup) string {
	for _, value := range []string{group.ID, group.Group, group.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func routingSub2APIGroupRatio(group routingSub2APIGroup) float64 {
	if group.RateMultiplier > 0 {
		return group.RateMultiplier
	}
	if group.Ratio > 0 {
		return group.Ratio
	}
	return 0
}

func routingSub2APIChannelServesBinding(channel routingSub2APIChannel, binding model.RoutingChannelBinding) bool {
	if channel.ClaudeCodeOnly && !binding.ServesClaudeCode {
		return false
	}
	if len(channel.Groups) > 0 {
		for _, group := range channel.Groups {
			if group == binding.UpstreamGroup || group == "all" {
				return true
			}
		}
		return false
	}
	if strings.TrimSpace(channel.Group) != "" && channel.Group != binding.UpstreamGroup && channel.Group != "all" {
		return false
	}
	return true
}

func routingSub2APIChannelModels(channel routingSub2APIChannel) []string {
	modelSet := map[string]struct{}{}
	for _, modelName := range channel.Models {
		if trimmed := strings.TrimSpace(modelName); trimmed != "" {
			modelSet[trimmed] = struct{}{}
		}
	}
	for _, modelName := range []string{channel.ModelName, channel.Model, channel.Name} {
		if trimmed := strings.TrimSpace(modelName); trimmed != "" {
			modelSet[trimmed] = struct{}{}
		}
	}
	models := make([]string, 0, len(modelSet))
	for modelName := range modelSet {
		models = append(models, modelName)
	}
	return models
}

func routingSub2APIChannelSnapshot(channelID int, modelName string, groupRatio float64, groupFound bool, channel routingSub2APIChannel, now int64) model.RoutingCostSnapshot {
	baseRatio := firstPositiveFloat(channel.InputPrice, channel.Input, channel.Price, channel.Rate, channel.Ratio)
	outputPrice := firstPositiveFloat(channel.OutputPrice, channel.Output)
	completionRatio := 1.0
	if baseRatio > 0 && outputPrice > 0 {
		completionRatio = outputPrice / baseRatio
	}
	confidence := model.RoutingCostConfidenceFull
	if !groupFound || baseRatio <= 0 {
		confidence = model.RoutingCostConfidenceGroupOnly
	}
	if baseRatio <= 0 {
		baseRatio = 1
	}

	extras := map[string]float64{}
	if outputPrice > 0 {
		extras["output_price"] = outputPrice
	}
	if value := firstPositiveFloat(channel.CachePrice, channel.Cache); value > 0 {
		extras["cache_price"] = value
	}
	if value := firstPositiveFloat(channel.PerRequestPrice, channel.PerRequest); value > 0 {
		extras["per_request_price"] = value
	}
	if value := firstPositiveFloat(channel.ImagePrice, channel.Image); value > 0 {
		extras["image_price"] = value
	}
	var extrasJSON *string
	if len(extras) > 0 {
		if data, err := common.Marshal(extras); err == nil {
			encoded := string(data)
			extrasJSON = &encoded
		}
	}

	return model.RoutingCostSnapshot{
		ChannelID:       channelID,
		ModelName:       modelName,
		GroupRatio:      groupRatio,
		BaseRatio:       baseRatio,
		CompletionRatio: completionRatio,
		BillingMode:     strings.TrimSpace(channel.BillingMode),
		ExtrasJSON:      extrasJSON,
		Confidence:      confidence,
		SnapshotTS:      now,
	}
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value
		}
	}
	return 0
}

func getRoutingSub2APICachedJWT(ctx context.Context, channelID int) (string, bool) {
	if channelID <= 0 {
		return "", false
	}
	if common.RedisEnabled && common.RDB != nil {
		encrypted, err := common.RDB.Get(ctx, routingSub2APIRedisJWTKey(channelID)).Result()
		if err == nil {
			token, decryptErr := common.DecryptAESGCMString(encrypted)
			return token, decryptErr == nil && strings.TrimSpace(token) != ""
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			common.SysError(fmt.Sprintf("sub2api jwt cache get failed: channel_id=%d err=%v", channelID, err))
		}
	}

	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	deleteRoutingSub2APIJWTCacheExpiredLocked(now)
	entry, ok := routingSub2APIJWTCache.values[channelID]
	routingSub2APIJWTCache.Unlock()
	if !ok {
		return "", false
	}
	token, err := common.DecryptAESGCMString(entry.Ciphertext)
	return token, err == nil && strings.TrimSpace(token) != ""
}

func setRoutingSub2APICachedJWT(ctx context.Context, channelID int, token string, ttl time.Duration) {
	if channelID <= 0 || strings.TrimSpace(token) == "" {
		return
	}
	encrypted, err := common.EncryptAESGCMString(token)
	if err != nil {
		common.SysError(fmt.Sprintf("sub2api jwt cache encrypt failed: channel_id=%d err=%v", channelID, err))
		return
	}
	if common.RedisEnabled && common.RDB != nil {
		if err = common.RDB.Set(ctx, routingSub2APIRedisJWTKey(channelID), encrypted, ttl).Err(); err != nil {
			common.SysError(fmt.Sprintf("sub2api jwt cache set failed: channel_id=%d err=%v", channelID, err))
		}
	}
	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	routingSub2APIJWTCache.values[channelID] = routingSub2APIJWTCacheEntry{
		Ciphertext: encrypted,
		ExpiresAt:  now + int64(ttl.Seconds()),
	}
	pruneRoutingSub2APIJWTCacheLocked(now, routingSub2APIMaxJWTEntries)
}

func pruneRoutingSub2APIJWTCacheLocked(now int64, maxEntries int) {
	deleteRoutingSub2APIJWTCacheExpiredLocked(now)
	if maxEntries <= 0 {
		maxEntries = routingSub2APIDefaultMaxJWTEntries
	}
	excess := len(routingSub2APIJWTCache.values) - maxEntries
	if excess <= 0 {
		return
	}

	type candidate struct {
		channelID int
		expiresAt int64
	}
	candidates := make([]candidate, 0, len(routingSub2APIJWTCache.values))
	for channelID, entry := range routingSub2APIJWTCache.values {
		candidates = append(candidates, candidate{channelID: channelID, expiresAt: entry.ExpiresAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].expiresAt == candidates[j].expiresAt {
			return candidates[i].channelID < candidates[j].channelID
		}
		return candidates[i].expiresAt < candidates[j].expiresAt
	})
	for _, entry := range candidates[:excess] {
		if _, ok := routingSub2APIJWTCache.values[entry.channelID]; !ok {
			continue
		}
		delete(routingSub2APIJWTCache.values, entry.channelID)
		routingSub2APIJWTCache.evictions++
	}
}

func deleteRoutingSub2APIJWTCacheExpiredLocked(now int64) {
	for channelID, entry := range routingSub2APIJWTCache.values {
		if entry.ExpiresAt <= now {
			delete(routingSub2APIJWTCache.values, channelID)
			routingSub2APIJWTCache.expirations++
		}
	}
}

// RoutingSub2APIJWTCacheRuntimeStats returns a read-only snapshot of the
// current process's local JWT cache counters and entry count.
func RoutingSub2APIJWTCacheRuntimeStats() RoutingSub2APIJWTCacheStats {
	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	return RoutingSub2APIJWTCacheStats{
		Entries:     len(routingSub2APIJWTCache.values),
		Expirations: routingSub2APIJWTCache.expirations,
		Evictions:   routingSub2APIJWTCache.evictions,
	}
}

func routingSub2APIRedisJWTKey(channelID int) string {
	return fmt.Sprintf("routing:sub2api:jwt:%d", channelID)
}

func routingSub2APIRedisLockKey(channelID int) string {
	return fmt.Sprintf("routing:sub2api:lock:%d", channelID)
}

func routingCleanCredentialErrorMessage(message string, credentials model.RoutingCredentials) string {
	message = routingCleanUpstreamErrorMessage(message)
	for _, secret := range []string{
		credentials.NewAPIAccessToken,
		credentials.GatewayAPIKey,
		credentials.Sub2APIEmail,
		credentials.Sub2APIPassword,
		credentials.Sub2APIToken,
	} {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	return message
}

func routingSub2APICachedJWTForTest(channelID int) string {
	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	return routingSub2APIJWTCache.values[channelID].Ciphertext
}

func resetRoutingSub2APITestState() {
	routingSub2APILoginCoordinator.Lock()
	defer routingSub2APILoginCoordinator.Unlock()
	routingSub2APILoginCoordinator.group = &singleflight.Group{}
	routingSub2APILoginCoordinator.generation++

	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	routingSub2APIJWTCache.values = map[int]routingSub2APIJWTCacheEntry{}
	routingSub2APIJWTCache.expirations = 0
	routingSub2APIJWTCache.evictions = 0
	routingSub2APIMaxJWTEntries = routingSub2APIDefaultMaxJWTEntries
}
