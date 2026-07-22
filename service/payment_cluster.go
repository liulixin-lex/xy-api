package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/go-redis/redis/v8"
)

const paymentRuntimeSchemaVersion = 3

var (
	ErrPaymentClusterSQLiteUnsupported    = errors.New("payment multi-node mode requires a shared MySQL or PostgreSQL database")
	ErrPaymentClusterRedisRequired        = errors.New("payment multi-node mode requires shared Redis")
	ErrPaymentClusterConfigMismatch       = errors.New("payment configuration is inconsistent across application nodes")
	ErrPaymentClusterKeyMismatch          = errors.New("payment or session keys are inconsistent across application nodes")
	ErrPaymentClusterNodeConflict         = errors.New("multiple application instances use the same node name")
	ErrPaymentClusterInventoryUnavailable = errors.New("payment application instance inventory is unavailable")
	ErrPaymentClusterModeRequired         = errors.New("multiple payment nodes require explicit multi-node mode")
	ErrPaymentClusterIdentityInvalid      = errors.New("payment cluster identity or expected membership is invalid")
	ErrPaymentClusterMembershipMismatch   = errors.New("live payment nodes do not match the expected cluster membership")
)

var paymentClusterIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,90}$`)

type paymentMultiNodeConfiguration struct {
	Enabled                       bool
	ClusterIdentityFingerprint    string
	ExpectedMembershipFingerprint string
	ExpectedNodes                 map[string]struct{}
	MinimumLiveNodes              int
}

// PaymentRuntimeInfo contains fingerprints only. It intentionally never
// serializes a credential, connection string, session secret, or provider
// payload into the shared instance inventory.
type PaymentRuntimeInfo struct {
	SchemaVersion                   int    `json:"schema_version"`
	ConfigurationVersion            int64  `json:"configuration_version"`
	ConfigurationFingerprint        string `json:"configuration_fingerprint"`
	PaymentSecretKeyID              string `json:"payment_secret_key_id"`
	PaymentSecretKeyringFingerprint string `json:"payment_secret_keyring_fingerprint"`
	SessionSecretFingerprint        string `json:"session_secret_fingerprint"`
	CryptoSecretFingerprint         string `json:"crypto_secret_fingerprint"`
	DatabaseType                    string `json:"database_type"`
	RedisEnabled                    bool   `json:"redis_enabled"`
	MultiNodeEnabled                bool   `json:"multi_node_enabled"`
	ClusterIdentityFingerprint      string `json:"cluster_identity_fingerprint,omitempty"`
	ExpectedMembershipFingerprint   string `json:"expected_membership_fingerprint,omitempty"`
	MinimumLiveNodes                int    `json:"minimum_live_nodes,omitempty"`
	Ready                           bool   `json:"ready"`
	ReadinessCode                   string `json:"readiness_code,omitempty"`
}

func currentPaymentRuntimeInfo() PaymentRuntimeInfo {
	multiNodeConfiguration, multiNodeErr := currentPaymentMultiNodeConfiguration()
	info := PaymentRuntimeInfo{
		SchemaVersion:                   paymentRuntimeSchemaVersion,
		PaymentSecretKeyID:              model.PaymentSecretPrimaryKeyID(),
		PaymentSecretKeyringFingerprint: model.PaymentSecretKeyringFingerprint(),
		SessionSecretFingerprint:        paymentRuntimeSecretFingerprint(common.SessionSecret),
		CryptoSecretFingerprint:         paymentRuntimeSecretFingerprint(common.CryptoSecret),
		DatabaseType:                    string(common.MainDatabaseType()),
		RedisEnabled:                    common.RedisEnabled && common.RDB != nil,
		Ready:                           true,
	}
	if multiNodeErr != nil {
		info.Ready = false
		info.ReadinessCode = "multi_node_configuration_invalid"
		return info
	}
	info.MultiNodeEnabled = multiNodeConfiguration.Enabled
	info.ClusterIdentityFingerprint = multiNodeConfiguration.ClusterIdentityFingerprint
	info.ExpectedMembershipFingerprint = multiNodeConfiguration.ExpectedMembershipFingerprint
	info.MinimumLiveNodes = multiNodeConfiguration.MinimumLiveNodes
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		info.Ready = false
		info.ReadinessCode = "configuration_sync_failed"
		return info
	}
	version, err := model.CurrentPaymentConfigurationVersion()
	if err != nil {
		info.Ready = false
		info.ReadinessCode = "configuration_version_invalid"
		return info
	}
	info.ConfigurationVersion = version
	info.ConfigurationFingerprint = paymentRuntimeConfigurationFingerprint()
	if info.PaymentSecretKeyID == "" {
		info.Ready = false
		info.ReadinessCode = "payment_secret_key_missing"
	} else if info.PaymentSecretKeyringFingerprint == "" {
		info.Ready = false
		info.ReadinessCode = "payment_secret_keyring_invalid"
	} else if !model.PaymentSecretStorageReady() {
		info.Ready = false
		info.ReadinessCode = "payment_secret_storage_unavailable"
	} else if info.SessionSecretFingerprint == "" {
		info.Ready = false
		info.ReadinessCode = "session_secret_missing"
	} else if info.CryptoSecretFingerprint == "" {
		info.Ready = false
		info.ReadinessCode = "crypto_secret_missing"
	}
	return info
}

// CurrentPaymentRuntimeInfo exposes the sanitized local payment runtime state
// to administrator diagnostics. It contains fingerprints only, never secrets.
func CurrentPaymentRuntimeInfo() PaymentRuntimeInfo {
	return currentPaymentRuntimeInfo()
}

func PaymentClusterReadinessCode(err error) string {
	switch {
	case err == nil:
		return "ready"
	case errors.Is(err, ErrPaymentClusterSQLiteUnsupported):
		return "shared_database_required"
	case errors.Is(err, ErrPaymentClusterRedisRequired):
		return "shared_redis_required"
	case errors.Is(err, ErrPaymentClusterKeyMismatch):
		return "key_mismatch"
	case errors.Is(err, ErrPaymentClusterNodeConflict):
		return "node_identity_conflict"
	case errors.Is(err, ErrPaymentClusterInventoryUnavailable):
		return "inventory_unavailable"
	case errors.Is(err, ErrPaymentClusterModeRequired):
		return "multi_node_mode_required"
	case errors.Is(err, ErrPaymentClusterIdentityInvalid):
		return "cluster_identity_invalid"
	case errors.Is(err, ErrPaymentClusterMembershipMismatch):
		return "membership_mismatch"
	case errors.Is(err, ErrPaymentClusterConfigMismatch):
		return "configuration_mismatch"
	default:
		return "inventory_unavailable"
	}
}

func paymentRuntimeConfigurationFingerprint() string {
	common.OptionMapRWMutex.RLock()
	keys := make([]string, 0, len(common.OptionMap))
	for key := range common.OptionMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	builder := strings.Builder{}
	for _, key := range keys {
		builder.WriteString(strconv.Itoa(len(key)))
		builder.WriteByte(':')
		builder.WriteString(key)
		value := common.OptionMap[key]
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	common.OptionMapRWMutex.RUnlock()
	// Stripe test-mode authorization is deliberately environment-only and must
	// therefore be included separately from the shared OptionMap.
	builder.WriteString("stripe_test_mode:")
	builder.WriteString(strconv.FormatBool(setting.StripeTestModeEnabled()))
	// A hash of the Redis target detects replicas accidentally pointed at
	// different Redis clusters without publishing the URL or its password.
	builder.WriteString("redis_target:")
	builder.WriteString(paymentRuntimeSecretFingerprint(os.Getenv("REDIS_CONN_STRING")))
	multiNodeConfiguration, err := currentPaymentMultiNodeConfiguration()
	builder.WriteString("payment_multi_node:")
	if err != nil {
		builder.WriteString("invalid:")
		builder.WriteString(paymentRuntimeIdentifierFingerprint(
			os.Getenv("PAYMENT_MULTI_NODE_ENABLED") + "\x00" +
				os.Getenv("PAYMENT_CLUSTER_ID") + "\x00" + os.Getenv("PAYMENT_CLUSTER_NODES") + "\x00" +
				os.Getenv("PAYMENT_CLUSTER_MIN_LIVE_NODES"),
		))
	} else {
		builder.WriteString(strconv.FormatBool(multiNodeConfiguration.Enabled))
		builder.WriteByte(':')
		builder.WriteString(multiNodeConfiguration.ClusterIdentityFingerprint)
		builder.WriteByte(':')
		builder.WriteString(multiNodeConfiguration.ExpectedMembershipFingerprint)
		builder.WriteByte(':')
		builder.WriteString(strconv.Itoa(multiNodeConfiguration.MinimumLiveNodes))
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", digest[:16])
}

func currentPaymentMultiNodeConfiguration() (paymentMultiNodeConfiguration, error) {
	configuration := paymentMultiNodeConfiguration{}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PAYMENT_MULTI_NODE_ENABLED"))) {
	case "", "0", "false":
		return configuration, nil
	case "1", "true":
		configuration.Enabled = true
	default:
		return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
	}

	clusterID := strings.TrimSpace(os.Getenv("PAYMENT_CLUSTER_ID"))
	if !paymentClusterIdentityPattern.MatchString(clusterID) {
		return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
	}
	rawNodes := strings.Split(os.Getenv("PAYMENT_CLUSTER_NODES"), ",")
	if len(rawNodes) < 2 || len(rawNodes) > 64 {
		return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
	}
	nodes := make([]string, 0, len(rawNodes))
	configuration.ExpectedNodes = make(map[string]struct{}, len(rawNodes))
	for _, rawNode := range rawNodes {
		node := strings.TrimSpace(rawNode)
		if !paymentClusterIdentityPattern.MatchString(node) {
			return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
		}
		if _, exists := configuration.ExpectedNodes[node]; exists {
			return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
		}
		configuration.ExpectedNodes[node] = struct{}{}
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	minimumLiveNodes := len(nodes)/2 + 1
	if rawMinimum := strings.TrimSpace(os.Getenv("PAYMENT_CLUSTER_MIN_LIVE_NODES")); rawMinimum != "" {
		parsedMinimum, err := strconv.Atoi(rawMinimum)
		if err != nil {
			return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
		}
		minimumLiveNodes = parsedMinimum
	}
	if minimumLiveNodes <= len(nodes)/2 || minimumLiveNodes > len(nodes) {
		return paymentMultiNodeConfiguration{}, ErrPaymentClusterIdentityInvalid
	}
	configuration.ClusterIdentityFingerprint = paymentRuntimeIdentifierFingerprint(clusterID)
	configuration.ExpectedMembershipFingerprint = paymentRuntimeIdentifierFingerprint(strings.Join(nodes, "\x00"))
	configuration.MinimumLiveNodes = minimumLiveNodes
	return configuration, nil
}

func paymentRuntimeIdentifierFingerprint(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:16])
}

func paymentRuntimeSecretFingerprint(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:8])
}

func paymentClusterRedisMembershipKey(configuration paymentMultiNodeConfiguration) string {
	return "payment:cluster:" + configuration.ClusterIdentityFingerprint + ":instances"
}

func reportPaymentClusterRedisLease(inventoryKey string) error {
	configuration, err := currentPaymentMultiNodeConfiguration()
	if err != nil {
		return err
	}
	if !configuration.Enabled {
		return nil
	}
	if !common.RedisEnabled || common.RDB == nil {
		return ErrPaymentClusterRedisRequired
	}
	inventoryKey = strings.TrimSpace(inventoryKey)
	if inventoryKey == "" {
		return ErrPaymentClusterInventoryUnavailable
	}
	now := common.GetTimestamp()
	key := paymentClusterRedisMembershipKey(configuration)
	pipeline := common.RDB.TxPipeline()
	pipeline.ZRemRangeByScore(context.Background(), key, "-inf", strconv.FormatInt(now-1, 10))
	pipeline.ZAdd(context.Background(), key, &redis.Z{
		Score:  float64(now + model.SystemInstanceStaleAfterSeconds),
		Member: inventoryKey,
	})
	pipeline.Expire(context.Background(), key, time.Duration(model.SystemInstanceStaleAfterSeconds*3)*time.Second)
	_, err = pipeline.Exec(context.Background())
	return err
}

func unregisterPaymentClusterRedisLease(inventoryKey string) error {
	configuration, err := currentPaymentMultiNodeConfiguration()
	if err != nil || !configuration.Enabled {
		return err
	}
	if !common.RedisEnabled || common.RDB == nil {
		return ErrPaymentClusterRedisRequired
	}
	return common.RDB.ZRem(
		context.Background(), paymentClusterRedisMembershipKey(configuration), strings.TrimSpace(inventoryKey),
	).Err()
}

func livePaymentClusterRedisInventory(configuration paymentMultiNodeConfiguration, now int64) (map[string]struct{}, error) {
	if !configuration.Enabled || !common.RedisEnabled || common.RDB == nil {
		return nil, ErrPaymentClusterRedisRequired
	}
	key := paymentClusterRedisMembershipKey(configuration)
	pipeline := common.RDB.TxPipeline()
	pipeline.ZRemRangeByScore(context.Background(), key, "-inf", strconv.FormatInt(now-1, 10))
	membersCommand := pipeline.ZRangeByScore(context.Background(), key, &redis.ZRangeBy{
		Min: strconv.FormatInt(now, 10), Max: "+inf",
	})
	if _, err := pipeline.Exec(context.Background()); err != nil {
		return nil, err
	}
	members, err := membersCommand.Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(members))
	for _, member := range members {
		member = strings.TrimSpace(member)
		if member == "" {
			return nil, ErrPaymentClusterMembershipMismatch
		}
		result[member] = struct{}{}
	}
	return result, nil
}

// EnsurePaymentClusterReady is called before accepting new browser-driven
// payment work and before a worker creates an upstream order. Single-node
// SQLite/Redis-off deployments remain supported. Once another live node is
// observed in the shared database, payment creation is fail-closed unless the
// database, Redis, configuration, encryption key, and session key agree.
func EnsurePaymentClusterReady() error {
	multiNodeConfiguration, multiNodeErr := currentPaymentMultiNodeConfiguration()
	if multiNodeErr != nil {
		return ErrPaymentClusterIdentityInvalid
	}
	local := currentPaymentRuntimeInfo()
	if !local.Ready {
		if local.ReadinessCode == "payment_secret_key_missing" || local.ReadinessCode == "payment_secret_keyring_invalid" ||
			local.ReadinessCode == "payment_secret_storage_unavailable" || local.ReadinessCode == "session_secret_missing" ||
			local.ReadinessCode == "crypto_secret_missing" {
			return ErrPaymentClusterKeyMismatch
		}
		if local.ReadinessCode == "multi_node_configuration_invalid" {
			return ErrPaymentClusterIdentityInvalid
		}
		return ErrPaymentClusterConfigMismatch
	}
	if model.DB == nil || !model.DB.Migrator().HasTable(&model.SystemInstance{}) {
		return ErrPaymentClusterInventoryUnavailable
	}
	now := common.GetTimestamp()
	var instances []model.SystemInstance
	if err := model.DB.Where("last_seen_at >= ?", now-model.SystemInstanceStaleAfterSeconds).
		Order("node_name ASC").Find(&instances).Error; err != nil {
		return err
	}
	identity, _, err := currentSystemInstanceIdentity()
	if err != nil {
		return ErrPaymentClusterInventoryUnavailable
	}
	localNode := strings.TrimSpace(identity.Name)
	if multiNodeConfiguration.Enabled {
		if !identity.ManuallyConfigured {
			return ErrPaymentClusterIdentityInvalid
		}
		if _, expected := multiNodeConfiguration.ExpectedNodes[localNode]; !expected {
			return ErrPaymentClusterIdentityInvalid
		}
	}
	localInventoryKey := currentSystemInstanceInventoryKey(localNode)
	peers := make([]PaymentRuntimeInfo, 0, len(instances))
	liveNodes := make(map[string]int, len(instances))
	localRegistered := false
	for i := range instances {
		var info SystemInstanceInfo
		if err := common.UnmarshalJsonStr(instances[i].Info, &info); err != nil {
			return ErrPaymentClusterConfigMismatch
		}
		logicalNode := strings.TrimSpace(info.Node.Name)
		if logicalNode == "" {
			return ErrPaymentClusterConfigMismatch
		}
		liveNodes[logicalNode]++
		if liveNodes[logicalNode] > 1 {
			return ErrPaymentClusterNodeConflict
		}
		if info.Payment.SchemaVersion != paymentRuntimeSchemaVersion {
			return ErrPaymentClusterConfigMismatch
		}
		if strings.TrimSpace(instances[i].NodeName) == localInventoryKey {
			if logicalNode != localNode {
				return ErrPaymentClusterConfigMismatch
			}
			localRegistered = true
			continue
		}
		peers = append(peers, info.Payment)
	}
	if !localRegistered {
		return ErrPaymentClusterInventoryUnavailable
	}
	if multiNodeConfiguration.Enabled {
		if !local.RedisEnabled {
			return ErrPaymentClusterRedisRequired
		}
		redisInventory, redisErr := livePaymentClusterRedisInventory(multiNodeConfiguration, now)
		if redisErr != nil {
			return ErrPaymentClusterInventoryUnavailable
		}
		if len(redisInventory) != len(instances) {
			return ErrPaymentClusterMembershipMismatch
		}
		for i := range instances {
			if _, witnessed := redisInventory[strings.TrimSpace(instances[i].NodeName)]; !witnessed {
				return ErrPaymentClusterMembershipMismatch
			}
		}
	}
	if len(peers) == 0 {
		if multiNodeConfiguration.Enabled {
			if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
				return ErrPaymentClusterSQLiteUnsupported
			}
			if !local.RedisEnabled {
				return ErrPaymentClusterRedisRequired
			}
			return ErrPaymentClusterMembershipMismatch
		}
		return nil
	}
	if !multiNodeConfiguration.Enabled {
		return ErrPaymentClusterModeRequired
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return ErrPaymentClusterSQLiteUnsupported
	}
	if !local.RedisEnabled {
		return ErrPaymentClusterRedisRequired
	}
	if len(liveNodes) < multiNodeConfiguration.MinimumLiveNodes {
		return ErrPaymentClusterMembershipMismatch
	}
	for liveNode := range liveNodes {
		if _, expected := multiNodeConfiguration.ExpectedNodes[liveNode]; !expected {
			return ErrPaymentClusterMembershipMismatch
		}
	}
	for _, peer := range peers {
		if !peer.Ready || !peer.RedisEnabled || !peer.MultiNodeEnabled ||
			peer.ClusterIdentityFingerprint != local.ClusterIdentityFingerprint ||
			peer.ExpectedMembershipFingerprint != local.ExpectedMembershipFingerprint ||
			peer.MinimumLiveNodes != local.MinimumLiveNodes ||
			peer.DatabaseType != local.DatabaseType ||
			peer.ConfigurationVersion != local.ConfigurationVersion ||
			peer.ConfigurationFingerprint != local.ConfigurationFingerprint {
			return ErrPaymentClusterConfigMismatch
		}
		if peer.PaymentSecretKeyID != local.PaymentSecretKeyID ||
			peer.PaymentSecretKeyringFingerprint != local.PaymentSecretKeyringFingerprint ||
			peer.SessionSecretFingerprint != local.SessionSecretFingerprint ||
			peer.CryptoSecretFingerprint != local.CryptoSecretFingerprint {
			return ErrPaymentClusterKeyMismatch
		}
	}
	return nil
}
