package model

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingCredentialEncryptionTwoPhaseActivation(t *testing.T) {
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, routingCredentialTestKeyRing(
		t, "key-a", routingCredentialTestKey('a'), nil,
	))
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "")

	credentials := RoutingCredentials{NewAPIAccessToken: "new-api-secret", Sub2APIPassword: "password-secret"}
	legacy := RoutingChannelBinding{ChannelID: 2001}
	require.NoError(t, legacy.SetCredentials(credentials))
	assert.Equal(t, RoutingCredentialLegacyKeyVersion, legacy.KeyVersion)
	_, state, err := legacy.GetCredentialsWithState()
	require.NoError(t, err)
	assert.False(t, state.NeedsReencrypt)

	require.NoError(t, os.Setenv(RoutingCredentialEncryptionWriteVersionEnv, "2"))
	decoded, state, err := legacy.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, credentials, decoded)
	assert.True(t, state.NeedsReencrypt)

	current := RoutingChannelBinding{ChannelID: legacy.ChannelID}
	require.NoError(t, current.SetCredentials(credentials))
	assert.Equal(t, RoutingCredentialKeyVersion, current.KeyVersion)
	_, state, err = current.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, "key-a", state.KeyID)
	assert.False(t, state.NeedsReencrypt)

	// During activation, upgraded nodes that still write v1 can read v2 because
	// the key ring was staged fleet-wide before WRITE_VERSION changed.
	require.NoError(t, os.Unsetenv(RoutingCredentialEncryptionWriteVersionEnv))
	decoded, state, err = current.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, credentials, decoded)
	assert.False(t, state.NeedsReencrypt)
}

func TestRoutingCredentialEncryptionReadsPreRotationV1Envelope(t *testing.T) {
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	legacyPayload, err := common.Marshal(routingCredentialsEnvelope{
		NewAPIAccessToken: "pre-rotation-secret",
		CustomCAPEM:       "legacy-custom-ca",
	})
	require.NoError(t, err)
	legacyCiphertext, err := common.EncryptAESGCMString(string(legacyPayload))
	require.NoError(t, err)
	binding := RoutingChannelBinding{
		ChannelID: 2008, EncCredentials: &legacyCiphertext,
		KeyVersion: RoutingCredentialLegacyKeyVersion,
	}

	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(
		t, "active", routingCredentialTestKey('a'), nil,
	))
	credentials, state, err := binding.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, "pre-rotation-secret", credentials.NewAPIAccessToken)
	assert.Equal(t, "legacy-custom-ca", credentials.CustomCAPEM)
	assert.Equal(t, RoutingCredentialLegacyKeyVersion, state.KeyVersion)
	assert.True(t, state.NeedsReencrypt)
}

func TestRoutingCredentialEncryptionCurrentPreviousAndRetirement(t *testing.T) {
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	keyA := routingCredentialTestKey('a')
	keyB := routingCredentialTestKey('b')
	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(t, "key-a", keyA, nil))

	credentials := RoutingCredentials{Sub2APIEmail: "admin@example.com", Sub2APIPassword: "secret-password"}
	oldBinding := RoutingChannelBinding{ChannelID: 2002}
	require.NoError(t, oldBinding.SetCredentials(credentials))
	var oldEnvelope routingCredentialCiphertextEnvelope
	require.NoError(t, common.UnmarshalJsonStr(*oldBinding.EncCredentials, &oldEnvelope))
	assert.Equal(t, "key-a", oldEnvelope.KeyID)

	previous := []routingCredentialKeyDocument{{ID: "key-a", KeyBase64: keyA}}
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv,
		routingCredentialTestKeyRing(t, "key-b", keyB, previous))
	decoded, state, err := oldBinding.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, credentials, decoded)
	assert.Equal(t, "key-a", state.KeyID)
	assert.True(t, state.NeedsReencrypt)

	newBinding := RoutingChannelBinding{ChannelID: oldBinding.ChannelID}
	require.NoError(t, newBinding.SetCredentials(credentials))
	var newEnvelope routingCredentialCiphertextEnvelope
	require.NoError(t, common.UnmarshalJsonStr(*newBinding.EncCredentials, &newEnvelope))
	assert.Equal(t, "key-b", newEnvelope.KeyID)

	// An instance still writing key-a can decrypt key-b while key-b is staged in
	// its previous ring, which keeps a rolling current-key switch bidirectional.
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv,
		routingCredentialTestKeyRing(t, "key-a", keyA,
			[]routingCredentialKeyDocument{{ID: "key-b", KeyBase64: keyB}}))
	decoded, state, err = newBinding.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, credentials, decoded)
	assert.True(t, state.NeedsReencrypt)

	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv,
		routingCredentialTestKeyRing(t, "key-b", keyB, nil))
	_, _, err = oldBinding.GetCredentialsWithState()
	require.ErrorIs(t, err, ErrCredentialKeyUnavailable)
	assert.NotContains(t, err.Error(), keyA)
	assert.NotContains(t, err.Error(), credentials.Sub2APIPassword)
	decoded, state, err = newBinding.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, credentials, decoded)
	assert.False(t, state.NeedsReencrypt)
}

