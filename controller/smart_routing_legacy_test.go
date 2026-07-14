package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestListSmartRoutingBindingsLegacyLimit(t *testing.T) {
	tests := []struct {
		name   string
		dbType common.DatabaseType
		envKey string
		open   func(string) (*gorm.DB, error)
	}{
		{
			name:   "sqlite",
			dbType: common.DatabaseTypeSQLite,
			open: func(dsn string) (*gorm.DB, error) {
				return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
			},
		},
		{
			name:   "mysql",
			dbType: common.DatabaseTypeMySQL,
			envKey: "ROUTING_TEST_MYSQL_DSN",
			open: func(dsn string) (*gorm.DB, error) {
				return gorm.Open(mysql.Open(dsn), &gorm.Config{})
			},
		},
		{
			name:   "postgres",
			dbType: common.DatabaseTypePostgreSQL,
			envKey: "ROUTING_TEST_POSTGRES_DSN",
			open: func(dsn string) (*gorm.DB, error) {
				return gorm.Open(postgres.Open(dsn), &gorm.Config{})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := t.TempDir() + "/legacy-bindings.db"
			if test.envKey != "" {
				dsn = os.Getenv(test.envKey)
				if dsn == "" {
					t.Skipf("set %s to run the %s contract", test.envKey, test.name)
				}
			}
			db, err := test.open(dsn)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })

			previousDB := model.DB
			previousLogDB := model.LOG_DB
			previousMainType := common.MainDatabaseType()
			previousLogType := common.LogDatabaseType()
			model.DB = db
			model.LOG_DB = db
			common.SetDatabaseTypes(test.dbType, test.dbType)
			t.Cleanup(func() {
				model.DB = previousDB
				model.LOG_DB = previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
			})

			require.NoError(t, db.Migrator().DropTable(&model.RoutingChannelBinding{}))
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.RoutingChannelBinding{}))
			t.Cleanup(func() { _ = db.Migrator().DropTable(&model.RoutingChannelBinding{}) })

			bindings := make([]model.RoutingChannelBinding, legacySmartRoutingBindingLimit)
			for index := range bindings {
				bindings[index] = model.RoutingChannelBinding{
					ChannelID:     index + 1,
					UpstreamType:  model.RoutingUpstreamTypeNewAPI,
					BaseURL:       fmt.Sprintf("https://upstream-%d.example.com", index+1),
					UpstreamGroup: "default",
					Enabled:       true,
				}
			}
			require.NoError(t, db.CreateInBatches(&bindings, 100).Error)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/smart-routing/bindings", nil)
			ListSmartRoutingBindings(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			var successResponse struct {
				Success bool                 `json:"success"`
				Data    []routingBindingView `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &successResponse))
			require.True(t, successResponse.Success)
			require.Len(t, successResponse.Data, legacySmartRoutingBindingLimit)
			assert.Equal(t, 1, successResponse.Data[0].ChannelID)
			assert.Equal(t, legacySmartRoutingBindingLimit, successResponse.Data[len(successResponse.Data)-1].ChannelID)

			invalidCiphertext := "must-not-be-decrypted"
			require.NoError(t, db.Create(&model.RoutingChannelBinding{
				ChannelID:      legacySmartRoutingBindingLimit + 1,
				UpstreamType:   model.RoutingUpstreamTypeNewAPI,
				BaseURL:        "https://overflow.example.com",
				UpstreamGroup:  "default",
				EncCredentials: &invalidCiphertext,
				KeyVersion:     model.RoutingCredentialKeyVersion,
				Enabled:        true,
			}).Error)

			recorder = httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/smart-routing/bindings", nil)
			ListSmartRoutingBindings(ctx)

			require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
			var overflowResponse struct {
				Success   bool   `json:"success"`
				Code      string `json:"code"`
				Successor string `json:"successor"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &overflowResponse))
			assert.False(t, overflowResponse.Success)
			assert.Equal(t, "legacy_result_too_large", overflowResponse.Code)
			assert.Equal(t, "/api/channel-routing/v2/cost-bindings", overflowResponse.Successor)
			assert.NotContains(t, recorder.Body.String(), invalidCiphertext)
		})
	}
}
