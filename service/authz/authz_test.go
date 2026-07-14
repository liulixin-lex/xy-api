package authz

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
	})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&authzUserForTest{}, &model.CasbinRule{}, &model.AuthzRole{}))
	return db
}

type authzUserForTest struct {
	Id        int `gorm:"primaryKey"`
	Role      int
	Status    int
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (authzUserForTest) TableName() string {
	return "users"
}

func TestInitSeedsBuiltInRolesAndPoliciesOnce(t *testing.T) {
	db := newAuthzTestDB(t)

	require.NoError(t, Init(db))
	require.NoError(t, Init(db))

	// root is a superuser role and is granted everything implicitly, so only the
	// admin baseline is written as explicit policy rows.
	var count int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Count(&count).Error)
	assert.Equal(t, int64(len(PermissionsForRole(BuiltInRoleAdmin))), count)

	var roles []model.AuthzRole
	require.NoError(t, db.Order("sort asc").Find(&roles).Error)
	require.Len(t, roles, 2)
	assert.Equal(t, BuiltInRoleRoot, roles[0].Key)
	assert.Equal(t, BuiltInRoleAdmin, roles[1].Key)

	assert.True(t, Can(1, common.RoleRootUser, ChannelSensitiveWrite))
	assert.True(t, Can(2, common.RoleAdminUser, ChannelRead))
	assert.True(t, Can(2, common.RoleAdminUser, ChannelOperate))
	assert.True(t, Can(2, common.RoleAdminUser, ChannelWrite))
	assert.False(t, Can(2, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(3, common.RoleCommonUser, ChannelRead))
}

func TestInitOnSlaveOnlyLoadsPolicies(t *testing.T) {
	wasMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
	})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))

	require.NoError(t, Init(db))

	var roleCount int64
	require.NoError(t, db.Model(&model.AuthzRole{}).Count(&roleCount).Error)
	assert.Equal(t, int64(0), roleCount)
	var policyCount int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Count(&policyCount).Error)
	assert.Equal(t, int64(0), policyCount)
	assert.False(t, Can(2, common.RoleAdminUser, ChannelRead))
}

func TestSetUserPermissionsStoresOnlyOverrides(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	require.NoError(t, SetUserPermissions(42, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          false,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
			"unknown":            true,
		},
		"unknown": {
			ActionRead: true,
		},
	}))

	assert.True(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(42, common.RoleAdminUser, ChannelWrite))
	assert.Equal(t, PermissionsMap{
		ResourceBillingProjectionOps: {
			ActionBillingProjectionRead: true, ActionBillingProjectionRequeue: false,
			ActionBillingProjectionResolve: false,
		},
		ResourceBillingReview: {
			ActionBillingReviewRead: true, ActionBillingReviewResolve: false,
		},
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          false,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
		},
		ResourceChannelRouting: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionDeploy:         false,
			ActionSensitiveWrite: false,
			ActionAuditExport:    false,
		},
		ResourceSystemSetting: {
			ActionManage: false,
		},
	}, ExplicitUserPermissions(42))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionSensitiveWrite: true,
			ActionWrite:          false,
		},
	}, ExplicitUserOverrides(42))

	var userPolicyCount int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Where("v0 = ?", UserSubject(42)).Count(&userPolicyCount).Error)
	assert.Equal(t, int64(2), userPolicyCount)

	require.NoError(t, SetUserPermissions(42, PermissionsMap{ResourceChannel: {
		ActionRead:           true,
		ActionOperate:        true,
		ActionWrite:          true,
		ActionSensitiveWrite: false,
		ActionSecretView:     false,
	}}))
	assert.False(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.Equal(t, PermissionsMap{
		ResourceBillingProjectionOps: {
			ActionBillingProjectionRead: true, ActionBillingProjectionRequeue: false,
			ActionBillingProjectionResolve: false,
		},
		ResourceBillingReview: {
			ActionBillingReviewRead: true, ActionBillingReviewResolve: false,
		},
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionSensitiveWrite: false,
			ActionSecretView:     false,
		},
		ResourceChannelRouting: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionDeploy:         false,
			ActionSensitiveWrite: false,
			ActionAuditExport:    false,
		},
		ResourceSystemSetting: {
			ActionManage: false,
		},
	}, ExplicitUserPermissions(42))
	assert.Empty(t, ExplicitUserOverrides(42))
}

