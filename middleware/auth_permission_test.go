package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type permissionUserForTest struct {
	Id        int `gorm:"primaryKey"`
	Role      int
	Status    int
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (permissionUserForTest) TableName() string {
	return "users"
}

func TestRequirePermissionUsesFreshDatabasePolicyForHighRiskRoutingActions(t *testing.T) {
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = wasMaster })
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&permissionUserForTest{}, &model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, db.Create(&permissionUserForTest{
		Id: 42, Role: common.RoleAdminUser, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, authz.Init(db))
	require.NoError(t, authz.SetUserPermissions(42, authz.PermissionsMap{
		authz.ResourceChannelRouting: {
			authz.ActionDeploy: true,
		},
	}))
	require.True(t, authz.Can(42, common.RoleAdminUser, authz.ChannelRoutingDeploy))
	require.NoError(t, db.Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?",
		"p", authz.UserSubject(42), authz.ResourceChannelRouting, authz.ActionDeploy,
	).Delete(&model.CasbinRule{}).Error)
	require.True(t, authz.Can(42, common.RoleAdminUser, authz.ChannelRoutingDeploy))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("role", common.RoleAdminUser)
		c.Next()
	})
	deployCalled := false
	router.GET("/deploy", RequirePermission(authz.ChannelRoutingDeploy), func(c *gin.Context) {
		deployCalled = true
		c.Status(http.StatusNoContent)
	})
	readCalled := false
	router.GET("/read", RequirePermission(authz.ChannelRoutingRead), func(c *gin.Context) {
		readCalled = true
		c.Status(http.StatusNoContent)
	})

	deployRecorder := httptest.NewRecorder()
	router.ServeHTTP(deployRecorder, httptest.NewRequest(http.MethodGet, "/deploy", nil))
	assert.Equal(t, http.StatusForbidden, deployRecorder.Code)
	assert.False(t, deployCalled)

	readRecorder := httptest.NewRecorder()
	router.ServeHTTP(readRecorder, httptest.NewRequest(http.MethodGet, "/read", nil))
	assert.Equal(t, http.StatusNoContent, readRecorder.Code)
	assert.True(t, readCalled)
}
