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
	"reflect"
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
	routingSub2APIMaxPricingIntervals  = 64
)

type routingSub2APIEnvelope struct {
	Code    *int            `json:"code"`
	Success *bool           `json:"success,omitempty"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// routingSub2APIID matches the numeric int64 IDs in Sub2API's public wire DTOs.
type routingSub2APIID string

func (id *routingSub2APIID) UnmarshalJSON(data []byte) error {
	if common.GetJsonType(data) != "number" {
		return errors.New("invalid sub2api ID")
	}
	value := strings.TrimSpace(string(data))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return errors.New("invalid sub2api ID")
	}
	*id = routingSub2APIID(strconv.FormatInt(parsed, 10))
	return nil
}

type routingSub2APIGroup struct {
	ID                           routingSub2APIID `json:"id"`
	Name                         string           `json:"name"`
	Description                  string           `json:"description"`
	Platform                     string           `json:"platform"`
	SubscriptionType             string           `json:"subscription_type"`
	RateMultiplier               float64          `json:"rate_multiplier"`
	Ratio                        float64          `json:"ratio"`
	IsExclusive                  bool             `json:"is_exclusive"`
	Status                       string           `json:"status"`
	DailyLimitUSD                *float64         `json:"daily_limit_usd"`
	WeeklyLimitUSD               *float64         `json:"weekly_limit_usd"`
	MonthlyLimitUSD              *float64         `json:"monthly_limit_usd"`
	AllowImageGeneration         bool             `json:"allow_image_generation"`
	AllowBatchImageGeneration    bool             `json:"allow_batch_image_generation"`
	ImageRateIndependent         bool             `json:"image_rate_independent"`
	ImageRateMultiplier          float64          `json:"image_rate_multiplier"`
	BatchImageDiscountMultiplier float64          `json:"batch_image_discount_multiplier"`
	BatchImageHoldMultiplier     float64          `json:"batch_image_hold_multiplier"`
	VideoRateIndependent         bool             `json:"video_rate_independent"`
	VideoRateMultiplier          float64          `json:"video_rate_multiplier"`
	PeakRateEnabled              bool             `json:"peak_rate_enabled"`
	PeakStart                    string           `json:"peak_start"`
	PeakEnd                      string           `json:"peak_end"`
	PeakRateMultiplier           float64          `json:"peak_rate_multiplier"`
	ImagePrice1K                 *float64         `json:"image_price_1k"`
	ImagePrice2K                 *float64         `json:"image_price_2k"`
	ImagePrice4K                 *float64         `json:"image_price_4k"`
	VideoPrice480P               *float64         `json:"video_price_480p"`
	VideoPrice720P               *float64         `json:"video_price_720p"`
	VideoPrice1080P              *float64         `json:"video_price_1080p"`
	WebSearchPricePerCall        *float64         `json:"web_search_price_per_call"`
	ClaudeCodeOnly               bool             `json:"claude_code_only"`
	ServesClaudeCode             bool             `json:"serves_claude_code"`
	FallbackGroupID              *int64           `json:"fallback_group_id"`
	FallbackGroupIDOnInvalid     *int64           `json:"fallback_group_id_on_invalid_request"`
	AllowMessagesDispatch        bool             `json:"allow_messages_dispatch"`
	RequireOAuthOnly             bool             `json:"require_oauth_only"`
	RequirePrivacySet            bool             `json:"require_privacy_set"`
	RPMLimit                     int              `json:"rpm_limit"`
	contractError                string
}

type routingSub2APIGroupMetadata struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Platform         string `json:"platform,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
	ClaudeCodeOnly   bool   `json:"claude_code_only"`
}

type routingSub2APIPricingInterval struct {
	MinTokens       int      `json:"min_tokens"`
	MaxTokens       *int     `json:"max_tokens"`
	TierLabel       string   `json:"tier_label"`
	InputPrice      *float64 `json:"input_price"`
	OutputPrice     *float64 `json:"output_price"`
	CacheWritePrice *float64 `json:"cache_write_price"`
	CacheReadPrice  *float64 `json:"cache_read_price"`
	PerRequestPrice *float64 `json:"per_request_price"`
}

type routingSub2APIChannel struct {
	Model            string                      `json:"model"`
	ModelName        string                      `json:"model_name"`
	Name             string                      `json:"name"`
	Models           []string                    `json:"models"`
	Platform         string                      `json:"platform"`
	Group            string                      `json:"group"`
	Groups           []string                    `json:"groups"`
	ClaudeCodeOnly   bool                        `json:"claude_code_only"`
	ServesClaudeCode bool                        `json:"serves_claude_code"`
	BillingMode      string                      `json:"billing_mode"`
	PerTokenPrices   bool                        `json:"per_token_prices,omitempty"`
	OfficialPricing  *routingSub2APIModelPricing `json:"official_pricing,omitempty"`

	InputPrice       float64                         `json:"input_price"`
	OutputPrice      float64                         `json:"output_price"`
	CachePrice       float64                         `json:"cache_price"`
	CacheWritePrice  float64                         `json:"cache_write_price"`
	CacheReadPrice   float64                         `json:"cache_read_price"`
	PerRequestPrice  float64                         `json:"per_request_price"`
	ImagePrice       float64                         `json:"image_price"`
	ImageOutputPrice float64                         `json:"image_output_price"`
	Price            float64                         `json:"price"`
	Rate             float64                         `json:"rate"`
	Ratio            float64                         `json:"ratio"`
	Input            float64                         `json:"input"`
	Output           float64                         `json:"output"`
	Cache            float64                         `json:"cache"`
	PerRequest       float64                         `json:"per_request"`
	Image            float64                         `json:"image"`
	Intervals        []routingSub2APIPricingInterval `json:"intervals"`
}

type routingSub2APIAvailableChannel struct {
	Name        string                          `json:"name"`
	Description string                          `json:"description"`
	Platforms   []routingSub2APIPlatformSection `json:"platforms"`
}

type routingSub2APIPlatformSection struct {
	Platform        string                         `json:"platform"`
	Groups          []routingSub2APIGroup          `json:"groups"`
	SupportedModels []routingSub2APISupportedModel `json:"supported_models"`
}

type routingSub2APISupportedModel struct {
	Name     string                      `json:"name"`
	Platform string                      `json:"platform"`
	Pricing  *routingSub2APIModelPricing `json:"pricing"`
}

