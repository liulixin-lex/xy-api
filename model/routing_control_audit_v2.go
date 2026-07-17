package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	routingControlAuditJSONMaxBytes = 60 << 10
	routingControlAuditMaxNodes     = 4_096
	routingControlAuditMaxDepth     = 16
)

type RoutingControlAuditPublicPayload struct {
	Summary         any `json:"summary"`
	Subject         any `json:"subject,omitempty"`
	Changes         any `json:"changes,omitempty"`
	Impact          any `json:"impact,omitempty"`
	Recommendations any `json:"recommendations,omitempty"`
	Relations       any `json:"relations,omitempty"`
}

type RoutingControlAuditTechnicalPayload struct {
	BeforeHash string `json:"before_hash,omitempty"`
	AfterHash  string `json:"after_hash,omitempty"`
	Details    any    `json:"details,omitempty"`
}

func (audit *RoutingControlAudit) BeforeCreate(tx *gorm.DB) error {
	if audit == nil {
		return ErrRoutingControlAuditInvalid
	}
	normalized, err := normalizeRoutingControlAuditForInsert(tx, *audit)
	if err != nil {
		return err
	}
	*audit = normalized
	return nil
}

func (*RoutingControlAudit) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingControlAuditImmutable
}

func (*RoutingControlAudit) BeforeDelete(*gorm.DB) error {
	return ErrRoutingControlAuditImmutable
}

func (audit RoutingControlAudit) PublicPayload() RoutingControlAuditPublicPayload {
	summary := routingControlAuditPublicValue(decodeRoutingControlAuditJSON(audit.SummaryJSON, true), true)
	return RoutingControlAuditPublicPayload{
		Summary:         summary,
		Subject:         routingControlAuditPublicValue(decodeRoutingControlAuditJSON(audit.SubjectSnapshotJSON, false), false),
		Changes:         routingControlAuditPublicValue(decodeRoutingControlAuditJSON(audit.ChangeSetJSON, false), false),
		Impact:          routingControlAuditPublicValue(decodeRoutingControlAuditJSON(audit.ImpactJSON, false), false),
		Recommendations: routingControlAuditPublicValue(decodeRoutingControlAuditJSON(audit.RecommendationJSON, false), false),
		Relations:       routingControlAuditPublicValue(decodeRoutingControlAuditJSON(audit.RelationJSON, false), false),
	}
}

func (audit RoutingControlAudit) TechnicalPayload() RoutingControlAuditTechnicalPayload {
	details := make(map[string]any)
	existing := decodeRoutingControlAuditJSON(audit.TechnicalJSON, false)
	if record, ok := existing.(map[string]any); ok {
		for key, value := range record {
			details[key] = value
		}
	} else if existing != nil {
		details["record"] = existing
	}
	documents := []struct {
		key   string
		value any
	}{
		{key: "summary_fields", value: decodeRoutingControlAuditJSON(audit.SummaryJSON, false)},
		{key: "subject_fields", value: decodeRoutingControlAuditJSON(audit.SubjectSnapshotJSON, false)},
		{key: "change_fields", value: decodeRoutingControlAuditJSON(audit.ChangeSetJSON, false)},
		{key: "impact_fields", value: decodeRoutingControlAuditJSON(audit.ImpactJSON, false)},
		{key: "recommendation_fields", value: decodeRoutingControlAuditJSON(audit.RecommendationJSON, false)},
		{key: "relation_fields", value: decodeRoutingControlAuditJSON(audit.RelationJSON, false)},
	}
	for _, document := range documents {
		split := splitRoutingControlAuditValue(document.value)
		if split.hasTechnical {
			details[document.key] = split.technical
		}
	}
	var payload any
	if len(details) > 0 {
		payload = details
	}
	return RoutingControlAuditTechnicalPayload{
		BeforeHash: audit.BeforeHash,
		AfterHash:  audit.AfterHash,
		Details:    payload,
	}
}

type routingControlAuditValueSplit struct {
	public       any
	technical    any
	hasPublic    bool
	hasTechnical bool
}

func routingControlAuditPublicValue(value any, required bool) any {
	split := splitRoutingControlAuditValue(value)
	if split.hasPublic {
		return split.public
	}
	if required {
		return map[string]any{"status": "unavailable"}
	}
	return nil
}

