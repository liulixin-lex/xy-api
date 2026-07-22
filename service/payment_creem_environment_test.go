package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCreemAPIEnvironment(t *testing.T) {
	for _, test := range []struct {
		name     string
		raw      string
		want     string
		wantLive bool
		wantErr  bool
	}{
		{name: "production", raw: " prod ", want: "prod", wantLive: true},
		{name: "test", raw: "TEST", want: "test"},
		{name: "sandbox", raw: "sandbox", want: "sandbox"},
		{name: "missing", raw: "", wantErr: true},
		{name: "webhook-only local is rejected", raw: "local", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, livemode, err := ParseCreemAPIEnvironment(test.raw)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, environment)
			assert.Equal(t, test.wantLive, livemode)
		})
	}
}

func TestParseCreemWebhookEnvironment(t *testing.T) {
	for _, test := range []struct {
		name     string
		raw      string
		want     string
		wantLive bool
		wantErr  bool
	}{
		{name: "production local", raw: " local ", want: "local", wantLive: true},
		{name: "test", raw: "TEST", want: "test"},
		{name: "sandbox", raw: "sandbox", want: "sandbox"},
		{name: "missing", raw: "", wantErr: true},
		{name: "api-only prod is rejected", raw: "prod", wantErr: true},
		{name: "unknown", raw: "live", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, livemode, err := ParseCreemWebhookEnvironment(test.raw)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, environment)
			assert.Equal(t, test.wantLive, livemode)
		})
	}
}
