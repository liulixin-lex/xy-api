package channelrouting

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func init() {
	model.RegisterRoutingPricingChangePublisher(func(version model.RoutingPricingVersion) {
		if version.Epoch < 0 {
			return
		}
		if _, err := PublishRoutingEvent(RoutingEventTypePricingChanged, uint64(version.Epoch), map[string]any{
			"pricing_epoch":     version.Epoch,
			"pricing_hash":      version.StateHash,
			"updated_time":      version.UpdatedTime,
			"refresh_resources": []string{"overview", "groups", "channels", "costs", "decisions"},
		}); err != nil {
			common.SysError("publish channel routing pricing event: " + common.SanitizeErrorMessage(err.Error()))
		}
	})
}

func consumeRoutingPricingEventPayloadContext(
	ctx context.Context,
	revision uint64,
	payload []byte,
) (bool, error) {
	var event struct {
		PricingEpoch int64  `json:"pricing_epoch"`
		PricingHash  string `json:"pricing_hash"`
		UpdatedTime  int64  `json:"updated_time"`
	}
	if revision == 0 || common.Unmarshal(payload, &event) != nil || event.PricingEpoch <= 0 ||
		uint64(event.PricingEpoch) != revision || !validRoutingEventDocumentHash(event.PricingHash) ||
		event.UpdatedTime <= 0 {
		return false, ErrRoutingEventInvalid
	}
	if err := model.RefreshOptionsFromDatabaseChecked(); err != nil {
		return false, fmt.Errorf("refresh pricing options: %w", err)
	}
	version, err := model.GetRoutingPricingVersionDBContext(ctx, model.DB)
	if err != nil {
		return false, fmt.Errorf("load routing pricing version: %w", err)
	}
	if version.Epoch < event.PricingEpoch {
		return false, model.ErrRoutingChannelConfigurationChanged
	}
	if version.Epoch > event.PricingEpoch {
		return false, nil
	}
	if version.StateHash != event.PricingHash {
		return false, model.ErrRoutingSchemaNotReady
	}
	model.NotifyRoutingTopologyChanged()
	return true, nil
}
