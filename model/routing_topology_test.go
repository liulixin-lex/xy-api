package model

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileLegacyRoutingTopologyPreservesStableIdentity(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-topology-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	priority := int64(9)
	weight := uint(17)
	require.NoError(t, DB.Create(&[]Channel{
		{
			Id:       101,
			Name:     "shared-channel",
			Key:      "key-a\nkey-b\nkey-a",
			Group:    " gpt-plus, gpt-pro, gpt-plus ",
			Priority: &priority,
			Weight:   &weight,
			ChannelInfo: ChannelInfo{
				IsMultiKey: true,
			},
		},
		{
			Id:    102,
			Name:  "single-channel",
			Key:   "single-key",
			Group: "gpt-plus",
		},
	}).Error)

	first, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, first.ActivePools)
	assert.Equal(t, 3, first.ActiveMembers)
	assert.Equal(t, 3, first.ActiveCredentials)

	pools := loadRoutingPoolsForTest(t)
	plusPool := pools["gpt-plus"]
	proPool := pools["gpt-pro"]
	require.NotZero(t, plusPool.ID)
	require.NotZero(t, proPool.ID)

	members := loadRoutingMembersForTest(t)
	plusMemberID := members[routingMemberTestKey{poolID: plusPool.ID, channelID: 101}].ID
	proMemberID := members[routingMemberTestKey{poolID: proPool.ID, channelID: 101}].ID
	require.NotZero(t, plusMemberID)
	require.NotZero(t, proMemberID)
	assert.NotEqual(t, plusMemberID, proMemberID)

	credentials := loadRoutingCredentialsForTest(t, 101)
	keyAID := credentials["key-a"].ID
	keyBID := credentials["key-b"].ID
	require.NotZero(t, keyAID)
	require.NotZero(t, keyBID)
	assert.Equal(t, 2, credentials["key-a"].CurrentOccurrences)
	assert.Equal(t, 0, credentials["key-a"].LastSeenIndex)
	require.NoError(t, DB.Model(&RoutingCredentialRef{}).Where("id = ?", keyAID).Update("fingerprint_version", 0).Error)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	credentials = loadRoutingCredentialsForTest(t, 101)
	assert.Equal(t, RoutingCredentialFingerprintVersion, credentials["key-a"].FingerprintVersion)

	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 101).Updates(map[string]any{
		"group": "gpt-pro,gpt-enterprise",
		"key":   "key-b\nkey-a",
	}).Error)
	second, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, second.ActivePools)
	assert.Equal(t, 3, second.ActiveMembers)
	assert.Equal(t, 3, second.ActiveCredentials)

	pools = loadRoutingPoolsForTest(t)
	members = loadRoutingMembersForTest(t)
	assert.False(t, members[routingMemberTestKey{poolID: plusPool.ID, channelID: 101}].Active)
	assert.Equal(t, proMemberID, members[routingMemberTestKey{poolID: proPool.ID, channelID: 101}].ID)
	enterpriseMember := members[routingMemberTestKey{poolID: pools["gpt-enterprise"].ID, channelID: 101}]
	assert.True(t, enterpriseMember.Active)

	credentials = loadRoutingCredentialsForTest(t, 101)
	assert.Equal(t, keyAID, credentials["key-a"].ID)
	assert.Equal(t, keyBID, credentials["key-b"].ID)
	assert.Equal(t, 1, credentials["key-a"].LastSeenIndex)
	assert.Equal(t, 0, credentials["key-b"].LastSeenIndex)

	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 101).Update("key", "key-b\nkey-c").Error)
	third, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, third.ActiveCredentials)

	credentials = loadRoutingCredentialsForTest(t, 101)
	assert.False(t, credentials["key-a"].Active)
	assert.NotZero(t, credentials["key-a"].RetiredTime)
	assert.Equal(t, keyBID, credentials["key-b"].ID)
	keyCID := credentials["key-c"].ID
	require.NotZero(t, keyCID)
	assert.NotEqual(t, keyAID, keyCID)

	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 101).Update("key", "key-a\nkey-b\nkey-c").Error)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	credentials = loadRoutingCredentialsForTest(t, 101)
	assert.True(t, credentials["key-a"].Active)
	assert.Zero(t, credentials["key-a"].RetiredTime)
	assert.Equal(t, keyAID, credentials["key-a"].ID)

	before := routingTopologyIdentitySnapshotForTest(t)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before, routingTopologyIdentitySnapshotForTest(t))

	encoded, err := common.Marshal(credentials["key-a"])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"fingerprint":`)
	assert.NotContains(t, string(encoded), credentials["key-a"].Fingerprint)
	assert.NotContains(t, string(encoded), "key-a")
}

func TestReconcileLegacyRoutingTopologyFailsClosedWithoutPersistentSecret(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))
	require.NoError(t, DB.Create(&Channel{Id: 201, Name: "channel", Key: "secret", Group: "default"}).Error)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "runtime-random-secret"
	t.Setenv("CRYPTO_SECRET", "")
	t.Setenv("SESSION_SECRET", "")
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	_, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.ErrorIs(t, err, ErrCredentialSecretUnstable)

	var poolCount int64
	require.NoError(t, DB.Model(&RoutingPool{}).Count(&poolCount).Error)
	assert.Zero(t, poolCount)
}

func TestReconcileLegacyRoutingTopologyRejectsDifferentPersistentSecret(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))
	require.NoError(t, DB.Create(&Channel{Id: 202, Name: "channel", Key: "serving-key", Group: "default"}).Error)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret-a"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	_, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var original RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ?", 202).First(&original).Error)

	common.CryptoSecret = "stable-routing-secret-b"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.ErrorIs(t, err, ErrCredentialSecretMismatch)
	var refs []RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ?", 202).Find(&refs).Error)
	require.Len(t, refs, 1)
	assert.Equal(t, original.ID, refs[0].ID)
	assert.True(t, refs[0].Active)
}

func TestReconcileLegacyRoutingTopologyIsolatesRecreatedChannelCredentials(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-generation-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	original := Channel{Id: 205, Name: "original", Key: "same-key", Group: "default"}
	require.NoError(t, DB.Create(&original).Error)
	_, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var originalCredential RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ? AND active = ?", original.Id, true).First(&originalCredential).Error)
	require.Equal(t, original.RoutingGeneration, originalCredential.ChannelGeneration)

	require.NoError(t, DB.Delete(&Channel{}, original.Id).Error)
	replacement := Channel{Id: original.Id, Name: "replacement", Key: original.Key, Group: original.Group}
	require.NoError(t, DB.Create(&replacement).Error)
	require.NotEqual(t, original.RoutingGeneration, replacement.RoutingGeneration)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	var credentials []RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ?", original.Id).Order("id asc").Find(&credentials).Error)
	require.Len(t, credentials, 2)
	assert.Equal(t, originalCredential.ID, credentials[0].ID)
	assert.False(t, credentials[0].Active)
	assert.Equal(t, original.RoutingGeneration, credentials[0].ChannelGeneration)
	assert.True(t, credentials[1].Active)
	assert.NotEqual(t, originalCredential.ID, credentials[1].ID)
	assert.Equal(t, replacement.RoutingGeneration, credentials[1].ChannelGeneration)
	assert.NotEqual(t, credentials[0].Fingerprint, credentials[1].Fingerprint)
}

func TestReconcileLegacyRoutingTopologyKeepsCaseDistinctGroups(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-case-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	require.NoError(t, DB.Create(&Channel{Id: 203, Name: "channel", Key: "key", Group: "VIP,vip"}).Error)

	_, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var pools []RoutingPool
	require.NoError(t, DB.Order("group_name asc").Find(&pools).Error)
	require.Len(t, pools, 2)
	assert.NotEqual(t, pools[0].GroupKey, pools[1].GroupKey)
	assert.ElementsMatch(t, []string{"VIP", "vip"}, []string{pools[0].GroupName, pools[1].GroupName})
}

func TestReconcileLegacyRoutingTopologyFailsBeforeWritesOnCredentialByteLimit(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(
		&Channel{},
		&RoutingTopologyMetadata{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
	))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-limit-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	require.NoError(t, DB.Create(&Channel{
		Id: 204, Name: "oversized", Key: strings.Repeat("k", routingTopologyMaxKeyBytesPerChannel+1), Group: "default",
	}).Error)

	_, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.ErrorIs(t, err, ErrRoutingTopologyLimitExceeded)
	var poolCount int64
	require.NoError(t, DB.Model(&RoutingPool{}).Count(&poolCount).Error)
	assert.Zero(t, poolCount)
	var metadataCount int64
	require.NoError(t, DB.Model(&RoutingTopologyMetadata{}).Count(&metadataCount).Error)
	assert.Zero(t, metadataCount)
}

type routingMemberTestKey struct {
	poolID    int
	channelID int
}

func loadRoutingPoolsForTest(t *testing.T) map[string]RoutingPool {
	t.Helper()
	var pools []RoutingPool
	require.NoError(t, DB.Order("id asc").Find(&pools).Error)
	result := make(map[string]RoutingPool, len(pools))
	for _, pool := range pools {
		result[pool.GroupName] = pool
	}
	return result
}

func loadRoutingMembersForTest(t *testing.T) map[routingMemberTestKey]RoutingPoolMember {
	t.Helper()
	var members []RoutingPoolMember
	require.NoError(t, DB.Order("id asc").Find(&members).Error)
	result := make(map[routingMemberTestKey]RoutingPoolMember, len(members))
	for _, member := range members {
		result[routingMemberTestKey{poolID: member.PoolID, channelID: member.ChannelID}] = member
	}
	return result
}

func loadRoutingCredentialsForTest(t *testing.T, channelID int) map[string]RoutingCredentialRef {
	t.Helper()
	var channel Channel
	require.NoError(t, DB.Select("id", "routing_generation").Where("id = ?", channelID).First(&channel).Error)
	var refs []RoutingCredentialRef
	require.NoError(t, DB.Where("channel_id = ?", channelID).Order("id asc").Find(&refs).Error)
	result := make(map[string]RoutingCredentialRef, len(refs))
	for _, key := range []string{"key-a", "key-b", "key-c"} {
		fingerprint, err := RoutingCredentialFingerprint(channelID, channel.RoutingGeneration, key)
		require.NoError(t, err)
		for _, ref := range refs {
			if ref.Fingerprint == fingerprint {
				result[key] = ref
				break
			}
		}
	}
	return result
}

func routingTopologyIdentitySnapshotForTest(t *testing.T) map[string][]int {
	t.Helper()
	var pools []RoutingPool
	var members []RoutingPoolMember
	var refs []RoutingCredentialRef
	require.NoError(t, DB.Order("id asc").Find(&pools).Error)
	require.NoError(t, DB.Order("id asc").Find(&members).Error)
	require.NoError(t, DB.Order("id asc").Find(&refs).Error)
	result := map[string][]int{
		"pools":       make([]int, 0, len(pools)),
		"members":     make([]int, 0, len(members)),
		"credentials": make([]int, 0, len(refs)),
	}
	for _, pool := range pools {
		result["pools"] = append(result["pools"], pool.ID)
	}
	for _, member := range members {
		result["members"] = append(result["members"], member.ID)
	}
	for _, ref := range refs {
		result["credentials"] = append(result["credentials"], ref.ID)
	}
	return result
}
