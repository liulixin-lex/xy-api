package service

import (
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func enablePaymentMultiNodeModeForTest(t *testing.T, nodes ...string) {
	t.Helper()
	require.GreaterOrEqual(t, len(nodes), 2)
	originalName := common.NodeName
	originalSource := common.NodeNameSource
	originalManual := common.NodeNameManuallyConfigured
	common.NodeName = nodes[0]
	common.NodeNameSource = common.NodeNameSourceManual
	common.NodeNameManuallyConfigured = true
	t.Cleanup(func() {
		common.NodeName = originalName
		common.NodeNameSource = originalSource
		common.NodeNameManuallyConfigured = originalManual
	})
	t.Setenv("PAYMENT_MULTI_NODE_ENABLED", "true")
	t.Setenv("PAYMENT_CLUSTER_ID", "payment-test-cluster")
	t.Setenv("PAYMENT_CLUSTER_NODES", strings.Join(nodes, ","))
	t.Setenv("PAYMENT_CLUSTER_MIN_LIVE_NODES", strconv.Itoa(len(nodes)/2+1))
}

func usePaymentClusterRedisForTest(t *testing.T, databaseType common.DatabaseType) {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	originalMainType := common.MainDatabaseType()
	originalRedisEnabled := common.RedisEnabled
	originalRedisClient := common.RDB
	common.SetMainDatabaseType(databaseType)
	common.RedisEnabled = true
	common.RDB = client
	t.Setenv("REDIS_CONN_STRING", "redis://"+server.Addr()+"/0")
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = originalRedisClient
		common.RedisEnabled = originalRedisEnabled
		common.SetMainDatabaseType(originalMainType)
	})
}

func upsertPaymentClusterDatabasePeerForTest(
	t *testing.T,
	inventoryKey string,
	logicalNode string,
	runtime PaymentRuntimeInfo,
) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, model.UpsertSystemInstance(inventoryKey, SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          common.NodeIdentity{Name: logicalNode},
		Payment:       runtime,
	}, now, now))
}

func witnessPaymentClusterPeerForTest(t *testing.T, inventoryKey string) {
	t.Helper()
	require.NoError(t, reportPaymentClusterRedisLease(inventoryKey))
}

func upsertWitnessedPaymentClusterPeerForTest(
	t *testing.T,
	inventoryKey string,
	logicalNode string,
	runtime PaymentRuntimeInfo,
) {
	t.Helper()
	upsertPaymentClusterDatabasePeerForTest(t, inventoryKey, logicalNode, runtime)
	witnessPaymentClusterPeerForTest(t, inventoryKey)
}

func TestPaymentClusterReadinessKeepsSingleNodeSQLiteCompatible(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-single-node-test-key-0001")
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	assert.NoError(t, EnsurePaymentClusterReady())
}

func TestPaymentRuntimeInfoRejectsMissingSessionSecret(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-session-test-key-000001")
	original := common.SessionSecret
	common.SessionSecret = ""
	t.Cleanup(func() { common.SessionSecret = original })

	info := currentPaymentRuntimeInfo()
	assert.False(t, info.Ready)
	assert.Equal(t, "session_secret_missing", info.ReadinessCode)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterKeyMismatch)
}

func TestPaymentRuntimeInfoRejectsDuplicatePreviousPaymentKey(t *testing.T) {
	key := "payment-cluster-duplicate-key-at-least-32-bytes"
	t.Setenv("PAYMENT_SECRET_KEY", key)
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "  "+key+"  ")

	info := currentPaymentRuntimeInfo()
	assert.False(t, info.Ready)
	assert.Equal(t, "payment_secret_keyring_invalid", info.ReadinessCode)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterKeyMismatch)
}

func TestPaymentClusterReadinessRejectsInvalidExplicitMultiNodeConfiguration(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-invalid-mode-key-0000001")
	t.Setenv("PAYMENT_MULTI_NODE_ENABLED", "true")
	t.Setenv("PAYMENT_CLUSTER_ID", "")
	t.Setenv("PAYMENT_CLUSTER_NODES", "payment-a,payment-b")

	info := currentPaymentRuntimeInfo()
	assert.False(t, info.Ready)
	assert.Equal(t, "multi_node_configuration_invalid", info.ReadinessCode)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterIdentityInvalid)
	assert.Equal(t, "cluster_identity_invalid", PaymentClusterReadinessCode(ErrPaymentClusterIdentityInvalid))
}

func TestPaymentClusterReadinessRejectsNonMajorityMinimum(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-a", "payment-b", "payment-c", "payment-d")
	t.Setenv("PAYMENT_CLUSTER_MIN_LIVE_NODES", "2")

	_, err := currentPaymentMultiNodeConfiguration()
	assert.ErrorIs(t, err, ErrPaymentClusterIdentityInvalid)
	info := currentPaymentRuntimeInfo()
	assert.False(t, info.Ready)
	assert.Equal(t, "multi_node_configuration_invalid", info.ReadinessCode)
}

