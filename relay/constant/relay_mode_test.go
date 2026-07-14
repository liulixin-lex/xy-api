package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPath2RelayModeMidjourneyPreservesStatefulOperationKinds(t *testing.T) {
	tests := map[string]int{
		"/mj/submit/action":        RelayModeMidjourneyAction,
		"/mj/submit/change":        RelayModeMidjourneyChange,
		"/mj/submit/simple-change": RelayModeMidjourneySimpleChange,
		"/mj/submit/modal":         RelayModeMidjourneyModal,
		"/mj/submit/video":         RelayModeMidjourneyVideo,
	}
	for path, expected := range tests {
		assert.Equal(t, expected, Path2RelayModeMidjourney(path), path)
	}
}
