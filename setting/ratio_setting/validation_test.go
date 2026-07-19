package ratio_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlatPriceRatioUpdatersRejectInvalidValuesWithoutMutation(t *testing.T) {
	tests := []struct {
		name    string
		update  func(string) error
		current func() map[string]float64
	}{
		{name: "ModelPrice", update: UpdateModelPriceByJSONString, current: GetModelPriceCopy},
		{name: "ModelRatio", update: UpdateModelRatioByJSONString, current: GetModelRatioCopy},
		{name: "CompletionRatio", update: UpdateCompletionRatioByJSONString, current: GetCompletionRatioCopy},
		{name: "CacheRatio", update: UpdateCacheRatioByJSONString, current: GetCacheRatioCopy},
		{name: "CreateCacheRatio", update: UpdateCreateCacheRatioByJSONString, current: GetCreateCacheRatioCopy},
		{name: "ImageRatio", update: UpdateImageRatioByJSONString, current: GetImageRatioCopy},
		{name: "AudioRatio", update: UpdateAudioRatioByJSONString, current: GetAudioRatioCopy},
		{name: "AudioCompletionRatio", update: UpdateAudioCompletionRatioByJSONString, current: GetAudioCompletionRatioCopy},
		{name: "GroupRatio", update: UpdateGroupRatioByJSONString, current: GetGroupRatioCopy},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := test.current()
			originalJSON, err := common.Marshal(original)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, test.update(string(originalJSON)))
			})

			require.NoError(t, test.update(`{"sentinel":1}`))
			for _, invalid := range []string{
				`{"negative":-0.1}`,
				`{"null":null}`,
				`{"overflow":1e309}`,
				`{"nan":NaN}`,
				`null`,
			} {
				require.Error(t, test.update(invalid))
				assert.Equal(t, map[string]float64{"sentinel": 1}, test.current())
			}

			require.NoError(t, test.update(`{"zero":0}`))
			assert.Equal(t, map[string]float64{"zero": 0}, test.current())
		})
	}
}

func TestGroupGroupRatioUpdaterRejectsInvalidValuesWithoutMutation(t *testing.T) {
	original := groupGroupRatioMap.ReadAll()
	originalJSON, err := common.Marshal(original)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, UpdateGroupGroupRatioByJSONString(string(originalJSON)))
	})

	require.NoError(t, UpdateGroupGroupRatioByJSONString(`{"vip":{"sentinel":1}}`))
	for _, invalid := range []string{
		`{"vip":{"negative":-0.1}}`,
		`{"vip":{"null":null}}`,
		`{"vip":{"overflow":1e309}}`,
		`{"vip":null}`,
		`null`,
	} {
		require.Error(t, UpdateGroupGroupRatioByJSONString(invalid))
		assert.Equal(t, map[string]map[string]float64{
			"vip": {"sentinel": 1},
		}, groupGroupRatioMap.ReadAll())
	}

	require.NoError(t, UpdateGroupGroupRatioByJSONString(`{"vip":{"free":0}}`))
	assert.Equal(t, map[string]map[string]float64{
		"vip": {"free": 0},
	}, groupGroupRatioMap.ReadAll())
}
