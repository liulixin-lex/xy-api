package controller

import (
	"bytes"
	"context"
	"crypto/subtle"
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
	"github.com/go-redis/redis/v8"
	"golang.org/x/sync/singleflight"
)

const (
	routingSub2APILockTTL              = 30 * time.Second
	routingSub2APIDefaultUnlockTimeout = 2 * time.Second
	routingSub2APITokenTTLBuffer       = 60 * time.Second
	routingSub2APIDefaultTokenTTL      = time.Hour
	routingSub2APIMaxTokenTTL          = 24 * time.Hour
	routingSub2APIRetiredTTL           = routingSub2APIMaxTokenTTL + routingSub2APILockTTL
	routingSub2APIMaxTokenBytes        = 16 << 10
	routingSub2APIMaxCachedJWTBytes    = 24 << 10
	routingSub2APIDefaultMaxJWTEntries = 4_096
	routingSub2APIDefaultMaxJWTBytes   = 16 << 20
	routingSub2APIDefaultMaxRetired    = 4_096
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

type routingSub2APIAuthKey struct {
	ChannelID   int
	Fingerprint string
}

type routingSub2APIGeneration struct {
	Value     uint64
	ExpiresAt int64
}

type routingSub2APIJWTActivationFence struct {
	authKey                  routingSub2APIAuthKey
	localGeneration          uint64
	localRetiredExpiresAt    int64
	hasLocalRetirement       bool
	redisRetirementMarker    string
	hasRedisRetirementMarker bool
}

// RoutingSub2APIJWTCacheStats reports this process's local JWT cache only.
// Redis-backed JWT cache activity is intentionally excluded.
type RoutingSub2APIJWTCacheStats struct {
	Entries     int
	Bytes       int
	Expirations int64
	Evictions   int64
}

var routingSub2APIJWTCache = struct {
	sync.Mutex
	values      map[routingSub2APIAuthKey]routingSub2APIJWTCacheEntry
	expirations int64
	evictions   int64
}{
	values: map[routingSub2APIAuthKey]routingSub2APIJWTCacheEntry{},
}

var routingSub2APIMaxJWTEntries = routingSub2APIDefaultMaxJWTEntries
var routingSub2APIMaxJWTBytes = routingSub2APIDefaultMaxJWTBytes
var routingSub2APIMaxRetired = routingSub2APIDefaultMaxRetired
var routingSub2APIUnlockTimeout = routingSub2APIDefaultUnlockTimeout
var routingSub2APILoginCoordinator = struct {
	sync.RWMutex
	group       *singleflight.Group
	epoch       uint64
	generations map[routingSub2APIAuthKey]routingSub2APIGeneration
	retired     map[routingSub2APIAuthKey]int64
}{
	group:       &singleflight.Group{},
	generations: map[routingSub2APIAuthKey]routingSub2APIGeneration{},
	retired:     map[routingSub2APIAuthKey]int64{},
}

func fetchRoutingSub2APICostSnapshots(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) ([]model.RoutingCostSnapshot, error) {
	managedJWT := strings.TrimSpace(credentials.Sub2APIToken) == ""
	authKey := newRoutingSub2APIAuthKey(binding, credentials)
	for attempt := 0; attempt < 2; attempt++ {
		jwt, err := routingSub2APIJWT(ctx, binding, credentials)
		if err != nil {
			return nil, err
		}
		snapshots, managedJWTRejected, err := fetchRoutingSub2APICostSnapshotsWithJWT(ctx, binding, credentials, jwt)
		if err == nil {
			return snapshots, nil
		}
		if !managedJWT || !managedJWTRejected || attempt > 0 {
			return nil, err
		}
		evictRoutingSub2APIJWT(ctx, authKey, jwt)
	}
	return nil, errors.New("sub2api fetch retry exhausted")
}

func fetchRoutingSub2APICostSnapshotsWithJWT(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials, jwt string) ([]model.RoutingCostSnapshot, bool, error) {
	if err := fetchRoutingSub2APIBalance(ctx, binding, credentials, jwt); err != nil && routingUpstreamAuthError(err) {
		return nil, strings.TrimSpace(credentials.GatewayAPIKey) == "", err
	}

	groupsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/available", jwt, nil)
	if err != nil {
		return nil, routingUpstreamAuthError(err), err
	}
	groups := parseRoutingSub2APIGroups(groupsRaw)
	groupInfo, groupFound := groups[binding.UpstreamGroup]
	groupRatio := routingSub2APIGroupRatio(groupInfo)
	if groupRatio <= 0 {
		groupRatio = 1
	}

	ratesRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/rates", jwt, nil)
	if err != nil {
		return nil, routingUpstreamAuthError(err), err
	}
	if ratio, ok := parseRoutingSub2APIRates(ratesRaw)[binding.UpstreamGroup]; ok && ratio > 0 {
		groupRatio = ratio
	}

	channelsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/channels/available", jwt, nil)
	if err != nil {
		return nil, routingUpstreamAuthError(err), err
	}
	channels := parseRoutingSub2APIChannels(channelsRaw)

	now := common.GetTimestamp()
	snapshots := make([]model.RoutingCostSnapshot, 0, len(channels))
	modelNameMap, err := routingModelReverseMapping(ctx, binding.ChannelID)
	if err != nil {
		return nil, false, err
	}
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
	return snapshots, false, nil
}

func routingSub2APIJWT(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (string, error) {
	if token := strings.TrimSpace(credentials.Sub2APIToken); token != "" {
		if err := validateRoutingSub2APIToken(token); err != nil {
			return "", err
		}
		return token, nil
	}
	authKey := newRoutingSub2APIAuthKey(binding, credentials)
	if token, ok, err, retired := readRoutingSub2APICachedJWT(ctx, authKey); err != nil {
		return "", err
	} else if retired {
		return "", routingAuthErrorf("sub2api authentication identity is retired")
	} else if ok {
		return token, nil
	}

	routingSub2APILoginCoordinator.RLock()
	loginGroup := routingSub2APILoginCoordinator.group
	loginEpoch := routingSub2APILoginCoordinator.epoch
	loginGeneration := routingSub2APILoginCoordinator.generations[authKey].Value
	if routingSub2APILocallyRetiredLocked(authKey, common.GetTimestamp()) {
		routingSub2APILoginCoordinator.RUnlock()
		return "", routingAuthErrorf("sub2api authentication identity is retired")
	}
	resultChannel := loginGroup.DoChan(routingSub2APISingleflightKey(authKey), func() (any, error) {
		sharedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routingSub2APILockTTL)
		defer cancel()

		if token, ok, cacheErr, retired := readRoutingSub2APICachedJWT(sharedCtx, authKey); cacheErr != nil {
			return "", cacheErr
		} else if retired {
			return "", routingAuthErrorf("sub2api authentication identity is retired")
		} else if ok {
			return token, nil
		}

		unlockRedis, lockErr := acquireRoutingSub2APIRedisLock(sharedCtx, authKey)
		if lockErr != nil {
			return "", lockErr
		}
		if unlockRedis != nil {
			defer unlockRedis()
		}
		if token, ok, cacheErr, retired := readRoutingSub2APICachedJWT(sharedCtx, authKey); cacheErr != nil {
			return "", cacheErr
		} else if retired {
			return "", routingAuthErrorf("sub2api authentication identity is retired")
		} else if ok {
			return token, nil
		}
		routingSub2APILoginCoordinator.RLock()
		loginCurrent := loginEpoch == routingSub2APILoginCoordinator.epoch &&
			loginGeneration == routingSub2APILoginCoordinator.generations[authKey].Value &&
			!routingSub2APILocallyRetiredLocked(authKey, common.GetTimestamp())
		routingSub2APILoginCoordinator.RUnlock()
		if !loginCurrent {
			return "", routingAuthErrorf("sub2api authentication identity is retired")
		}

		token, ttl, loginErr := loginRoutingSub2API(sharedCtx, binding, credentials)
		if loginErr != nil {
			return "", loginErr
		}
		setRoutingSub2APICachedJWTIfCurrent(sharedCtx, authKey, token, ttl, loginEpoch, loginGeneration)
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

func acquireRoutingSub2APIRedisLock(ctx context.Context, authKey routingSub2APIAuthKey) (func(), error) {
	if !common.RedisEnabled || common.RDB == nil {
		return nil, nil
	}
	lockKey := routingSub2APIRedisLockKey(authKey)
	lockOwner := common.GetRandomString(32)
	deadline := time.Now().Add(routingSub2APILockTTL)
	for {
		acquired, err := common.RDB.SetNX(ctx, lockKey, lockOwner, routingSub2APILockTTL).Result()
		if err != nil {
			return nil, fmt.Errorf("sub2api login lock failed: %w", err)
		}
		if acquired {
			return func() {
				releaseRoutingSub2APIRedisLock(authKey, lockOwner)
			}, nil
		}
		if _, ok, cacheErr, retired := readRoutingSub2APICachedJWT(ctx, authKey); cacheErr != nil {
			return nil, cacheErr
		} else if retired {
			return nil, routingAuthErrorf("sub2api authentication identity is retired")
		} else if ok {
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

func releaseRoutingSub2APIRedisLock(authKey routingSub2APIAuthKey, lockOwner string) {
	ctx, cancel := context.WithTimeout(context.Background(), routingSub2APIUnlockTimeout)
	defer cancel()

	script := redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`)
	if err := script.Run(ctx, common.RDB, []string{routingSub2APIRedisLockKey(authKey)}, lockOwner).Err(); err != nil {
		common.SysError(fmt.Sprintf("sub2api login lock release failed: channel_id=%d err=%v", authKey.ChannelID, err))
	}
}

func loginRoutingSub2API(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (string, time.Duration, error) {
	email := strings.TrimSpace(credentials.Sub2APIEmail)
	if email == "" || credentials.Sub2APIPassword == "" {
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
		ExpiresIn   *int64 `json:"expires_in"`
	}
	if err = common.Unmarshal(data, &response); err != nil {
		var token string
		if strErr := common.Unmarshal(data, &token); strErr != nil {
			return "", 0, errors.New("invalid sub2api login response")
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
		return "", 0, routingAuthErrorf("sub2api login did not return a token")
	}
	if err = validateRoutingSub2APIToken(token); err != nil {
		return "", 0, err
	}
	return token, routingSub2APILoginCacheTTL(response.ExpiresIn), nil
}

func validateRoutingSub2APIToken(token string) error {
	if len(token) > routingSub2APIMaxTokenBytes {
		return routingAuthErrorf("sub2api token exceeds size limit")
	}
	return nil
}

func routingSub2APILoginCacheTTL(expiresIn *int64) time.Duration {
	if expiresIn == nil {
		return routingSub2APIDefaultTokenTTL
	}
	bufferSeconds := int64(routingSub2APITokenTTLBuffer / time.Second)
	if *expiresIn <= bufferSeconds {
		return 0
	}
	maxSeconds := int64(routingSub2APIMaxTokenTTL / time.Second)
	expiresInSeconds := *expiresIn
	if expiresInSeconds > maxSeconds {
		expiresInSeconds = maxSeconds
	}
	return time.Duration(expiresInSeconds-bufferSeconds) * time.Second
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

	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, routingAuthErrorf("sub2api endpoint %s returned %s", path, response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sub2api endpoint %s returned %s", path, response.Status)
	}

	bodyBytes, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return nil, err
	}
	var envelope routingSub2APIEnvelope
	if err = common.Unmarshal(bodyBytes, &envelope); err != nil {
		return nil, errors.New("invalid sub2api response")
	}
	if (envelope.Success != nil && !*envelope.Success) || envelope.Code != 0 {
		message := envelope.Message
		if strings.TrimSpace(message) == "" {
			message = "sub2api endpoint returned code != 0"
		}
		authFailure := routingSub2APIEnvelopeAuthFailure(envelope)
		message = routingCleanCredentialErrorMessage(message, credentials, bearer)
		if authFailure {
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
		return persistRoutingBalance(ctx, binding, balance, common.GetTimestamp())
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

func getRoutingSub2APICachedJWT(ctx context.Context, authKey routingSub2APIAuthKey) (string, bool) {
	token, ok, _, _ := readRoutingSub2APICachedJWT(ctx, authKey)
	return token, ok
}

func readRoutingSub2APICachedJWT(ctx context.Context, authKey routingSub2APIAuthKey) (string, bool, error, bool) {
	if authKey.ChannelID <= 0 || authKey.Fingerprint == "" {
		return "", false, nil, false
	}
	routingSub2APILoginCoordinator.RLock()
	locallyRetired := routingSub2APILocallyRetiredLocked(authKey, common.GetTimestamp())
	routingSub2APILoginCoordinator.RUnlock()
	if locallyRetired {
		return "", false, nil, true
	}
	if common.RedisEnabled && common.RDB != nil {
		values, err := common.RDB.MGet(
			ctx,
			routingSub2APIRedisRetiredKey(authKey),
			routingSub2APIRedisJWTKey(authKey),
		).Result()
		if err == nil {
			if len(values) != 2 {
				return "", false, errors.New("sub2api jwt cache returned an invalid response"), false
			}
			if values[0] != nil {
				routingSub2APIJWTCache.Lock()
				if _, ok := routingSub2APIJWTCache.values[authKey]; ok {
					delete(routingSub2APIJWTCache.values, authKey)
					routingSub2APIJWTCache.evictions++
				}
				routingSub2APIJWTCache.Unlock()
				return "", false, nil, true
			}
			if encrypted, ok := values[1].(string); ok {
				if token, valid := decodeRoutingSub2APICachedJWT(encrypted); valid {
					return token, true, nil, false
				}
			}
			return "", false, nil, false
		}
		common.SysError(fmt.Sprintf("sub2api jwt cache get failed: channel_id=%d err=%v", authKey.ChannelID, err))
		return "", false, fmt.Errorf("sub2api jwt cache get failed: %w", err), false
	}

	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	deleteRoutingSub2APIJWTCacheExpiredLocked(now)
	entry, ok := routingSub2APIJWTCache.values[authKey]
	if ok && len(entry.Ciphertext) > routingSub2APIMaxCachedJWTBytes {
		delete(routingSub2APIJWTCache.values, authKey)
		routingSub2APIJWTCache.evictions++
		ok = false
	}
	routingSub2APIJWTCache.Unlock()
	if !ok {
		return "", false, nil, false
	}
	token, ok := decodeRoutingSub2APICachedJWT(entry.Ciphertext)
	if ok {
		return token, true, nil, false
	}
	routingSub2APIJWTCache.Lock()
	if current, exists := routingSub2APIJWTCache.values[authKey]; exists && current.Ciphertext == entry.Ciphertext {
		delete(routingSub2APIJWTCache.values, authKey)
		routingSub2APIJWTCache.evictions++
	}
	routingSub2APIJWTCache.Unlock()
	return "", false, nil, false
}

func setRoutingSub2APICachedJWT(ctx context.Context, authKey routingSub2APIAuthKey, token string, ttl time.Duration) {
	routingSub2APILoginCoordinator.RLock()
	defer routingSub2APILoginCoordinator.RUnlock()
	if routingSub2APILocallyRetiredLocked(authKey, common.GetTimestamp()) {
		return
	}
	storeRoutingSub2APICachedJWT(ctx, authKey, token, ttl)
}

func setRoutingSub2APICachedJWTIfCurrent(ctx context.Context, authKey routingSub2APIAuthKey, token string, ttl time.Duration, epoch uint64, generation uint64) {
	routingSub2APILoginCoordinator.RLock()
	defer routingSub2APILoginCoordinator.RUnlock()
	if epoch != routingSub2APILoginCoordinator.epoch || generation != routingSub2APILoginCoordinator.generations[authKey].Value {
		return
	}
	if routingSub2APILocallyRetiredLocked(authKey, common.GetTimestamp()) {
		return
	}
	storeRoutingSub2APICachedJWT(ctx, authKey, token, ttl)
}

func storeRoutingSub2APICachedJWT(ctx context.Context, authKey routingSub2APIAuthKey, token string, ttl time.Duration) {
	token = strings.TrimSpace(token)
	if authKey.ChannelID <= 0 || authKey.Fingerprint == "" || token == "" || ttl <= 0 || validateRoutingSub2APIToken(token) != nil {
		return
	}
	if ttl > routingSub2APIMaxTokenTTL {
		ttl = routingSub2APIMaxTokenTTL
	}
	ttlSeconds := int64(ttl / time.Second)
	if ttlSeconds <= 0 {
		return
	}
	encrypted, err := common.EncryptAESGCMString(token)
	if err != nil {
		common.SysError(fmt.Sprintf("sub2api jwt cache encrypt failed: channel_id=%d err=%v", authKey.ChannelID, err))
		return
	}
	if len(encrypted) > routingSub2APIMaxCachedJWTBytes {
		return
	}
	if common.RedisEnabled && common.RDB != nil {
		const setUnlessRetiredScript = `
if redis.call("EXISTS", KEYS[1]) == 1 then
  return 0
end
redis.call("SET", KEYS[2], ARGV[1], "PX", ARGV[2])
return 1`
		written, writeErr := common.RDB.Eval(
			ctx,
			setUnlessRetiredScript,
			[]string{routingSub2APIRedisRetiredKey(authKey), routingSub2APIRedisJWTKey(authKey)},
			encrypted,
			ttl.Milliseconds(),
		).Int64()
		if writeErr != nil {
			err = writeErr
			common.SysError(fmt.Sprintf("sub2api jwt cache set failed: channel_id=%d err=%v", authKey.ChannelID, err))
			return
		}
		if written != 1 {
			return
		}
	}
	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	maxInt64 := int64(^uint64(0) >> 1)
	expiresAt := maxInt64
	if now <= maxInt64-ttlSeconds {
		expiresAt = now + ttlSeconds
	}
	routingSub2APIJWTCache.values[authKey] = routingSub2APIJWTCacheEntry{
		Ciphertext: encrypted,
		ExpiresAt:  expiresAt,
	}
	pruneRoutingSub2APIJWTCacheLocked(now, routingSub2APIMaxJWTEntries)
}

func decodeRoutingSub2APICachedJWT(encrypted string) (string, bool) {
	if encrypted == "" || len(encrypted) > routingSub2APIMaxCachedJWTBytes {
		return "", false
	}
	token, err := common.DecryptAESGCMString(encrypted)
	if err != nil {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || validateRoutingSub2APIToken(token) != nil {
		return "", false
	}
	return token, true
}

func pruneRoutingSub2APIJWTCacheLocked(now int64, maxEntries int) {
	deleteRoutingSub2APIJWTCacheExpiredLocked(now)
	if maxEntries <= 0 {
		maxEntries = routingSub2APIDefaultMaxJWTEntries
	}
	maxBytes := routingSub2APIMaxJWTBytes
	if maxBytes <= 0 {
		maxBytes = routingSub2APIDefaultMaxJWTBytes
	}
	cacheBytes := routingSub2APIJWTCacheBytesLocked()
	if len(routingSub2APIJWTCache.values) <= maxEntries && cacheBytes <= maxBytes {
		return
	}

	type candidate struct {
		authKey   routingSub2APIAuthKey
		expiresAt int64
	}
	candidates := make([]candidate, 0, len(routingSub2APIJWTCache.values))
	for authKey, entry := range routingSub2APIJWTCache.values {
		candidates = append(candidates, candidate{authKey: authKey, expiresAt: entry.ExpiresAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].expiresAt == candidates[j].expiresAt {
			if candidates[i].authKey.ChannelID == candidates[j].authKey.ChannelID {
				return candidates[i].authKey.Fingerprint < candidates[j].authKey.Fingerprint
			}
			return candidates[i].authKey.ChannelID < candidates[j].authKey.ChannelID
		}
		return candidates[i].expiresAt < candidates[j].expiresAt
	})
	for _, candidate := range candidates {
		if len(routingSub2APIJWTCache.values) <= maxEntries && cacheBytes <= maxBytes {
			break
		}
		entry, ok := routingSub2APIJWTCache.values[candidate.authKey]
		if !ok {
			continue
		}
		delete(routingSub2APIJWTCache.values, candidate.authKey)
		cacheBytes -= len(entry.Ciphertext)
		routingSub2APIJWTCache.evictions++
	}
}

func routingSub2APIJWTCacheBytesLocked() int {
	cacheBytes := 0
	for _, entry := range routingSub2APIJWTCache.values {
		cacheBytes += len(entry.Ciphertext)
	}
	return cacheBytes
}

func deleteRoutingSub2APIJWTCacheExpiredLocked(now int64) {
	for authKey, entry := range routingSub2APIJWTCache.values {
		if entry.ExpiresAt <= now {
			delete(routingSub2APIJWTCache.values, authKey)
			routingSub2APIJWTCache.expirations++
		}
	}
}

// RoutingSub2APIJWTCacheRuntimeStats returns a read-only snapshot of the
// current process's local JWT cache counters, entry count, and ciphertext bytes.
func RoutingSub2APIJWTCacheRuntimeStats() RoutingSub2APIJWTCacheStats {
	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	return RoutingSub2APIJWTCacheStats{
		Entries:     len(routingSub2APIJWTCache.values),
		Bytes:       routingSub2APIJWTCacheBytesLocked(),
		Expirations: routingSub2APIJWTCache.expirations,
		Evictions:   routingSub2APIJWTCache.evictions,
	}
}

func newRoutingSub2APIAuthKey(binding model.RoutingChannelBinding, credentials model.RoutingCredentials) routingSub2APIAuthKey {
	credentialFields := []string{"persisted"}
	if binding.ID > 0 {
		credentialFields = append(credentialFields, strconv.Itoa(binding.ID))
		if binding.EncCredentials != nil {
			credentialFields = append(credentialFields, *binding.EncCredentials)
		}
	} else {
		credentialFields = []string{
			"inline",
			strings.TrimSpace(credentials.Sub2APIEmail),
			credentials.Sub2APIPassword,
			strings.TrimSpace(credentials.Sub2APIToken),
		}
	}
	fingerprintFields := append([]string{
		strings.TrimRight(strings.TrimSpace(binding.BaseURL), "/"),
		strings.TrimSpace(binding.UpstreamType),
		strconv.Itoa(binding.KeyVersion),
	}, credentialFields...)

	var fingerprintInput strings.Builder
	for _, field := range fingerprintFields {
		fingerprintInput.WriteString(strconv.Itoa(len(field)))
		fingerprintInput.WriteByte(':')
		fingerprintInput.WriteString(field)
	}
	return routingSub2APIAuthKey{
		ChannelID:   binding.ChannelID,
		Fingerprint: common.GenerateHMAC(fingerprintInput.String()),
	}
}

func routingSub2APIRedisJWTKey(authKey routingSub2APIAuthKey) string {
	return fmt.Sprintf("routing:sub2api:jwt:%d:%s", authKey.ChannelID, authKey.Fingerprint)
}

func routingSub2APIRedisRetiredKey(authKey routingSub2APIAuthKey) string {
	return fmt.Sprintf("routing:sub2api:retired:%d:%s", authKey.ChannelID, authKey.Fingerprint)
}

func routingSub2APILegacyRedisJWTKey(channelID int) string {
	return fmt.Sprintf("routing:sub2api:jwt:%d", channelID)
}

func routingSub2APIRedisLockKey(authKey routingSub2APIAuthKey) string {
	return fmt.Sprintf("routing:sub2api:lock:%d:%s", authKey.ChannelID, authKey.Fingerprint)
}

func routingSub2APISingleflightKey(authKey routingSub2APIAuthKey) string {
	return fmt.Sprintf("%d:%s", authKey.ChannelID, authKey.Fingerprint)
}

func evictRoutingSub2APIJWT(ctx context.Context, authKey routingSub2APIAuthKey, rejectedToken string) {
	rejectedToken = strings.TrimSpace(rejectedToken)
	if authKey.ChannelID <= 0 || authKey.Fingerprint == "" || rejectedToken == "" {
		return
	}

	redisDeleted := false
	if common.RedisEnabled && common.RDB != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routingSub2APIUnlockTimeout)
		defer cancel()
		encrypted, err := common.RDB.Get(cleanupCtx, routingSub2APIRedisJWTKey(authKey)).Result()
		if err == nil {
			cachedToken, valid := decodeRoutingSub2APICachedJWT(encrypted)
			if valid && routingSub2APITokensEqual(cachedToken, rejectedToken) {
				const compareAndDeleteScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`
				deleted, deleteErr := common.RDB.Eval(
					cleanupCtx,
					compareAndDeleteScript,
					[]string{routingSub2APIRedisJWTKey(authKey)},
					encrypted,
				).Int64()
				if deleteErr != nil {
					common.SysError(fmt.Sprintf("sub2api jwt cache eviction failed: channel_id=%d err=%v", authKey.ChannelID, deleteErr))
				} else {
					redisDeleted = deleted == 1
				}
			}
		} else if !errors.Is(err, redis.Nil) {
			common.SysError(fmt.Sprintf("sub2api jwt cache eviction read failed: channel_id=%d err=%v", authKey.ChannelID, err))
		}
	}

	routingSub2APILoginCoordinator.Lock()
	routingSub2APIJWTCache.Lock()
	localDeleted := false
	if entry, ok := routingSub2APIJWTCache.values[authKey]; ok {
		cachedToken, valid := decodeRoutingSub2APICachedJWT(entry.Ciphertext)
		if valid && routingSub2APITokensEqual(cachedToken, rejectedToken) {
			localDeleted = true
		}
	}
	if localDeleted {
		delete(routingSub2APIJWTCache.values, authKey)
		routingSub2APIJWTCache.evictions++
	}
	routingSub2APIJWTCache.Unlock()
	if redisDeleted || ((!common.RedisEnabled || common.RDB == nil) && localDeleted) {
		advanceRoutingSub2APIGenerationLocked(authKey, common.GetTimestamp(), routingSub2APILockTTL)
	}
	routingSub2APILoginCoordinator.Unlock()
}

func invalidateRoutingSub2APIJWT(ctx context.Context, binding model.RoutingChannelBinding) {
	if binding.ChannelID <= 0 {
		return
	}
	authKey := newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{})

	routingSub2APILoginCoordinator.Lock()
	now := common.GetTimestamp()
	advanceRoutingSub2APIGenerationLocked(authKey, now, routingSub2APIRetiredTTL)
	routingSub2APILoginCoordinator.retired[authKey] = now + int64(routingSub2APIRetiredTTL/time.Second)
	pruneRoutingSub2APILocalRetiredLocked(now)
	routingSub2APIJWTCache.Lock()
	for cachedKey := range routingSub2APIJWTCache.values {
		if cachedKey.ChannelID != binding.ChannelID {
			continue
		}
		delete(routingSub2APIJWTCache.values, cachedKey)
		routingSub2APIJWTCache.evictions++
	}
	routingSub2APIJWTCache.Unlock()
	routingSub2APILoginCoordinator.Unlock()

	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routingSub2APIUnlockTimeout)
	defer cancel()
	retiredMarker := common.GetRandomString(32)
	const retireScript = `
redis.call("SET", KEYS[1], ARGV[2], "PX", ARGV[1])
return redis.call("DEL", KEYS[2], KEYS[3])`
	if _, err := common.RDB.Eval(
		cleanupCtx,
		retireScript,
		[]string{
			routingSub2APIRedisRetiredKey(authKey),
			routingSub2APIRedisJWTKey(authKey),
			routingSub2APILegacyRedisJWTKey(binding.ChannelID),
		},
		routingSub2APIRetiredTTL.Milliseconds(),
		retiredMarker,
	).Result(); err != nil {
		common.SysError(fmt.Sprintf("sub2api jwt cache invalidation failed: channel_id=%d err=%v", binding.ChannelID, err))
	}
}

func prepareRoutingSub2APIJWTActivation(ctx context.Context, binding model.RoutingChannelBinding) (routingSub2APIJWTActivationFence, error) {
	if binding.ChannelID <= 0 || binding.UpstreamType != model.RoutingUpstreamTypeSub2API {
		return routingSub2APIJWTActivationFence{}, nil
	}
	authKey := newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{})
	fence := routingSub2APIJWTActivationFence{authKey: authKey}
	now := common.GetTimestamp()
	routingSub2APILoginCoordinator.RLock()
	fence.localGeneration = routingSub2APILoginCoordinator.generations[authKey].Value
	fence.localRetiredExpiresAt, fence.hasLocalRetirement = routingSub2APILoginCoordinator.retired[authKey]
	fence.hasLocalRetirement = fence.hasLocalRetirement && fence.localRetiredExpiresAt > now
	routingSub2APILoginCoordinator.RUnlock()

	if !common.RedisEnabled {
		return fence, nil
	}
	if common.RDB == nil {
		return routingSub2APIJWTActivationFence{}, errors.New("sub2api auth activation cache is unavailable")
	}
	observeCtx, cancel := context.WithTimeout(ctx, routingSub2APIUnlockTimeout)
	defer cancel()
	retiredKey := routingSub2APIRedisRetiredKey(authKey)
	retiredMarker, err := common.RDB.Get(observeCtx, retiredKey).Result()
	if errors.Is(err, redis.Nil) {
		return fence, nil
	}
	if err != nil {
		return routingSub2APIJWTActivationFence{}, fmt.Errorf("observe sub2api auth retirement: %w", err)
	}
	fence.redisRetirementMarker = retiredMarker
	fence.hasRedisRetirementMarker = true
	return fence, nil
}

func activateRoutingSub2APIJWT(ctx context.Context, fence routingSub2APIJWTActivationFence) {
	if fence.authKey.ChannelID <= 0 || fence.authKey.Fingerprint == "" {
		return
	}
	if fence.hasLocalRetirement {
		routingSub2APILoginCoordinator.Lock()
		generationUnchanged := routingSub2APILoginCoordinator.generations[fence.authKey].Value == fence.localGeneration
		retiredExpiresAt, stillRetired := routingSub2APILoginCoordinator.retired[fence.authKey]
		if generationUnchanged && stillRetired && retiredExpiresAt == fence.localRetiredExpiresAt {
			delete(routingSub2APILoginCoordinator.retired, fence.authKey)
		}
		routingSub2APILoginCoordinator.Unlock()
	}

	if !fence.hasRedisRetirementMarker {
		return
	}
	if !common.RedisEnabled || common.RDB == nil {
		common.SysError(fmt.Sprintf("sub2api auth activation failed: channel_id=%d cache unavailable", fence.authKey.ChannelID))
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routingSub2APIUnlockTimeout)
	defer cancel()
	const compareAndActivateScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`
	if err := common.RDB.Eval(
		cleanupCtx,
		compareAndActivateScript,
		[]string{routingSub2APIRedisRetiredKey(fence.authKey)},
		fence.redisRetirementMarker,
	).Err(); err != nil {
		common.SysError(fmt.Sprintf("sub2api auth activation failed: channel_id=%d err=%v", fence.authKey.ChannelID, err))
	}
}

func routingSub2APILocallyRetiredLocked(authKey routingSub2APIAuthKey, now int64) bool {
	expiresAt, ok := routingSub2APILoginCoordinator.retired[authKey]
	return ok && expiresAt > now
}

func pruneRoutingSub2APILocalRetiredLocked(now int64) {
	for authKey, expiresAt := range routingSub2APILoginCoordinator.retired {
		if expiresAt <= now {
			delete(routingSub2APILoginCoordinator.retired, authKey)
		}
	}
	maxEntries := routingSub2APIMaxRetired
	if maxEntries <= 0 {
		maxEntries = routingSub2APIDefaultMaxRetired
	}
	excess := len(routingSub2APILoginCoordinator.retired) - maxEntries
	if excess <= 0 {
		return
	}
	type retiredCandidate struct {
		authKey   routingSub2APIAuthKey
		expiresAt int64
	}
	candidates := make([]retiredCandidate, 0, len(routingSub2APILoginCoordinator.retired))
	for authKey, expiresAt := range routingSub2APILoginCoordinator.retired {
		candidates = append(candidates, retiredCandidate{authKey: authKey, expiresAt: expiresAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].expiresAt == candidates[j].expiresAt {
			if candidates[i].authKey.ChannelID == candidates[j].authKey.ChannelID {
				return candidates[i].authKey.Fingerprint < candidates[j].authKey.Fingerprint
			}
			return candidates[i].authKey.ChannelID < candidates[j].authKey.ChannelID
		}
		return candidates[i].expiresAt < candidates[j].expiresAt
	})
	for _, candidate := range candidates[:excess] {
		delete(routingSub2APILoginCoordinator.retired, candidate.authKey)
	}
}

func advanceRoutingSub2APIGenerationLocked(authKey routingSub2APIAuthKey, now int64, ttl time.Duration) {
	generation := routingSub2APILoginCoordinator.generations[authKey]
	generation.Value++
	expiresAt := now + int64(ttl/time.Second)
	if expiresAt > generation.ExpiresAt {
		generation.ExpiresAt = expiresAt
	}
	routingSub2APILoginCoordinator.generations[authKey] = generation
	pruneRoutingSub2APIGenerationsLocked(now)
}

func pruneRoutingSub2APIGenerationsLocked(now int64) {
	for authKey, generation := range routingSub2APILoginCoordinator.generations {
		if generation.ExpiresAt <= now {
			delete(routingSub2APILoginCoordinator.generations, authKey)
		}
	}
	maxEntries := routingSub2APIMaxRetired
	if maxEntries <= 0 {
		maxEntries = routingSub2APIDefaultMaxRetired
	}
	excess := len(routingSub2APILoginCoordinator.generations) - maxEntries
	if excess <= 0 {
		return
	}
	type generationCandidate struct {
		authKey   routingSub2APIAuthKey
		expiresAt int64
	}
	candidates := make([]generationCandidate, 0, len(routingSub2APILoginCoordinator.generations))
	for authKey, generation := range routingSub2APILoginCoordinator.generations {
		candidates = append(candidates, generationCandidate{authKey: authKey, expiresAt: generation.ExpiresAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].expiresAt == candidates[j].expiresAt {
			if candidates[i].authKey.ChannelID == candidates[j].authKey.ChannelID {
				return candidates[i].authKey.Fingerprint < candidates[j].authKey.Fingerprint
			}
			return candidates[i].authKey.ChannelID < candidates[j].authKey.ChannelID
		}
		return candidates[i].expiresAt < candidates[j].expiresAt
	})
	for _, candidate := range candidates[:excess] {
		delete(routingSub2APILoginCoordinator.generations, candidate.authKey)
	}
}

func routingSub2APITokensEqual(left string, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func routingCleanCredentialErrorMessage(message string, credentials model.RoutingCredentials, additionalSecrets ...string) string {
	secrets := append(routingCredentialSecrets(credentials), additionalSecrets...)
	message = common.SanitizeErrorMessage(message, secrets...)
	if message == "" {
		return "upstream auth failed"
	}
	return message
}

func routingCredentialSecrets(credentials model.RoutingCredentials) []string {
	return []string{
		credentials.NewAPIAccessToken,
		credentials.GatewayAPIKey,
		credentials.Sub2APIEmail,
		credentials.Sub2APIPassword,
		credentials.Sub2APIToken,
	}
}

func routingSub2APICachedJWTForTest(channelID int) string {
	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	for authKey, entry := range routingSub2APIJWTCache.values {
		if authKey.ChannelID == channelID {
			return entry.Ciphertext
		}
	}
	return ""
}

func resetRoutingSub2APITestState() {
	routingSub2APILoginCoordinator.Lock()
	defer routingSub2APILoginCoordinator.Unlock()
	routingSub2APILoginCoordinator.group = &singleflight.Group{}
	routingSub2APILoginCoordinator.epoch++
	routingSub2APILoginCoordinator.generations = map[routingSub2APIAuthKey]routingSub2APIGeneration{}
	routingSub2APILoginCoordinator.retired = map[routingSub2APIAuthKey]int64{}

	routingSub2APIJWTCache.Lock()
	defer routingSub2APIJWTCache.Unlock()
	routingSub2APIJWTCache.values = map[routingSub2APIAuthKey]routingSub2APIJWTCacheEntry{}
	routingSub2APIJWTCache.expirations = 0
	routingSub2APIJWTCache.evictions = 0
	routingSub2APIMaxJWTEntries = routingSub2APIDefaultMaxJWTEntries
	routingSub2APIMaxJWTBytes = routingSub2APIDefaultMaxJWTBytes
	routingSub2APIMaxRetired = routingSub2APIDefaultMaxRetired
}