func TestClearUserAuthorizationRemovesOverrides(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	require.NoError(t, SetUserPermissions(90, PermissionsMap{ResourceChannel: {
		ActionWrite:          false,
		ActionSensitiveWrite: true,
	}}))

	assert.True(t, Can(90, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(90, common.RoleAdminUser, ChannelWrite))

	require.NoError(t, ClearUserAuthorization(90))

	assert.Empty(t, ExplicitUserOverrides(90))
	assert.True(t, Can(90, common.RoleAdminUser, ChannelRead))
	assert.True(t, Can(90, common.RoleAdminUser, ChannelWrite))
	assert.False(t, Can(90, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(90, common.RoleCommonUser, ChannelRead))
}

func TestSetUserPermissionsInTxDoesNotMutateEnforcerBeforeReload(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return SetUserPermissionsInTx(tx, 42, PermissionsMap{ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
		}})
	}))

	assert.False(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	require.NoError(t, ReloadPolicy())
	assert.True(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
}

func TestSetUserPermissionsInTxRollbackLeavesNoPolicy(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, SetUserPermissionsInTx(tx, 43, PermissionsMap{ResourceChannel: {
		ActionSensitiveWrite: true,
	}}))
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, ReloadPolicy())

	assert.False(t, Can(43, common.RoleAdminUser, ChannelSensitiveWrite))
	var count int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Where("v0 = ?", UserSubject(43)).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestAdapterAddPolicyIsIdempotent(t *testing.T) {
	db := newAuthzTestDB(t)
	adapter := newGormAdapter(db)
	rule := []string{UserSubject(55), ResourceChannel, ActionSensitiveWrite, EffectAllow}

	require.NoError(t, adapter.AddPolicy("p", "p", rule))
	require.NoError(t, adapter.AddPolicy("p", "p", rule))

	var count int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ? AND v3 = ?",
		"p",
		UserSubject(55),
		ResourceChannel,
		ActionSensitiveWrite,
		EffectAllow,
	).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestCapabilitiesUseCatalogShape(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	capabilities := Capabilities(7, common.RoleAdminUser)

	assert.True(t, capabilities[ResourceChannel][ActionRead])
	assert.True(t, capabilities[ResourceChannel][ActionOperate])
	assert.True(t, capabilities[ResourceChannel][ActionWrite])
	assert.False(t, capabilities[ResourceChannel][ActionSensitiveWrite])
	assert.False(t, capabilities[ResourceChannel][ActionSecretView])
	assert.True(t, capabilities[ResourceChannelRouting][ActionRead])
	assert.True(t, capabilities[ResourceChannelRouting][ActionOperate])
	assert.True(t, capabilities[ResourceChannelRouting][ActionWrite])
	assert.False(t, capabilities[ResourceChannelRouting][ActionDeploy])
	assert.False(t, capabilities[ResourceChannelRouting][ActionSensitiveWrite])
	assert.False(t, capabilities[ResourceChannelRouting][ActionAuditExport])
	assert.False(t, capabilities[ResourceSystemSetting][ActionManage])
	assert.True(t, capabilities[ResourceBillingProjectionOps][ActionBillingProjectionRead])
	assert.False(t, capabilities[ResourceBillingProjectionOps][ActionBillingProjectionRequeue])
	assert.False(t, capabilities[ResourceBillingProjectionOps][ActionBillingProjectionResolve])
}

func TestSystemSettingPermissionRequiresExplicitAdminGrant(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	assert.True(t, Can(1, common.RoleRootUser, SystemSettingManage))
	assert.False(t, Can(2, common.RoleAdminUser, SystemSettingManage))

	require.NoError(t, SetUserPermissions(2, PermissionsMap{
		ResourceSystemSetting: {
			ActionManage: true,
		},
	}))

	assert.True(t, Can(2, common.RoleAdminUser, SystemSettingManage))
}