func TestPaymentRuntimeInfoRejectsUndecryptablePaymentStorage(t *testing.T) {
	truncate(t)
	model.InitOptionMap()
	oldKey := "payment-cluster-storage-old-key-at-least-32-bytes"
	newKey := "payment-cluster-storage-new-key-at-least-32-bytes"
	t.Setenv("PAYMENT_SECRET_KEY", oldKey)
	require.NoError(t, model.UpdateOption("StripeApiSecret", "sk_test_cluster_storage"))
	t.Cleanup(func() {
		require.NoError(t, model.UpdateOption("StripeApiSecret", ""))
		require.NoError(t, model.DeleteOptions([]string{"StripeApiSecret"}))
	})

	t.Setenv("PAYMENT_SECRET_KEY", newKey)
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "")
	info := currentPaymentRuntimeInfo()
	assert.False(t, info.Ready)
	assert.Equal(t, "payment_secret_storage_unavailable", info.ReadinessCode)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterKeyMismatch)
}

func TestPaymentRuntimeSecretFingerprintUsesExactBytes(t *testing.T) {
	assert.NotEqual(t, paymentRuntimeSecretFingerprint("shared-secret"), paymentRuntimeSecretFingerprint(" shared-secret "))
	assert.Empty(t, paymentRuntimeSecretFingerprint(" \t\n"))
	assert.Equal(t, paymentRuntimeSecretFingerprint("shared-secret"), paymentRuntimeSecretFingerprint("shared-secret"))
}

func TestPaymentRuntimeConfigurationFingerprintIncludesStripeCheckoutAllowedHosts(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	optionMapWasNil := common.OptionMap == nil
	if optionMapWasNil {
		common.OptionMap = make(map[string]string)
	}
	original, existed := common.OptionMap["StripeCheckoutAllowedHosts"]
	common.OptionMap["StripeCheckoutAllowedHosts"] = ""
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if optionMapWasNil {
			common.OptionMap = nil
		} else if existed {
			common.OptionMap["StripeCheckoutAllowedHosts"] = original
		} else {
			delete(common.OptionMap, "StripeCheckoutAllowedHosts")
		}
	})

	withoutCustomHost := paymentRuntimeConfigurationFingerprint()
	common.OptionMapRWMutex.Lock()
	common.OptionMap["StripeCheckoutAllowedHosts"] = "pay.example.com"
	common.OptionMapRWMutex.Unlock()
	assert.NotEqual(t, withoutCustomHost, paymentRuntimeConfigurationFingerprint())
}

func TestPaymentClusterReadinessRejectsDuplicateLogicalNodeName(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-node-name-test-key-00001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	local := currentPaymentRuntimeInfo()
	identity, _, err := currentSystemInstanceIdentity()
	require.NoError(t, err)
	upsertWitnessedPaymentClusterPeerForTest(t, "conflicting-inventory-key", identity.Name, local)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterNodeConflict)
	assert.Equal(t, "node_identity_conflict", PaymentClusterReadinessCode(ErrPaymentClusterNodeConflict))
}

func TestPaymentClusterReadinessRejectsSQLiteWhenAnotherNodeIsLive(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-sqlite-test-key-00000001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypeSQLite)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	local := currentPaymentRuntimeInfo()
	upsertWitnessedPaymentClusterPeerForTest(t, "payment-peer", "payment-peer", local)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterSQLiteUnsupported)
}

func TestPaymentClusterReadinessRequiresExplicitModeWhenAnotherNodeIsLive(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-mode-required-key-0000001")
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	local := currentPaymentRuntimeInfo()
	require.NoError(t, model.UpsertSystemInstance("payment-peer", SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          common.NodeIdentity{Name: "payment-peer"},
		Payment:       local,
	}, common.GetTimestamp(), common.GetTimestamp()))
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterModeRequired)
	assert.Equal(t, "multi_node_mode_required", PaymentClusterReadinessCode(ErrPaymentClusterModeRequired))
}

func TestPaymentClusterReadinessRejectsMissingExpectedMemberBeforeTaskWork(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-membership-test-key-00001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)

	require.NoError(t, ReportCurrentSystemInstance())
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterMembershipMismatch)
	assert.Equal(t, "membership_mismatch", PaymentClusterReadinessCode(ErrPaymentClusterMembershipMismatch))
}

func TestPaymentClusterReadinessAllowsExpectedNodeRestartAtMinimumLiveNodes(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer", "payment-spare")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-restart-test-key-0000001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)

	require.NoError(t, ReportCurrentSystemInstance())
	peerRuntime := currentPaymentRuntimeInfo()
	upsertWitnessedPaymentClusterPeerForTest(t, "payment-peer", "payment-peer", peerRuntime)
	upsertWitnessedPaymentClusterPeerForTest(t, "payment-spare", "payment-spare", peerRuntime)
	require.NoError(t, EnsurePaymentClusterReady())

	deleted, err := model.DeleteSystemInstance("payment-peer")
	require.NoError(t, err)
	require.True(t, deleted)
	require.NoError(t, unregisterPaymentClusterRedisLease("payment-peer"))
	assert.NoError(t, EnsurePaymentClusterReady())
}

