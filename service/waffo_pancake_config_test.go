package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWaffoPancakeConfigUsesSDKCredentialAndBindingRules(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}))

	require.NoError(t, ValidateWaffoPancakeConfig(
		"MER_AbCdEfGhIjKlMnOpQrStUv", privateKeyPEM, "https://payments.example.com/return",
		"STO_AbCdEfGhIjKlMnOpQrStUv", "PROD_AbCdEfGhIjKlMnOpQrStUv",
	))
	require.NoError(t, ValidateWaffoPancakeConfig("", "", "", "", ""))
	assert.Error(t, ValidateWaffoPancakeConfig(
		"MER_AbCdEfGhIjKlMnOpQrStUv", "invalid-private-key", "",
		"STO_AbCdEfGhIjKlMnOpQrStUv", "PROD_AbCdEfGhIjKlMnOpQrStUv",
	))
	assert.Error(t, ValidateWaffoPancakeConfig(
		"MER_AbCdEfGhIjKlMnOpQrStUv", privateKeyPEM, "", "invalid-store", "PROD_AbCdEfGhIjKlMnOpQrStUv",
	))
	assert.Error(t, ValidateWaffoPancakeConfig(
		"MER_AbCdEfGhIjKlMnOpQrStUv", privateKeyPEM, "", "STO_AbCdEfGhIjKlMnOpQrStUv", "invalid-product",
	))
	assert.Error(t, ValidateWaffoPancakeConfig(
		"MER_AbCdEfGhIjKlMnOpQrStUv", privateKeyPEM, "", "", "",
	))
}
