package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESGCMStringRoundTripUsesVersionPrefix(t *testing.T) {
	previousSecret := CryptoSecret
	CryptoSecret = "persistent-routing-secret"
	t.Cleanup(func() { CryptoSecret = previousSecret })

	ciphertext, err := EncryptAESGCMString("newapi-access-token")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(ciphertext, "v1:"))
	assert.NotContains(t, ciphertext, "newapi-access-token")

	plaintext, err := DecryptAESGCMString(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "newapi-access-token", plaintext)
}

func TestAESGCMStringRejectsWrongSecret(t *testing.T) {
	previousSecret := CryptoSecret
	CryptoSecret = "secret-a"
	t.Cleanup(func() { CryptoSecret = previousSecret })

	ciphertext, err := EncryptAESGCMString("sub2api-jwt")
	require.NoError(t, err)

	CryptoSecret = "secret-b"
	_, err = DecryptAESGCMString(ciphertext)
	require.Error(t, err)
}

func TestCryptoSecretIsPersistentRequiresExplicitStableSecret(t *testing.T) {
	previousSecret := CryptoSecret
	CryptoSecret = "runtime-random-secret"
	t.Cleanup(func() { CryptoSecret = previousSecret })
	t.Setenv("CRYPTO_SECRET", "")
	t.Setenv("SESSION_SECRET", "")

	assert.False(t, CryptoSecretIsPersistent())

	t.Setenv("SESSION_SECRET", "random_string")
	CryptoSecret = "random_string"
	assert.False(t, CryptoSecretIsPersistent())

	t.Setenv("SESSION_SECRET", "stable-session-secret")
	CryptoSecret = "stable-session-secret"
	assert.True(t, CryptoSecretIsPersistent())

	t.Setenv("CRYPTO_SECRET", "stable-crypto-secret")
	CryptoSecret = "stable-crypto-secret"
	assert.True(t, CryptoSecretIsPersistent())
}
