package model

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
)

type optionUpdateTestConfig struct {
	Value string `json:"value"`
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
