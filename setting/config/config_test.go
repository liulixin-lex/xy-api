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
	registered := &testConfigWithMap{Name: "before", Modes: map[string]string{"a": "one"}}
	manager.Register("test", registered)

	var snapshot testConfigWithMap
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, "before", snapshot.Name)

	replacement := testConfigWithMap{Name: "after", Modes: map[string]string{"b": "two"}}
	require.True(t, manager.Replace("test", replacement))
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, "after", snapshot.Name)
	assert.Equal(t, map[string]string{"b": "two"}, snapshot.Modes)

	pointerReplacement := &testConfigWithMap{Name: "pointer", Modes: map[string]string{"c": "three"}}
	require.True(t, manager.Replace("test", pointerReplacement))
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, "pointer", snapshot.Name)
	assert.Equal(t, map[string]string{"c": "three"}, snapshot.Modes)
}

func TestConfigManagerSnapshotRejectsMismatchedDestination(t *testing.T) {
	manager := NewConfigManager()
	manager.Register("test", &testConfigWithMap{Name: "value"})

	var wrong string
	var nilConfig *testConfigWithMap
	assert.False(t, manager.Snapshot("missing", &testConfigWithMap{}))
	assert.False(t, manager.Snapshot("test", nil))
	assert.False(t, manager.Snapshot("test", nilConfig))
	assert.False(t, manager.Snapshot("test", testConfigWithMap{}))
	assert.False(t, manager.Snapshot("test", &wrong))

	manager.Register("value", testConfigWithMap{Name: "value"})
	assert.False(t, manager.Snapshot("value", &testConfigWithMap{}))
	manager.Register("nil", nilConfig)
	assert.False(t, manager.Snapshot("nil", &testConfigWithMap{}))
}

func TestConfigManagerReplaceRejectsMismatchedReplacement(t *testing.T) {
	manager := NewConfigManager()
	manager.Register("test", &testConfigWithMap{Name: "value"})

	var nilConfig *testConfigWithMap
	assert.False(t, manager.Replace("missing", testConfigWithMap{}))
	var replaced bool
	assert.NotPanics(t, func() {
		replaced = manager.Replace("test", nil)
	})
	assert.False(t, replaced)
	assert.False(t, manager.Replace("test", nilConfig))
	assert.False(t, manager.Replace("test", "wrong"))

	manager.Register("value", testConfigWithMap{Name: "value"})
	assert.False(t, manager.Replace("value", testConfigWithMap{}))
	manager.Register("nil", nilConfig)
	assert.False(t, manager.Replace("nil", testConfigWithMap{}))
}
