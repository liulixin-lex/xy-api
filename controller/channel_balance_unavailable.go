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

var channelBalanceRefreshState = struct {
	sync.Mutex
	inflight map[int]struct{}
}{inflight: make(map[int]struct{})}

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
	if channelID <= 0 {
		return
	}
	if success {
		routinghotcache.ClearChannelBalanceUnavailable(channelID, attemptStartedAt)
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
	if _, recorded := routinghotcache.RecordChannelBalanceUnavailable(
		channelID,
		statusCode,
		classification.Rule,
		retryAfter,
		baseCooldown,
		maxCooldown,
		now,
	); recorded {
		scheduleChannelBalanceRefresh(channelID)
	}
}

func scheduleChannelBalanceRefresh(channelID int) {
	if channelID <= 0 {
		return
	}
	channelBalanceRefreshState.Lock()
	if _, refreshing := channelBalanceRefreshState.inflight[channelID]; refreshing {
		channelBalanceRefreshState.Unlock()
		return
	}
	channelBalanceRefreshState.inflight[channelID] = struct{}{}
	channelBalanceRefreshState.Unlock()

	runChannelBalanceRefresh(func() {
		defer func() {
			channelBalanceRefreshState.Lock()
			delete(channelBalanceRefreshState.inflight, channelID)
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
		if channel == nil || channel.ChannelInfo.IsMultiKey {
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
			routinghotcache.ClearChannelBalanceUnavailable(channelID, startedAt)
		}
	})
}

func resetChannelBalanceRefreshForTest() {
	channelBalanceRefreshState.Lock()
	channelBalanceRefreshState.inflight = make(map[int]struct{})
	channelBalanceRefreshState.Unlock()
	loadChannelForBalanceRefresh = func(channelID int) (*model.Channel, error) {
		return model.GetChannelById(channelID, true)
	}
	updateChannelBalanceForRefresh = updateChannelBalance
	runChannelBalanceRefresh = func(refresh func()) { gopool.Go(refresh) }
}
