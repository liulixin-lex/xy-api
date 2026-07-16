package model

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type routingPolicyApprovalLegacyIndex struct {
	ID             int64  `gorm:"primaryKey"`
	DraftID        int64  `gorm:"uniqueIndex:idx_routing_policy_approval_actor,priority:1"`
	DraftVersion   int64  `gorm:"uniqueIndex:idx_routing_policy_approval_actor,priority:2"`
	ActivationHash string `gorm:"type:char(64)"`
	ActorID        int    `gorm:"uniqueIndex:idx_routing_policy_approval_actor,priority:3"`
}

func (routingPolicyApprovalLegacyIndex) TableName() string {
	return (RoutingPolicyApproval{}).TableName()
}

type routingPolicyRollbackApprovalLegacyIndex struct {
	ID                   int64  `gorm:"primaryKey"`
	ExpectedRevision     int64  `gorm:"uniqueIndex:idx_routing_policy_rollback_approval_actor,priority:1"`
	ExpectedActivationID int64  `gorm:"uniqueIndex:idx_routing_policy_rollback_approval_actor,priority:2"`
	TargetRevision       int64  `gorm:"uniqueIndex:idx_routing_policy_rollback_approval_actor,priority:3"`
	ActivationHash       string `gorm:"type:char(64)"`
	ActorID              int    `gorm:"uniqueIndex:idx_routing_policy_rollback_approval_actor,priority:4"`
}

func (routingPolicyRollbackApprovalLegacyIndex) TableName() string {
	return (RoutingPolicyRollbackApproval{}).TableName()
}

func TestRoutingPolicyApprovalIntentIndexMigrationUpgradesLegacySQLite(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingPolicyApprovalIntentIndexMigrationContract(t, db)
}

func TestRoutingPolicyApprovalIntentIndexMigrationRepairsWrongNamedIndexes(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&routingPolicyApprovalLegacyIndex{}, &routingPolicyRollbackApprovalLegacyIndex{},
	))
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX "+routingPolicyApprovalIntentActorUniqueIndex+
			" ON routing_policy_approvals (draft_id, draft_version, actor_id)",
	).Error)
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX "+routingPolicyRollbackApprovalIntentActorUniqueIndex+
			" ON routing_policy_rollback_approvals (expected_revision, expected_activation_id, target_revision, actor_id)",
	).Error)

	require.NoError(t, MigrateRoutingPolicyApprovalIntentIndexes(db))
	ready, err := routingCriticalIndexReady(
		db, (RoutingPolicyApproval{}).TableName(), routingPolicyApprovalIntentActorUniqueIndex,
		[]string{"draft_id", "draft_version", "activation_hash", "actor_id"},
	)
	require.NoError(t, err)
	assert.True(t, ready)
	ready, err = routingCriticalIndexReady(
		db, (RoutingPolicyRollbackApproval{}).TableName(), routingPolicyRollbackApprovalIntentActorUniqueIndex,
		[]string{"expected_revision", "expected_activation_id", "target_revision", "activation_hash", "actor_id"},
	)
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestRoutingPolicyApprovalIntentIndexMigrationExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			runRoutingPolicyApprovalIntentIndexMigrationContract(t, db)
		})
	}
}

