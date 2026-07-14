package model

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	// Stage the key ring on every upgraded node first. WRITE_VERSION defaults to
	// v1 and must only be changed to v2 after the whole fleet can read v2.
	RoutingCredentialEncryptionKeyRingEnv      = "ROUTING_CREDENTIAL_ENCRYPTION_KEY_RING"
	RoutingCredentialEncryptionWriteVersionEnv = "ROUTING_CREDENTIAL_ENCRYPTION_WRITE_VERSION"

	routingCredentialKeyRingMaxBytes        = 8 << 10
	routingCredentialPreviousKeyLimit       = 4
	routingCredentialReencryptBatchMax      = 500
	routingCredentialKeyIDMaxBytes          = 64
	routingCredentialPlaintextMaxBytes      = 128 << 10
	routingCredentialCiphertextMaxBytes     = 256 << 10
	routingCredentialAESKeyBytes            = 32
	routingCredentialLegacyCiphertextPrefix = "v1:"
)

var (
	ErrCredentialEncryptionConfig = errors.New("routing credential encryption configuration is invalid")
	ErrCredentialEnvelopeInvalid  = fmt.Errorf("%w: routing credential envelope is invalid", ErrCredentialKeyMismatch)
	ErrCredentialKeyUnavailable   = fmt.Errorf("%w: routing credential encryption key is unavailable", ErrCredentialKeyMismatch)
)

type RoutingCredentialEnvelopeState struct {
	KeyVersion     int
	KeyID          string
	NeedsReencrypt bool
}

type RoutingCredentialReencryptBatchResult struct {
	NextID    int
	Scanned   int
	Changed   int
	Conflicts int
	Done      bool
}

type routingCredentialEncryptionKey struct {
	id  string
	key [routingCredentialAESKeyBytes]byte
}

type routingCredentialEncryptionKeyRing struct {
	current routingCredentialEncryptionKey
	keys    map[string][routingCredentialAESKeyBytes]byte
}

type routingCredentialEncryptionConfig struct {
	writeVersion int
	keyRing      *routingCredentialEncryptionKeyRing
}

type routingCredentialCiphertextEnvelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type routingCredentialKeyDocument struct {
	ID        string `json:"id"`
	KeyBase64 string `json:"key_base64"`
}

// ValidateRoutingCredentialEncryptionConfiguration validates the environment
// document {"current":{"id":"...","key_base64":"..."},"previous":[]}.
// Keys are canonical base64-encoded 32-byte AES keys; key IDs and key material
// must be unique across the bounded ring.
func ValidateRoutingCredentialEncryptionConfiguration() error {
	_, err := routingCredentialEncryptionConfigFromEnvironment()
	return err
}

