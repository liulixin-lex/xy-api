package model

import (
	"context"
	"unicode/utf8"
)

const RoutingRuntimeCheckpointMaxPageSize = 100

func ListActiveRoutingRuntimeCheckpointsContext(
	ctx context.Context,
	kind string,
	scope string,
	now int64,
	beforeObservedTime int64,
	beforeID int64,
	limit int,
) ([]RoutingRuntimeCheckpoint, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingPolicyText(kind, 32) || kind == "" ||
		!utf8.ValidString(scope) || scope == "" || len(scope) > 4<<10 ||
		now <= 0 || limit <= 0 || limit > RoutingRuntimeCheckpointMaxPageSize ||
		(beforeObservedTime == 0) != (beforeID == 0) || beforeObservedTime < 0 || beforeID < 0 {
		return nil, false, ErrRoutingRuntimeCheckpointInvalid
	}
	query := DB.WithContext(ctx).
		Where("checkpoint_kind = ? AND scope_hash = ? AND scope = ? AND expires_time > ?", kind, routingPolicyHash([]byte(scope)), scope, now)
	if beforeObservedTime > 0 {
		query = query.Where(
			"(observed_time < ? OR (observed_time = ? AND id < ?))",
			beforeObservedTime,
			beforeObservedTime,
			beforeID,
		)
	}
	checkpoints := make([]RoutingRuntimeCheckpoint, 0, limit+1)
	if err := query.Order("observed_time desc").Order("id desc").Limit(limit + 1).Find(&checkpoints).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(checkpoints) > limit
	if hasMore {
		checkpoints = checkpoints[:limit]
	}
	for index := range checkpoints {
		if err := checkpoints[index].Validate(); err != nil {
			return nil, false, err
		}
	}
	return checkpoints, hasMore, nil
}
