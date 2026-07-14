package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
)

func routingCapacityTokenEstimate(
	c *gin.Context,
) (channelrouting.CapacityDimensionEstimate, channelrouting.CapacityDimensionEstimate) {
	if c == nil {
		unknown := channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown}
		return unknown, unknown
	}
	return routingCapacityDimensionEstimate(
			c, constant.ContextKeyRoutingCapacityInputState,
			constant.ContextKeyRoutingCapacityInput, constant.ContextKeyRoutingCapacityInputKnown,
		), routingCapacityDimensionEstimate(
			c, constant.ContextKeyRoutingCapacityOutputState,
			constant.ContextKeyRoutingCapacityOutput, constant.ContextKeyRoutingCapacityOutputKnown,
		)
}

func routingCapacityDimensionEstimate(
	c *gin.Context,
	stateKey constant.ContextKey,
	valueKey constant.ContextKey,
	legacyKnownKey constant.ContextKey,
) channelrouting.CapacityDimensionEstimate {
	tokens := common.GetContextKeyInt(c, valueKey)
	if value, found := common.GetContextKey(c, stateKey); found {
		state, ok := value.(channelrouting.CapacityDimensionState)
		if !ok {
			if raw, stringOK := value.(string); stringOK {
				state = channelrouting.CapacityDimensionState(raw)
				ok = true
			}
		}
		if ok {
			return channelrouting.CapacityDimensionEstimate{State: state, Tokens: tokens}
		}
	}
	if value, found := common.GetContextKey(c, legacyKnownKey); found {
		if known, ok := value.(bool); ok && known {
			return channelrouting.CapacityDimensionEstimate{
				State: channelrouting.CapacityDimensionBoundedKnown, Tokens: tokens,
			}
		}
	}
	return channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown}
}

func routingCostRequestProfile(c *gin.Context) *model.RoutingCostRequestProfile {
	if c == nil {
		return nil
	}
	profile, ok := common.GetContextKeyType[*model.RoutingCostRequestProfile](c, constant.ContextKeyRoutingCostProfile)
	if !ok || profile == nil {
		return nil
	}
	return profile
}

func routingRequestProfile(
	c *gin.Context,
	group string,
	retryIndex int,
	promptTokens int,
	completionTokens int,
) (*channelrouting.RequestProfile, error) {
	if c == nil {
		return nil, nil
	}
	setting := smart_routing_setting.GetSetting()
	if !setting.Enabled || !setting.RequestProfileV2Enabled {
		return nil, nil
	}
	template, ok := common.GetContextKeyType[channelrouting.RequestProfileV2Input](
		c,
		constant.ContextKeyRoutingRequestProfile,
	)
	if !ok {
		if pointer, pointerOK := common.GetContextKeyType[*channelrouting.RequestProfileV2Input](
			c,
			constant.ContextKeyRoutingRequestProfile,
		); pointerOK && pointer != nil {
			template = *pointer
			ok = true
		}
	}
	if !ok {
		return nil, nil
	}
	template.GroupName = group
	template.RetryIndex = retryIndex
	if template.InputTokens.State != channelrouting.RequestQuantityNotApplicable &&
		common.GetContextKeyBool(c, constant.ContextKeyRoutingPromptKnown) {
		template.InputTokens = channelrouting.KnownRequestQuantity(int64(max(promptTokens, 0)))
	}
	if template.OutputTokens.State != channelrouting.RequestQuantityNotApplicable &&
		common.GetContextKeyBool(c, constant.ContextKeyRoutingOutputKnown) {
		template.OutputTokens = channelrouting.KnownRequestQuantity(int64(max(completionTokens, 0)))
	}
	profile, err := channelrouting.NewRequestProfileV2(template)
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

type RoutingRequestAttemptPolicy struct {
	RetryAllowed             bool
	CrossChannelRetryAllowed bool
	HedgeAllowed             bool
}

func ChannelRoutingRequestAttemptPolicy(c *gin.Context) (RoutingRequestAttemptPolicy, bool) {
	if c == nil {
		return RoutingRequestAttemptPolicy{}, false
	}
	setting := smart_routing_setting.GetSetting()
	if !setting.Enabled || !setting.RequestProfileV2Enabled {
		return RoutingRequestAttemptPolicy{}, false
	}
	template, ok := common.GetContextKeyType[channelrouting.RequestProfileV2Input](
		c,
		constant.ContextKeyRoutingRequestProfile,
	)
	if !ok {
		if pointer, pointerOK := common.GetContextKeyType[*channelrouting.RequestProfileV2Input](
			c,
			constant.ContextKeyRoutingRequestProfile,
		); pointerOK && pointer != nil {
			template = *pointer
			ok = true
		}
	}
	if !ok {
		return RoutingRequestAttemptPolicy{}, false
	}
	return RoutingRequestAttemptPolicy{
		RetryAllowed:             template.RetryAllowed,
		CrossChannelRetryAllowed: template.CrossChannelRetryAllowed,
		HedgeAllowed:             template.HedgeAllowed,
	}, true
}

func routingStrictCapacityCost(
	session *channelrouting.RequestRoutingSession,
	group string,
	channelID int,
	param *RetryParam,
	input channelrouting.CapacityDimensionEstimate,
	output channelrouting.CapacityDimensionEstimate,
) (float64, bool, error) {
	if session == nil || channelID <= 0 || param == nil || param.Ctx == nil {
		return 0, false, nil
	}
	if _, err := input.Demand(0); err != nil {
		return 0, false, err
	}
	if _, err := output.Demand(0); err != nil {
		return 0, false, err
	}
	costProfile := routingCostRequestProfile(param.Ctx)
	if costProfile == nil {
		costProfile = &model.RoutingCostRequestProfile{
			PromptTokens:             int64(input.Tokens),
			MaximumPromptTokens:      int64(input.Tokens),
			ExpectedCompletionTokens: int64(output.Tokens),
			MaximumCompletionTokens:  int64(output.Tokens),
			MaxAttempts:              1,
			KnowledgeSpecified:       true,
			InputTokensKnown:         input.Known(),
			MaximumCompletionKnown:   output.Known(),
		}
	}
	requestProfile, err := routingRequestProfile(
		param.Ctx,
		group,
		param.GetRetry(),
		input.Tokens,
		output.Tokens,
	)
	if err != nil {
		return 0, false, err
	}
	return session.WorstCaseCostForChannel(channelID, channelrouting.RequestRoutingCostInput{
		RequestPath:             param.RequestPath,
		ModelName:               param.ModelName,
		IsStream:                common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
		RetryIndex:              param.GetRetry(),
		PromptTokenEstimate:     input.Tokens,
		CompletionTokenEstimate: output.Tokens,
		CostProfile:             costProfile,
		Profile:                 requestProfile,
	})
}
