package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateTopupGroupRatioRejectsInvalidJSONWithoutChangingActiveConfiguration(t *testing.T) {
	original := TopupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateTopupGroupRatioByJSONString(original))
	})

	require.NoError(t, UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.25}`))
	err := UpdateTopupGroupRatioByJSONString(`{"default":`)
	require.Error(t, err)
	assert.InDelta(t, 1.25, GetTopupGroupRatio("vip"), 0.000001)
}