func TestRoutingCredentialEncryptionBindsCiphertextToChannel(t *testing.T) {
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(
		t, "channel-bound", routingCredentialTestKey('c'), nil,
	))

	binding := RoutingChannelBinding{ChannelID: 2003}
	require.NoError(t, binding.SetCredentials(RoutingCredentials{GatewayAPIKey: "gateway-secret"}))
	tampered := binding
	tampered.ChannelID++
	_, _, err := tampered.GetCredentialsWithState()
	require.ErrorIs(t, err, ErrCredentialEnvelopeInvalid)
}

func TestRoutingCredentialEncryptionConfigurationValidation(t *testing.T) {
	validKey := routingCredentialTestKey('v')
	validRing := routingCredentialTestKeyRing(t, "valid-key", validKey, nil)
	tests := []struct {
		name         string
		keyRing      *string
		writeVersion *string
	}{
		{name: "write v2 without ring", writeVersion: routingCredentialString("2")},
		{name: "blank ring", keyRing: routingCredentialString("")},
		{name: "oversized ring", keyRing: routingCredentialString(strings.Repeat("x", routingCredentialKeyRingMaxBytes+1))},
		{name: "malformed json", keyRing: routingCredentialString(`{"current":`)},
		{name: "unknown root field", keyRing: routingCredentialString(`{"current":{"id":"key","key_base64":"` + validKey + `"},"unknown":true}`)},
		{name: "missing current", keyRing: routingCredentialString(`{"previous":[]}`)},
		{name: "invalid key id", keyRing: routingCredentialString(`{"current":{"id":"not allowed","key_base64":"` + validKey + `"}}`)},
		{name: "weak key", keyRing: routingCredentialString(routingCredentialTestKeyRing(t, "weak", base64.StdEncoding.EncodeToString(make([]byte, 31)), nil))},
		{name: "non canonical key", keyRing: routingCredentialString(`{"current":{"id":"key","key_base64":"` + strings.TrimRight(validKey, "=") + `"}}`)},
		{name: "unknown key field", keyRing: routingCredentialString(`{"current":{"id":"key","key_base64":"` + validKey + `","secret":"hidden"}}`)},
		{name: "duplicate key id", keyRing: routingCredentialString(routingCredentialTestKeyRing(t, "same", validKey, []routingCredentialKeyDocument{{ID: "same", KeyBase64: routingCredentialTestKey('d')}}))},
		{name: "duplicate key material", keyRing: routingCredentialString(routingCredentialTestKeyRing(t, "current", validKey, []routingCredentialKeyDocument{{ID: "previous", KeyBase64: validKey}}))},
		{name: "too many previous keys", keyRing: routingCredentialString(routingCredentialTestKeyRing(t, "current", validKey, []routingCredentialKeyDocument{
			{ID: "p1", KeyBase64: routingCredentialTestKey('1')},
			{ID: "p2", KeyBase64: routingCredentialTestKey('2')},
			{ID: "p3", KeyBase64: routingCredentialTestKey('3')},
			{ID: "p4", KeyBase64: routingCredentialTestKey('4')},
			{ID: "p5", KeyBase64: routingCredentialTestKey('5')},
		}))},
		{name: "invalid write version", keyRing: &validRing, writeVersion: routingCredentialString("3")},
		{name: "write version whitespace", keyRing: &validRing, writeVersion: routingCredentialString(" 2 ")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRoutingCredentialEnvOptionalForTest(t, RoutingCredentialEncryptionKeyRingEnv, test.keyRing)
			setRoutingCredentialEnvOptionalForTest(t, RoutingCredentialEncryptionWriteVersionEnv, test.writeVersion)
			require.ErrorIs(t, ValidateRoutingCredentialEncryptionConfiguration(), ErrCredentialEncryptionConfig)
		})
	}

	t.Run("valid staged ring", func(t *testing.T) {
		setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, validRing)
		setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "")
		require.NoError(t, ValidateRoutingCredentialEncryptionConfiguration())
	})
	t.Run("valid active ring", func(t *testing.T) {
		setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, validRing)
		setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "2")
		require.NoError(t, ValidateRoutingCredentialEncryptionConfiguration())
	})
}

