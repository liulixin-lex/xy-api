package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
)

func TestShouldDisableChannelUsesServingCredentialClassification(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	originalRanges := operation_setting.AutomaticDisableStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{
		{Start: http.StatusUnauthorized, End: http.StatusUnauthorized},
		{Start: http.StatusForbidden, End: http.StatusForbidden},
	}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalEnabled
		operation_setting.AutomaticDisableStatusCodeRanges = originalRanges
	})

	credential401 := types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	credential403 := types.NewErrorWithStatusCode(errors.New("forbidden"), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
	credential403.SetResponseStatusCode(http.StatusBadRequest)
	contentSafety403 := types.NewErrorWithStatusCode(errors.New(CSAMViolationMarker), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
	contentSafety403.SetResponseStatusCode(http.StatusBadRequest)
	caller400 := types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest)
	gateway500 := types.NewErrorWithStatusCode(errors.New("database unavailable"), types.ErrorCodeQueryDataError, http.StatusInternalServerError)
	config400 := types.NewErrorWithStatusCode(errors.New("model mapping failed"), types.ErrorCodeChannelModelMappedError, http.StatusBadRequest)

	tests := []struct {
		name           string
		apiErr         *types.NewAPIError
		classification routingerror.Classification
		want           bool
	}{
		{name: "serving 401 credential", apiErr: credential401, classification: routingerror.ClassifyAPIError(credential401, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay}), want: true},
		{name: "serving source 403 credential", apiErr: credential403, classification: routingerror.ClassifyAPIError(credential403, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay}), want: true},
		{name: "content safety 403", apiErr: contentSafety403, classification: routingerror.ClassifyAPIError(contentSafety403, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay, Signal: routingerror.SignalContentSafety})},
		{name: "caller", apiErr: caller400, classification: routingerror.ClassifyAPIError(caller400, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})},
		{name: "gateway", apiErr: gateway500, classification: routingerror.ClassifyAPIError(gateway500, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})},
		{name: "config", apiErr: config400, classification: routingerror.ClassifyAPIError(config400, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShouldDisableChannel(tt.apiErr, tt.classification))
		})
	}
}
