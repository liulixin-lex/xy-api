package routingcapability

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	PolicySchemaVersion = 1
	maxModels           = 512
	maxModelNameBytes   = 128
)

var ErrInvalid = errors.New("routing capability policy is invalid")

type Evidence struct {
	RequestKindsKnown     []string
	RequestKindsSupported []string
	CapabilitiesKnown     []string
	CapabilitiesSupported []string
}

type Overrides struct {
	Present  bool
	Revision uint64
	Default  *Evidence
	Models   map[string]Evidence
}

var requestKindNames = map[string]struct{}{
	"chat_completions": {}, "responses": {}, "responses_compaction": {}, "claude_messages": {},
	"gemini_generate": {}, "image": {}, "audio": {}, "embedding": {}, "rerank": {}, "realtime": {},
	"task": {}, "midjourney": {}, "suno": {}, "gemini_embedding": {}, "gemini_batch_embedding": {},
}

var capabilityNames = map[string]struct{}{
	"tools": {}, "json_schema": {}, "vision": {}, "audio_input": {}, "audio_output": {},
	"image_output": {}, "file_input": {}, "video_input": {}, "video_output": {}, "realtime": {},
	"stateful": {},
}

func ParsePoolPolicy(policyJSON []byte) (bool, error) {
	if len(strings.TrimSpace(string(policyJSON))) == 0 {
		return false, nil
	}
	var document map[string]json.RawMessage
	if err := common.Unmarshal(policyJSON, &document); err != nil {
		return false, fmt.Errorf("%w: pool policy document", ErrInvalid)
	}
	raw, exists := document["capability_routing"]
	if !exists {
		return false, nil
	}
	if nullOrEmpty(raw) {
		return false, fmt.Errorf("%w: capability_routing", ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return false, fmt.Errorf("%w: capability_routing", ErrInvalid)
	}
	for key := range fields {
		if key != "enabled" && key != "schema_version" {
			return false, fmt.Errorf("%w: capability_routing field %q", ErrInvalid, key)
		}
	}
	enabledRaw, enabledExists := fields["enabled"]
	versionRaw, versionExists := fields["schema_version"]
	if !enabledExists || !versionExists {
		return false, fmt.Errorf("%w: capability_routing version", ErrInvalid)
	}
	var enabled bool
	var schemaVersion int
	if common.Unmarshal(enabledRaw, &enabled) != nil || common.Unmarshal(versionRaw, &schemaVersion) != nil ||
		schemaVersion != PolicySchemaVersion {
		return false, fmt.Errorf("%w: capability_routing version", ErrInvalid)
	}
	return enabled, nil
}

func ParseMemberOverrides(overridesJSON []byte) (Overrides, error) {
	if len(strings.TrimSpace(string(overridesJSON))) == 0 {
		return Overrides{}, nil
	}
	var document map[string]json.RawMessage
	if err := common.Unmarshal(overridesJSON, &document); err != nil {
		return Overrides{}, fmt.Errorf("%w: member override document", ErrInvalid)
	}
	raw, exists := document["capabilities"]
	if !exists {
		return Overrides{}, nil
	}
	if nullOrEmpty(raw) {
		return Overrides{}, fmt.Errorf("%w: capability document", ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return Overrides{}, fmt.Errorf("%w: capability document", ErrInvalid)
	}
	allowed := map[string]struct{}{"revision": {}, "default": {}, "models": {}}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return Overrides{}, fmt.Errorf("%w: capability field %q", ErrInvalid, key)
		}
	}
	revisionRaw, revisionExists := fields["revision"]
	var revision uint64
	if !revisionExists || common.Unmarshal(revisionRaw, &revision) != nil || revision == 0 {
		return Overrides{}, fmt.Errorf("%w: capability revision", ErrInvalid)
	}
	result := Overrides{Present: true, Revision: revision}
	if defaultRaw, exists := fields["default"]; exists {
		evidence, err := parseEvidence(defaultRaw)
		if err != nil {
			return Overrides{}, err
		}
		result.Default = &evidence
	}
	if modelsRaw, exists := fields["models"]; exists {
		if nullOrEmpty(modelsRaw) {
			return Overrides{}, fmt.Errorf("%w: capability models", ErrInvalid)
		}
		var models map[string]json.RawMessage
		if err := common.Unmarshal(modelsRaw, &models); err != nil || len(models) > maxModels {
			return Overrides{}, fmt.Errorf("%w: capability models", ErrInvalid)
		}
		result.Models = make(map[string]Evidence, len(models))
		for modelName, evidenceRaw := range models {
			if !validModelName(modelName) {
				return Overrides{}, fmt.Errorf("%w: capability model name", ErrInvalid)
			}
			evidence, err := parseEvidence(evidenceRaw)
			if err != nil {
				return Overrides{}, err
			}
			result.Models[modelName] = evidence
		}
	}
	if result.Default == nil && len(result.Models) == 0 {
		return Overrides{}, fmt.Errorf("%w: capability evidence is empty", ErrInvalid)
	}
	return result, nil
}

