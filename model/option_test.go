package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type optionUpdateTestConfig struct {
	Value string `json:"value"`
}

type optionAtomicPublishValue struct {
	Value string `json:"value"`
}

type optionAtomicPublishConfig struct {
	First  optionAtomicPublishValue `json:"first"`
	Second optionAtomicPublishValue `json:"second"`
}

type optionOrderConfig struct {
	First  string `json:"first"`
	Second string `json:"second"`
}

type optionCommitGatePool struct {
	gorm.ConnPool
	firstCommitted     chan struct{}
	releaseFirstCommit chan struct{}
	claimed            atomic.Bool
}

func (pool *optionCommitGatePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	beginner, ok := pool.ConnPool.(gorm.TxBeginner)
	if !ok {
		return nil, gorm.ErrInvalidTransaction
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &optionCommitGateTx{
		ConnPool:  tx,
		committer: tx,
		gate:      pool,
	}, nil
}

type optionCommitGateTx struct {
	gorm.ConnPool
	committer gorm.TxCommitter
	gate      *optionCommitGatePool
}

func (tx *optionCommitGateTx) Commit() error {
	err := tx.committer.Commit()
	if err == nil && tx.gate.claimed.CompareAndSwap(false, true) {
		close(tx.gate.firstCommitted)
		<-tx.gate.releaseFirstCommit
	}
	return err
}

func (tx *optionCommitGateTx) Rollback() error {
	return tx.committer.Rollback()
}

var (
	optionAtomicPublishOnce    sync.Once
	optionAtomicPublishEntered chan struct{}
	optionAtomicPublishRelease chan struct{}
)

func (value *optionAtomicPublishValue) UnmarshalJSON(data []byte) error {
	optionAtomicPublishOnce.Do(func() {
		close(optionAtomicPublishEntered)
		<-optionAtomicPublishRelease
	})
	type plainValue optionAtomicPublishValue
	return common.Unmarshal(data, (*plainValue)(value))
}

func TestHandleConfigUpdateSerializesWithSnapshot(t *testing.T) {
	const configName = "model_option_concurrency_test"
	config.GlobalConfig.Register(configName, &optionUpdateTestConfig{})

	start := make(chan struct{})
	var wait sync.WaitGroup
	var invalid atomic.Bool
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		for iteration := 0; iteration < 100; iteration++ {
			if !handleConfigUpdate(configName+".value", strconv.Itoa(iteration)) {
				invalid.Store(true)
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		for iteration := 0; iteration < 100; iteration++ {
			var snapshot optionUpdateTestConfig
			if !config.GlobalConfig.Snapshot(configName, &snapshot) {
				invalid.Store(true)
				return
			}
		}
	}()
	close(start)
	wait.Wait()

	assert.False(t, invalid.Load())
}

func TestUpdateOptionsBulkPublishesNamespaceAsCompleteSnapshot(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })

	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&Option{}))

	const configName = "model_option_atomic_publish_test"
	registered := &optionAtomicPublishConfig{
		First:  optionAtomicPublishValue{Value: "old-first"},
		Second: optionAtomicPublishValue{Value: "old-second"},
	}
	config.GlobalConfig.Register(configName, registered)

	oldFirst := `{"value":"old-first"}`
	oldSecond := `{"value":"old-second"}`
	newFirst := `{"value":"new-first"}`
	newSecond := `{"value":"new-second"}`
	firstKey := configName + ".first"
	secondKey := configName + ".second"

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{
		firstKey:  oldFirst,
		secondKey: oldSecond,
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	optionAtomicPublishOnce = sync.Once{}
	optionAtomicPublishEntered = make(chan struct{})
	optionAtomicPublishRelease = make(chan struct{})

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- UpdateOptionsBulk(map[string]string{
			firstKey:  newFirst,
			secondKey: newSecond,
		})
	}()
	<-optionAtomicPublishEntered

	type observedSnapshot struct {
		optionFirst  string
		optionSecond string
		config       optionAtomicPublishConfig
		configOK     bool
	}
	readerStarted := make(chan struct{})
	observed := make(chan observedSnapshot, 1)
	go func() {
		close(readerStarted)
		common.OptionMapRWMutex.RLock()
		snapshot := observedSnapshot{
			optionFirst:  common.OptionMap[firstKey],
			optionSecond: common.OptionMap[secondKey],
		}
		snapshot.configOK = config.GlobalConfig.Snapshot(configName, &snapshot.config)
		common.OptionMapRWMutex.RUnlock()
		observed <- snapshot
	}()
	<-readerStarted
	close(optionAtomicPublishRelease)

	snapshot := <-observed
	require.NoError(t, <-writeDone)
	require.True(t, snapshot.configOK)
	assert.Equal(t, newFirst, snapshot.optionFirst)
	assert.Equal(t, newSecond, snapshot.optionSecond)
	assert.Equal(t, "new-first", snapshot.config.First.Value)
	assert.Equal(t, "new-second", snapshot.config.Second.Value)
}

