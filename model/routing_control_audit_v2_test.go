package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingControlAuditIsImmutableAndRecursivelyRedacted(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&User{}, &Channel{}, &RoutingChannelLifecycle{}, &RoutingControlAudit{}))
	actor := User{Id: 71, Username: "audit-operator", DisplayName: "Audit Operator", Role: common.RoleAdminUser, Password: "not-used"}
	require.NoError(t, db.Create(&actor).Error)
	channel := Channel{Id: 701, Name: "snapshot-name", Group: "vip", Key: "serving-key"}
	require.NoError(t, db.Create(&channel).Error)

	audit := RoutingControlAudit{
		SubjectType: RoutingControlSubjectChannelConfiguration, SubjectID: int64(channel.Id),
		Action: RoutingControlActionUpdate, ActorID: actor.Id,
		BeforeHash: strings.Repeat("a", 64), AfterHash: strings.Repeat("b", 64),
		SummaryJSON:         `{"changed_keys":["upstream_cost_multiplier"],"api_key":"secret-key","nested":{"token":"secret-token","cookie":"session=secret-cookie","password":"secret-password","secret_key":"secret-signing-key","credential_id":987654,"api_key_index":12}}`,
		SubjectSnapshotJSON: `{"channel_id":701,"document_hash":"` + strings.Repeat("c", 64) + `"}`,
		RelationJSON:        `{"operation_id":17,"outbox_id":23}`,
		TechnicalJSON:       `{"authorization":"Bearer secret-bearer","safe":"visible"}`,
		CreatedTimeMs:       1_000,
	}
	require.NoError(t, db.Create(&audit).Error)
	require.NoError(t, db.Model(&User{}).Where("id = ?", actor.Id).Updates(map[string]any{
		"username": "renamed-operator", "display_name": "Renamed Operator",
	}).Error)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Update("name", "renamed-channel").Error)

	stored, err := GetRoutingControlAuditContext(context.Background(), audit.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingControlAuditSchemaVersion, stored.SchemaVersion)
	assert.Equal(t, "Audit Operator", stored.ActorName)
	assert.Equal(t, "snapshot-name", stored.SubjectName)
	assert.Equal(t, channel.RoutingIdentity, stored.SubjectIdentity)
	assert.Equal(t, channel.RoutingGeneration, stored.SubjectGeneration)
	encoded, err := common.Marshal(struct {
		Public    RoutingControlAuditPublicPayload    `json:"public"`
		Technical RoutingControlAuditTechnicalPayload `json:"technical"`
	}{Public: stored.PublicPayload(), Technical: stored.TechnicalPayload()})
	require.NoError(t, err)
	encodedText := string(encoded)
	for _, secret := range []string{
		"secret-key", "secret-token", "secret-cookie", "secret-password", "secret-signing-key",
		"secret-bearer", "987654", `"api_key_index":12`,
	} {
		assert.NotContains(t, encodedText, secret)
	}
	assert.Contains(t, encodedText, "[redacted]")
	assert.Contains(t, encodedText, "visible")
	publicEncoded, err := common.Marshal(stored.PublicPayload())
	require.NoError(t, err)
	assert.NotContains(t, string(publicEncoded), "document_hash")
	assert.NotContains(t, string(publicEncoded), "outbox_id")
	assert.Contains(t, string(publicEncoded), `"operation_id":17`)
	technicalEncoded, err := common.Marshal(stored.TechnicalPayload())
	require.NoError(t, err)
	assert.Contains(t, string(technicalEncoded), "document_hash")
	assert.Contains(t, string(technicalEncoded), `"outbox_id":23`)

	update := db.Model(&RoutingControlAudit{}).Where("id = ?", audit.ID).Update("subject_name", "tampered")
	require.ErrorIs(t, update.Error, ErrRoutingControlAuditImmutable)
	deletion := db.Where("id = ?", audit.ID).Delete(&RoutingControlAudit{})
	require.ErrorIs(t, deletion.Error, ErrRoutingControlAuditImmutable)
	unchanged, err := GetRoutingControlAuditContext(context.Background(), audit.ID)
	require.NoError(t, err)
	assert.Equal(t, stored.SubjectName, unchanged.SubjectName)
}