func TestInitDBFailsFastForInvalidRoutingCredentialEncryptionConfiguration(t *testing.T) {
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, `{"current":null}`)
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "2")
	require.ErrorIs(t, InitDB(), ErrCredentialEncryptionConfig)
}

func TestRoutingCredentialEncryptionConcurrentRoundTrip(t *testing.T) {
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(
		t, "concurrent", routingCredentialTestKey('x'), nil,
	))
	credentials := RoutingCredentials{
		NewAPIAccessToken: "new-api-secret",
		GatewayAPIKey:     "gateway-secret",
		Sub2APIEmail:      "admin@example.com",
		Sub2APIPassword:   "password-secret",
	}

	const workers = 64
	errorsCh := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			binding := RoutingChannelBinding{ChannelID: 2004}
			if err := binding.SetCredentials(credentials); err != nil {
				errorsCh <- err
				return
			}
			decoded, state, err := binding.GetCredentialsWithState()
			if err != nil {
				errorsCh <- err
				return
			}
			if decoded != credentials || state.KeyID != "concurrent" || state.NeedsReencrypt {
				errorsCh <- errors.New("routing credential concurrent round trip mismatch")
			}
		}()
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
}

func TestRoutingCredentialRotationPreservesStableCredentialID(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))
	setRoutingCredentialCryptoSecretForTest(t, "stable-and-independent-fingerprint-secret")
	require.NoError(t, DB.Create(&Channel{Id: 2005, Name: "stable", Key: "serving-key", Group: "default"}).Error)
	_, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var before RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ? AND active = ?", 2005, true).First(&before).Error)

	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(
		t, "envelope-a", routingCredentialTestKey('a'), nil,
	))
	binding := RoutingChannelBinding{ChannelID: 2005}
	require.NoError(t, binding.SetCredentials(RoutingCredentials{NewAPIAccessToken: "cost-source-secret"}))
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, routingCredentialTestKeyRing(
		t,
		"envelope-b",
		routingCredentialTestKey('b'),
		[]routingCredentialKeyDocument{{ID: "envelope-a", KeyBase64: routingCredentialTestKey('a')}},
	))
	credentials, state, err := binding.GetCredentialsWithState()
	require.NoError(t, err)
	assert.True(t, state.NeedsReencrypt)
	require.NoError(t, binding.SetCredentials(credentials))

	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var after RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ? AND active = ?", 2005, true).First(&after).Error)
	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.Fingerprint, after.Fingerprint)
}

func TestRoutingCredentialReencryptDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "sqlite", dbType: common.DatabaseTypeSQLite},
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *gorm.DB
			if test.dbType == common.DatabaseTypeSQLite {
				db = openRoutingSQLiteTestDB(t)
			} else {
				dsn := os.Getenv(test.envKey)
				if dsn == "" {
					t.Skipf("%s is not set", test.envKey)
				}
				db = openRoutingExternalTestDB(t, test.dbType, dsn)
			}
			withRoutingTestDB(t, db, test.dbType)
			runRoutingCredentialReencryptDatabaseContract(t)
		})
	}
}

func TestRoutingCredentialBindingUpdateReencryptsOnSafeWrite(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&RoutingChannelBinding{},
		&RoutingCostSnapshot{},
		&RoutingChannelHealthState{},
	))
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, "")
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "")
	binding := RoutingChannelBinding{
		ChannelID: 2007, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://old.example.com", UpstreamGroup: "default", Enabled: true,
	}
	require.NoError(t, binding.SetCredentials(RoutingCredentials{NewAPIAccessToken: "legacy-secret"}))
	require.NoError(t, DB.Create(&binding).Error)

	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(
		t, "active", routingCredentialTestKey('a'), nil,
	))
	expected := binding
	updated := binding
	updated.BaseURL = "https://new.example.com"
	require.NoError(t, UpdateRoutingChannelBindingAndInvalidateCostContext(context.Background(), expected, &updated))
	assert.Equal(t, RoutingCredentialKeyVersion, updated.KeyVersion)

	var stored RoutingChannelBinding
	require.NoError(t, DB.Where("id = ?", binding.ID).First(&stored).Error)
	credentials, state, err := stored.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, "legacy-secret", credentials.NewAPIAccessToken)
	assert.Equal(t, "active", state.KeyID)
	assert.False(t, state.NeedsReencrypt)
}

func TestRoutingCredentialBatchReencryptsDisabledBindingsAndResumes(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}))
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, "")
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "")

	bindings := []RoutingChannelBinding{
		{ChannelID: 2101, UpstreamType: RoutingUpstreamTypeNewAPI, BaseURL: "https://one.example", UpstreamGroup: "default", Enabled: true},
		{ChannelID: 2102, UpstreamType: RoutingUpstreamTypeSub2API, BaseURL: "https://two.example", UpstreamGroup: "default", Enabled: false},
	}
	for index := range bindings {
		require.NoError(t, bindings[index].SetCredentials(RoutingCredentials{GatewayAPIKey: "secret-" + strconv.Itoa(index)}))
		require.NoError(t, DB.Create(&bindings[index]).Error)
	}
	require.NoError(t, DB.Create(&RoutingChannelBinding{
		ChannelID: 2103, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://empty.example", UpstreamGroup: "default", Enabled: false,
	}).Error)

	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(
		t, "batch-current", routingCredentialTestKey('b'), nil,
	))
	cursor := 0
	totalScanned := 0
	totalChanged := 0
	for {
		batch, err := ReencryptRoutingChannelBindingCredentialsBatchContext(context.Background(), cursor, 1)
		require.NoError(t, err)
		totalScanned += batch.Scanned
		totalChanged += batch.Changed
		if batch.Done {
			break
		}
		require.Greater(t, batch.NextID, cursor)
		cursor = batch.NextID
	}
	assert.Equal(t, 2, totalScanned)
	assert.Equal(t, 2, totalChanged)

	for _, original := range bindings {
		var stored RoutingChannelBinding
		require.NoError(t, DB.Where("id = ?", original.ID).First(&stored).Error)
		_, state, err := stored.GetCredentialsWithState()
		require.NoError(t, err)
		assert.Equal(t, RoutingCredentialKeyVersion, stored.KeyVersion)
		assert.Equal(t, "batch-current", state.KeyID)
		assert.False(t, state.NeedsReencrypt)
	}

	secondPass, err := ReencryptRoutingChannelBindingCredentialsBatchContext(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.True(t, secondPass.Done)
	assert.Equal(t, 2, secondPass.Scanned)
	assert.Zero(t, secondPass.Changed)
	_, err = ReencryptRoutingChannelBindingCredentialsBatchContext(context.Background(), 0, 0)
	assert.ErrorIs(t, err, ErrCredentialEncryptionConfig)
}

