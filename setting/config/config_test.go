package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testConfigWithMap struct {
	Modes map[string]string `json:"modes"`
	Exprs map[string]string `json:"exprs"`
	Name  string            `json:"name"`
}

type testScalarConfig struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestUpdateConfigFromMap_MapReplacement(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{
			"model-a": "tiered_expr",
			"model-b": "tiered_expr",
		},
		Exprs: map[string]string{
			"model-a": "p * 5 + c * 25",
			"model-b": "p * 10 + c * 50",
		},
		Name: "billing",
	}

	// Simulate removing model-a: new value only has model-b
	err := UpdateConfigFromMap(cfg, map[string]string{
		"modes": `{"model-b": "tiered_expr"}`,
		"exprs": `{"model-b": "p * 10 + c * 50"}`,
	})
	if err != nil {
		t.Fatalf("UpdateConfigFromMap failed: %v", err)
	}

	if _, ok := cfg.Modes["model-a"]; ok {
		t.Errorf("Modes still contains model-a after it was removed from the update; got %v", cfg.Modes)
	}
	if _, ok := cfg.Exprs["model-a"]; ok {
		t.Errorf("Exprs still contains model-a after it was removed from the update; got %v", cfg.Exprs)
	}

	if cfg.Modes["model-b"] != "tiered_expr" {
		t.Errorf("Modes[model-b] = %q, want %q", cfg.Modes["model-b"], "tiered_expr")
	}
	if cfg.Exprs["model-b"] != "p * 10 + c * 50" {
		t.Errorf("Exprs[model-b] = %q, want %q", cfg.Exprs["model-b"], "p * 10 + c * 50")
	}
}

func TestUpdateConfigFromMap_EmptyMapClearsAll(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{
			"model-a": "tiered_expr",
		},
		Exprs: map[string]string{
			"model-a": "p * 5 + c * 25",
		},
	}

	err := UpdateConfigFromMap(cfg, map[string]string{
		"modes": `{}`,
		"exprs": `{}`,
	})
	if err != nil {
		t.Fatalf("UpdateConfigFromMap failed: %v", err)
	}

	if len(cfg.Modes) != 0 {
		t.Errorf("Modes should be empty after updating with {}, got %v", cfg.Modes)
	}
	if len(cfg.Exprs) != 0 {
		t.Errorf("Exprs should be empty after updating with {}, got %v", cfg.Exprs)
	}
}

func TestUpdateConfigFromMap_ScalarFieldsUnchanged(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{"m": "v"},
		Name:  "old",
	}

	err := UpdateConfigFromMap(cfg, map[string]string{
		"name": "new",
	})
	if err != nil {
		t.Fatalf("UpdateConfigFromMap failed: %v", err)
	}

	if cfg.Name != "new" {
		t.Errorf("Name = %q, want %q", cfg.Name, "new")
	}
	// modes was not in configMap, should remain unchanged
	if cfg.Modes["m"] != "v" {
		t.Errorf("Modes should be unchanged, got %v", cfg.Modes)
	}
}

func TestConfigManagerSnapshotAndReplaceUseRegisteredValue(t *testing.T) {
	manager := NewConfigManager()
	registered := &testScalarConfig{Name: "before", Count: 1}
	manager.Register("test", registered)

	var snapshot testScalarConfig
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, testScalarConfig{Name: "before", Count: 1}, snapshot)

	replacement := testScalarConfig{Name: "after", Count: 2}
	require.True(t, manager.Replace("test", replacement))
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, testScalarConfig{Name: "after", Count: 2}, snapshot)

	pointerReplacement := &testScalarConfig{Name: "pointer", Count: 3}
	require.True(t, manager.Replace("test", pointerReplacement))
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, testScalarConfig{Name: "pointer", Count: 3}, snapshot)
}

func TestConfigManagerSnapshotRejectsMismatchedDestination(t *testing.T) {
	manager := NewConfigManager()
	manager.Register("test", &testScalarConfig{Name: "value"})

	var wrong string
	var nilConfig *testScalarConfig
	assert.False(t, manager.Snapshot("missing", &testScalarConfig{}))
	assert.False(t, manager.Snapshot("test", nil))
	assert.False(t, manager.Snapshot("test", nilConfig))
	assert.False(t, manager.Snapshot("test", testScalarConfig{}))
	assert.False(t, manager.Snapshot("test", &wrong))

	manager.Register("value", testScalarConfig{Name: "value"})
	assert.False(t, manager.Snapshot("value", &testScalarConfig{}))
	manager.Register("nil", nilConfig)
	assert.False(t, manager.Snapshot("nil", &testScalarConfig{}))
}

func TestConfigManagerReplaceRejectsMismatchedReplacement(t *testing.T) {
	manager := NewConfigManager()
	manager.Register("test", &testScalarConfig{Name: "value"})

	var nilConfig *testScalarConfig
	assert.False(t, manager.Replace("missing", testScalarConfig{}))
	var replaced bool
	assert.NotPanics(t, func() {
		replaced = manager.Replace("test", nil)
	})
	assert.False(t, replaced)
	assert.False(t, manager.Replace("test", nilConfig))
	assert.False(t, manager.Replace("test", "wrong"))

	manager.Register("value", testScalarConfig{Name: "value"})
	assert.False(t, manager.Replace("value", testScalarConfig{}))
	manager.Register("nil", nilConfig)
	assert.False(t, manager.Replace("nil", testScalarConfig{}))
}

func TestConfigManagerUpdateFromMapUpdatesRegisteredValue(t *testing.T) {
	manager := NewConfigManager()
	manager.Register("test", &testScalarConfig{Name: "before", Count: 1})

	found, err := manager.UpdateFromMap("test", map[string]string{
		"name":  "after",
		"count": "2",
	})
	require.NoError(t, err)
	require.True(t, found)

	var snapshot testScalarConfig
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, testScalarConfig{Name: "after", Count: 2}, snapshot)
}

func TestConfigManagerUpdateFromMapRejectsMissingOrInvalidTarget(t *testing.T) {
	manager := NewConfigManager()

	found, err := manager.UpdateFromMap("missing", map[string]string{"name": "after"})
	require.NoError(t, err)
	assert.False(t, found)

	manager.Register("value", testScalarConfig{Name: "before"})
	found, err = manager.UpdateFromMap("value", map[string]string{"name": "after"})
	require.NoError(t, err)
	assert.False(t, found)

	manager.Register("nil", (*testScalarConfig)(nil))
	found, err = manager.UpdateFromMap("nil", map[string]string{"name": "after"})
	require.NoError(t, err)
	assert.False(t, found)
}