func splitRoutingControlAuditValue(value any) routingControlAuditValueSplit {
	switch typed := value.(type) {
	case map[string]any:
		public := make(map[string]any)
		technical := make(map[string]any)
		for key, child := range typed {
			if routingControlAuditTechnicalKey(key) {
				technical[key] = child
				continue
			}
			split := splitRoutingControlAuditValue(child)
			if split.hasPublic {
				public[key] = split.public
			}
			if split.hasTechnical {
				technical[key] = split.technical
			}
		}
		return routingControlAuditValueSplit{
			public: public, technical: technical,
			hasPublic: len(public) > 0, hasTechnical: len(technical) > 0,
		}
	case []any:
		public := make([]any, 0, len(typed))
		technical := make([]any, 0, len(typed))
		for _, child := range typed {
			split := splitRoutingControlAuditValue(child)
			if split.hasPublic {
				public = append(public, split.public)
			}
			if split.hasTechnical {
				technical = append(technical, split.technical)
			}
		}
		return routingControlAuditValueSplit{
			public: public, technical: technical,
			hasPublic: len(public) > 0, hasTechnical: len(technical) > 0,
		}
	case nil:
		return routingControlAuditValueSplit{public: nil, hasPublic: true}
	default:
		return routingControlAuditValueSplit{public: typed, hasPublic: true}
	}
}

func routingControlAuditTechnicalKey(key string) bool {
	if routingControlAuditSensitiveKey(key) {
		return true
	}
	var normalized strings.Builder
	for _, char := range strings.ToLower(key) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			normalized.WriteRune(char)
		}
	}
	value := normalized.String()
	if strings.Contains(value, "hash") || strings.Contains(value, "etag") ||
		strings.Contains(value, "idempotency") {
		return true
	}
	switch value {
	case "eventid", "outbox", "outboxid", "systemtaskid", "rawresult", "rawjson":
		return true
	default:
		return false
	}
}

func normalizeRoutingControlAuditForInsert(tx *gorm.DB, audit RoutingControlAudit) (RoutingControlAudit, error) {
	if audit.SchemaVersion == 0 {
		audit.SchemaVersion = RoutingControlAuditSchemaVersion
	}
	if audit.SchemaVersion != RoutingControlAuditSchemaVersion ||
		!validRoutingControlAuditSubjectType(audit.SubjectType) ||
		!validRoutingControlAuditAction(audit.Action) {
		return RoutingControlAudit{}, ErrRoutingControlAuditInvalid
	}
	if audit.EventType == "" {
		audit.EventType = audit.SubjectType + "." + audit.Action
	}
	if audit.Source == "" {
		audit.Source = routingControlAuditSource(audit)
	}
	if audit.Result == "" {
		audit.Result = RoutingControlAuditResultSucceeded
	}
	if audit.SubjectIdentity == "" {
		audit.SubjectIdentity = routingControlAuditSubjectIdentity(audit)
	}
	if audit.SubjectName == "" {
		audit.SubjectName = routingControlAuditSubjectName(audit)
	}
	if audit.ActorName == "" {
		actorName, actorRole, err := routingControlAuditActorSnapshot(tx, audit.ActorID)
		if err != nil {
			return RoutingControlAudit{}, err
		}
		audit.ActorName = actorName
		if audit.ActorRole == 0 {
			audit.ActorRole = actorRole
		}
	}
	if err := hydrateRoutingControlAuditChannelSnapshot(tx, &audit); err != nil {
		return RoutingControlAudit{}, err
	}

	var err error
	audit.SummaryJSON, err = sanitizeRoutingControlAuditJSON(audit.SummaryJSON, true)
	if err != nil {
		return RoutingControlAudit{}, err
	}
	optionalDocuments := []struct {
		source string
		target *string
	}{
		{source: audit.SubjectSnapshotJSON, target: &audit.SubjectSnapshotJSON},
		{source: audit.ChangeSetJSON, target: &audit.ChangeSetJSON},
		{source: audit.ImpactJSON, target: &audit.ImpactJSON},
		{source: audit.RecommendationJSON, target: &audit.RecommendationJSON},
		{source: audit.RelationJSON, target: &audit.RelationJSON},
		{source: audit.TechnicalJSON, target: &audit.TechnicalJSON},
	}
	for _, document := range optionalDocuments {
		*document.target, err = sanitizeRoutingControlAuditJSON(document.source, false)
		if err != nil {
			return RoutingControlAudit{}, err
		}
	}
	audit.Reason = truncateRoutingControlAuditText(common.SanitizeErrorMessage(audit.Reason), 512)
	audit.ErrorMessage = truncateRoutingControlAuditText(common.SanitizeErrorMessage(audit.ErrorMessage), common.SafeErrorMaxRunes)
	audit.ActorName = truncateRoutingControlAuditText(common.SanitizeErrorMessage(audit.ActorName), 128)
	audit.SubjectName = truncateRoutingControlAuditText(common.SanitizeErrorMessage(audit.SubjectName), 256)
	if err := validateNormalizedRoutingControlAudit(audit); err != nil {
		return RoutingControlAudit{}, err
	}
	return audit, nil
}

