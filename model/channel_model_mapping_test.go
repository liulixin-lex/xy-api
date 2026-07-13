package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveChannelModelMapping(t *testing.T) {
	tests := []struct {
		name       string
		mapping    string
		model      string
		want       string
		wantMapped bool
		wantErr    error
	}{
		{name: "empty", model: "local", want: "local"},
		{name: "direct", mapping: `{"local":"upstream"}`, model: "local", want: "upstream", wantMapped: true},
		{name: "chain", mapping: `{"local":"alias","alias":"upstream"}`, model: "local", want: "upstream", wantMapped: true},
		{name: "self", mapping: `{"local":"local"}`, model: "local", want: "local"},
		{name: "chain ending in self", mapping: `{"local":"upstream","upstream":"upstream"}`, model: "local", want: "upstream", wantMapped: true},
		{name: "cycle", mapping: `{"local":"alias","alias":"local"}`, model: "local", wantErr: ErrChannelModelMappingCycle},
		{name: "invalid", mapping: `{`, model: "local", wantErr: ErrChannelModelMappingInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, mapped, err := ResolveChannelModelMapping(test.mapping, test.model)
			if test.wantErr != nil {
				assert.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
			assert.Equal(t, test.wantMapped, mapped)
		})
	}
}
