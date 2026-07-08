package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const aesGCMStringPrefix = "v1:"

var ErrAESGCMCiphertextVersion = errors.New("unsupported AES-GCM ciphertext version")

func EncryptAESGCMString(plaintext string) (string, error) {
	block, err := aes.NewCipher(aesGCMKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return aesGCMStringPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

func DecryptAESGCMString(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, aesGCMStringPrefix) {
		return "", ErrAESGCMCiphertextVersion
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, aesGCMStringPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(aesGCMKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("AES-GCM ciphertext is shorter than nonce")
	}
	nonce := raw[:gcm.NonceSize()]
	payload := raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func CryptoSecretIsPersistent() bool {
	if secret, ok := os.LookupEnv("CRYPTO_SECRET"); ok {
		if isPersistentSecret(secret) {
			return CryptoSecret == secret
		}
	}
	if secret, ok := os.LookupEnv("SESSION_SECRET"); ok {
		return isPersistentSecret(secret) && CryptoSecret == secret
	}
	return false
}

func isPersistentSecret(secret string) bool {
	secret = strings.TrimSpace(secret)
	return secret != "" && secret != "random_string"
}

func aesGCMKey() []byte {
	sum := sha256.Sum256([]byte(CryptoSecret))
	return sum[:]
}