func TestRoutingControlPolicyDiffCoversActivationPoolMemberAndPolicyFields(t *testing.T) {
	beforeEnabled := true
	afterEnabled := false
	beforePriority := int64(1)
	afterPriority := int64(2)
	beforeWeight := int64(100)
	afterWeight := int64(0)
	before := RoutingPolicyDocument{SchemaVersion: RoutingPolicySchemaVersion, Pools: []RoutingPolicyPoolContent{{
		PoolID: 11, GroupName: "vip", DisplayName: "VIP", DeploymentStage: RoutingDeploymentStageShadow,
		PolicyProfile: RoutingPolicyProfileBalanced, DefaultEnabled: &beforeEnabled,
		DefaultPriority: &beforePriority, DefaultWeight: &beforeWeight,
		Policy: json.RawMessage(`{"hedge":{"enabled":false},"cost_budget":1,"capacity":{"rpm":100},"slo":{"latency_ms":1000}}`),
		Members: []RoutingPolicyMemberContent{{
			MemberID: 101, ChannelID: 1001, RoutingGeneration: strings.Repeat("a", 32),
			EnabledOverride: &beforeEnabled, PriorityOverride: &beforePriority, WeightOverride: &beforeWeight,
			CredentialIDs: []int{987654}, Overrides: json.RawMessage(`{"region":"a"}`),
		}},
	}}}
	after := RoutingPolicyDocument{SchemaVersion: RoutingPolicySchemaVersion, Pools: []RoutingPolicyPoolContent{{
		PoolID: 11, GroupName: "vip", DisplayName: "VIP", DeploymentStage: RoutingDeploymentStageActive,
		PolicyProfile: RoutingPolicyProfileBalanced, DefaultEnabled: &afterEnabled,
		DefaultPriority: &afterPriority, DefaultWeight: &afterWeight,
		Policy: json.RawMessage(`{"hedge":{"enabled":true},"cost_budget":2,"capacity":{"rpm":200},"slo":{"latency_ms":1000}}`),
		Members: []RoutingPolicyMemberContent{{
			MemberID: 101, ChannelID: 1001, RoutingGeneration: strings.Repeat("a", 32),
			EnabledOverride: &afterEnabled, PriorityOverride: &afterPriority, WeightOverride: &afterWeight,
			CredentialIDs: []int{876543}, Overrides: json.RawMessage(`{"region":"b"}`),
		}},
	}}}

	diff, err := routingControlPolicyDiff(
		before, after, RoutingDeploymentStageShadow, 0, RoutingDeploymentStageActive, 0,
	)
	require.NoError(t, err)
	encoded, err := common.Marshal(diff)
	require.NoError(t, err)
	text := string(encoded)
	for _, field := range []string{
		`"field":"stage"`, `"field":"deployment_stage"`, `"field":"default_weight"`,
		`"field":"policy.hedge"`, `"field":"policy.cost_budget"`, `"field":"policy.capacity"`,
		`"field":"weight_override"`, `"field":"overrides_hash"`,
	} {
		assert.Contains(t, text, field)
	}
	assert.NotContains(t, text, "987654")
	assert.NotContains(t, text, "876543")
	assert.False(t, diff.Truncated)
}

func TestRoutingControlAuditFiltersOperationalMetadata(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingControlAudit{}))
	correlationID := strings.Repeat("c", 32)
	require.NoError(t, db.Create(&RoutingControlAudit{
		SubjectType: RoutingControlSubjectOperation, SubjectID: 11,
		Action: RoutingControlActionRetry, Source: RoutingControlAuditSourceAdmin,
		Result: RoutingControlAuditResultFailed, ActorID: 7, SummaryJSON: `{"status":"failed"}`,
		NeedsAttention: true, CorrelationID: correlationID, CreatedTimeMs: 1_000,
	}).Error)
	require.NoError(t, db.Create(&RoutingControlAudit{
		SubjectType: RoutingControlSubjectRuntimeSettings, SubjectID: 1,
		Action: RoutingControlActionUpdate, Source: RoutingControlAuditSourceSystem,
		Result: RoutingControlAuditResultSucceeded, SummaryJSON: `{"status":"succeeded"}`,
		CreatedTimeMs: 2_000,
	}).Error)

	needsAttention := true
	audits, err := ListRoutingControlAuditsContext(context.Background(), RoutingControlAuditFilter{
		SubjectType: RoutingControlSubjectOperation, Source: RoutingControlAuditSourceAdmin,
		Result: RoutingControlAuditResultFailed, CorrelationID: correlationID,
		NeedsAttention: &needsAttention, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, audits, 1)
	assert.Equal(t, int64(11), audits[0].SubjectID)
}

func TestRoutingPolicyPublicationWritesStructuredControlAudit(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, db.AutoMigrate(&RoutingControlAudit{}))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 10, Reason: "initial shadow"},
	)
	require.NoError(t, err)
	secondDocument := routingPolicyDocumentForTest(0)
	secondDocument.Pools[0].DeploymentStage = RoutingDeploymentStageActive
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, secondDocument,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 10, Reason: "promote active"},
	)
	require.NoError(t, err)

	var audits []RoutingControlAudit
	require.NoError(t, db.Where("subject_type = ?", RoutingControlSubjectPolicyRevision).Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 2)
	latest := audits[1]
	assert.Equal(t, RoutingControlActionPublish, latest.Action)
	assert.Equal(t, second.Revision.Revision, latest.PolicyRevision)
	assert.Equal(t, second.Activation.ID, latest.ActivationID)
	assert.Equal(t, "user-10", latest.ActorName)
	assert.Equal(t, RoutingControlAuditSourceAdmin, latest.Source)
	assert.Equal(t, RoutingControlAuditResultSucceeded, latest.Result)
	assert.Len(t, latest.BeforeHash, 64)
	assert.Len(t, latest.AfterHash, 64)
	payload := latest.PublicPayload()
	encoded, err := common.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"field":"weight_override"`)
	assert.Contains(t, string(encoded), `"runtime_snapshot_rebuild":true`)
}
