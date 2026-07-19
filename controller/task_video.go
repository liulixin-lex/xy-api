package controller

import (
	"context"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

// UpdateVideoTaskAll is kept as the controller compatibility entrypoint. The
// implementation lives in service so scheduled and legacy callers share the
// same CAS-guarded, durable billing lifecycle instead of maintaining a second
// split wallet-only settlement path.
func UpdateVideoTaskAll(ctx context.Context, platform constant.TaskPlatform, taskChannelM map[int][]string, taskM map[string]*model.Task) error {
	index := make(service.TaskPollingIndex)
	for upstreamID, task := range taskM {
		if task == nil {
			continue
		}
		if index[task.ChannelId] == nil {
			index[task.ChannelId] = make(map[string][]*model.Task)
		}
		index[task.ChannelId][upstreamID] = append(index[task.ChannelId][upstreamID], task)
	}
	return service.UpdateVideoTasks(ctx, platform, taskChannelM, index)
}