func TestPaymentClusterReadinessRejectsQuorateSplitMembershipViews(t *testing.T) {
	enablePaymentMultiNodeModeForTest(
		t,
		"payment-local",
		"payment-common-peer",
		"payment-database-only-peer",
		"payment-redis-only-peer",
	)
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-split-view-test-key-0001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)

	require.NoError(t, ReportCurrentSystemInstance())
	peerRuntime := currentPaymentRuntimeInfo()
	upsertPaymentClusterDatabasePeerForTest(
		t,
		"payment-common-peer",
		"payment-common-peer",
		peerRuntime,
	)
	witnessPaymentClusterPeerForTest(t, "payment-common-peer")
	upsertPaymentClusterDatabasePeerForTest(
		t,
		"payment-database-only-peer",
		"payment-database-only-peer",
		peerRuntime,
	)
	witnessPaymentClusterPeerForTest(t, "payment-redis-only-peer")

	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterMembershipMismatch)
}

func TestPaymentClusterReadinessRejectsUnknownFutureRuntimeSchema(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-future-schema-key-000001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	peer := currentPaymentRuntimeInfo()
	peer.SchemaVersion = paymentRuntimeSchemaVersion + 1
	upsertWitnessedPaymentClusterPeerForTest(t, "future-payment-peer", "future-payment-peer", peer)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterConfigMismatch)
}

func TestPaymentClusterReadinessRejectsMismatchedNodeConfiguration(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-shared-test-key-000000001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)

	peer := currentPaymentRuntimeInfo()
	peer.ConfigurationFingerprint = "different"
	require.NoError(t, ReportCurrentSystemInstance())
	upsertWitnessedPaymentClusterPeerForTest(t, "payment-peer", "payment-peer", peer)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterConfigMismatch)
}

func TestPaymentClusterReadinessRejectsMismatchedCryptoSecret(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-crypto-test-key-000000001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)

	require.NoError(t, ReportCurrentSystemInstance())
	peer := currentPaymentRuntimeInfo()
	peer.CryptoSecretFingerprint = paymentRuntimeSecretFingerprint("different-crypto-secret")
	upsertWitnessedPaymentClusterPeerForTest(t, "payment-peer", "payment-peer", peer)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterKeyMismatch)
}

func TestPaymentClusterReadinessRejectsMismatchedPaymentKeyring(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-keyring-primary-key-000001")
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "payment-cluster-keyring-previous-key-0001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)

	require.NoError(t, ReportCurrentSystemInstance())
	peer := currentPaymentRuntimeInfo()
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "")
	peer.PaymentSecretKeyringFingerprint = model.PaymentSecretKeyringFingerprint()
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "payment-cluster-keyring-previous-key-0001")
	upsertWitnessedPaymentClusterPeerForTest(t, "payment-peer", "payment-peer", peer)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterKeyMismatch)
}

func TestPaymentClusterReadinessRequiresLiveLocalInventory(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-inventory-test-key-000001")
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterInventoryUnavailable)

	require.NoError(t, ReportCurrentSystemInstance())
	assert.NoError(t, EnsurePaymentClusterReady())
	identity, _, err := currentSystemInstanceIdentity()
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.SystemInstance{}).
		Where("node_name = ?", currentSystemInstanceInventoryKey(identity.Name)).
		Update("last_seen_at", common.GetTimestamp()-model.SystemInstanceStaleAfterSeconds-1).Error)
	assert.ErrorIs(t, EnsurePaymentClusterReady(), ErrPaymentClusterInventoryUnavailable)
}

func TestUnregisterCurrentSystemInstanceDeletesOnlyCurrentOwner(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-cluster-unregister-test-key-00001")
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	peerKey := "same-logical-name-other-owner"
	require.NoError(t, model.UpsertSystemInstance(peerKey, SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          common.NodeIdentity{Name: common.GetNodeIdentity().Name},
		Payment:       currentPaymentRuntimeInfo(),
	}, common.GetTimestamp(), common.GetTimestamp()))

	require.NoError(t, UnregisterCurrentSystemInstance())
	identity, _, err := currentSystemInstanceIdentity()
	require.NoError(t, err)
	var currentCount, peerCount int64
	require.NoError(t, model.DB.Model(&model.SystemInstance{}).
		Where("node_name = ?", currentSystemInstanceInventoryKey(identity.Name)).Count(&currentCount).Error)
	require.NoError(t, model.DB.Model(&model.SystemInstance{}).Where("node_name = ?", peerKey).Count(&peerCount).Error)
	assert.Zero(t, currentCount)
	assert.EqualValues(t, 1, peerCount)
}
