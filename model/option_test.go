package model

import (
	"fmt"
	"maps"
	"runtime"
	"strconv"
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
