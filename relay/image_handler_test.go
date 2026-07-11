package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareImageSettlementAppliesRequestedCountToCommittedMissingUsage(t *testing.T) {
	imageN := uint(3)
	info := &relaycommon.RelayInfo{}
	info.PriceData.UsePrice = true

	usage, gotN := prepareImageSettlement(info, &dto.ImageRequest{N: &imageN}, nil, true)

	assert.Equal(t, imageN, gotN)
	assert.Equal(t, float64(imageN), info.PriceData.OtherRatios()["n"])
	usageDto, ok := usage.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 1, usageDto.PromptTokens)
	assert.Equal(t, 1, usageDto.TotalTokens)
}

func TestPrepareImageSettlementKeepsAdaptorImageCount(t *testing.T) {
	imageN := uint(3)
	info := &relaycommon.RelayInfo{}
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 2)
	usage := &dto.Usage{PromptTokens: 4, TotalTokens: 4}

	gotUsage, gotN := prepareImageSettlement(info, &dto.ImageRequest{N: &imageN}, usage, true)

	assert.Equal(t, imageN, gotN)
	assert.Same(t, usage, gotUsage)
	assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
}