func TestBillingReviewReadDefaultsToAdminButResolveRequiresFreshExplicitGrant(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	require.NoError(t, db.Create(&[]authzUserForTest{
		{Id: 1, Role: common.RoleRootUser, Status: common.UserStatusEnabled},
		{Id: 42, Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
	}).Error)

	assert.True(t, Can(1, common.RoleRootUser, BillingReviewRead))
	assert.True(t, Can(1, common.RoleRootUser, BillingReviewResolve))
	assert.True(t, Can(42, common.RoleAdminUser, BillingReviewRead))
	assert.False(t, Can(42, common.RoleAdminUser, BillingReviewResolve))

	require.NoError(t, SetUserPermissions(42, PermissionsMap{
		ResourceBillingReview: {ActionBillingReviewResolve: true},
	}))
	allowed, err := CanCurrent(context.Background(), 42, common.RoleAdminUser, BillingReviewResolve)
	require.NoError(t, err)
	assert.True(t, allowed)

	require.NoError(t, db.Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?",
		"p", UserSubject(42), ResourceBillingReview, ActionBillingReviewResolve,
	).Delete(&model.CasbinRule{}).Error)
	assert.True(t, Can(42, common.RoleAdminUser, BillingReviewResolve), "local policy remains stale until reload")
	allowed, err = CanCurrent(context.Background(), 42, common.RoleAdminUser, BillingReviewResolve)
	require.NoError(t, err)
	assert.False(t, allowed, "resolve revocation must take effect immediately")
}