func TestLoadOptionsFromDatabasePublishesNamespaceAtomically(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })

	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&Option{}))

	const configName = "model_option_database_atomic_publish_test"
	registered := &optionAtomicPublishConfig{
		First:  optionAtomicPublishValue{Value: "old-first"},
		Second: optionAtomicPublishValue{Value: "old-second"},
	}
	config.GlobalConfig.Register(configName, registered)

	oldFirst := `{"value":"old-first"}`
	oldSecond := `{"value":"old-second"}`
	newFirst := `{"value":"new-first"}`
	newSecond := `{"value":"new-second"}`
	firstKey := configName + ".first"
	secondKey := configName + ".second"
	require.NoError(t, db.Create(&[]Option{
		{Key: firstKey, Value: newFirst},
		{Key: secondKey, Value: newSecond},
	}).Error)

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{
		firstKey:  oldFirst,
		secondKey: oldSecond,
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	optionAtomicPublishOnce = sync.Once{}
	optionAtomicPublishEntered = make(chan struct{})
	optionAtomicPublishRelease = make(chan struct{})

	loadDone := make(chan struct{})
	go func() {
		loadOptionsFromDatabase()
		close(loadDone)
	}()
	<-optionAtomicPublishEntered

	type observedSnapshot struct {
		optionFirst  string
		optionSecond string
		config       optionAtomicPublishConfig
		configOK     bool
	}
	readerStarted := make(chan struct{})
	observed := make(chan observedSnapshot, 1)
	go func() {
		close(readerStarted)
		common.OptionMapRWMutex.RLock()
		snapshot := observedSnapshot{
			optionFirst:  common.OptionMap[firstKey],
			optionSecond: common.OptionMap[secondKey],
		}
		snapshot.configOK = config.GlobalConfig.Snapshot(configName, &snapshot.config)
		common.OptionMapRWMutex.RUnlock()
		observed <- snapshot
	}()
	<-readerStarted
	close(optionAtomicPublishRelease)

	snapshot := <-observed
	<-loadDone
	require.True(t, snapshot.configOK)
	assert.Equal(t, newFirst, snapshot.optionFirst)
	assert.Equal(t, newSecond, snapshot.optionSecond)
	assert.Equal(t, "new-first", snapshot.config.First.Value)
	assert.Equal(t, "new-second", snapshot.config.Second.Value)
}

func TestUpdateOptionsBulkRollbackDoesNotPublish(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&Option{}))

	const configName = "model_option_rollback_publish_test"
	registered := &optionOrderConfig{First: "initial-first", Second: "initial-second"}
	config.GlobalConfig.Register(configName, registered)
	firstKey := configName + ".first"
	secondKey := configName + ".second"

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{
		firstKey:  "initial-first",
		secondKey: "initial-second",
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	forcedErr := errors.New("forced option persistence failure")
	callbackName := "test:fail_option_bulk_upsert:" + t.Name()
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		option, ok := tx.Statement.Dest.(*Option)
		if ok && option.Key == secondKey {
			tx.AddError(forcedErr)
		}
	}))

	err = UpdateOptionsBulk(map[string]string{
		firstKey:  "new-first",
		secondKey: "new-second",
	})
	require.ErrorIs(t, err, forcedErr)

	var count int64
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
	common.OptionMapRWMutex.RLock()
	optionFirst := common.OptionMap[firstKey]
	optionSecond := common.OptionMap[secondKey]
	var snapshot optionOrderConfig
	configOK := config.GlobalConfig.Snapshot(configName, &snapshot)
	common.OptionMapRWMutex.RUnlock()
	require.True(t, configOK)
	assert.Equal(t, "initial-first", optionFirst)
	assert.Equal(t, "initial-second", optionSecond)
	assert.Equal(t, "initial-first", snapshot.First)
	assert.Equal(t, "initial-second", snapshot.Second)
}

