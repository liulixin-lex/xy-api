package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const routingChannelTrafficPolicyTTL = 30 * time.Second

var ErrRoutingChannelTrafficRestricted = errors.New("channel is restricted to Claude Code traffic")
var routingChannelTrafficPolicyMu sync.Mutex

func routingRequestTrafficClass(c *gin.Context) channelrouting.RequestTrafficClass {
	if c == nil {
		return channelrouting.RequestTrafficClassLegacy
	}
	if template, ok := common.GetContextKeyType[channelrouting.RequestProfileV2Input](
		c,
		constant.ContextKeyRoutingRequestProfile,
	); ok {
		return template.TrafficClass
	}
	if template, ok := common.GetContextKeyType[*channelrouting.RequestProfileV2Input](
		c,
		constant.ContextKeyRoutingRequestProfile,
	); ok && template != nil {
		return template.TrafficClass
	}
	return channelrouting.RequestTrafficClassLegacy
}

func ensureRoutingChannelTrafficPolicies(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().Unix()
	state := routinghotcache.ChannelTrafficPoliciesState()
	if state.Initialized && state.LoadedAtUnix > 0 && now-state.LoadedAtUnix < int64(routingChannelTrafficPolicyTTL/time.Second) {
		return nil
	}

	routingChannelTrafficPolicyMu.Lock()
	defer routingChannelTrafficPolicyMu.Unlock()
	state = routinghotcache.ChannelTrafficPoliciesState()
	if state.Initialized && state.LoadedAtUnix > 0 && now-state.LoadedAtUnix < int64(routingChannelTrafficPolicyTTL/time.Second) {
		return nil
	}

	bindings := make([]model.RoutingChannelBinding, 0)
	if model.DB != nil && model.DB.Migrator().HasTable(&model.RoutingChannelBinding{}) {
		if err := model.DB.WithContext(ctx).
			Select("channel_id", "serves_claude_code").
			Find(&bindings).Error; err != nil {
			return err
		}
	}
	routinghotcache.ReplaceChannelTrafficPolicies(bindings, now)
	return nil
}

func ChannelRoutingTrafficAdmissible(c *gin.Context, channelID int) (bool, error) {
	if channelID <= 0 {
		return false, nil
	}
	if routingRequestTrafficClass(c) == channelrouting.RequestTrafficClassClaudeCode {
		return true, nil
	}
	var ctx context.Context
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	if err := ensureRoutingChannelTrafficPolicies(ctx); err != nil {
		return false, err
	}
	policy, initialized := routinghotcache.GetChannelTrafficPolicy(channelID)
	if !initialized {
		return false, errors.New("channel traffic policy cache is unavailable")
	}
	return !policy.ClaudeCodeOnly, nil
}

func filterRoutingTrafficAdmissibleChannels(
	c *gin.Context,
	channels []*model.Channel,
) ([]*model.Channel, error) {
	filtered := make([]*model.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel == nil || channel.Id <= 0 {
			return nil, errors.New("channel routing cache contains an invalid channel")
		}
		admissible, err := ChannelRoutingTrafficAdmissible(c, channel.Id)
		if err != nil {
			return nil, err
		}
		if admissible {
			filtered = append(filtered, channel)
		}
	}
	return filtered, nil
}

func getRandomSatisfiedChannelForRequest(
	param *RetryParam,
	group string,
	retry int,
) (*model.Channel, error) {
	if param == nil {
		return nil, errors.New("routing param is nil")
	}
	if routingRequestTrafficClass(param.Ctx) == channelrouting.RequestTrafficClassClaudeCode {
		return model.GetRandomSatisfiedChannel(group, param.ModelName, retry, param.RequestPath)
	}
	var ctx context.Context
	if param.Ctx != nil && param.Ctx.Request != nil {
		ctx = param.Ctx.Request.Context()
	}
	if err := ensureRoutingChannelTrafficPolicies(ctx); err != nil {
		return nil, err
	}
	state := routinghotcache.ChannelTrafficPoliciesState()
	if state.RestrictedChannels == 0 {
		return model.GetRandomSatisfiedChannel(group, param.ModelName, retry, param.RequestPath)
	}
	return model.GetRandomSatisfiedChannelWithEligibility(
		group,
		param.ModelName,
		retry,
		param.RequestPath,
		func(channel *model.Channel) bool {
			if channel == nil {
				return false
			}
			policy, initialized := routinghotcache.GetChannelTrafficPolicy(channel.Id)
			return initialized && !policy.ClaudeCodeOnly
		},
	)
}
