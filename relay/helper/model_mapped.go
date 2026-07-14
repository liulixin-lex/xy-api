package helper

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}

	isResponsesCompact := info.RelayMode == relayconstant.RelayModeResponsesCompact
	originModelName := info.OriginModelName
	mappingModelName := originModelName
	if isResponsesCompact && strings.HasSuffix(originModelName, ratio_setting.CompactModelSuffix) {
		mappingModelName = strings.TrimSuffix(originModelName, ratio_setting.CompactModelSuffix)
	}

	upstreamModelName, mapped, err := model.ResolveChannelModelMapping(c.GetString("model_mapping"), mappingModelName)
	if err != nil {
		if errors.Is(err, model.ErrChannelModelMappingInvalid) {
			return fmt.Errorf("unmarshal_model_mapping_failed")
		}
		if errors.Is(err, model.ErrChannelModelMappingCycle) {
			return errors.New("model_mapping_contains_cycle")
		}
		return err
	}
	info.IsModelMapped = mapped
	info.UpstreamModelName = upstreamModelName

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}
