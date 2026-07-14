package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupMidjourneyModelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/midjourney.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Midjourney{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB := DB
	previousType := common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		_ = sqlDB.Close()
	})
	return db
}

func TestMidjourneyIdentityLookupsFailClosedOnDuplicates(t *testing.T) {
	db := setupMidjourneyModelTestDB(t)
	require.NoError(t, db.Create(&[]Midjourney{
		{UserId: 1, MjId: "task-public", UpstreamTaskID: "upstream-duplicate"},
		{UserId: 1, MjId: "task-public", UpstreamTaskID: "upstream-duplicate"},
	}).Error)

	_, err := FindMidjourneyByPublicID(1, "task-public")
	require.ErrorIs(t, err, ErrMidjourneyIdentityAmbiguous)
	_, err = FindMidjourneyByUpstreamID("upstream-duplicate")
	require.ErrorIs(t, err, ErrMidjourneyIdentityAmbiguous)
	_, err = FindMidjourneysByPublicIDs(1, []string{"task-public", "task-public"})
	require.ErrorIs(t, err, ErrMidjourneyIdentityAmbiguous)
}