func runRoutingCredentialReencryptDatabaseContract(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}))
	setRoutingCredentialCryptoSecretForTest(t, "stable-routing-fingerprint-secret")
	keyA := routingCredentialTestKey('a')
	keyB := routingCredentialTestKey('b')
	setRoutingCredentialRotationForTest(t, routingCredentialTestKeyRing(t, "key-a", keyA, nil))

	binding := RoutingChannelBinding{
		ChannelID: 2006, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://routing.example.com", UpstreamGroup: "default", Enabled: true,
	}
	credentials := RoutingCredentials{NewAPIAccessToken: "database-secret"}
	require.NoError(t, binding.SetCredentials(credentials))
	require.NoError(t, DB.Create(&binding).Error)
	first := binding
	second := binding

	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, routingCredentialTestKeyRing(
		t, "key-b", keyB, []routingCredentialKeyDocument{{ID: "key-a", KeyBase64: keyA}},
	))
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, 2)
	var waitGroup sync.WaitGroup
	for _, candidate := range []*RoutingChannelBinding{&first, &second} {
		waitGroup.Add(1)
		go func(binding *RoutingChannelBinding) {
			defer waitGroup.Done()
			changed, err := ReencryptRoutingChannelBindingCredentialsContext(context.Background(), binding)
			results <- result{changed: changed, err: err}
		}(candidate)
	}
	waitGroup.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for result := range results {
		switch {
		case result.err == nil && result.changed:
			successes++
		case errors.Is(result.err, ErrRoutingBindingChanged):
			conflicts++
		default:
			require.NoError(t, result.err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)

	var stored RoutingChannelBinding
	require.NoError(t, DB.Where("id = ?", binding.ID).First(&stored).Error)
	decoded, state, err := stored.GetCredentialsWithState()
	require.NoError(t, err)
	assert.Equal(t, credentials, decoded)
	assert.Equal(t, "key-b", state.KeyID)
	assert.False(t, state.NeedsReencrypt)
}

func routingCredentialTestKey(value byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(value), routingCredentialAESKeyBytes)))
}

func routingCredentialTestKeyRing(
	t *testing.T,
	currentID string,
	currentKey string,
	previous []routingCredentialKeyDocument,
) string {
	t.Helper()
	payload, err := common.Marshal(struct {
		Current  routingCredentialKeyDocument   `json:"current"`
		Previous []routingCredentialKeyDocument `json:"previous,omitempty"`
	}{
		Current:  routingCredentialKeyDocument{ID: currentID, KeyBase64: currentKey},
		Previous: previous,
	})
	require.NoError(t, err)
	return string(payload)
}

func setRoutingCredentialCryptoSecretForTest(t *testing.T, secret string) {
	t.Helper()
	previous := common.CryptoSecret
	common.CryptoSecret = secret
	t.Cleanup(func() { common.CryptoSecret = previous })
	setRoutingCredentialEnvForTest(t, "CRYPTO_SECRET", secret)
}

func setRoutingCredentialRotationForTest(t *testing.T, keyRing string) {
	t.Helper()
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionKeyRingEnv, keyRing)
	setRoutingCredentialEnvForTest(t, RoutingCredentialEncryptionWriteVersionEnv, "2")
}

func setRoutingCredentialEnvForTest(t *testing.T, key string, value string) {
	t.Helper()
	if value == "" {
		setRoutingCredentialEnvOptionalForTest(t, key, nil)
		return
	}
	setRoutingCredentialEnvOptionalForTest(t, key, &value)
}

func setRoutingCredentialEnvOptionalForTest(t *testing.T, key string, value *string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if value == nil {
		require.NoError(t, os.Unsetenv(key))
	} else {
		require.NoError(t, os.Setenv(key, *value))
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func routingCredentialString(value string) *string {
	return &value
}