func validateNormalizedRoutingControlAudit(audit RoutingControlAudit) error {
	if audit.SchemaVersion != RoutingControlAuditSchemaVersion || audit.SubjectID < 0 || audit.ActorID < 0 ||
		audit.CreatedTimeMs <= 0 || !validRoutingControlAuditSubjectType(audit.SubjectType) ||
		!validRoutingControlAuditAction(audit.Action) || !validRoutingControlAuditSource(audit.Source) ||
		!validRoutingControlAuditResult(audit.Result) ||
		!validRoutingControlAuditText(audit.EventType, 96, false) ||
		!validRoutingControlAuditText(audit.SubjectIdentity, 128, false) ||
		!validRoutingControlAuditText(audit.SubjectGeneration, 32, true) ||
		!validRoutingControlAuditText(audit.SubjectName, 256, false) ||
		!validRoutingControlAuditText(audit.ActorName, 128, false) ||
		!validRoutingControlAuditText(audit.ErrorCode, 64, true) ||
		!validRoutingControlAuditText(audit.CorrelationID, 64, true) ||
		(audit.BeforeHash != "" && !validRoutingHash(audit.BeforeHash)) ||
		(audit.AfterHash != "" && !validRoutingHash(audit.AfterHash)) ||
		len(audit.SummaryJSON) == 0 || len(audit.SummaryJSON) > routingControlAuditJSONMaxBytes {
		return ErrRoutingControlAuditInvalid
	}
	for _, value := range []string{
		audit.SubjectSnapshotJSON,
		audit.ChangeSetJSON,
		audit.ImpactJSON,
		audit.RecommendationJSON,
		audit.RelationJSON,
		audit.TechnicalJSON,
	} {
		if len(value) > routingControlAuditJSONMaxBytes || !utf8.ValidString(value) {
			return ErrRoutingControlAuditInvalid
		}
	}
	return nil
}

func validRoutingControlAuditSubjectType(subjectType string) bool {
	switch subjectType {
	case RoutingControlSubjectRuntimeSettings,
		RoutingControlSubjectCostBinding,
		RoutingControlSubjectChannelConfiguration,
		RoutingControlSubjectChannelLifecycle,
		RoutingControlSubjectPolicyDraft,
		RoutingControlSubjectPolicyRevision,
		RoutingControlSubjectPolicyActivation,
		RoutingControlSubjectPolicyRiskAcceptance,
		RoutingControlSubjectOperation,
		RoutingControlSubjectPricing:
		return true
	default:
		return false
	}
}

func IsRoutingControlAuditSubjectType(subjectType string) bool {
	return validRoutingControlAuditSubjectType(subjectType)
}

func validRoutingControlAuditAction(action string) bool {
	switch action {
	case RoutingControlActionBootstrap,
		RoutingControlActionReconcile,
		RoutingControlActionCreate,
		RoutingControlActionUpdate,
		RoutingControlActionDelete,
		RoutingControlActionValidate,
		RoutingControlActionPublish,
		RoutingControlActionRollback,
		RoutingControlActionRiskAccept,
		RoutingControlActionRotate,
		RoutingControlActionRetire,
		RoutingControlActionRetry,
		RoutingControlActionCancel:
		return true
	default:
		return false
	}
}

