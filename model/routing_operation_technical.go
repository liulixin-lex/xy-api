package model

import "github.com/QuantumNous/new-api/common"

type RoutingOperationTechnicalPayload struct {
	IdempotencyHash    string `json:"idempotency_hash"`
	RequestKeyHash     string `json:"request_key_hash,omitempty"`
	RequestPayloadHash string `json:"request_payload_hash,omitempty"`
	EvaluationHash     string `json:"evaluation_hash"`
	SystemTaskID       string `json:"system_task_id,omitempty"`
	ClaimUntilMs       int64  `json:"claim_until_ms,omitempty"`
	ResultOutboxID     int64  `json:"result_outbox_id,omitempty"`
	ResultPayloadHash  string `json:"result_payload_hash,omitempty"`
	Result             any    `json:"result,omitempty"`
}

func (operation RoutingOperation) TechnicalPayload() (RoutingOperationTechnicalPayload, error) {
	payload, err := operation.ResultPayload()
	if err != nil {
		return RoutingOperationTechnicalPayload{}, err
	}
	technical := RoutingOperationTechnicalPayload{
		IdempotencyHash: operation.IdempotencyHash, RequestPayloadHash: operation.RequestPayloadHash,
		EvaluationHash: operation.EvaluationHash, SystemTaskID: operation.SystemTaskID,
		ClaimUntilMs: operation.ClaimUntilMs, ResultOutboxID: operation.ResultOutboxID,
		ResultPayloadHash: operation.ResultPayloadHash,
	}
	if operation.RequestKeyHash != nil {
		technical.RequestKeyHash = *operation.RequestKeyHash
	}
	if len(payload) == 0 {
		return technical, nil
	}
	sanitized, err := sanitizeRoutingControlAuditJSON(string(payload), false)
	if err != nil {
		return RoutingOperationTechnicalPayload{}, err
	}
	if sanitized != "" && common.UnmarshalJsonStr(sanitized, &technical.Result) != nil {
		return RoutingOperationTechnicalPayload{}, ErrRoutingOperationCorrupt
	}
	return technical, nil
}