func runRoutingPolicyApprovalIntentIndexMigrationContract(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&routingPolicyApprovalLegacyIndex{}, &routingPolicyRollbackApprovalLegacyIndex{},
	))
	require.True(t, db.Migrator().HasIndex(&RoutingPolicyApproval{}, routingPolicyApprovalActorLegacyUniqueIndex))
	require.True(t, db.Migrator().HasIndex(
		&RoutingPolicyRollbackApproval{}, routingPolicyRollbackApprovalActorLegacyUniqueIndex,
	))

	// Production AutoMigrate creates the new intent indexes before the dedicated
	// migration removes the legacy, overly restrictive actor indexes.
	require.NoError(t, db.AutoMigrate(&RoutingPolicyApproval{}, &RoutingPolicyRollbackApproval{}))
	require.True(t, db.Migrator().HasIndex(&RoutingPolicyApproval{}, routingPolicyApprovalIntentActorUniqueIndex))
	require.True(t, db.Migrator().HasIndex(
		&RoutingPolicyRollbackApproval{}, routingPolicyRollbackApprovalIntentActorUniqueIndex,
	))
	require.NoError(t, MigrateRoutingPolicyApprovalIntentIndexes(db))
	require.NoError(t, MigrateRoutingPolicyApprovalIntentIndexes(db), "the migration must be retry-safe")

	assert.False(t, db.Migrator().HasIndex(&RoutingPolicyApproval{}, routingPolicyApprovalActorLegacyUniqueIndex))
	assert.False(t, db.Migrator().HasIndex(
		&RoutingPolicyRollbackApproval{}, routingPolicyRollbackApprovalActorLegacyUniqueIndex,
	))
	ready, err := routingCriticalIndexReady(
		db, (RoutingPolicyApproval{}).TableName(), routingPolicyApprovalIntentActorUniqueIndex,
		[]string{"draft_id", "draft_version", "activation_hash", "actor_id"},
	)
	require.NoError(t, err)
	assert.True(t, ready)
	ready, err = routingCriticalIndexReady(
		db, (RoutingPolicyRollbackApproval{}).TableName(), routingPolicyRollbackApprovalIntentActorUniqueIndex,
		[]string{"expected_revision", "expected_activation_id", "target_revision", "activation_hash", "actor_id"},
	)
	require.NoError(t, err)
	assert.True(t, ready)

	firstHash := strings.Repeat("a", 64)
	secondHash := strings.Repeat("b", 64)
	draftApproval := routingPolicyApprovalForIntentIndexTest(firstHash)
	require.NoError(t, db.Create(&draftApproval).Error)
	draftApproval.ID = 0
	draftApproval.ActivationHash = secondHash
	require.NoError(t, db.Create(&draftApproval).Error, "the same actor may approve a distinct immutable intent")
	draftApproval.ID = 0
	require.Error(t, db.Create(&draftApproval).Error, "an exact approval retry remains unique")

	rollbackApproval := routingPolicyRollbackApprovalForIntentIndexTest(firstHash)
	require.NoError(t, db.Create(&rollbackApproval).Error)
	rollbackApproval.ID = 0
	rollbackApproval.ActivationHash = secondHash
	require.NoError(t, db.Create(&rollbackApproval).Error, "the same actor may approve a distinct rollback intent")
	rollbackApproval.ID = 0
	require.Error(t, db.Create(&rollbackApproval).Error, "an exact rollback approval retry remains unique")
}

func routingPolicyApprovalForIntentIndexTest(activationHash string) RoutingPolicyApproval {
	return RoutingPolicyApproval{
		DraftID: 1, DraftVersion: 2, DraftETag: strings.Repeat("c", 64),
		DocumentHash: strings.Repeat("d", 64), HeadRevision: 1, HeadHash: strings.Repeat("e", 64),
		ActivationStage: RoutingDeploymentStageActive, ActivationReasonHash: strings.Repeat("f", 64),
		ActivationHash: activationHash, ActorID: 10, CreatedTimeMs: 1,
	}
}

func routingPolicyRollbackApprovalForIntentIndexTest(activationHash string) RoutingPolicyRollbackApproval {
	return RoutingPolicyRollbackApproval{
		ExpectedRevision: 3, ExpectedActivationID: 4, ExpectedHeadHash: strings.Repeat("c", 64),
		TargetRevision: 1, TargetContentHash: strings.Repeat("d", 64),
		ActivationStage: RoutingDeploymentStageActive, ActivationReasonHash: strings.Repeat("f", 64),
		ActivationHash: activationHash, ActorID: 10, CreatedTimeMs: 1,
	}
}
