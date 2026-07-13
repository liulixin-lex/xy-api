package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	RoutingPolicyDraftStatusEditing   = "editing"
	RoutingPolicyDraftStatusValidated = "validated"
	RoutingPolicyDraftStatusPublished = "published"

	RoutingPolicyDraftMaxPageSize      = 100
	RoutingPolicyDraftMaxDocumentBytes = routingPolicyMaxCanonicalBytes
	routingPolicyDraftSchemaVersion    = 1
)

var (
	ErrRoutingPolicyDraftInvalid   = errors.New("invalid routing policy draft")
	ErrRoutingPolicyDraftNotFound  = errors.New("routing policy draft not found")
	ErrRoutingPolicyDraftConflict  = errors.New("routing policy draft version conflict")
	ErrRoutingPolicyDraftImmutable = errors.New("routing policy draft is immutable")
)

type RoutingPolicyDraftConflictError struct {
	DraftID         int64  `json:"draft_id"`
	ExpectedVersion int64  `json:"expected_version"`
	ActualVersion   int64  `json:"actual_version"`
	ExpectedETag    string `json:"expected_etag"`
	ActualETag      string `json:"actual_etag"`
	ActualStatus    string `json:"actual_status"`
}

func (err *RoutingPolicyDraftConflictError) Error() string {
	if err == nil {
		return ErrRoutingPolicyDraftConflict.Error()
	}
	return fmt.Sprintf(
		"%s: draft_id=%d expected_version=%d actual_version=%d status=%s",
		ErrRoutingPolicyDraftConflict,
		err.DraftID,
		err.ExpectedVersion,
		err.ActualVersion,
		err.ActualStatus,
	)
}

func (err *RoutingPolicyDraftConflictError) Unwrap() error {
	return ErrRoutingPolicyDraftConflict
}