func TestUpdateOptionsBulkPersistsKeysInLexicographicOrder(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&Option{}))

	expectedOrder := []string{
		"option_bulk_order_00",
		"option_bulk_order_01",
		"option_bulk_order_02",
		"option_bulk_order_03",
		"option_bulk_order_04",
		"option_bulk_order_05",
		"option_bulk_order_06",
		"option_bulk_order_07",
		"option_bulk_order_08",
		"option_bulk_order_09",
		"option_bulk_order_10",
		"option_bulk_order_11",
	}
	values := map[string]string{
		expectedOrder[11]: "value-11",
		expectedOrder[10]: "value-10",
		expectedOrder[9]:  "value-09",
		expectedOrder[8]:  "value-08",
		expectedOrder[7]:  "value-07",
		expectedOrder[6]:  "value-06",
		expectedOrder[5]:  "value-05",
		expectedOrder[4]:  "value-04",
		expectedOrder[3]:  "value-03",
		expectedOrder[2]:  "value-02",
		expectedOrder[1]:  "value-01",
		expectedOrder[0]:  "value-00",
	}

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	observedOrder := make([]string, 0, len(values))
	callbackName := "test:record_option_bulk_write_order:" + t.Name()
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		option, ok := tx.Statement.Dest.(*Option)
		if !ok {
			return
		}
		if _, tracked := values[option.Key]; tracked {
			observedOrder = append(observedOrder, option.Key)
		}
	}))

	require.NoError(t, UpdateOptionsBulk(values))
	assert.Equal(t, expectedOrder, observedOrder)
}

