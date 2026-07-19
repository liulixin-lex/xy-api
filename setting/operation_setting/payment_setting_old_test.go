package operation_setting

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePayMethodsRejectsUnknownFields(t *testing.T) {
	_, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","secret":"must-not-be-exposed"}]`)
	assert.Error(t, err)

	methods, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","icon":"SiAlipay","color":"#1677ff","min_topup":"10"}]`)
	require.NoError(t, err)
	require.Len(t, methods, 1)
	assert.Equal(t, "form_post", methods[0]["flow"])

	_, err = ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","min_topup":"10001"}]`)
	assert.Error(t, err)
}

func TestParsePayMethodsRejectsReservedTypesOnEpay(t *testing.T) {
	for _, methodType := range []string{"stripe", "xorpay_native", "xorpay_alipay", "waffo_pancake"} {
		_, err := ParsePayMethodsByJsonString(fmt.Sprintf(
			`[{"name":"reserved","type":%q,"provider":"epay"}]`, methodType,
		))
		assert.Error(t, err)
	}
}