func TestBillingProjectionReadDefaultsToAdminButMutationsRequireFreshExplicitGrants(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	require.NoError(t, db.Create(&[]authzUserForTest{
		{Id: 1, Role: common.RoleRootUser, Status: common.UserStatusEnabled},
		{Id: 42, Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
	}).Error)

	assert.True(t, Can(1, common.RoleRootUser, BillingProjectionRead))
	assert.True(t, Can(1, common.RoleRootUser, BillingProjectionRequeue))
	assert.True(t, Can(1, common.RoleRootUser, BillingProjectionResolve))
	assert.True(t, Can(42, common.RoleAdminUser, BillingProjectionRead))
	assert.False(t, Can(42, common.RoleAdminUser, BillingProjectionRequeue))
	assert.False(t, Can(42, common.RoleAdminUser, BillingProjectionResolve))

	require.NoError(t, SetUserPermissions(42, PermissionsMap{
		ResourceBillingProjectionOps: {
			ActionBillingProjectionRequeue: true,
			ActionBillingProjectionResolve: true,
		},
	}))
	for _, permission := range []Permission{BillingProjectionRequeue, BillingProjectionResolve} {
		allowed, err := CanCurrent(context.Background(), 42, common.RoleAdminUser, permission)
		require.NoError(t, err)
		assert.True(t, allowed)
	}

	require.NoError(t, db.Where(
		"ptype = ? AND v0 = ? AND v1 = ?", "p", UserSubject(42), ResourceBillingProjectionOps,
	).Delete(&model.CasbinRule{}).Error)
	assert.True(t, Can(42, common.RoleAdminUser, BillingProjectionRequeue), "local policy remains stale until reload")
	for _, permission := range []Permission{BillingProjectionRequeue, BillingProjectionResolve} {
		allowed, err := CanCurrent(context.Background(), 42, common.RoleAdminUser, permission)
		require.NoError(t, err)
		assert.False(t, allowed, "mutation revocation must take effect immediately")
	}
}

func TestCanCurrentRejectsARevokedGrantBeforePolicyReload(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	require.NoError(t, db.Create(&authzUserForTest{
		Id: 42, Role: common.RoleAdminUser, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, SetUserPermissions(42, PermissionsMap{
		ResourceChannelRouting: {
			ActionDeploy: true,
		},
	}))
	require.True(t, Can(42, common.RoleAdminUser, ChannelRoutingDeploy))

	require.NoError(t, db.Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?",
		"p", UserSubject(42), ResourceChannelRouting, ActionDeploy,
	).Delete(&model.CasbinRule{}).Error)
	assert.True(t, Can(42, common.RoleAdminUser, ChannelRoutingDeploy), "the local snapshot intentionally remains stale")

	allowed, err := CanCurrent(context.Background(), 42, common.RoleAdminUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestCanCurrentFailsClosedWhenPolicyDatabaseIsUnavailable(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	require.NoError(t, db.Create(&authzUserForTest{
		Id: 42, Role: common.RoleAdminUser, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, SetUserPermissions(42, PermissionsMap{
		ResourceChannelRouting: {
			ActionAuditExport: true,
		},
	}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	allowed, err := CanCurrent(context.Background(), 42, common.RoleAdminUser, ChannelRoutingAuditExport)
	require.Error(t, err)
	assert.False(t, allowed)

	allowed, err = CanCurrent(context.Background(), 1, common.RoleRootUser, ChannelRoutingAuditExport)
	require.Error(t, err)
	assert.False(t, allowed)
}

func TestCanCurrentUsesCurrentUserStatusAndRoleInsteadOfSessionRole(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	require.NoError(t, db.Create(&[]authzUserForTest{
		{Id: 1, Role: common.RoleRootUser, Status: common.UserStatusEnabled},
		{Id: 2, Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
		{Id: 3, Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
		{Id: 4, Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
	}).Error)
	require.NoError(t, SetUserPermissions(2, PermissionsMap{
		ResourceChannelRouting: {
			ActionDeploy: true,
		},
	}))
	require.NoError(t, SetUserPermissions(4, PermissionsMap{
		ResourceChannelRouting: {
			ActionDeploy: true,
		},
	}))

	allowed, err := CanCurrent(context.Background(), 1, common.RoleRootUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.True(t, allowed)
	require.NoError(t, db.Model(&authzUserForTest{}).Where("id = ?", 1).
		Update("status", common.UserStatusDisabled).Error)
	allowed, err = CanCurrent(context.Background(), 1, common.RoleRootUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.False(t, allowed, "a disabled root user must fail closed")

	allowed, err = CanCurrent(context.Background(), 2, common.RoleAdminUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.True(t, allowed)
	require.NoError(t, db.Model(&authzUserForTest{}).Where("id = ?", 2).
		Update("role", common.RoleCommonUser).Error)
	allowed, err = CanCurrent(context.Background(), 2, common.RoleAdminUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.False(t, allowed, "a demoted admin user must fail closed")

	allowed, err = CanCurrent(context.Background(), 3, common.RoleRootUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.False(t, allowed, "a stale root session must not override the database role")

	require.NoError(t, db.Delete(&authzUserForTest{}, 4).Error)
	allowed, err = CanCurrent(context.Background(), 4, common.RoleAdminUser, ChannelRoutingDeploy)
	require.NoError(t, err)
	assert.False(t, allowed, "a soft-deleted admin user must fail closed")
}

func TestRequiresFreshPolicyIsLimitedToHighRiskChannelRoutingActions(t *testing.T) {
	assert.True(t, RequiresFreshPolicy(ChannelRoutingDeploy))
	assert.True(t, RequiresFreshPolicy(ChannelRoutingSensitiveWrite))
	assert.True(t, RequiresFreshPolicy(ChannelRoutingAuditExport))
	assert.False(t, RequiresFreshPolicy(ChannelRoutingRead))
	assert.False(t, RequiresFreshPolicy(ChannelRoutingOperate))
	assert.False(t, RequiresFreshPolicy(ChannelRoutingWrite))
	assert.False(t, RequiresFreshPolicy(ChannelSensitiveWrite))
	assert.False(t, RequiresFreshPolicy(BillingProjectionRead))
	assert.True(t, RequiresFreshPolicy(BillingProjectionRequeue))
	assert.True(t, RequiresFreshPolicy(BillingProjectionResolve))
}