func TestUpdateOptionsBulkUsesCanonicalOrderForCaseInsensitiveAliases(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.Exec(`CREATE TABLE options (
		"key" TEXT COLLATE NOCASE PRIMARY KEY,
		"value" TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Create(&[]Option{
		{Key: "a", Value: "old-a"},
		{Key: "B", Value: "old-b"},
	}).Error)

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	observedOrder := make([]string, 0, 2)
	callbackName := "test:record_option_canonical_lock_order:" + t.Name()
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		option, ok := tx.Statement.Dest.(*Option)
		if !ok || (!strings.EqualFold(option.Key, "a") && !strings.EqualFold(option.Key, "b")) {
			return
		}
		observedOrder = append(observedOrder, option.Key)
	}))

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"A": "first-a",
		"b": "first-b",
	}))
	firstOrder := append([]string(nil), observedOrder...)
	observedOrder = observedOrder[:0]

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"a": "second-a",
		"B": "second-b",
	}))
	secondOrder := append([]string(nil), observedOrder...)

	assert.Equal(t, []string{"b", "A"}, firstOrder)
	assert.Equal(t, []string{"B", "a"}, secondOrder)
}

func TestUpdateOptionConcurrentFirstWriteSameKeySucceeds(t *testing.T) {
	previousDB := DB
	dsn := filepath.Join(t.TempDir(), "options.db") + "?_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(2)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	require.NoError(t, db.AutoMigrate(&Option{}))

	const key = "option_concurrent_first_write"
	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	delete(common.OptionMap, key)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	createEntered := make(chan struct{}, 2)
	releaseCreate := make(chan struct{})
	var releaseCreateOnce sync.Once
	t.Cleanup(func() { releaseCreateOnce.Do(func() { close(releaseCreate) }) })
	callbackName := "test:block_concurrent_option_create:" + t.Name()
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		option, ok := tx.Statement.Dest.(*Option)
		if !ok || option.Key != key {
			return
		}
		createEntered <- struct{}{}
		<-releaseCreate
	}))

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- UpdateOption(key, "first") }()
	go func() { secondDone <- UpdateOption(key, "second") }()
	<-createEntered
	<-createEntered
	releaseCreateOnce.Do(func() { close(releaseCreate) })

	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)

	var persisted Option
	require.NoError(t, db.First(&persisted, "key = ?", key).Error)
	assert.Contains(t, []string{"first", "second"}, persisted.Value)
	var count int64
	require.NoError(t, db.Model(&Option{}).Where("key = ?", key).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	common.OptionMapRWMutex.RLock()
	published := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, persisted.Value, published)
}

func TestUpdateOptionPublishesCanonicalKeyForDatabaseEquivalentAlias(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.Exec(`CREATE TABLE options (
		"key" TEXT COLLATE NOCASE PRIMARY KEY,
		"value" TEXT NOT NULL
	)`).Error)

	const (
		canonicalKey = "SystemName"
		aliasKey     = "systemname"
		oldValue     = "old-system-name"
		newValue     = "new-system-name"
	)
	require.NoError(t, db.Create(&Option{Key: canonicalKey, Value: oldValue}).Error)

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	previousSystemName := common.SystemName
	common.OptionMap = map[string]string{canonicalKey: oldValue}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.SystemName = previousSystemName
		common.OptionMapRWMutex.Unlock()
	})

	require.NoError(t, UpdateOption(aliasKey, newValue))

	var persisted []Option
	require.NoError(t, db.Find(&persisted).Error)
	require.Equal(t, []Option{{Key: canonicalKey, Value: newValue}}, persisted)
	common.OptionMapRWMutex.RLock()
	canonicalValue := common.OptionMap[canonicalKey]
	_, aliasPublished := common.OptionMap[aliasKey]
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, newValue, canonicalValue)
	assert.False(t, aliasPublished)
}

func TestUpdateOptionUsesCanonicalNamespaceForGroupedPublish(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.Exec(`CREATE TABLE options (
		"key" TEXT COLLATE NOCASE PRIMARY KEY,
		"value" TEXT NOT NULL
	)`).Error)

	const configName = "ModelOptionCanonicalNamespaceTest"
	canonicalFirstKey := configName + ".first"
	canonicalSecondKey := configName + ".second"
	aliasFirstKey := strings.ToLower(canonicalFirstKey)
	require.NoError(t, db.Create(&[]Option{
		{Key: canonicalFirstKey, Value: "database-first"},
		{Key: canonicalSecondKey, Value: "database-second"},
	}).Error)
	config.GlobalConfig.Register(configName, &optionOrderConfig{
		First:  "memory-first",
		Second: "memory-second",
	})

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{
		canonicalFirstKey:  "memory-first",
		canonicalSecondKey: "memory-second",
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	require.NoError(t, UpdateOption(aliasFirstKey, "new-first"))

	var persisted []Option
	require.NoError(t, db.Order("key asc").Find(&persisted).Error)
	require.Equal(t, []Option{
		{Key: canonicalFirstKey, Value: "new-first"},
		{Key: canonicalSecondKey, Value: "database-second"},
	}, persisted)
	common.OptionMapRWMutex.RLock()
	optionFirst := common.OptionMap[canonicalFirstKey]
	optionSecond := common.OptionMap[canonicalSecondKey]
	_, aliasPublished := common.OptionMap[aliasFirstKey]
	var snapshot optionOrderConfig
	configOK := config.GlobalConfig.Snapshot(configName, &snapshot)
	common.OptionMapRWMutex.RUnlock()
	require.True(t, configOK)
	assert.Equal(t, "new-first", optionFirst)
	assert.Equal(t, "database-second", optionSecond)
	assert.False(t, aliasPublished)
	assert.Equal(t, "new-first", snapshot.First)
	assert.Equal(t, "database-second", snapshot.Second)
}

func TestUpdateOptionDoesNotPublishOlderBulkCommitAfterNewerCommit(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&Option{}))

	const configName = "model_option_commit_publish_order_test"
	registered := &optionOrderConfig{First: "initial-first", Second: "initial-second"}
	config.GlobalConfig.Register(configName, registered)
	firstKey := configName + ".first"
	secondKey := configName + ".second"

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{
		firstKey:  "initial-first",
		secondKey: "initial-second",
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	gate := &optionCommitGatePool{
		ConnPool:           db.ConnPool,
		firstCommitted:     make(chan struct{}),
		releaseFirstCommit: make(chan struct{}),
	}
	db.ConnPool = gate
	db.Statement.ConnPool = gate
	var releaseFirstCommit sync.Once
	t.Cleanup(func() { releaseFirstCommit.Do(func() { close(gate.releaseFirstCommit) }) })

	olderDone := make(chan error, 1)
	go func() {
		olderDone <- UpdateOptionsBulk(map[string]string{
			firstKey:  "older-first",
			secondKey: "older-second",
		})
	}()
	<-gate.firstCommitted

	newerDone := make(chan error, 1)
	go func() {
		newerDone <- UpdateOption(firstKey, "newer-first")
	}()
	require.NoError(t, <-newerDone)

	common.OptionMapRWMutex.RLock()
	intermediateOptionFirst := common.OptionMap[firstKey]
	intermediateOptionSecond := common.OptionMap[secondKey]
	var intermediateSnapshot optionOrderConfig
	intermediateConfigOK := config.GlobalConfig.Snapshot(configName, &intermediateSnapshot)
	common.OptionMapRWMutex.RUnlock()
	require.True(t, intermediateConfigOK)
	assert.Equal(t, "newer-first", intermediateOptionFirst)
	assert.Equal(t, "older-second", intermediateOptionSecond)
	assert.Equal(t, "newer-first", intermediateSnapshot.First)
	assert.Equal(t, "older-second", intermediateSnapshot.Second)

	releaseFirstCommit.Do(func() { close(gate.releaseFirstCommit) })
	require.NoError(t, <-olderDone)

	var persisted []Option
	require.NoError(t, db.Order("key asc").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	assert.Equal(t, "newer-first", persisted[0].Value)
	assert.Equal(t, "older-second", persisted[1].Value)

	common.OptionMapRWMutex.RLock()
	optionFirst := common.OptionMap[firstKey]
	optionSecond := common.OptionMap[secondKey]
	var snapshot optionOrderConfig
	configOK := config.GlobalConfig.Snapshot(configName, &snapshot)
	common.OptionMapRWMutex.RUnlock()
	require.True(t, configOK)
	assert.Equal(t, "newer-first", optionFirst)
	assert.Equal(t, "older-second", optionSecond)
	assert.Equal(t, "newer-first", snapshot.First)
	assert.Equal(t, "older-second", snapshot.Second)
}

func TestLoadOptionsFromDatabaseDoesNotPublishSnapshotOlderThanCompletedUpdate(t *testing.T) {
	previousDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&Option{}))

	const configName = "model_option_load_publish_order_test"
	registered := &optionOrderConfig{First: "older-first", Second: "older-second"}
	config.GlobalConfig.Register(configName, registered)
	firstKey := configName + ".first"
	secondKey := configName + ".second"
	require.NoError(t, db.Create(&[]Option{
		{Key: firstKey, Value: "older-first"},
		{Key: secondKey, Value: "older-second"},
	}).Error)

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = map[string]string{
		firstKey:  "older-first",
		secondKey: "older-second",
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	loadRead := make(chan struct{})
	releaseLoad := make(chan struct{})
	var releaseLoadOnce sync.Once
	t.Cleanup(func() { releaseLoadOnce.Do(func() { close(releaseLoad) }) })
	var captured atomic.Bool
	callbackName := "test:block_option_load_after_read:" + t.Name()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "options" ||
			!captured.CompareAndSwap(false, true) {
			return
		}
		close(loadRead)
		<-releaseLoad
	}))

	loadDone := make(chan struct{})
	go func() {
		loadOptionsFromDatabase()
		close(loadDone)
	}()
	<-loadRead

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- UpdateOptionsBulk(map[string]string{
			firstKey:  "newer-first",
			secondKey: "newer-second",
		})
	}()
	require.NoError(t, <-updateDone)
	releaseLoadOnce.Do(func() { close(releaseLoad) })
	<-loadDone

	var persisted []Option
	require.NoError(t, db.Order("key asc").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	assert.Equal(t, "newer-first", persisted[0].Value)
	assert.Equal(t, "newer-second", persisted[1].Value)

	common.OptionMapRWMutex.RLock()
	optionFirst := common.OptionMap[firstKey]
	optionSecond := common.OptionMap[secondKey]
	var snapshot optionOrderConfig
	configOK := config.GlobalConfig.Snapshot(configName, &snapshot)
	common.OptionMapRWMutex.RUnlock()
	require.True(t, configOK)
	assert.Equal(t, "newer-first", optionFirst)
	assert.Equal(t, "newer-second", optionSecond)
	assert.Equal(t, "newer-first", snapshot.First)
	assert.Equal(t, "newer-second", snapshot.Second)
}