type RoutingPolicyDraft struct {
	ID                    int64  `json:"id" gorm:"primaryKey"`
	BaseRevision          int64  `json:"base_revision" gorm:"bigint;index;not null"`
	BaseHash              string `json:"base_hash" gorm:"type:varchar(64);not null"`
	Version               int64  `json:"version" gorm:"bigint;not null"`
	ETag                  string `json:"etag" gorm:"column:etag;type:char(64);index;not null"`
	DocumentHash          string `json:"document_hash" gorm:"type:char(64);index;not null"`
	DocumentJSON          []byte `json:"-" gorm:"not null"`
	Status                string `json:"status" gorm:"type:varchar(24);index;not null"`
	CreatedBy             int    `json:"created_by" gorm:"index;not null"`
	UpdatedBy             int    `json:"updated_by" gorm:"index;not null"`
	ValidatedHeadRevision int64  `json:"validated_head_revision" gorm:"bigint;index;not null"`
	ValidatedHeadHash     string `json:"validated_head_hash" gorm:"type:varchar(64);not null"`
	PublishedRevision     int64  `json:"published_revision" gorm:"bigint;index;not null"`
	CreatedTimeMs         int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
	UpdatedTimeMs         int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
	ValidatedTimeMs       int64  `json:"validated_time_ms" gorm:"bigint;index;not null"`
	PublishedTimeMs       int64  `json:"published_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingPolicyDraft) TableName() string {
	return "routing_policy_drafts"
}

type RoutingPolicyDraftSummary struct {
	ID                    int64  `json:"id"`
	BaseRevision          int64  `json:"base_revision"`
	BaseHash              string `json:"base_hash"`
	Version               int64  `json:"version"`
	ETag                  string `json:"etag" gorm:"column:etag"`
	DocumentHash          string `json:"document_hash"`
	Status                string `json:"status"`
	CreatedBy             int    `json:"created_by"`
	UpdatedBy             int    `json:"updated_by"`
	ValidatedHeadRevision int64  `json:"validated_head_revision"`
	ValidatedHeadHash     string `json:"validated_head_hash"`
	PublishedRevision     int64  `json:"published_revision"`
	CreatedTimeMs         int64  `json:"created_time_ms"`
	UpdatedTimeMs         int64  `json:"updated_time_ms"`
	ValidatedTimeMs       int64  `json:"validated_time_ms"`
	PublishedTimeMs       int64  `json:"published_time_ms"`
}

func CreateRoutingPolicyDraftContext(
	ctx context.Context,
	baseRevision int64,
	document RoutingPolicyDocument,
	actorID int,
) (RoutingPolicyDraft, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if baseRevision < 0 || actorID < 0 {
		return RoutingPolicyDraft{}, ErrRoutingPolicyDraftInvalid
	}
	_, documentHash, documentJSON, err := normalizeRoutingPolicyDraftDocument(document)
	if err != nil {
		return RoutingPolicyDraft{}, err
	}
	if err := ctx.Err(); err != nil {
		return RoutingPolicyDraft{}, err
	}

	var stored RoutingPolicyDraft
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, headErr := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if headErr != nil {
			return headErr
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if head.CurrentRevision != baseRevision {
			return newRoutingPolicyRevisionConflict(baseRevision, head)
		}
		nowMs := time.Now().UnixMilli()
		draft := RoutingPolicyDraft{
			BaseRevision:  baseRevision,
			BaseHash:      head.CurrentHash,
			Version:       1,
			DocumentHash:  documentHash,
			DocumentJSON:  append([]byte(nil), documentJSON...),
			Status:        RoutingPolicyDraftStatusEditing,
			CreatedBy:     actorID,
			UpdatedBy:     actorID,
			CreatedTimeMs: nowMs,
			UpdatedTimeMs: nowMs,
		}
		draft.ETag, err = routingPolicyDraftETag(draft)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(&draft).Error; err != nil {
			return err
		}
		stored = draft
		return nil
	})
	if err != nil {
		return RoutingPolicyDraft{}, err
	}
	if err := validateStoredRoutingPolicyDraft(stored); err != nil {
		return RoutingPolicyDraft{}, err
	}
	return cloneRoutingPolicyDraft(stored), nil
}

func GetRoutingPolicyDraftContext(ctx context.Context, id int64) (RoutingPolicyDraft, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 {
		return RoutingPolicyDraft{}, ErrRoutingPolicyDraftNotFound
	}
	var draft RoutingPolicyDraft
	err := DB.WithContext(ctx).Where("id = ?", id).First(&draft).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RoutingPolicyDraft{}, ErrRoutingPolicyDraftNotFound
	}
	if err != nil {
		return RoutingPolicyDraft{}, err
	}
	if err := validateStoredRoutingPolicyDraft(draft); err != nil {
		return RoutingPolicyDraft{}, err
	}
	return cloneRoutingPolicyDraft(draft), nil
}

func ListRoutingPolicyDraftsContext(
	ctx context.Context,
	beforeID int64,
	limit int,
) ([]RoutingPolicyDraftSummary, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if beforeID < 0 || limit < 1 || limit > RoutingPolicyDraftMaxPageSize {
		return nil, false, ErrRoutingPolicyDraftInvalid
	}
	query := DB.WithContext(ctx).Model(&RoutingPolicyDraft{}).Order("id desc").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var drafts []RoutingPolicyDraftSummary
	if err := query.Find(&drafts).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(drafts) > limit
	if hasMore {
		drafts = drafts[:limit]
	}
	for index := range drafts {
		if err := validateRoutingPolicyDraftMetadata(drafts[index].draftMetadata()); err != nil {
			return nil, false, err
		}
	}
	return drafts, hasMore, nil
}

func UpdateRoutingPolicyDraftContext(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	expectedETag string,
	document RoutingPolicyDocument,
	actorID int,
) (RoutingPolicyDraft, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || expectedVersion <= 0 || !validRoutingHash(expectedETag) || actorID < 0 {
		return RoutingPolicyDraft{}, ErrRoutingPolicyDraftInvalid
	}
	_, documentHash, documentJSON, err := normalizeRoutingPolicyDraftDocument(document)
	if err != nil {
		return RoutingPolicyDraft{}, err
	}

	var stored RoutingPolicyDraft
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		draft, loadErr := loadRoutingPolicyDraftForUpdate(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}
		if err := requireRoutingPolicyDraftVersion(draft, expectedVersion, expectedETag); err != nil {
			return err
		}
		if draft.Status == RoutingPolicyDraftStatusPublished {
			return ErrRoutingPolicyDraftImmutable
		}
		if draft.Version == math.MaxInt64 {
			return ErrRoutingPolicyDraftInvalid
		}
		draft.Version++
		draft.DocumentHash = documentHash
		draft.DocumentJSON = append([]byte(nil), documentJSON...)
		draft.Status = RoutingPolicyDraftStatusEditing
		draft.UpdatedBy = actorID
		draft.ValidatedHeadRevision = 0
		draft.ValidatedHeadHash = ""
		draft.ValidatedTimeMs = 0
		draft.UpdatedTimeMs = time.Now().UnixMilli()
		draft.ETag, err = routingPolicyDraftETag(draft)
		if err != nil {
			return err
		}
		if err := updateRoutingPolicyDraftCAS(ctx, tx, id, expectedVersion, expectedETag, draft); err != nil {
			return err
		}
		stored = draft
		return nil
	})
	if err != nil {
		return RoutingPolicyDraft{}, err
	}
	return cloneRoutingPolicyDraft(stored), nil
}

func ValidateRoutingPolicyDraftContext(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	expectedETag string,
	actorID int,
) (RoutingPolicyDraft, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || expectedVersion <= 0 || !validRoutingHash(expectedETag) || actorID < 0 {
		return RoutingPolicyDraft{}, ErrRoutingPolicyDraftInvalid
	}

	var stored RoutingPolicyDraft
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		draft, loadErr := loadRoutingPolicyDraftForUpdate(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}
		if err := requireRoutingPolicyDraftVersion(draft, expectedVersion, expectedETag); err != nil {
			return err
		}
		if draft.Status == RoutingPolicyDraftStatusPublished {
			return ErrRoutingPolicyDraftImmutable
		}
		if draft.Version == math.MaxInt64 {
			return ErrRoutingPolicyDraftInvalid
		}
		document, err := draft.Document()
		if err != nil {
			return err
		}
		if err := validateRoutingPolicyPoolIdentitiesTx(tx.WithContext(ctx), document); err != nil {
			return err
		}
		if err := validateRoutingPolicyMemberIdentitiesTx(tx.WithContext(ctx), document); err != nil {
			return err
		}
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if err := validateRoutingPolicyLiveReferencesTx(tx.WithContext(ctx), document); err != nil {
			return err
		}
		nowMs := time.Now().UnixMilli()
		draft.Version++
		draft.Status = RoutingPolicyDraftStatusValidated
		draft.UpdatedBy = actorID
		draft.ValidatedHeadRevision = head.CurrentRevision
		draft.ValidatedHeadHash = head.CurrentHash
		draft.ValidatedTimeMs = nowMs
		draft.UpdatedTimeMs = nowMs
		draft.ETag, err = routingPolicyDraftETag(draft)
		if err != nil {
			return err
		}
		if err := updateRoutingPolicyDraftCAS(ctx, tx, id, expectedVersion, expectedETag, draft); err != nil {
			return err
		}
		stored = draft
		return nil
	})
	if err != nil {
		return RoutingPolicyDraft{}, err
	}
	return cloneRoutingPolicyDraft(stored), nil
}

func PublishRoutingPolicyDraftContext(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	expectedETag string,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyDraft, RoutingPolicyPublishResult, error) {
	draft, published, _, err := publishRoutingPolicyDraftContext(
		ctx, id, expectedVersion, expectedETag, activation, RoutingOperationRequestIdentity{}, false,
	)
	return draft, published, err
}

func PublishRoutingPolicyDraftWithOperationContext(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	expectedETag string,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyDraft, RoutingPolicyPublishResult, RoutingOperation, error) {
	return publishRoutingPolicyDraftContext(
		ctx, id, expectedVersion, expectedETag, activation, RoutingOperationRequestIdentity{}, true,
	)
}

func PublishRoutingPolicyDraftWithOperationRequestContext(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	expectedETag string,
	activation RoutingPolicyActivationSpec,
	requestIdentity RoutingOperationRequestIdentity,
) (RoutingPolicyDraft, RoutingPolicyPublishResult, RoutingOperation, error) {
	if !validRoutingOperationRequestIdentity(requestIdentity) {
		return RoutingPolicyDraft{}, RoutingPolicyPublishResult{}, RoutingOperation{}, ErrRoutingOperationInvalid
	}
	return publishRoutingPolicyDraftContext(
		ctx, id, expectedVersion, expectedETag, activation, requestIdentity, true,
	)
}

func publishRoutingPolicyDraftContext(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	expectedETag string,
	activation RoutingPolicyActivationSpec,
	requestIdentity RoutingOperationRequestIdentity,
	createOperation bool,
) (RoutingPolicyDraft, RoutingPolicyPublishResult, RoutingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || expectedVersion <= 0 || !validRoutingHash(expectedETag) || activation.Validate() != nil {
		return RoutingPolicyDraft{}, RoutingPolicyPublishResult{}, RoutingOperation{}, ErrRoutingPolicyDraftInvalid
	}

	var stored RoutingPolicyDraft
	var published RoutingPolicyPublishResult
	var operation RoutingOperation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		draft, loadErr := loadRoutingPolicyDraftForUpdate(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}
		if createOperation && requestIdentity != (RoutingOperationRequestIdentity{}) {
			existing, found, replayErr := getRoutingOperationByRequestIdentityDB(ctx, tx, requestIdentity)
			if replayErr != nil {
				return replayErr
			}
			if found {
				if existing.OperationType != RoutingOperationTypePolicyPublish ||
					existing.SubjectType != RoutingOperationSubjectPolicyDraft || existing.SubjectID != id ||
					existing.ActorID != activation.ActorID || existing.Status != RoutingOperationStatusSucceeded {
					return ErrRoutingOperationIdempotencyConflict
				}
				published, replayErr = routingPolicyPublishResultForOperationTx(ctx, tx, existing)
				if replayErr != nil {
					return replayErr
				}
				stored = draft
				operation = existing
				return nil
			}
		}
		if err := requireRoutingPolicyDraftVersion(draft, expectedVersion, expectedETag); err != nil {
			return err
		}
		if draft.Status == RoutingPolicyDraftStatusPublished {
			return ErrRoutingPolicyDraftImmutable
		}
		if draft.Status != RoutingPolicyDraftStatusValidated || draft.Version == math.MaxInt64 {
			return ErrRoutingPolicyDraftInvalid
		}
		document, err := draft.Document()
		if err != nil {
			return err
		}
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if head.CurrentRevision != draft.BaseRevision || head.CurrentHash != draft.BaseHash {
			return newRoutingPolicyRevisionConflict(draft.BaseRevision, head)
		}
		if routingPolicyPublishRequiresApproval(document, activation) {
			if _, err := requireRoutingPolicyApprovalQuorumDBContext(
				ctx, tx, draft, activation, RoutingPolicyRequiredApprovals,
			); err != nil {
				return err
			}
		}
		changedPoolIDs := make([]int, len(document.Pools))
		for index := range document.Pools {
			changedPoolIDs[index] = document.Pools[index].PoolID
		}
		published, err = publishNormalizedRoutingPolicyRevisionTx(
			ctx,
			tx,
			draft.BaseRevision,
			0,
			document,
			draft.DocumentHash,
			activation,
			changedPoolIDs,
			common.GetTimestamp(),
		)
		if err != nil {
			return err
		}
		nowMs := time.Now().UnixMilli()
		draft.Version++
		draft.Status = RoutingPolicyDraftStatusPublished
		draft.UpdatedBy = activation.ActorID
		draft.PublishedRevision = published.Revision.Revision
		draft.PublishedTimeMs = nowMs
		draft.UpdatedTimeMs = nowMs
		draft.ETag, err = routingPolicyDraftETag(draft)
		if err != nil {
			return err
		}
		if err := updateRoutingPolicyDraftCAS(ctx, tx, id, expectedVersion, expectedETag, draft); err != nil {
			return err
		}
		if createOperation {
			evaluationHash, hashErr := routingPolicyDraftPublishOperationHash(draft, expectedVersion, expectedETag, activation)
			if hashErr != nil {
				return hashErr
			}
			operation, _, err = createSucceededRoutingOperationTx(
				ctx,
				tx,
				RoutingOperationSpec{
					Type: RoutingOperationTypePolicyPublish, EvaluationHash: evaluationHash,
					SubjectType: RoutingOperationSubjectPolicyDraft, SubjectID: draft.ID,
					ExpectedRevision: head.CurrentRevision, ExpectedActivationID: head.CurrentActivationID,
					ActorID: activation.ActorID, Reason: activation.Reason,
					RequestKeyHash: requestIdentity.KeyHash, RequestPayloadHash: requestIdentity.PayloadHash,
				},
				RoutingOperationResult{
					Revision:     published.Revision.Revision,
					ActivationID: published.Activation.ID,
					OutboxID:     published.Outbox.ID,
				},
				struct {
					DraftID      int64 `json:"draft_id"`
					DraftVersion int64 `json:"draft_version"`
				}{DraftID: draft.ID, DraftVersion: draft.Version},
				nowMs,
			)
			if err != nil {
				return err
			}
		}
		stored = draft
		return nil
	})
	if err != nil {
		return RoutingPolicyDraft{}, RoutingPolicyPublishResult{}, RoutingOperation{}, err
	}
	return cloneRoutingPolicyDraft(stored), published, operation, nil
}

func routingPolicyPublishRequiresApproval(
	document RoutingPolicyDocument,
	activation RoutingPolicyActivationSpec,
) bool {
	if activation.Stage == RoutingDeploymentStageActive {
		return true
	}
	for index := range document.Pools {
		if document.Pools[index].PolicyProfile == RoutingPolicyProfileEnterpriseSLO {
			return true
		}
	}
	return false
}

func RoutingPolicyDeploymentRequiresApproval(
	document RoutingPolicyDocument,
	activation RoutingPolicyActivationSpec,
) (bool, error) {
	if err := ValidateRoutingPolicyActivationDocument(document, activation); err != nil {
		return false, err
	}
	return routingPolicyPublishRequiresApproval(document, activation), nil
}

func routingPolicyDraftPublishOperationHash(
	draft RoutingPolicyDraft,
	expectedVersion int64,
	expectedETag string,
	activation RoutingPolicyActivationSpec,
) (string, error) {
	payload, err := common.Marshal(struct {
		SchemaVersion      int    `json:"schema_version"`
		DraftID            int64  `json:"draft_id"`
		ExpectedVersion    int64  `json:"expected_version"`
		ExpectedETag       string `json:"expected_etag"`
		DocumentHash       string `json:"document_hash"`
		Stage              string `json:"stage"`
		TrafficBasisPoints int    `json:"traffic_basis_points"`
		ActorID            int    `json:"actor_id"`
		Reason             string `json:"reason"`
	}{
		SchemaVersion: 1, DraftID: draft.ID, ExpectedVersion: expectedVersion,
		ExpectedETag: expectedETag, DocumentHash: draft.DocumentHash, Stage: activation.Stage,
		TrafficBasisPoints: activation.TrafficBasisPoints, ActorID: activation.ActorID, Reason: activation.Reason,
	})
	if err != nil {
		return "", err
	}
	return routingPolicyHash(payload), nil
}

func (draft RoutingPolicyDraft) Document() (RoutingPolicyDocument, error) {
	if err := validateStoredRoutingPolicyDraft(draft); err != nil {
		return RoutingPolicyDocument{}, err
	}
	var document RoutingPolicyDocument
	if err := common.Unmarshal(draft.DocumentJSON, &document); err != nil {
		return RoutingPolicyDocument{}, ErrRoutingPolicyDraftInvalid
	}
	return document, nil
}

func (draft RoutingPolicyDraft) Summary() RoutingPolicyDraftSummary {
	return draft.summary()
}

func normalizeRoutingPolicyDraftDocument(
	document RoutingPolicyDocument,
) (RoutingPolicyDocument, string, []byte, error) {
	normalized, documentHash, err := normalizeRoutingPolicyDocument(document)
	if err != nil {
		return RoutingPolicyDocument{}, "", nil, err
	}
	canonical, err := common.Marshal(normalized)
	if err != nil || len(canonical) == 0 || len(canonical) > routingPolicyMaxCanonicalBytes {
		return RoutingPolicyDocument{}, "", nil, ErrRoutingPolicyDraftInvalid
	}
	return normalized, documentHash, canonical, nil
}

func loadRoutingPolicyDraftForUpdate(ctx context.Context, tx *gorm.DB, id int64) (RoutingPolicyDraft, error) {
	var draft RoutingPolicyDraft
	err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", id).First(&draft).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RoutingPolicyDraft{}, ErrRoutingPolicyDraftNotFound
	}
	if err != nil {
		return RoutingPolicyDraft{}, err
	}
	if err := validateStoredRoutingPolicyDraft(draft); err != nil {
		return RoutingPolicyDraft{}, err
	}
	return draft, nil
}

func requireRoutingPolicyDraftVersion(
	draft RoutingPolicyDraft,
	expectedVersion int64,
	expectedETag string,
) error {
	if draft.Version == expectedVersion && draft.ETag == expectedETag {
		return nil
	}
	return &RoutingPolicyDraftConflictError{
		DraftID:         draft.ID,
		ExpectedVersion: expectedVersion,
		ActualVersion:   draft.Version,
		ExpectedETag:    expectedETag,
		ActualETag:      draft.ETag,
		ActualStatus:    draft.Status,
	}
}

func updateRoutingPolicyDraftCAS(
	ctx context.Context,
	tx *gorm.DB,
	id int64,
	expectedVersion int64,
	expectedETag string,
	draft RoutingPolicyDraft,
) error {
	updated := tx.WithContext(ctx).Model(&RoutingPolicyDraft{}).
		Where("id = ? AND version = ? AND etag = ?", id, expectedVersion, expectedETag).
		Updates(map[string]any{
			"version":                 draft.Version,
			"etag":                    draft.ETag,
			"document_hash":           draft.DocumentHash,
			"document_json":           draft.DocumentJSON,
			"status":                  draft.Status,
			"updated_by":              draft.UpdatedBy,
			"validated_head_revision": draft.ValidatedHeadRevision,
			"validated_head_hash":     draft.ValidatedHeadHash,
			"published_revision":      draft.PublishedRevision,
			"updated_time_ms":         draft.UpdatedTimeMs,
			"validated_time_ms":       draft.ValidatedTimeMs,
			"published_time_ms":       draft.PublishedTimeMs,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		var actual RoutingPolicyDraft
		if err := tx.WithContext(ctx).Where("id = ?", id).First(&actual).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoutingPolicyDraftNotFound
			}
			return err
		}
		return requireRoutingPolicyDraftVersion(actual, expectedVersion, expectedETag)
	}
	return nil
}

func validateStoredRoutingPolicyDraft(draft RoutingPolicyDraft) error {
	if err := validateRoutingPolicyDraftMetadata(draft.summary().draftMetadata()); err != nil {
		return err
	}
	if len(draft.DocumentJSON) == 0 || len(draft.DocumentJSON) > routingPolicyMaxCanonicalBytes {
		return ErrRoutingPolicyDraftInvalid
	}
	var document RoutingPolicyDocument
	if err := common.Unmarshal(draft.DocumentJSON, &document); err != nil {
		return ErrRoutingPolicyDraftInvalid
	}
	_, documentHash, canonical, err := normalizeRoutingPolicyDraftDocument(document)
	if err != nil || documentHash != draft.DocumentHash || !bytes.Equal(canonical, draft.DocumentJSON) {
		return ErrRoutingPolicyDraftInvalid
	}
	return nil
}

func validateRoutingPolicyDraftMetadata(draft RoutingPolicyDraft) error {
	if draft.ID <= 0 || draft.BaseRevision < 0 || draft.Version <= 0 || draft.CreatedBy < 0 || draft.UpdatedBy < 0 ||
		draft.CreatedTimeMs <= 0 || draft.UpdatedTimeMs < draft.CreatedTimeMs || !validRoutingHash(draft.DocumentHash) ||
		!validRoutingHash(draft.ETag) {
		return ErrRoutingPolicyDraftInvalid
	}
	if draft.BaseRevision == 0 {
		if draft.BaseHash != "" {
			return ErrRoutingPolicyDraftInvalid
		}
	} else if !validRoutingHash(draft.BaseHash) {
		return ErrRoutingPolicyDraftInvalid
	}
	switch draft.Status {
	case RoutingPolicyDraftStatusEditing:
		if draft.ValidatedHeadRevision != 0 || draft.ValidatedHeadHash != "" || draft.PublishedRevision != 0 ||
			draft.ValidatedTimeMs != 0 || draft.PublishedTimeMs != 0 {
			return ErrRoutingPolicyDraftInvalid
		}
	case RoutingPolicyDraftStatusValidated:
		if draft.ValidatedHeadRevision < 0 || draft.PublishedRevision != 0 || draft.ValidatedTimeMs <= 0 ||
			draft.PublishedTimeMs != 0 ||
			(draft.ValidatedHeadRevision == 0 && draft.ValidatedHeadHash != "") ||
			(draft.ValidatedHeadRevision > 0 && !validRoutingHash(draft.ValidatedHeadHash)) {
			return ErrRoutingPolicyDraftInvalid
		}
	case RoutingPolicyDraftStatusPublished:
		if draft.ValidatedTimeMs <= 0 || draft.PublishedRevision <= 0 || draft.PublishedTimeMs <= 0 ||
			draft.PublishedTimeMs < draft.ValidatedTimeMs {
			return ErrRoutingPolicyDraftInvalid
		}
	default:
		return ErrRoutingPolicyDraftInvalid
	}
	etag, err := routingPolicyDraftETag(draft)
	if err != nil || etag != draft.ETag {
		return ErrRoutingPolicyDraftInvalid
	}
	return nil
}

func (draft RoutingPolicyDraft) summary() RoutingPolicyDraftSummary {
	return RoutingPolicyDraftSummary{
		ID: draft.ID, BaseRevision: draft.BaseRevision, BaseHash: draft.BaseHash,
		Version: draft.Version, ETag: draft.ETag, DocumentHash: draft.DocumentHash, Status: draft.Status,
		CreatedBy: draft.CreatedBy, UpdatedBy: draft.UpdatedBy,
		ValidatedHeadRevision: draft.ValidatedHeadRevision, ValidatedHeadHash: draft.ValidatedHeadHash,
		PublishedRevision: draft.PublishedRevision, CreatedTimeMs: draft.CreatedTimeMs, UpdatedTimeMs: draft.UpdatedTimeMs,
		ValidatedTimeMs: draft.ValidatedTimeMs, PublishedTimeMs: draft.PublishedTimeMs,
	}
}

func (draft RoutingPolicyDraftSummary) draftMetadata() RoutingPolicyDraft {
	return RoutingPolicyDraft{
		ID: draft.ID, BaseRevision: draft.BaseRevision, BaseHash: draft.BaseHash,
		Version: draft.Version, ETag: draft.ETag, DocumentHash: draft.DocumentHash, Status: draft.Status,
		CreatedBy: draft.CreatedBy, UpdatedBy: draft.UpdatedBy,
		ValidatedHeadRevision: draft.ValidatedHeadRevision, ValidatedHeadHash: draft.ValidatedHeadHash,
		PublishedRevision: draft.PublishedRevision, CreatedTimeMs: draft.CreatedTimeMs, UpdatedTimeMs: draft.UpdatedTimeMs,
		ValidatedTimeMs: draft.ValidatedTimeMs, PublishedTimeMs: draft.PublishedTimeMs,
	}
}

func routingPolicyDraftETag(draft RoutingPolicyDraft) (string, error) {
	if draft.BaseRevision < 0 || draft.Version <= 0 || !validRoutingHash(draft.DocumentHash) {
		return "", ErrRoutingPolicyDraftInvalid
	}
	payload, err := common.Marshal(struct {
		SchemaVersion         int    `json:"schema_version"`
		BaseRevision          int64  `json:"base_revision"`
		BaseHash              string `json:"base_hash"`
		Version               int64  `json:"version"`
		DocumentHash          string `json:"document_hash"`
		Status                string `json:"status"`
		ValidatedHeadRevision int64  `json:"validated_head_revision"`
		ValidatedHeadHash     string `json:"validated_head_hash"`
		PublishedRevision     int64  `json:"published_revision"`
	}{
		SchemaVersion:         routingPolicyDraftSchemaVersion,
		BaseRevision:          draft.BaseRevision,
		BaseHash:              draft.BaseHash,
		Version:               draft.Version,
		DocumentHash:          draft.DocumentHash,
		Status:                draft.Status,
		ValidatedHeadRevision: draft.ValidatedHeadRevision,
		ValidatedHeadHash:     draft.ValidatedHeadHash,
		PublishedRevision:     draft.PublishedRevision,
	})
	if err != nil {
		return "", err
	}
	return routingPolicyHash(payload), nil
}

func cloneRoutingPolicyDraft(draft RoutingPolicyDraft) RoutingPolicyDraft {
	draft.DocumentJSON = append([]byte(nil), draft.DocumentJSON...)
	return draft
}
