package controller

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
)

type channelBalanceRefreshKey struct {
	ChannelID         int
	ChannelGeneration string
}

var channelBalanceRefreshState = struct {
	sync.Mutex
	inflight map[channelBalanceRefreshKey]struct{}
}{inflight: make(map[channelBalanceRefreshKey]struct{})}

var (
	loadChannelForBalanceRefresh = func(channelID int) (*model.Channel, error) {
		return model.GetChannelById(channelID, true)
	}
	updateChannelBalanceForRefresh = updateChannelBalance
	runChannelBalanceRefresh       = func(refresh func()) { gopool.Go(refresh) }
)

func recordChannelBalanceAttemptEffect(
	channelID int,
	success bool,
	statusCode int,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
	attemptStartedAt time.Time,
	now time.Time,
) {
	recordChannelBalanceAttemptEffectForGeneration(
		channelID, "", success, statusCode, apiErr, classification, attemptStartedAt, now,
	)
}

func recordChannelBalanceAttemptEffectForGeneration(
	channelID int,
	channelGeneration string,
	success bool,
	statusCode int,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
	attemptStartedAt time.Time,
	now time.Time,
) {
	if channelID <= 0 {
		return
	}
	if success {
		routinghotcache.ClearChannelBalanceUnavailableForGeneration(
			channelID, channelGeneration, attemptStartedAt,
		)
		return
	}
	if statusCode != 402 || classification.Responsibility != routingerror.ResponsibilityCapacity ||
		classification.Scope != routingerror.ScopeChannel ||
		classification.CapacityEffect != routingerror.CapacityCooldown {
		return
	}
	setting := smart_routing_setting.Normalize(smart_routing_setting.GetSetting())
	maxCooldown := time.Duration(setting.MaxCooldownSec) * time.Second
	if maxCooldown <= 0 {
		maxCooldown = routingbreaker.DefaultConfig().MaxCooldown
	}
	baseCooldown := time.Duration(setting.BackoffBaseMs429) * time.Millisecond
	if baseCooldown <= 0 {
		baseCooldown = time.Second
	}
	retryAfter := retryAfterFromAPIError(apiErr, maxCooldown)
	if _, recorded := routinghotcache.RecordChannelBalanceUnavailableForGeneration(
		channelID,
		channelGeneration,
		statusCode,
		classification.Rule,
		retryAfter,
		baseCooldown,
		maxCooldown,
		now,
	); recorded {
		scheduleChannelBalanceRefreshForGeneration(channelID, channelGeneration)
	}
}

func scheduleChannelBalanceRefresh(channelID int) {
	scheduleChannelBalanceRefreshForGeneration(channelID, "")
}

func scheduleChannelBalanceRefreshForGeneration(channelID int, channelGeneration string) {
	if channelID <= 0 {
		return
	}
	key := channelBalanceRefreshKey{ChannelID: channelID, ChannelGeneration: channelGeneration}
	channelBalanceRefreshState.Lock()
	if _, refreshing := channelBalanceRefreshState.inflight[key]; refreshing {
		channelBalanceRefreshState.Unlock()
		return
	}
	channelBalanceRefreshState.inflight[key] = struct{}{}
	channelBalanceRefreshState.Unlock()

	runChannelBalanceRefresh(func() {
		defer func() {
			channelBalanceRefreshState.Lock()
			delete(channelBalanceRefreshState.inflight, key)
			channelBalanceRefreshState.Unlock()
		}()
		startedAt := time.Now()
		channel, err := loadChannelForBalanceRefresh(channelID)
		if err != nil {
			common.SysError(fmt.Sprintf(
				"refresh channel %d balance after upstream 402: %s",
				channelID,
				common.SanitizeErrorMessage(err.Error()),
			))
			return
		}
		if channel == nil || channel.ChannelInfo.IsMultiKey ||
			(channelGeneration != "" && channel.RoutingGeneration != channelGeneration) {
			return
		}
		balance, err := updateChannelBalanceForRefresh(channel)
		if err != nil {
			common.SysError(fmt.Sprintf(
				"refresh channel %d balance after upstream 402: %s",
				channelID,
				common.SanitizeErrorMessage(err.Error()),
			))
			return
		}
		if balance > 0 {
			routinghotcache.ClearChannelBalanceUnavailableForGeneration(
				channelID, channelGeneration, startedAt,
			)
		}
	})
}

func resetChannelBalanceRefreshForTest() {
	channelBalanceRefreshState.Lock()
	channelBalanceRefreshState.inflight = make(map[channelBalanceRefreshKey]struct{})
	channelBalanceRefreshState.Unlock()
	loadChannelForBalanceRefresh = func(channelID int) (*model.Channel, error) {
		return model.GetChannelById(channelID, true)
	}
	updateChannelBalanceForRefresh = updateChannelBalance
	runChannelBalanceRefresh = func(refresh func()) { gopool.Go(refresh) }
}
