package model

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingPolicyMatchingFailedSimulationRequiresAtomicRiskAcceptance(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	draft, err := CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 2,
	)
	require.NoError(t, err)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	evidence, err := CreateRoutingPolicySimulationEvidenceContext(
		context.Background(), RoutingPolicySimulationEvidenceSpec{
			OperationID: 99, Draft: draft.Summary(), Head: head, TargetBound: true,
			TargetStage: RoutingDeploymentStageActive,
			RiskState:   RoutingPolicySimulationRiskFail,
			RiskPayload: map[string]any{"state": "fail", "reason": "capacity_insufficient"},
		},
	)
	require.NoError(t, err)

	activation := RoutingPolicyActivationSpec{
		Stage: RoutingDeploymentStageActive, ActorID: 7, Reason: "deploy with known risk",
	}
	requestIdentity := RoutingOperationRequestIdentity{
		KeyHash: strings.Repeat("a", 64), PayloadHash: strings.Repeat("b", 64),
	}
	_, _, _, err = PublishRoutingPolicyDraftWithOperationRequestAndRiskContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation,
		requestIdentity, RoutingPolicyRiskAcceptanceSpec{},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicySimulationRiskAcceptanceRequired)
	unchanged, err := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusValidated, unchanged.Status)
	assert.Equal(t, draft.Version, unchanged.Version)
	var acceptanceCount int64
	require.NoError(t, db.Model(&RoutingPolicyRiskAcceptance{}).Count(&acceptanceCount).Error)
	assert.Zero(t, acceptanceCount)

	publishedDraft, published, operation, err := PublishRoutingPolicyDraftWithOperationRequestAndRiskContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation,
		requestIdentity, RoutingPolicyRiskAcceptanceSpec{
			Accepted: true, Reason: "Capacity evidence is incomplete; monitored rollout is authorized.",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusPublished, publishedDraft.Status)
	assert.Equal(t, RoutingDeploymentStageActive, published.Activation.Stage)
	assert.Equal(t, RoutingOperationStatusSucceeded, operation.Status)

	var acceptance RoutingPolicyRiskAcceptance
	require.NoError(t, db.First(&acceptance).Error)
	assert.Equal(t, evidence.ID, acceptance.EvidenceID)
	assert.Equal(t, evidence.RiskHash, acceptance.RiskHash)
	assert.Equal(t, published.Revision.Revision, acceptance.PublishedRevision)
	assert.Equal(t, activation.ActorID, acceptance.ActorID)
	assert.ErrorIs(t, db.Model(&RoutingPolicyRiskAcceptance{}).Where("id = ?", acceptance.ID).
		Update("reason", "tampered").Error, ErrRoutingPolicyHistoryImmutable)
	assert.ErrorIs(t, db.Where("id = ?", acceptance.ID).Delete(&RoutingPolicyRiskAcceptance{}).Error, ErrRoutingPolicyHistoryImmutable)
}

func TestRoutingPolicySimulationUnknownOrTargetMismatchDoesNotBlockPublish(t *testing.T) {
	for _, test := range []struct {
		name        string
		riskState   string
		targetBound bool
		targetStage string
	}{
		{name: "unknown evidence", riskState: RoutingPolicySimulationRiskUnknown, targetBound: true, targetStage: RoutingDeploymentStageActive},
		{name: "failed evidence for another target", riskState: RoutingPolicySimulationRiskFail, targetBound: true, targetStage: RoutingDeploymentStageShadow},
		{name: "unbound failed evidence", riskState: RoutingPolicySimulationRiskFail, targetBound: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			require.NoError(t, migrateRoutingPolicyModelsForTest(db))
			require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
			base, err := PublishRoutingPolicyRevisionContext(
				context.Background(), 0, routingPolicyDocumentForTest(100),
				RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
			)
			require.NoError(t, err)
			draft, err := CreateRoutingPolicyDraftContext(
				context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(200), 2,
			)
			require.NoError(t, err)
			draft, err = ValidateRoutingPolicyDraftContext(
				context.Background(), draft.ID, draft.Version, draft.ETag, 2,
			)
			require.NoError(t, err)
			head, err := GetRoutingPolicyHeadContext(context.Background())
			require.NoError(t, err)
			_, err = CreateRoutingPolicySimulationEvidenceContext(
				context.Background(), RoutingPolicySimulationEvidenceSpec{
					OperationID: 101, Draft: draft.Summary(), Head: head, TargetBound: test.targetBound,
					TargetStage: test.targetStage, RiskState: test.riskState,
					RiskPayload: map[string]any{"state": test.riskState},
				},
			)
			require.NoError(t, err)

			publishedDraft, published, err := PublishRoutingPolicyDraftContext(
				context.Background(), draft.ID, draft.Version, draft.ETag,
				RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 3, Reason: "single admin publish"},
			)
			require.NoError(t, err)
			assert.Equal(t, RoutingPolicyDraftStatusPublished, publishedDraft.Status)
			assert.Equal(t, RoutingDeploymentStageActive, published.Activation.Stage)
		})
	}
}

func TestRoutingPolicyTamperedSimulationEvidenceFailsClosed(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	draft, err := CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 2,
	)
	require.NoError(t, err)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	evidence, err := CreateRoutingPolicySimulationEvidenceContext(
		context.Background(), RoutingPolicySimulationEvidenceSpec{
			OperationID: 301, Draft: draft.Summary(), Head: head, TargetBound: true,
			TargetStage: RoutingDeploymentStageActive, RiskState: RoutingPolicySimulationRiskFail,
			RiskPayload: map[string]any{"state": "fail"},
		},
	)
	require.NoError(t, err)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingPolicySimulationEvidence{}).
		Where("id = ?", evidence.ID).Update("risk_hash", "tampered").Error)

	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 3, Reason: "publish"},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicySimulationEvidenceInvalid)
	stored, loadErr := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
	require.NoError(t, loadErr)
	assert.Equal(t, RoutingPolicyDraftStatusValidated, stored.Status)
}

func TestRoutingPolicyStaleFailedSimulationDoesNotBlockChangedDraft(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	draft, err := CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 2,
	)
	require.NoError(t, err)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	_, err = CreateRoutingPolicySimulationEvidenceContext(
		context.Background(), RoutingPolicySimulationEvidenceSpec{
			OperationID: 302, Draft: draft.Summary(), Head: head, TargetBound: true,
			TargetStage: RoutingDeploymentStageActive, RiskState: RoutingPolicySimulationRiskFail,
			RiskPayload: map[string]any{"state": "fail"},
		},
	)
	require.NoError(t, err)
	draft, err = UpdateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, routingPolicyDocumentForTest(250), 2,
	)
	require.NoError(t, err)
	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 2,
	)
	require.NoError(t, err)

	publishedDraft, _, err := PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 3, Reason: "publish changed draft"},
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusPublished, publishedDraft.Status)
}
