package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentEventInboxPersistsBeforeAuthorityValidationAndRetries(t *testing.T) {
	truncateTables(t)
	input := PaymentEventInput{
		Provider:          "inbox_test",
		EventKey:          "inbox_authority_retry",
		EventType:         "provider.event",
		NormalizedPayload: `{"event":"authority_retry"}`,
	}

	require.NoError(t, RecordPaymentEventReceived(input))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", input.Provider, input.EventKey).First(&event).Error)
	assert.Equal(t, PaymentEventStatusReceived, event.Status)
	assert.Zero(t, event.Attempts)

	require.NoError(t, MarkPaymentEventValidationFailed(input.Provider, input.EventKey, "provider_authority_validation_failed"))
	require.NoError(t, DB.First(&event, event.ID).Error)
	assert.Equal(t, PaymentEventStatusFailed, event.Status)
	assert.Equal(t, "provider_authority_validation_failed", event.LastError)
	assert.Equal(t, 1, event.Attempts)

	result, err := ProcessPaymentEvent(input)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NoError(t, DB.First(&event, event.ID).Error)
	assert.Equal(t, PaymentEventStatusProcessed, event.Status)
	assert.Equal(t, 2, event.Attempts)
	assert.Empty(t, event.LastError)

	require.NoError(t, MarkPaymentEventValidationFailed(input.Provider, input.EventKey, "provider_authority_validation_failed"))
	require.NoError(t, DB.First(&event, event.ID).Error)
	assert.Equal(t, PaymentEventStatusProcessed, event.Status)
	assert.Equal(t, 2, event.Attempts)
}