func validRoutingControlAuditSource(source string) bool {
	return source == RoutingControlAuditSourceSystem || source == RoutingControlAuditSourceAdmin ||
		source == RoutingControlAuditSourceMigration || source == RoutingControlAuditSourceReconcile
}

func IsRoutingControlAuditSource(source string) bool {
	return validRoutingControlAuditSource(source)
}

func validRoutingControlAuditResult(result string) bool {
	return result == RoutingControlAuditResultSucceeded || result == RoutingControlAuditResultPartial ||
		result == RoutingControlAuditResultFailed || result == RoutingControlAuditResultRejected
}

func IsRoutingControlAuditResult(result string) bool {
	return validRoutingControlAuditResult(result)
}

func validRoutingControlAuditText(value string, maxBytes int, optional bool) bool {
	if value == "" {
		return optional
	}
	return len(value) <= maxBytes && utf8.ValidString(value)
}

func routingControlAuditSource(audit RoutingControlAudit) string {
	var summary map[string]any
	if common.UnmarshalJsonStr(audit.SummaryJSON, &summary) == nil {
		if source, ok := summary["source"].(string); ok {
			source = strings.ToLower(strings.TrimSpace(source))
			switch {
			case strings.Contains(source, "migration"):
				return RoutingControlAuditSourceMigration
			case strings.Contains(source, "reconcile"), strings.Contains(source, "external_option"):
				return RoutingControlAuditSourceReconcile
			}
		}
	}
	if audit.ActorID > 0 {
		return RoutingControlAuditSourceAdmin
	}
	return RoutingControlAuditSourceSystem
}

func routingControlAuditSubjectIdentity(audit RoutingControlAudit) string {
	if audit.SubjectGeneration != "" {
		return audit.SubjectGeneration
	}
	prefix := audit.SubjectType
	if audit.SubjectID > 0 {
		return fmt.Sprintf("%s:%d", prefix, audit.SubjectID)
	}
	return prefix
}

func routingControlAuditSubjectName(audit RoutingControlAudit) string {
	switch audit.SubjectType {
	case RoutingControlSubjectRuntimeSettings:
		return "Channel routing runtime settings"
	case RoutingControlSubjectPolicyRevision:
		return fmt.Sprintf("Routing policy revision %d", audit.SubjectID)
	case RoutingControlSubjectPolicyDraft:
		return fmt.Sprintf("Routing policy draft %d", audit.SubjectID)
	case RoutingControlSubjectPolicyRiskAcceptance:
		return fmt.Sprintf("Routing policy risk acceptance %d", audit.SubjectID)
	case RoutingControlSubjectPricing:
		return "Routing pricing"
	default:
		return strings.ReplaceAll(audit.SubjectType, "_", " ")
	}
}