type routingSub2APIModelPricing struct {
	BillingMode      string                          `json:"billing_mode"`
	InputPrice       *float64                        `json:"input_price"`
	OutputPrice      *float64                        `json:"output_price"`
	CacheWritePrice  *float64                        `json:"cache_write_price"`
	CacheReadPrice   *float64                        `json:"cache_read_price"`
	ImageOutputPrice *float64                        `json:"image_output_price"`
	PerRequestPrice  *float64                        `json:"per_request_price"`
	Intervals        []routingSub2APIPricingInterval `json:"intervals"`
}

type routingSub2APIAccountPricing struct {
	Groups           map[string]routingSub2APIGroup
	Rates            map[string]float64
	Channels         []routingSub2APIChannel
	Profile          routingSub2APIUserProfile
	BalanceKnown     bool
	Balance          float64
	BalanceUpdatedAt int64
	SyncStatus       string
	SyncError        string
}

type routingSub2APIUserProfile struct {
	ID       int64
	Email    string
	Username string
	Balance  *float64
}

type routingSub2APIProfileObservation struct {
	Profile    routingSub2APIUserProfile
	ObservedAt int64
}

func (pricing routingSub2APIAccountPricing) VersionMaterial() any {
	return struct {
		Groups   map[string]routingSub2APIGroup `json:"groups"`
		Rates    map[string]float64             `json:"rates"`
		Channels []routingSub2APIChannel        `json:"channels"`
	}{
		Groups:   pricing.Groups,
		Rates:    pricing.Rates,
		Channels: pricing.Channels,
	}
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
	pricing, err := fetchRoutingSub2APIAccountPricingForGroups(
		ctx,
		binding,
		credentials,
		[]string{binding.UpstreamGroup},
	)
	if err != nil {
		return nil, err
	}
	groupInfo, groupFound := pricing.Groups[binding.UpstreamGroup]
	if !groupFound {
		return nil, errors.New("sub2api bound group is not available to the account")
	}
	if groupFound && groupInfo.PeakRateEnabled {
		return nil, errors.New("sub2api peak group pricing requires unavailable server timezone context")
	}
	groupRatio := routingSub2APIGroupRatio(groupInfo)
	if ratio, ok := routingSub2APIResolvedGroupRate(pricing.Groups, pricing.Rates, binding.UpstreamGroup); ok {
		if !routingCostNonNegativeFinite(ratio) || ratio <= 0 {
			return nil, errors.New("sub2api returned an invalid group ratio")
		}
		groupRatio = ratio
		groupFound = true
	}
	if !routingCostNonNegativeFinite(groupRatio) || groupRatio <= 0 {
		return nil, errors.New("sub2api returned an invalid group ratio")
	}

	now := common.GetTimestamp()
	snapshots := make([]model.RoutingCostSnapshot, 0, len(pricing.Channels))
	modelNameMap, err := routingModelReverseMapping(ctx, binding.ChannelID)
	if err != nil {
		return nil, err
	}
	for _, channel := range pricing.Channels {
		if !routingSub2APIChannelServesBinding(channel, binding) {
			continue
		}
		for _, modelName := range routingSub2APIChannelModels(channel) {
			if localName, ok := modelNameMap[modelName]; ok {
				modelName = localName
			}
			snapshot, snapshotErr := routingSub2APIChannelSnapshot(binding.ChannelID, modelName, groupRatio, groupFound, channel, now)
			if snapshotErr != nil {
				return nil, fmt.Errorf("invalid sub2api price for model %s: %w", modelName, snapshotErr)
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	if len(snapshots) == 0 {
		return nil, errors.New("sub2api returned no pricing for the bound group")
	}
	return snapshots, nil
}

func fetchRoutingSub2APIAccountPricing(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (routingSub2APIAccountPricing, error) {
	return fetchRoutingSub2APIAccountPricingForGroups(ctx, binding, credentials, nil)
}

func fetchRoutingSub2APIAccountPricingForGroups(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	requestedGroups []string,
) (routingSub2APIAccountPricing, error) {
	return fetchRoutingSub2APIAccountPricingForGroupsWithProfile(
		ctx,
		binding,
		credentials,
		nil,
		requestedGroups,
	)
}

func fetchRoutingSub2APIAccountPricingForGroupsWithProfile(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	preloadedProfile *routingSub2APIProfileObservation,
	requestedGroups []string,
) (routingSub2APIAccountPricing, error) {
	usePreloadedProfile := preloadedProfile != nil
	return fetchRoutingSub2APIWithJWTRetry(
		ctx,
		binding,
		credentials,
		func(
			ctx context.Context,
			binding model.RoutingChannelBinding,
			credentials model.RoutingCredentials,
			jwt string,
		) (routingSub2APIAccountPricing, bool, error) {
			profile := preloadedProfile
			if !usePreloadedProfile {
				profile = nil
			}
			usePreloadedProfile = false
			return fetchRoutingSub2APIAccountPricingWithJWTAndProfile(
				ctx,
				binding,
				credentials,
				jwt,
				profile,
				requestedGroups,
			)
		},
	)
}

func fetchRoutingSub2APIGroupDiscoveryPayload(ctx context.Context, binding model.RoutingChannelBinding) (_ routingPricingResponse, err error) {
	credentials, err := binding.GetCredentials()
	if err != nil {
		return routingPricingResponse{}, routingSafeErrorWithCredentials(err, model.RoutingCredentials{})
	}
	defer func() {
		if err != nil {
			err = routingSafeErrorWithCredentials(err, credentials)
		}
	}()

	groups, err := fetchRoutingSub2APIAccountGroups(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	uniqueGroups := make(map[string]routingSub2APIGroup)
	for _, group := range groups {
		identity := strings.TrimSpace(string(group.ID))
		if identity != "" {
			uniqueGroups[identity] = group
		}
	}
	groupIDs := make([]string, 0, len(uniqueGroups))
	for groupID := range uniqueGroups {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	usableGroup := make(map[string]string, len(groupIDs))
	groupMeta := make(map[string]routingSub2APIGroupMetadata, len(groupIDs))
	for _, groupID := range groupIDs {
		group := uniqueGroups[groupID]
		groupName := strings.TrimSpace(group.Name)
		if groupName == "" {
			groupName = groupID
		}
		usableGroup[groupName] = groupID
		groupMeta[groupName] = routingSub2APIGroupMetadata{
			ID:               groupID,
			Name:             groupName,
			Platform:         strings.TrimSpace(group.Platform),
			SubscriptionType: strings.ToLower(strings.TrimSpace(group.SubscriptionType)),
			ClaudeCodeOnly:   group.ClaudeCodeOnly,
		}
	}
	return routingPricingResponse{
		Success: true, UsableGroup: usableGroup, Sub2APIGroupMeta: groupMeta,
	}, nil
}

func fetchRoutingSub2APIAccountGroups(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (map[string]routingSub2APIGroup, error) {
	return fetchRoutingSub2APIWithJWTRetry(ctx, binding, credentials, fetchRoutingSub2APIAccountGroupsWithJWT)
}

func fetchRoutingSub2APIAccountProfile(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (routingSub2APIUserProfile, error) {
	return fetchRoutingSub2APIWithJWTRetry(ctx, binding, credentials, fetchRoutingSub2APIAccountProfileWithJWT)
}

func fetchRoutingSub2APIWithJWTRetry[T any](
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	fetch func(context.Context, model.RoutingChannelBinding, model.RoutingCredentials, string) (T, bool, error),
) (T, error) {
	var zero T
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return zero, err
	}
	managedJWT := strings.TrimSpace(credentials.Sub2APIToken) == ""
	authKey := newRoutingSub2APIAuthKey(binding, credentials)
	for attempt := 0; attempt < 2; attempt++ {
		jwt, err := routingSub2APIJWT(ctx, binding, credentials)
		if err != nil {
			return zero, err
		}
		result, managedJWTRejected, err := fetch(ctx, binding, credentials, jwt)
		if err == nil {
			return result, nil
		}
		if !managedJWT || !managedJWTRejected || attempt > 0 {
			return zero, err
		}
		evictRoutingSub2APIJWT(ctx, authKey, jwt)
	}
	return zero, errors.New("sub2api fetch retry exhausted")
}

func fetchRoutingSub2APIAccountGroupsWithJWT(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials, jwt string) (map[string]routingSub2APIGroup, bool, error) {
	if _, err := fetchRoutingSub2APIUserProfile(ctx, binding, credentials, jwt); err != nil {
		return nil, routingUpstreamAuthError(err), err
	}
	groupsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/available", jwt, nil)
	if err != nil {
		return nil, routingUpstreamAuthError(err), err
	}
	groups, err := parseRoutingSub2APIGroups(groupsRaw)
	if err != nil {
		return nil, false, err
	}
	if len(groups) == 0 {
		return nil, false, errors.New("sub2api returned no available groups")
	}
	if err := validateRoutingSub2APIGroupContract(groups); err != nil {
		return nil, false, err
	}
	return groups, false, nil
}

func fetchRoutingSub2APIAccountProfileWithJWT(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	jwt string,
) (routingSub2APIUserProfile, bool, error) {
	profile, err := fetchRoutingSub2APIUserProfile(ctx, binding, credentials, jwt)
	if err != nil {
		return routingSub2APIUserProfile{}, routingUpstreamAuthError(err), err
	}
	return profile, false, nil
}

func fetchRoutingSub2APIAccountPricingWithJWT(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	jwt string,
	requestedGroups []string,
) (routingSub2APIAccountPricing, bool, error) {
	return fetchRoutingSub2APIAccountPricingWithJWTAndProfile(
		ctx,
		binding,
		credentials,
		jwt,
		nil,
		requestedGroups,
	)
}

func fetchRoutingSub2APIAccountPricingWithJWTAndProfile(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	jwt string,
	preloadedProfile *routingSub2APIProfileObservation,
	requestedGroups []string,
) (routingSub2APIAccountPricing, bool, error) {
	profile := routingSub2APIUserProfile{}
	profileObservedAt := int64(0)
	if preloadedProfile != nil && preloadedProfile.ObservedAt > 0 {
		profile = preloadedProfile.Profile
		profileObservedAt = preloadedProfile.ObservedAt
	} else {
		var profileErr error
		profile, profileErr = fetchRoutingSub2APIUserProfile(ctx, binding, credentials, jwt)
		if profileErr != nil {
			return routingSub2APIAccountPricing{}, routingUpstreamAuthError(profileErr), profileErr
		}
		profileObservedAt = common.GetTimestamp()
	}
	balance := 0.0
	balanceKnown := profile.Balance != nil
	if balanceKnown {
		balance = *profile.Balance
	}

	groupsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/available", jwt, nil)
	if err != nil {
		return routingSub2APIAccountPricing{}, routingUpstreamAuthError(err), err
	}
	groups, err := parseRoutingSub2APIGroups(groupsRaw)
	if err != nil {
		return routingSub2APIAccountPricing{}, false, err
	}
	if len(groups) == 0 {
		return routingSub2APIAccountPricing{}, false, errors.New("sub2api returned no available groups")
	}
	if len(requestedGroups) > 0 {
		groups, err = selectRoutingSub2APIGroups(groups, requestedGroups)
		if err != nil {
			return routingSub2APIAccountPricing{}, false, err
		}
	} else if err := validateRoutingSub2APIGroupContract(groups); err != nil {
		return routingSub2APIAccountPricing{}, false, err
	}

	ratesRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/groups/rates", jwt, nil)
	if err != nil {
		return routingSub2APIAccountPricing{}, routingUpstreamAuthError(err), err
	}
	var rates map[string]float64
	if len(requestedGroups) > 0 {
		rates, err = parseRoutingSub2APIRatesForGroups(ratesRaw, groups)
	} else {
		rates, err = parseRoutingSub2APIRates(ratesRaw)
	}
	if err != nil {
		return routingSub2APIAccountPricing{}, false, err
	}

	channelsRaw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/channels/available", jwt, nil)
	if err != nil {
		return routingSub2APIAccountPricing{}, routingUpstreamAuthError(err), err
	}
	availableChannels, err := parseRoutingSub2APIAvailableChannels(channelsRaw)
	if err != nil {
		return routingSub2APIAccountPricing{}, false, err
	}
	if len(requestedGroups) > 0 {
		availableChannels, err = selectRoutingSub2APIAvailableChannels(groups, availableChannels)
	} else {
		err = validateRoutingSub2APIAvailableChannelGroups(groups, availableChannels)
	}
	if err != nil {
		return routingSub2APIAccountPricing{}, false, err
	}
	channels, err := routingSub2APIChannelsFromAvailable(availableChannels)
	if err != nil {
		return routingSub2APIAccountPricing{}, false, err
	}
	if len(channels) == 0 {
		if len(requestedGroups) > 0 {
			return routingSub2APIAccountPricing{}, false, errors.New("sub2api returned no pricing for the bound group")
		}
		return routingSub2APIAccountPricing{}, false, errors.New("sub2api available channels returned no usable model pricing")
	}
	pricing := routingSub2APIAccountPricing{
		Groups:           groups,
		Rates:            rates,
		Channels:         channels,
		Profile:          profile,
		BalanceKnown:     balanceKnown,
		Balance:          balance,
		BalanceUpdatedAt: profileObservedAt,
		SyncStatus:       model.RoutingUpstreamSyncStatusSuccess,
	}
	if !balanceKnown {
		pricing.BalanceUpdatedAt = 0
	}
	return pricing, false, nil
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
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return nil, err
	}
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
	if response.StatusCode == http.StatusUnauthorized {
		return nil, routingAuthErrorf("sub2api endpoint %s returned %s", path, response.Status)
	}
	if response.StatusCode == http.StatusForbidden {
		if routingSub2APIJWTManagementEndpoint(path) {
			bodyBytes, readErr := readRoutingCostJSON(response, defaultRoutingJSONLimits)
			if readErr == nil {
				var envelope routingSub2APIEnvelope
				if common.Unmarshal(bodyBytes, &envelope) == nil && strings.TrimSpace(envelope.Message) != "" {
					message := routingCleanCredentialErrorMessage(envelope.Message, credentials, bearer)
					return nil, fmt.Errorf("sub2api management capability unavailable: %s", message)
				}
			}
			return nil, fmt.Errorf("sub2api management capability unavailable: endpoint %s returned %s", path, response.Status)
		}
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
	if envelope.Code == nil {
		return nil, errors.New("invalid sub2api response")
	}
	if (envelope.Success != nil && !*envelope.Success) || *envelope.Code != 0 {
		message := envelope.Message
		if strings.TrimSpace(message) == "" {
			message = "sub2api endpoint returned code != 0"
		}
		authFailure := routingSub2APIEnvelopeAuthFailure(path, envelope)
		message = routingCleanCredentialErrorMessage(message, credentials, bearer)
		if authFailure {
			return nil, routingAuthErrorf("%s", message)
		}
		return nil, fmt.Errorf("%s", message)
	}
	return envelope.Data, nil
}

func routingSub2APIEnvelopeAuthFailure(path string, envelope routingSub2APIEnvelope) bool {
	if envelope.Code != nil && *envelope.Code == http.StatusUnauthorized {
		return true
	}
	if envelope.Code != nil && *envelope.Code == http.StatusForbidden {
		return !routingSub2APIJWTManagementEndpoint(path)
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

func routingSub2APIJWTManagementEndpoint(path string) bool {
	switch path {
	case "/api/v1/auth/me",
		"/api/v1/groups/available",
		"/api/v1/groups/rates",
		"/api/v1/channels/available":
		return true
	default:
		return false
	}
}

func fetchRoutingSub2APIBalance(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials, jwt string) error {
	balance, known, err := fetchRoutingSub2APIBalanceValue(ctx, binding, credentials, jwt)
	if err != nil || !known {
		return err
	}
	return persistRoutingBalance(ctx, binding, balance, common.GetTimestamp())
}

func fetchRoutingSub2APIBalanceValue(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	jwt string,
) (float64, bool, error) {
	jwt = strings.TrimSpace(jwt)
	if jwt == "" {
		return 0, false, nil
	}
	profile, err := fetchRoutingSub2APIUserProfile(ctx, binding, credentials, jwt)
	if err != nil {
		return 0, false, err
	}
	if profile.Balance != nil {
		return *profile.Balance, true, nil
	}
	return 0, false, nil
}

func fetchRoutingSub2APIUserProfile(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	jwt string,
) (routingSub2APIUserProfile, error) {
	jwt = strings.TrimSpace(jwt)
	if jwt == "" {
		return routingSub2APIUserProfile{}, errors.New("sub2api JWT is required for account profile")
	}
	raw, err := routingSub2APIRequest(ctx, binding, credentials, http.MethodGet, "/api/v1/auth/me", jwt, nil)
	if err != nil {
		return routingSub2APIUserProfile{}, err
	}
	return parseRoutingSub2APIUserProfile(raw)
}

func parseRoutingSub2APIUserProfile(raw json.RawMessage) (routingSub2APIUserProfile, error) {
	if len(raw) == 0 || common.GetJsonType(raw) != "object" {
		return routingSub2APIUserProfile{}, errors.New("sub2api returned an invalid account profile")
	}
	var profile struct {
		ID       routingSub2APIID `json:"id"`
		Email    string           `json:"email"`
		Username string           `json:"username"`
		Balance  *float64         `json:"balance"`
	}
	if err := common.Unmarshal(raw, &profile); err != nil ||
		(profile.Balance != nil && (math.IsNaN(*profile.Balance) || math.IsInf(*profile.Balance, 0))) {
		return routingSub2APIUserProfile{}, errors.New("sub2api returned an invalid account profile")
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(string(profile.ID)), 10, 64)
	if err != nil || userID <= 0 {
		return routingSub2APIUserProfile{}, errors.New("sub2api account profile is missing a valid user ID")
	}
	return routingSub2APIUserProfile{
		ID:       userID,
		Email:    strings.TrimSpace(profile.Email),
		Username: strings.TrimSpace(profile.Username),
		Balance:  profile.Balance,
	}, nil
}

func parseRoutingSub2APIBalance(raw json.RawMessage) (float64, bool) {
	profile, err := parseRoutingSub2APIUserProfile(raw)
	if err == nil && profile.Balance != nil {
		return *profile.Balance, true
	}
	return 0, false
}

func parseRoutingSub2APIGroups(raw json.RawMessage) (map[string]routingSub2APIGroup, error) {
	if len(raw) == 0 || common.GetJsonType(raw) != "array" {
		return nil, errors.New("invalid sub2api groups response")
	}
	var items []routingSub2APIGroup
	if err := common.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("invalid sub2api groups response")
	}
	canonical := make(map[string]routingSub2APIGroup, len(items))
	conflictedIDs := make(map[string]struct{})
	for _, item := range items {
		groupID := strings.TrimSpace(string(item.ID))
		if groupID == "" {
			return nil, errors.New("sub2api group is missing a valid ID")
		}
		if existing, exists := canonical[groupID]; exists {
			if !routingSub2APIGroupMetadataEqual(existing, item) {
				conflictedIDs[groupID] = struct{}{}
			}
		} else {
			canonical[groupID] = item
		}
	}
	groups := make(map[string]routingSub2APIGroup, len(items)*2)
	nameOwners := make(map[string]map[string]struct{}, len(canonical))
	for groupID, group := range canonical {
		if _, conflicted := conflictedIDs[groupID]; conflicted {
			group.contractError = "sub2api group metadata is inconsistent"
		}
		canonical[groupID] = group
		groups[groupID] = group
		groupName := strings.TrimSpace(group.Name)
		if groupName != "" {
			if nameOwners[groupName] == nil {
				nameOwners[groupName] = make(map[string]struct{}, 1)
			}
			nameOwners[groupName][groupID] = struct{}{}
		}
	}
	for groupName, owners := range nameOwners {
		if len(owners) != 1 {
			continue
		}
		ownerID := ""
		for groupID := range owners {
			ownerID = groupID
		}
		if canonicalGroup, exists := canonical[groupName]; exists &&
			strings.TrimSpace(string(canonicalGroup.ID)) != ownerID {
			continue
		}
		groups[groupName] = canonical[ownerID]
	}
	return groups, nil
}

func routingSub2APIGroupMetadataEqual(left routingSub2APIGroup, right routingSub2APIGroup) bool {
	left.ID = routingSub2APIID(strings.TrimSpace(string(left.ID)))
	right.ID = routingSub2APIID(strings.TrimSpace(string(right.ID)))
	left.Name = strings.TrimSpace(left.Name)
	right.Name = strings.TrimSpace(right.Name)
	left.Platform = strings.ToLower(strings.TrimSpace(left.Platform))
	right.Platform = strings.ToLower(strings.TrimSpace(right.Platform))
	left.SubscriptionType = strings.ToLower(strings.TrimSpace(left.SubscriptionType))
	right.SubscriptionType = strings.ToLower(strings.TrimSpace(right.SubscriptionType))
	left.PeakStart = strings.TrimSpace(left.PeakStart)
	right.PeakStart = strings.TrimSpace(right.PeakStart)
	left.PeakEnd = strings.TrimSpace(left.PeakEnd)
	right.PeakEnd = strings.TrimSpace(right.PeakEnd)
	return reflect.DeepEqual(left, right)
}

func validateRoutingSub2APIGroupContract(groups map[string]routingSub2APIGroup) error {
	canonical := make(map[string]routingSub2APIGroup)
	for _, group := range groups {
		groupID := strings.TrimSpace(string(group.ID))
		if err := validateRoutingSub2APIGroupMetadata(group); err != nil {
			return err
		}
		name := strings.TrimSpace(group.Name)
		platform := strings.TrimSpace(group.Platform)
		subscriptionType := strings.ToLower(strings.TrimSpace(group.SubscriptionType))
		if existing, exists := canonical[groupID]; exists {
			if strings.TrimSpace(existing.Name) != name ||
				!strings.EqualFold(strings.TrimSpace(existing.Platform), platform) ||
				!strings.EqualFold(strings.TrimSpace(existing.SubscriptionType), subscriptionType) {
				return errors.New("sub2api group metadata is inconsistent")
			}
			continue
		}
		group.Name = name
		group.Platform = platform
		group.SubscriptionType = subscriptionType
		canonical[groupID] = group
	}
	if len(canonical) == 0 {
		return errors.New("sub2api returned no canonical group metadata")
	}
	return nil
}

func validateRoutingSub2APIGroupMetadata(group routingSub2APIGroup) error {
	if group.contractError != "" {
		return errors.New(group.contractError)
	}
	groupID := strings.TrimSpace(string(group.ID))
	name := strings.TrimSpace(group.Name)
	platform := strings.TrimSpace(group.Platform)
	parsedID, idErr := strconv.ParseInt(groupID, 10, 64)
	if idErr != nil || parsedID <= 0 || name == "" || platform == "" {
		return errors.New("sub2api group metadata is missing id, name, or platform")
	}
	subscriptionType := strings.ToLower(strings.TrimSpace(group.SubscriptionType))
	if subscriptionType != "standard" && subscriptionType != "subscription" {
		return errors.New("sub2api group metadata contains an invalid subscription_type")
	}
	return nil
}

func selectRoutingSub2APIGroups(
	groups map[string]routingSub2APIGroup,
	requestedGroups []string,
) (map[string]routingSub2APIGroup, error) {
	available := make(map[string]routingSub2APIGroup)
	for key, group := range groups {
		groupID := strings.TrimSpace(string(group.ID))
		if groupID != "" && strings.TrimSpace(key) == groupID {
			available[groupID] = group
		}
	}
	canonical := make(map[string]routingSub2APIGroup, len(requestedGroups))
	for _, requestedGroup := range requestedGroups {
		requestedGroup = strings.TrimSpace(requestedGroup)
		matches := make(map[string]routingSub2APIGroup, 2)
		for groupID, group := range available {
			if requestedGroup == groupID || requestedGroup == strings.TrimSpace(group.Name) {
				matches[groupID] = group
			}
		}
		if requestedGroup == "" || len(matches) == 0 {
			return nil, errors.New("sub2api bound group is not available to the account")
		}
		if len(matches) > 1 {
			return nil, errors.New("sub2api bound group is ambiguous")
		}
		var group routingSub2APIGroup
		for _, matched := range matches {
			group = matched
		}
		if err := validateRoutingSub2APIGroupMetadata(group); err != nil {
			return nil, err
		}
		canonical[strings.TrimSpace(string(group.ID))] = group
	}
	selected := make(map[string]routingSub2APIGroup, len(canonical)*2)
	nameOwners := make(map[string][]string, len(canonical))
	for groupID, group := range canonical {
		selected[groupID] = group
		groupName := strings.TrimSpace(group.Name)
		if groupName != "" {
			nameOwners[groupName] = append(nameOwners[groupName], groupID)
		}
	}
	for groupName, owners := range nameOwners {
		if len(owners) != 1 {
			continue
		}
		ownerID := owners[0]
		if _, canonicalCollision := canonical[groupName]; canonicalCollision && groupName != ownerID {
			continue
		}
		selected[groupName] = canonical[ownerID]
	}
	return selected, nil
}

func validateRoutingSub2APIChannelGroupContract(
	groups map[string]routingSub2APIGroup,
	raw json.RawMessage,
) error {
	available, err := parseRoutingSub2APIAvailableChannels(raw)
	if err != nil {
		return err
	}
	return validateRoutingSub2APIAvailableChannelGroups(groups, available)
}

func validateRoutingSub2APIAvailableChannelGroups(
	groups map[string]routingSub2APIGroup,
	available []routingSub2APIAvailableChannel,
) error {
	canonical := make(map[string]routingSub2APIGroup)
	for _, group := range groups {
		groupID := strings.TrimSpace(string(group.ID))
		if groupID != "" {
			canonical[groupID] = group
		}
	}
	for _, channel := range available {
		for _, section := range channel.Platforms {
			for _, group := range section.Groups {
				groupID := strings.TrimSpace(string(group.ID))
				expected, exists := canonical[groupID]
				if !exists {
					return errors.New("sub2api channel group is missing from available groups")
				}
				if err := validateRoutingSub2APIAvailableChannelGroup(section.Platform, expected, group); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func selectRoutingSub2APIAvailableChannels(
	groups map[string]routingSub2APIGroup,
	available []routingSub2APIAvailableChannel,
) ([]routingSub2APIAvailableChannel, error) {
	canonical := make(map[string]routingSub2APIGroup)
	for _, group := range groups {
		canonical[strings.TrimSpace(string(group.ID))] = group
	}
	selectedChannels := make([]routingSub2APIAvailableChannel, 0, len(available))
	for _, channel := range available {
		selectedPlatforms := make([]routingSub2APIPlatformSection, 0, len(channel.Platforms))
		for _, section := range channel.Platforms {
			selectedGroups := make([]routingSub2APIGroup, 0, len(section.Groups))
			for _, group := range section.Groups {
				expected, exists := canonical[strings.TrimSpace(string(group.ID))]
				if !exists {
					continue
				}
				if err := validateRoutingSub2APIAvailableChannelGroup(section.Platform, expected, group); err != nil {
					return nil, err
				}
				selectedGroups = append(selectedGroups, group)
			}
			if len(selectedGroups) == 0 {
				continue
			}
			section.Groups = selectedGroups
			selectedPlatforms = append(selectedPlatforms, section)
		}
		if len(selectedPlatforms) == 0 {
			continue
		}
		channel.Platforms = selectedPlatforms
		selectedChannels = append(selectedChannels, channel)
	}
	return selectedChannels, nil
}

func validateRoutingSub2APIAvailableChannelGroup(
	sectionPlatform string,
	expected routingSub2APIGroup,
	actual routingSub2APIGroup,
) error {
	name := strings.TrimSpace(actual.Name)
	platform := strings.TrimSpace(actual.Platform)
	sectionPlatform = strings.TrimSpace(sectionPlatform)
	if sectionPlatform != "" && platform != "" && !strings.EqualFold(sectionPlatform, platform) {
		return errors.New("sub2api channel group platform is inconsistent")
	}
	subscriptionType := strings.ToLower(strings.TrimSpace(actual.SubscriptionType))
	if name == "" || name != strings.TrimSpace(expected.Name) ||
		platform == "" ||
		!strings.EqualFold(platform, strings.TrimSpace(expected.Platform)) ||
		subscriptionType != strings.ToLower(strings.TrimSpace(expected.SubscriptionType)) {
		return errors.New("sub2api channel group metadata does not match available groups")
	}
	return nil
}

func parseRoutingSub2APIAvailableChannels(raw json.RawMessage) ([]routingSub2APIAvailableChannel, error) {
	if len(raw) == 0 || common.GetJsonType(raw) != "array" {
		return nil, errors.New("invalid sub2api channels response")
	}
	var available []routingSub2APIAvailableChannel
	if err := common.Unmarshal(raw, &available); err != nil {
		return nil, errors.New("invalid sub2api channels response")
	}
	var shape []struct {
		Platforms json.RawMessage `json:"platforms"`
	}
	if err := common.Unmarshal(raw, &shape); err != nil {
		return nil, errors.New("invalid sub2api channels response")
	}
	for _, channel := range shape {
		if common.GetJsonType(channel.Platforms) != "array" {
			return nil, errors.New("sub2api channels response does not match the official nested contract")
		}
	}
	return available, nil
}

func parseRoutingSub2APIRates(raw json.RawMessage) (map[string]float64, error) {
	return parseRoutingSub2APIRatesForGroups(raw, nil)
}

func parseRoutingSub2APIRatesForGroups(
	raw json.RawMessage,
	groups map[string]routingSub2APIGroup,
) (map[string]float64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || common.GetJsonType(trimmed) != "object" {
		return nil, errors.New("invalid sub2api group rates response")
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(trimmed, &fields); err != nil {
		return nil, errors.New("invalid sub2api group rates response")
	}
	selectedGroupIDs := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		selectedGroupIDs[strings.TrimSpace(string(group.ID))] = struct{}{}
	}
	rates := make(map[string]float64, len(fields))
	for key, rawRate := range fields {
		groupID, err := strconv.ParseInt(key, 10, 64)
		if err != nil || groupID <= 0 || strconv.FormatInt(groupID, 10) != key {
			return nil, errors.New("invalid sub2api group rates response")
		}
		if len(selectedGroupIDs) > 0 {
			if _, selected := selectedGroupIDs[key]; !selected {
				continue
			}
		}
		var rate float64
		if common.Unmarshal(rawRate, &rate) != nil || !routingCostNonNegativeFinite(rate) || rate <= 0 {
			return nil, errors.New("invalid sub2api group rates response")
		}
		rates[key] = rate
	}
	return rates, nil
}

func parseRoutingSub2APIChannels(raw json.RawMessage) ([]routingSub2APIChannel, error) {
	if len(raw) == 0 || common.GetJsonType(raw) != "array" {
		return nil, errors.New("invalid sub2api channels response")
	}
	available, err := parseRoutingSub2APIAvailableChannels(raw)
	if err != nil {
		return nil, err
	}
	return routingSub2APIChannelsFromAvailable(available)
}

func routingSub2APIChannelsFromAvailable(available []routingSub2APIAvailableChannel) ([]routingSub2APIChannel, error) {
	channels := make([]routingSub2APIChannel, 0)
	for _, availableChannel := range available {
		for _, section := range availableChannel.Platforms {
			platform := strings.TrimSpace(section.Platform)
			if len(section.SupportedModels) > 0 && len(section.Groups) == 0 {
				return nil, errors.New("sub2api channel platform has models without groups")
			}
			groupSet := make(map[string]struct{})
			for _, group := range section.Groups {
				for _, alias := range routingSub2APIGroupAliases(group) {
					groupSet[alias] = struct{}{}
				}
			}
			groupAliases := make([]string, 0, len(groupSet))
			for alias := range groupSet {
				groupAliases = append(groupAliases, alias)
			}
			sort.Strings(groupAliases)

			for _, supportedModel := range section.SupportedModels {
				modelName := strings.TrimSpace(supportedModel.Name)
				if modelName == "" {
					return nil, errors.New("sub2api channel returned an empty model name")
				}
				modelPlatform := strings.TrimSpace(supportedModel.Platform)
				if platform != "" && modelPlatform != "" && platform != modelPlatform {
					return nil, errors.New("sub2api channel returned inconsistent model platform")
				}
				if modelPlatform == "" {
					modelPlatform = platform
				}
				channel := routingSub2APIChannel{
					Models:          []string{modelName},
					Platform:        modelPlatform,
					Groups:          append([]string(nil), groupAliases...),
					PerTokenPrices:  true,
					OfficialPricing: supportedModel.Pricing,
				}
				if supportedModel.Pricing != nil {
					pricing := supportedModel.Pricing
					channel.BillingMode = pricing.BillingMode
					channel.InputPrice = routingSub2APIPointerValue(pricing.InputPrice)
					channel.OutputPrice = routingSub2APIPointerValue(pricing.OutputPrice)
					channel.CacheWritePrice = routingSub2APIPointerValue(pricing.CacheWritePrice)
					channel.CacheReadPrice = routingSub2APIPointerValue(pricing.CacheReadPrice)
					channel.ImageOutputPrice = routingSub2APIPointerValue(pricing.ImageOutputPrice)
					channel.PerRequestPrice = routingSub2APIPointerValue(pricing.PerRequestPrice)
					channel.Intervals = append([]routingSub2APIPricingInterval(nil), pricing.Intervals...)
				}
				channels = append(channels, channel)
			}
		}
	}
	return channels, nil
}

func routingSub2APIPointerValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func routingSub2APITokenPricingExpression(pricing routingSub2APIModelPricing) (string, bool, error) {
	allPrices := []*float64{
		pricing.InputPrice,
		pricing.OutputPrice,
		pricing.CacheWritePrice,
		pricing.CacheReadPrice,
		pricing.ImageOutputPrice,
		pricing.PerRequestPrice,
	}
	for _, interval := range pricing.Intervals {
		allPrices = append(allPrices,
			interval.InputPrice,
			interval.OutputPrice,
			interval.CacheWritePrice,
			interval.CacheReadPrice,
			interval.PerRequestPrice,
		)
	}
	for _, price := range allPrices {
		if price != nil && !routingCostNonNegativeFinite(*price) {
			return "", false, model.ErrRoutingCostV2Invalid
		}
	}

	if len(pricing.Intervals) == 0 {
		// Flat channel overrides inherit missing token dimensions from Sub2API's
		// private global catalog. That fallback is not exposed by this endpoint,
		// so a partial flat price cannot be mapped without guessing.
		if pricing.InputPrice == nil || pricing.OutputPrice == nil ||
			pricing.CacheWritePrice == nil || pricing.CacheReadPrice == nil {
			return "", false, errors.New("sub2api flat token pricing omits inherited price dimensions")
		}
		expression, _, err := routingSub2APITokenTierExpression(
			"flat",
			*pricing.InputPrice,
			*pricing.OutputPrice,
			*pricing.CacheWritePrice,
			*pricing.CacheReadPrice,
			routingSub2APIPointerValue(pricing.ImageOutputPrice),
		)
		if err != nil {
			return "", false, err
		}
		return expression, true, nil
	}

	if len(pricing.Intervals) > routingSub2APIMaxPricingIntervals {
		return "", false, errors.New("sub2api pricing interval count exceeds limit")
	}
	intervals := make([]routingSub2APIPricingInterval, 0, len(pricing.Intervals))
	for _, interval := range pricing.Intervals {
		if interval.InputPrice == nil && interval.OutputPrice == nil &&
			interval.CacheWritePrice == nil && interval.CacheReadPrice == nil &&
			interval.PerRequestPrice == nil {
			continue
		}
		intervals = append(intervals, interval)
	}
	if len(intervals) == 0 {
		return "", false, errors.New("sub2api token pricing intervals contain no prices")
	}
	sort.SliceStable(intervals, func(left int, right int) bool {
		return intervals[left].MinTokens < intervals[right].MinTokens
	})
	for index := range intervals {
		interval := intervals[index]
		if interval.MinTokens < 0 || (interval.MaxTokens != nil && *interval.MaxTokens <= interval.MinTokens) {
			return "", false, errors.New("sub2api returned an invalid token pricing interval")
		}
		if index > 0 {
			previous := intervals[index-1]
			if previous.MaxTokens == nil || *previous.MaxTokens > interval.MinTokens {
				return "", false, errors.New("sub2api token pricing intervals overlap")
			}
		}
	}

	expression := fmt.Sprintf("tier(%s, 0)", strconv.Quote(model.RoutingCostSub2APIIntervalUnmatchedTier))
	known := false
	// Official Sub2API channel pricing treats a nil image_output_price as an
	// explicit zero once a channel override exists; it must not fall back to
	// the ordinary output price. Keeping img_o in the expression preserves
	// that contract, while a non-nil value (including 0) remains explicit.
	imageOutputPrice := routingSub2APIPointerValue(pricing.ImageOutputPrice)
	for index := len(intervals) - 1; index >= 0; index-- {
		interval := intervals[index]
		label := fmt.Sprintf("ctx_gt_%d", interval.MinTokens)
		condition := fmt.Sprintf("len > %d", interval.MinTokens)
		if interval.MaxTokens != nil {
			label = fmt.Sprintf("ctx_%d_%d", interval.MinTokens, *interval.MaxTokens)
			condition += fmt.Sprintf(" && len <= %d", *interval.MaxTokens)
		}
		tierExpression, tierKnown, err := routingSub2APITokenTierExpression(
			label,
			routingSub2APIPointerValue(interval.InputPrice),
			routingSub2APIPointerValue(interval.OutputPrice),
			routingSub2APIPointerValue(interval.CacheWritePrice),
			routingSub2APIPointerValue(interval.CacheReadPrice),
			imageOutputPrice,
		)
		if err != nil {
			return "", false, err
		}
		// A selected Sub2API token interval starts from zero and applies each
		// explicitly configured dimension. Presence, including an explicit 0,
		// therefore makes that tier known; numerical positivity does not.
		tierKnown = tierKnown || interval.InputPrice != nil || interval.OutputPrice != nil ||
			interval.CacheWritePrice != nil || interval.CacheReadPrice != nil
		known = known || tierKnown
		expression = fmt.Sprintf("%s ? %s : (%s)", condition, tierExpression, expression)
	}
	if len(expression) > 16_384 {
		return "", false, errors.New("sub2api pricing expression exceeds size limit")
	}
	return expression, known, nil
}

func routingSub2APITokenTierExpression(label string, inputPrice float64, outputPrice float64, cacheWritePrice float64, cacheReadPrice float64, imageOutputPrice float64) (string, bool, error) {
	inputPerMillion := inputPrice * 1_000_000
	outputPerMillion := outputPrice * 1_000_000
	cacheWritePerMillion := cacheWritePrice * 1_000_000
	cacheReadPerMillion := cacheReadPrice * 1_000_000
	imageOutputPerMillion := imageOutputPrice * 1_000_000
	for _, scaled := range []float64{
		inputPerMillion,
		outputPerMillion,
		cacheWritePerMillion,
		cacheReadPerMillion,
		imageOutputPerMillion,
	} {
		if !routingCostNonNegativeFinite(scaled) {
			return "", false, errors.New("sub2api token price overflows normalized units")
		}
	}
	known := inputPerMillion > 0 || outputPerMillion > 0 || cacheWritePerMillion > 0 ||
		cacheReadPerMillion > 0 || imageOutputPerMillion > 0
	return fmt.Sprintf(
		"tier(%s, p * %s + c * %s + cr * %s + cc * %s + cc1h * %s + img_o * %s)",
		strconv.Quote(label),
		strconv.FormatFloat(inputPerMillion, 'g', -1, 64),
		strconv.FormatFloat(outputPerMillion, 'g', -1, 64),
		strconv.FormatFloat(cacheReadPerMillion, 'g', -1, 64),
		strconv.FormatFloat(cacheWritePerMillion, 'g', -1, 64),
		strconv.FormatFloat(cacheWritePerMillion, 'g', -1, 64),
		strconv.FormatFloat(imageOutputPerMillion, 'g', -1, 64),
	), known, nil
}

func routingSub2APIGroupName(group routingSub2APIGroup) string {
	for _, value := range []string{string(group.ID), group.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func routingSub2APIGroupAliases(group routingSub2APIGroup) []string {
	aliases := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, value := range []string{string(group.ID), group.Name} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		aliases = append(aliases, value)
	}
	return aliases
}

func routingSub2APIResolvedGroupRate(groups map[string]routingSub2APIGroup, rates map[string]float64, groupKey string) (float64, bool) {
	groupKey = strings.TrimSpace(groupKey)
	if ratio, ok := rates[groupKey]; ok {
		return ratio, true
	}
	group, ok := groups[groupKey]
	if !ok {
		return 0, false
	}
	for _, alias := range routingSub2APIGroupAliases(group) {
		if ratio, exists := rates[alias]; exists {
			return ratio, true
		}
	}
	return 0, false
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

func routingSub2APIGroupUsesSubscription(group routingSub2APIGroup) bool {
	return strings.EqualFold(strings.TrimSpace(group.SubscriptionType), "subscription")
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
	sort.Strings(models)
	return models
}

func routingSub2APIChannelSnapshot(channelID int, modelName string, groupRatio float64, groupFound bool, channel routingSub2APIChannel, now int64) (model.RoutingCostSnapshot, error) {
	pricing, confidence, _, err := routingSub2APINormalizedPricing(channel, groupRatio, groupFound)
	if err != nil {
		return model.RoutingCostSnapshot{}, err
	}
	if confidence == model.RoutingCostConfidenceUnknown {
		return model.RoutingCostSnapshot{}, errors.New("sub2api returned no usable pricing")
	}
	legacyConfidence := model.RoutingCostConfidenceGroupOnly
	if confidence == model.RoutingCostConfidenceExact {
		legacyConfidence = model.RoutingCostConfidenceFull
	} else if confidence == model.RoutingCostConfidenceUnknown {
		legacyConfidence = model.RoutingCostConfidenceUnknown
	}
	baseRatio := 0.0
	if pricing.BaseRatio != nil && *pricing.BaseRatio > 0 {
		baseRatio = *pricing.BaseRatio
	}
	completionRatio := 0.0
	if pricing.CompletionRatio != nil && *pricing.CompletionRatio > 0 {
		completionRatio = *pricing.CompletionRatio
	}
	var extrasJSON *string
	if len(pricing.Extras) > 0 && string(pricing.Extras) != "{}" {
		encoded := string(pricing.Extras)
		extrasJSON = &encoded
	}
	var tiersJSON *string
	if len(pricing.Tiers) > 0 && string(pricing.Tiers) != "{}" {
		encoded := string(pricing.Tiers)
		tiersJSON = &encoded
	}
	if pricing.BillingExpression != "" {
		legacyConfidence = model.RoutingCostConfidenceUnknown
	}
	modelPrice := 0.0
	if pricing.ModelPrice != nil {
		modelPrice = *pricing.ModelPrice
	} else if pricing.PerRequestCost != nil {
		modelPrice = *pricing.PerRequestCost
	}
	return model.RoutingCostSnapshot{
		ChannelID:       channelID,
		ModelName:       modelName,
		QuotaType:       pricing.QuotaType,
		GroupRatio:      groupRatio,
		BaseRatio:       baseRatio,
		CompletionRatio: completionRatio,
		ModelPrice:      modelPrice,
		BillingMode:     pricing.BillingMode,
		TiersJSON:       tiersJSON,
		ExtrasJSON:      extrasJSON,
		Confidence:      legacyConfidence,
		SnapshotTS:      now,
	}, nil
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
		credentials.CustomCAPEM,
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
