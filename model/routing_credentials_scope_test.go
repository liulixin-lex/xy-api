package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingCredentialsAreScopedToUpstreamType(t *testing.T) {
	credentials := RoutingCredentials{
		NewAPIAccessToken: " newapi-token ",
		GatewayAPIKey:     " gateway-token ",
		Sub2APIEmail:      " operator@example.com ",
		Sub2APIPassword:   "sub2api-password",
		Sub2APIToken:      " sub2api-token ",
		CustomCAPEM:       " certificate ",
	}

	newAPI := credentials.ForUpstream(RoutingUpstreamTypeNewAPI)
	assert.Equal(t, "newapi-token", newAPI.NewAPIAccessToken)
	assert.Equal(t, "gateway-token", newAPI.GatewayAPIKey)
	assert.Empty(t, newAPI.Sub2APIEmail)
	assert.Empty(t, newAPI.Sub2APIPassword)
	assert.Empty(t, newAPI.Sub2APIToken)
	assert.Equal(t, "certificate", newAPI.CustomCAPEM)

	sub2API := credentials.ForUpstream(RoutingUpstreamTypeSub2API)
	assert.Empty(t, sub2API.NewAPIAccessToken)
	assert.Equal(t, "operator@example.com", sub2API.Sub2APIEmail)
	assert.Equal(t, "sub2api-password", sub2API.Sub2APIPassword)
	assert.Equal(t, "sub2api-token", sub2API.Sub2APIToken)
}

func TestRoutingCredentialReadinessRequiresProviderAuthentication(t *testing.T) {
	assert.False(t, (RoutingCredentials{CustomCAPEM: "certificate"}).ReadyForUpstream(RoutingUpstreamTypeNewAPI))
	assert.False(t, (RoutingCredentials{Sub2APIToken: "wrong-provider"}).ReadyForUpstream(RoutingUpstreamTypeNewAPI))
	assert.True(t, (RoutingCredentials{NewAPIAccessToken: "token"}).ReadyForUpstream(RoutingUpstreamTypeNewAPI))
	assert.False(t, (RoutingCredentials{Sub2APIEmail: "operator@example.com"}).ReadyForUpstream(RoutingUpstreamTypeSub2API))
	assert.True(t, (RoutingCredentials{
		Sub2APIEmail: "operator@example.com", Sub2APIPassword: "password",
	}).ReadyForUpstream(RoutingUpstreamTypeSub2API))
}

func TestSetRoutingCredentialsNormalizesEmptyEnvelope(t *testing.T) {
	binding := RoutingChannelBinding{ChannelID: 10, UpstreamType: RoutingUpstreamTypeNewAPI}
	require.NoError(t, binding.SetCredentials(RoutingCredentials{
		Sub2APIToken: "credential-for-another-provider",
	}))
	assert.Nil(t, binding.EncCredentials)
	assert.Zero(t, binding.KeyVersion)
}