func parseEvidence(raw []byte) (Evidence, error) {
	if nullOrEmpty(raw) {
		return Evidence{}, fmt.Errorf("%w: capability evidence", ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return Evidence{}, fmt.Errorf("%w: capability evidence", ErrInvalid)
	}
	for key := range fields {
		switch key {
		case "request_kinds_known", "request_kinds_supported", "capabilities_known", "capabilities_supported":
		default:
			return Evidence{}, fmt.Errorf("%w: capability evidence field %q", ErrInvalid, key)
		}
	}
	knownKinds, err := parseNames(fields["request_kinds_known"], requestKindNames, "request kind")
	if err != nil {
		return Evidence{}, err
	}
	supportedKinds, err := parseNames(fields["request_kinds_supported"], requestKindNames, "request kind")
	if err != nil {
		return Evidence{}, err
	}
	knownCapabilities, err := parseNames(fields["capabilities_known"], capabilityNames, "capability")
	if err != nil {
		return Evidence{}, err
	}
	supportedCapabilities, err := parseNames(fields["capabilities_supported"], capabilityNames, "capability")
	if err != nil {
		return Evidence{}, err
	}
	if !subset(supportedKinds, knownKinds) || !subset(supportedCapabilities, knownCapabilities) {
		return Evidence{}, fmt.Errorf("%w: supported values must be known", ErrInvalid)
	}
	return Evidence{
		RequestKindsKnown: knownKinds, RequestKindsSupported: supportedKinds,
		CapabilitiesKnown: knownCapabilities, CapabilitiesSupported: supportedCapabilities,
	}, nil
}

func parseNames(raw []byte, allowed map[string]struct{}, label string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, fmt.Errorf("%w: %s list", ErrInvalid, label)
	}
	var names []string
	if err := common.Unmarshal(raw, &names); err != nil || len(names) > len(allowed) {
		return nil, fmt.Errorf("%w: %s list", ErrInvalid, label)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != strings.TrimSpace(name) || name == "" {
			return nil, fmt.Errorf("%w: %s name", ErrInvalid, label)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("%w: duplicate %s %q", ErrInvalid, label, name)
		}
		if _, exists := allowed[name]; !exists {
			return nil, fmt.Errorf("%w: %s %q", ErrInvalid, label, name)
		}
		seen[name] = struct{}{}
	}
	return names, nil
}

func subset(values []string, known []string) bool {
	knownSet := make(map[string]struct{}, len(known))
	for _, value := range known {
		knownSet[value] = struct{}{}
	}
	for _, value := range values {
		if _, exists := knownSet[value]; !exists {
			return false
		}
	}
	return true
}

func nullOrEmpty(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

func validModelName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxModelNameBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