func routingControlAuditActorSnapshot(tx *gorm.DB, actorID int) (string, int, error) {
	if actorID <= 0 {
		return "system", 0, nil
	}
	fallback := fmt.Sprintf("user-%d", actorID)
	if tx == nil || tx.Dialector == nil || !tx.Migrator().HasTable(&User{}) {
		return fallback, 0, nil
	}
	columns := []string{"id"}
	if tx.Migrator().HasColumn(&User{}, "username") {
		columns = append(columns, "username")
	}
	if tx.Migrator().HasColumn(&User{}, "display_name") {
		columns = append(columns, "display_name")
	}
	if tx.Migrator().HasColumn(&User{}, "role") {
		columns = append(columns, "role")
	}
	var actor User
	err := tx.Unscoped().Select(columns).Where("id = ?", actorID).First(&actor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	name := strings.TrimSpace(actor.DisplayName)
	if name == "" {
		name = strings.TrimSpace(actor.Username)
	}
	if name == "" {
		name = fallback
	}
	return name, actor.Role, nil
}

func hydrateRoutingControlAuditChannelSnapshot(tx *gorm.DB, audit *RoutingControlAudit) error {
	if audit == nil || audit.SubjectID <= 0 ||
		(audit.SubjectType != RoutingControlSubjectChannelConfiguration &&
			audit.SubjectType != RoutingControlSubjectChannelLifecycle &&
			audit.SubjectType != RoutingControlSubjectCostBinding) ||
		tx == nil || tx.Dialector == nil || !tx.Migrator().HasTable(&Channel{}) {
		return nil
	}
	var channel Channel
	err := tx.Select("id", "name", "routing_identity", "routing_generation").Where("id = ?", audit.SubjectID).First(&channel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if audit.SubjectName == "" || audit.SubjectName == strings.ReplaceAll(audit.SubjectType, "_", " ") {
		audit.SubjectName = channel.Name
	}
	if audit.SubjectGeneration == "" {
		audit.SubjectGeneration = channel.RoutingGeneration
	}
	if audit.SubjectIdentity == "" || audit.SubjectIdentity == fmt.Sprintf("%s:%d", audit.SubjectType, audit.SubjectID) {
		audit.SubjectIdentity = channel.RoutingIdentity
	}
	return nil
}

func sanitizeRoutingControlAuditJSON(raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", ErrRoutingControlAuditInvalid
		}
		return "", nil
	}
	if len(raw) > routingControlAuditJSONMaxBytes || !utf8.ValidString(raw) {
		return "", ErrRoutingControlAuditInvalid
	}
	var decoded any
	if err := common.UnmarshalJsonStr(raw, &decoded); err != nil {
		return "", ErrRoutingControlAuditInvalid
	}
	nodes := 0
	sanitized, err := sanitizeRoutingControlAuditValue(decoded, 0, &nodes)
	if err != nil {
		return "", err
	}
	encoded, err := common.Marshal(sanitized)
	if err != nil || len(encoded) == 0 || len(encoded) > routingControlAuditJSONMaxBytes {
		return "", ErrRoutingControlAuditInvalid
	}
	return string(encoded), nil
}

func sanitizeRoutingControlAuditValue(value any, depth int, nodes *int) (any, error) {
	if nodes == nil || depth > routingControlAuditMaxDepth || *nodes >= routingControlAuditMaxNodes {
		return nil, ErrRoutingControlAuditInvalid
	}
	*nodes++
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, child := range typed {
			if !utf8.ValidString(key) {
				return nil, ErrRoutingControlAuditInvalid
			}
			if routingControlAuditSensitiveKey(key) {
				clean[key] = "[redacted]"
				continue
			}
			value, err := sanitizeRoutingControlAuditValue(child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			clean[key] = value
		}
		return clean, nil
	case []any:
		clean := make([]any, len(typed))
		for index := range typed {
			value, err := sanitizeRoutingControlAuditValue(typed[index], depth+1, nodes)
			if err != nil {
				return nil, err
			}
			clean[index] = value
		}
		return clean, nil
	case string:
		if strings.Contains(typed, "-----BEGIN") {
			return "[redacted]", nil
		}
		return common.SanitizeErrorMessage(typed), nil
	case nil, bool, float64:
		return typed, nil
	default:
		return nil, ErrRoutingControlAuditInvalid
	}
}

func routingControlAuditSensitiveKey(key string) bool {
	var normalized strings.Builder
	for _, char := range strings.ToLower(key) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			normalized.WriteRune(char)
		}
	}
	switch normalized.String() {
	case "key", "apikey", "token", "accesstoken", "refreshtoken", "oauthtoken",
		"authorization", "proxyauthorization", "cookie", "cookies", "setcookie",
		"password", "passwd", "pwd", "secret", "secretkey", "clientsecret", "privatekey",
		"accesskey", "accesskeyid", "credential", "credentials", "credentialid", "credentialids",
		"selectedcredentialid", "enccredentials", "apikeyindex", "keyindex",
		"headers", "requestheaders",
		"responseheaders", "requestbody", "responsebody", "rawpayload", "authenticationresponse":
		return true
	default:
		return false
	}
}

func decodeRoutingControlAuditJSON(raw string, required bool) any {
	clean, err := sanitizeRoutingControlAuditJSON(raw, required)
	if err != nil || clean == "" {
		if required {
			return map[string]any{"status": "unavailable"}
		}
		return nil
	}
	var decoded any
	if common.UnmarshalJsonStr(clean, &decoded) != nil {
		if required {
			return map[string]any{"status": "unavailable"}
		}
		return nil
	}
	return decoded
}

func truncateRoutingControlAuditText(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