func (binding *RoutingChannelBinding) GetCredentialsWithState() (
	RoutingCredentials,
	RoutingCredentialEnvelopeState,
	error,
) {
	if binding == nil || binding.EncCredentials == nil || *binding.EncCredentials == "" {
		return RoutingCredentials{}, RoutingCredentialEnvelopeState{}, nil
	}
	plaintext, state, err := decryptRoutingCredentials(
		binding.ChannelID,
		binding.KeyVersion,
		*binding.EncCredentials,
	)
	if err != nil {
		return RoutingCredentials{}, RoutingCredentialEnvelopeState{}, err
	}
	var envelope routingCredentialsEnvelope
	if err := common.Unmarshal(plaintext, &envelope); err != nil {
		return RoutingCredentials{}, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	credentials := RoutingCredentials{
		NewAPIAccessToken: envelope.NewAPIAccessToken,
		GatewayAPIKey:     envelope.GatewayAPIKey,
		Sub2APIEmail:      envelope.Sub2APIEmail,
		Sub2APIPassword:   envelope.Sub2APIPassword,
		Sub2APIToken:      envelope.Sub2APIToken,
		CustomCAPEM:       envelope.CustomCAPEM,
	}.ForUpstream(binding.UpstreamType)
	return credentials, state, nil
}

// ReencryptRoutingChannelBindingCredentialsContext uses an exact ciphertext CAS so
// key rotation cannot overwrite a concurrent credential update.
func ReencryptRoutingChannelBindingCredentialsContext(
	ctx context.Context,
	binding *RoutingChannelBinding,
) (bool, error) {
	if binding == nil {
		return false, nil
	}
	if binding.ID <= 0 || binding.ChannelID <= 0 {
		return false, ErrRoutingBindingChanged
	}
	if binding.EncCredentials == nil || *binding.EncCredentials == "" {
		return false, nil
	}
	credentials, state, err := binding.GetCredentialsWithState()
	if err != nil {
		return false, err
	}
	if !state.NeedsReencrypt {
		return false, nil
	}

	updated := *binding
	if err := updated.SetCredentials(credentials); err != nil {
		return false, err
	}
	if updated.KeyVersion != RoutingCredentialKeyVersion {
		return false, ErrCredentialEncryptionConfig
	}
	updated.UpdatedTime = nextRoutingBindingUpdatedTime(binding.UpdatedTime)
	if ctx == nil {
		ctx = context.Background()
	}
	result := DB.WithContext(ctx).Model(&RoutingChannelBinding{}).
		Where(
			"id = ? AND channel_id = ? AND key_version = ? AND enc_credentials = ? AND updated_time = ?",
			binding.ID,
			binding.ChannelID,
			binding.KeyVersion,
			*binding.EncCredentials,
			binding.UpdatedTime,
		).
		Updates(map[string]any{
			"enc_credentials": updated.EncCredentials,
			"key_version":     updated.KeyVersion,
			"updated_time":    updated.UpdatedTime,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, ErrRoutingBindingChanged
	}
	binding.EncCredentials = updated.EncCredentials
	binding.KeyVersion = updated.KeyVersion
	binding.UpdatedTime = updated.UpdatedTime
	return true, nil
}

// ReencryptRoutingChannelBindingCredentialsBatchContext migrates a bounded ID
// page, including disabled bindings. Callers resume with NextID; CAS conflicts
// are reported and left for the next full scan rather than overwriting a
// concurrent credential update.
func ReencryptRoutingChannelBindingCredentialsBatchContext(
	ctx context.Context,
	afterID int,
	limit int,
) (RoutingCredentialReencryptBatchResult, error) {
	result := RoutingCredentialReencryptBatchResult{NextID: afterID}
	if afterID < 0 || limit < 1 || limit > routingCredentialReencryptBatchMax {
		return result, ErrCredentialEncryptionConfig
	}
	config, err := routingCredentialEncryptionConfigFromEnvironment()
	if err != nil {
		return result, err
	}
	if config.writeVersion != RoutingCredentialKeyVersion {
		result.Done = true
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var bindings []RoutingChannelBinding
	if err := DB.WithContext(ctx).
		Where("id > ? AND enc_credentials IS NOT NULL AND enc_credentials <> ?", afterID, "").
		Order("id asc").Limit(limit).Find(&bindings).Error; err != nil {
		return result, err
	}
	result.Scanned = len(bindings)
	result.Done = len(bindings) < limit
	for index := range bindings {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		binding := &bindings[index]
		result.NextID = binding.ID
		changed, err := ReencryptRoutingChannelBindingCredentialsContext(ctx, binding)
		if errors.Is(err, ErrRoutingBindingChanged) {
			result.Conflicts++
			continue
		}
		if err != nil {
			return result, err
		}
		if changed {
			result.Changed++
		}
	}
	return result, nil
}

func prepareRoutingCredentialReencryptionForBindingUpdate(
	expected RoutingChannelBinding,
	updated *RoutingChannelBinding,
) error {
	if updated == nil || expected.EncCredentials == nil || *expected.EncCredentials == "" ||
		updated.EncCredentials == nil || *updated.EncCredentials != *expected.EncCredentials ||
		updated.KeyVersion != expected.KeyVersion {
		return nil
	}
	config, err := routingCredentialEncryptionConfigFromEnvironment()
	if err != nil {
		return err
	}
	if config.writeVersion != RoutingCredentialKeyVersion {
		return nil
	}
	credentials, state, err := expected.GetCredentialsWithState()
	if err != nil {
		return err
	}
	if !state.NeedsReencrypt {
		return nil
	}
	return updated.SetCredentials(credentials)
}

func encryptRoutingCredentials(channelID int, plaintext []byte) (string, int, error) {
	if len(plaintext) > routingCredentialPlaintextMaxBytes {
		return "", 0, ErrCredentialEnvelopeInvalid
	}
	config, err := routingCredentialEncryptionConfigFromEnvironment()
	if err != nil {
		return "", 0, err
	}
	if config.writeVersion != RoutingCredentialKeyVersion {
		if !common.CryptoSecretIsPersistent() {
			return "", 0, ErrCredentialSecretUnstable
		}
		ciphertext, err := common.EncryptAESGCMString(string(plaintext))
		if err != nil {
			return "", 0, ErrCredentialEnvelopeInvalid
		}
		return ciphertext, RoutingCredentialLegacyKeyVersion, nil
	}
	if channelID <= 0 || config.keyRing == nil {
		return "", 0, ErrCredentialEncryptionConfig
	}

	block, err := aes.NewCipher(config.keyRing.current.key[:])
	if err != nil {
		return "", 0, ErrCredentialEnvelopeInvalid
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", 0, ErrCredentialEnvelopeInvalid
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", 0, ErrCredentialEnvelopeInvalid
	}
	aad := routingCredentialEnvelopeAAD(channelID, config.keyRing.current.id)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	encoded, err := common.Marshal(routingCredentialCiphertextEnvelope{
		Version:    RoutingCredentialKeyVersion,
		KeyID:      config.keyRing.current.id,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil || len(encoded) > routingCredentialCiphertextMaxBytes {
		return "", 0, ErrCredentialEnvelopeInvalid
	}
	return string(encoded), RoutingCredentialKeyVersion, nil
}

func decryptRoutingCredentials(
	channelID int,
	keyVersion int,
	encoded string,
) ([]byte, RoutingCredentialEnvelopeState, error) {
	if len(encoded) > routingCredentialCiphertextMaxBytes {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	config, err := routingCredentialEncryptionConfigFromEnvironment()
	if err != nil {
		return nil, RoutingCredentialEnvelopeState{}, err
	}
	if keyVersion == RoutingCredentialLegacyKeyVersion {
		if !common.CryptoSecretIsPersistent() || !strings.HasPrefix(encoded, routingCredentialLegacyCiphertextPrefix) {
			return nil, RoutingCredentialEnvelopeState{}, ErrCredentialKeyUnavailable
		}
		plaintext, err := common.DecryptAESGCMString(encoded)
		if err != nil || len(plaintext) > routingCredentialPlaintextMaxBytes {
			return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
		}
		return []byte(plaintext), RoutingCredentialEnvelopeState{
			KeyVersion:     RoutingCredentialLegacyKeyVersion,
			NeedsReencrypt: config.writeVersion == RoutingCredentialKeyVersion,
		}, nil
	}
	if keyVersion != RoutingCredentialKeyVersion || channelID <= 0 || config.keyRing == nil {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialKeyUnavailable
	}

	data := []byte(encoded)
	if err := validateRoutingCredentialJSONFields(
		data,
		[]string{"version", "key_id", "nonce", "ciphertext"},
		[]string{"version", "key_id", "nonce", "ciphertext"},
	); err != nil {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	var envelope routingCredentialCiphertextEnvelope
	if err := common.Unmarshal(data, &envelope); err != nil ||
		envelope.Version != RoutingCredentialKeyVersion || !validRoutingCredentialKeyID(envelope.KeyID) {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	key, ok := config.keyRing.keys[envelope.KeyID]
	if !ok {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialKeyUnavailable
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	sealed, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() || len(sealed) < gcm.Overhead() {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	plaintext, err := gcm.Open(nil, nonce, sealed, routingCredentialEnvelopeAAD(channelID, envelope.KeyID))
	if err != nil || len(plaintext) > routingCredentialPlaintextMaxBytes {
		return nil, RoutingCredentialEnvelopeState{}, ErrCredentialEnvelopeInvalid
	}
	return plaintext, RoutingCredentialEnvelopeState{
		KeyVersion: RoutingCredentialKeyVersion,
		KeyID:      envelope.KeyID,
		NeedsReencrypt: config.writeVersion == RoutingCredentialKeyVersion &&
			envelope.KeyID != config.keyRing.current.id,
	}, nil
}

func routingCredentialEncryptionConfigFromEnvironment() (routingCredentialEncryptionConfig, error) {
	config := routingCredentialEncryptionConfig{writeVersion: RoutingCredentialLegacyKeyVersion}
	writeVersion, writeVersionSet := os.LookupEnv(RoutingCredentialEncryptionWriteVersionEnv)
	if writeVersionSet {
		if writeVersion != strconv.Itoa(RoutingCredentialLegacyKeyVersion) &&
			writeVersion != strconv.Itoa(RoutingCredentialKeyVersion) {
			return routingCredentialEncryptionConfig{}, ErrCredentialEncryptionConfig
		}
		config.writeVersion, _ = strconv.Atoi(writeVersion)
	}

	raw, keyRingSet := os.LookupEnv(RoutingCredentialEncryptionKeyRingEnv)
	if !keyRingSet {
		if config.writeVersion == RoutingCredentialKeyVersion {
			return routingCredentialEncryptionConfig{}, ErrCredentialEncryptionConfig
		}
		return config, nil
	}
	if raw == "" || len(raw) > routingCredentialKeyRingMaxBytes || !utf8.ValidString(raw) {
		return routingCredentialEncryptionConfig{}, ErrCredentialEncryptionConfig
	}
	keyRing, err := parseRoutingCredentialEncryptionKeyRing([]byte(raw))
	if err != nil {
		return routingCredentialEncryptionConfig{}, err
	}
	config.keyRing = keyRing
	return config, nil
}

func parseRoutingCredentialEncryptionKeyRing(data []byte) (*routingCredentialEncryptionKeyRing, error) {
	if err := validateRoutingCredentialJSONFields(
		data,
		[]string{"current", "previous"},
		[]string{"current"},
	); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil {
		return nil, ErrCredentialEncryptionConfig
	}
	current, err := parseRoutingCredentialEncryptionKey(fields["current"])
	if err != nil {
		return nil, err
	}
	previous := make([]json.RawMessage, 0)
	if rawPrevious, ok := fields["previous"]; ok {
		if common.GetJsonType(rawPrevious) != "array" || common.Unmarshal(rawPrevious, &previous) != nil ||
			len(previous) > routingCredentialPreviousKeyLimit {
			return nil, ErrCredentialEncryptionConfig
		}
	}

	keys := make(map[string][routingCredentialAESKeyBytes]byte, len(previous)+1)
	keys[current.id] = current.key
	keyMaterials := map[[routingCredentialAESKeyBytes]byte]struct{}{current.key: {}}
	for _, rawPrevious := range previous {
		key, err := parseRoutingCredentialEncryptionKey(rawPrevious)
		if err != nil {
			return nil, err
		}
		if _, duplicate := keys[key.id]; duplicate {
			return nil, ErrCredentialEncryptionConfig
		}
		if _, duplicate := keyMaterials[key.key]; duplicate {
			return nil, ErrCredentialEncryptionConfig
		}
		keys[key.id] = key.key
		keyMaterials[key.key] = struct{}{}
	}
	return &routingCredentialEncryptionKeyRing{current: current, keys: keys}, nil
}

func parseRoutingCredentialEncryptionKey(data []byte) (routingCredentialEncryptionKey, error) {
	if err := validateRoutingCredentialJSONFields(
		data,
		[]string{"id", "key_base64"},
		[]string{"id", "key_base64"},
	); err != nil {
		return routingCredentialEncryptionKey{}, err
	}
	var document routingCredentialKeyDocument
	if err := common.Unmarshal(data, &document); err != nil || !validRoutingCredentialKeyID(document.ID) {
		return routingCredentialEncryptionKey{}, ErrCredentialEncryptionConfig
	}
	decoded, err := base64.StdEncoding.DecodeString(document.KeyBase64)
	if err != nil || len(decoded) != routingCredentialAESKeyBytes ||
		base64.StdEncoding.EncodeToString(decoded) != document.KeyBase64 {
		return routingCredentialEncryptionKey{}, ErrCredentialEncryptionConfig
	}
	key := routingCredentialEncryptionKey{id: document.ID}
	copy(key.key[:], decoded)
	return key, nil
}

func validateRoutingCredentialJSONFields(data []byte, allowed []string, required []string) error {
	if common.GetJsonType(data) != "object" {
		return ErrCredentialEncryptionConfig
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrCredentialEncryptionConfig
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowedSet[field]; !ok {
			return ErrCredentialEncryptionConfig
		}
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return ErrCredentialEncryptionConfig
		}
	}
	return nil
}

func validRoutingCredentialKeyID(value string) bool {
	if value == "" || len(value) > routingCredentialKeyIDMaxBytes {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func routingCredentialEnvelopeAAD(channelID int, keyID string) []byte {
	return []byte("routing-credential-envelope:v" + strconv.Itoa(RoutingCredentialKeyVersion) + "\x00" +
		strconv.Itoa(channelID) + "\x00" + keyID)
}
