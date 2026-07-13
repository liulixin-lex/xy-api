package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

var ErrChannelModelMappingInvalid = errors.New("unmarshal_model_mapping_failed")
var ErrChannelModelMappingCycle = errors.New("model_mapping_contains_cycle")

func ResolveChannelModelMapping(modelMapping string, modelName string) (string, bool, error) {
	if modelName == "" || modelMapping == "" || modelMapping == "{}" {
		return modelName, false, nil
	}
	modelMap := make(map[string]string)
	if err := common.UnmarshalJsonStr(modelMapping, &modelMap); err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrChannelModelMappingInvalid, err)
	}
	currentModel := modelName
	visitedModels := map[string]struct{}{currentModel: {}}
	for {
		mappedModel, exists := modelMap[currentModel]
		if !exists || mappedModel == "" {
			return currentModel, currentModel != modelName, nil
		}
		if _, visited := visitedModels[mappedModel]; visited {
			if mappedModel == currentModel {
				return currentModel, currentModel != modelName, nil
			}
			return "", false, ErrChannelModelMappingCycle
		}
		visitedModels[mappedModel] = struct{}{}
		currentModel = mappedModel
	}
}
