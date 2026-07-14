package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/textproto"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"golang.org/x/net/http/httpguts"
)

const (
	AsyncBillingAcceptanceIntentProtocol = 1
	AsyncBillingReplayProtocol           = 1

	maxAsyncBillingIntentBytes        = 1 << 20
	maxAsyncBillingReplayBodyBytes    = 3 << 20
	maxAsyncBillingReplayHeadersBytes = 16 << 10
	maxAsyncBillingRequestScopeBytes  = 191
	maxAsyncBillingRequestPathBytes   = 512
	maxAsyncBillingRequestIDBytes     = 191
	maxAsyncBillingClientIPBytes      = 128
	maxAsyncBillingActionBytes        = 128
	maxAsyncBillingContentBytes       = 8 << 10
	maxAsyncBillingNodeNameBytes      = 128
)

var (
	ErrAsyncBillingIdempotencyConflict = errors.New("async billing idempotency key conflicts with a different request")
	ErrAsyncBillingRequestInProgress   = errors.New("async billing idempotent request is still in progress")
	ErrAsyncBillingRequestReleased     = errors.New("async billing idempotent request was released; use a new idempotency key")
	ErrAsyncBillingReplayUnavailable   = errors.New("async billing replay response is unavailable")
)

// AsyncBillingAcceptedAuditSnapshot freezes the request-scoped fields used by
// accepted and terminal billing projections. It intentionally excludes raw
// credentials and request bodies.
type AsyncBillingAcceptedAuditSnapshot struct {
	RequestID         string             `json:"request_id,omitempty"`
	RequestPath       string             `json:"request_path"`
	ClientIP          string             `json:"client_ip,omitempty"`
	Action            string             `json:"action"`
	Content           string             `json:"content"`
	OriginModelName   string             `json:"origin_model_name"`
	UpstreamModelName string             `json:"upstream_model_name,omitempty"`
	IsModelMapped     bool               `json:"is_model_mapped,omitempty"`
	Group             string             `json:"group"`
	NodeName          string             `json:"node_name,omitempty"`
	ModelPrice        float64            `json:"model_price"`
	ModelRatio        float64            `json:"model_ratio,omitempty"`
	GroupRatio        float64            `json:"group_ratio"`
	UserGroupRatio    *float64           `json:"user_group_ratio,omitempty"`
	OtherRatios       map[string]float64 `json:"other_ratios,omitempty"`
	QuotaClamp        *common.QuotaClamp `json:"quota_clamp,omitempty"`
	AdminInfo         map[string]any     `json:"admin_info,omitempty"`
	DataExportEnabled bool               `json:"data_export_enabled"`
}

type AsyncBillingAcceptanceIntentSpec struct {
	Task       *Task
	Midjourney *Midjourney
	Audit      AsyncBillingAcceptedAuditSnapshot
}

type asyncBillingTaskAcceptanceIntent struct {
	Platform       constant.TaskPlatform `json:"platform"`
	Group          string                `json:"group"`
	Action         string                `json:"action"`
	Status         TaskStatus            `json:"status"`
	Progress       string                `json:"progress"`
	SubmitTime     int64                 `json:"submit_time"`
	Properties     Properties            `json:"properties"`
	BillingContext *TaskBillingContext   `json:"billing_context,omitempty"`
}

type asyncBillingMidjourneyAcceptanceIntent struct {
	Code        int    `json:"code"`
	Action      string `json:"action"`
	Prompt      string `json:"prompt,omitempty"`
	SubmitTime  int64  `json:"submit_time"`
	Status      string `json:"status"`
	Progress    string `json:"progress"`
	Group       string `json:"group"`
	Description string `json:"description,omitempty"`
}

type asyncBillingAcceptanceIntentEnvelope struct {
	ProtocolVersion int                                     `json:"protocol_version"`
	Kind            string                                  `json:"kind"`
	Audit           AsyncBillingAcceptedAuditSnapshot       `json:"audit"`
	Task            *asyncBillingTaskAcceptanceIntent       `json:"task,omitempty"`
	Midjourney      *asyncBillingMidjourneyAcceptanceIntent `json:"midjourney,omitempty"`
}

type AsyncBillingReplaySpec struct {
	StatusCode  int
	ContentType string
	HeadersJSON string
	Body        []byte
}

type AsyncBillingReplayResponse struct {
	StatusCode  int
	ContentType string
	HeadersJSON string
	Body        []byte
}

func validateAsyncBillingAuditSnapshot(snapshot AsyncBillingAcceptedAuditSnapshot) error {
	fields := []struct {
		value string
		limit int
	}{
		{snapshot.RequestID, maxAsyncBillingRequestIDBytes},
		{snapshot.RequestPath, maxAsyncBillingRequestPathBytes},
		{snapshot.ClientIP, maxAsyncBillingClientIPBytes},
		{snapshot.Action, maxAsyncBillingActionBytes},
		{snapshot.Content, maxAsyncBillingContentBytes},
		{snapshot.OriginModelName, 191},
		{snapshot.UpstreamModelName, 191},
		{snapshot.Group, 64},
		{snapshot.NodeName, maxAsyncBillingNodeNameBytes},
	}
	for _, field := range fields {
		if len(field.value) > field.limit || !utf8.ValidString(field.value) || strings.ContainsRune(field.value, '\x00') {
			return ErrAsyncBillingReservationInvariant
		}
	}
	if strings.TrimSpace(snapshot.RequestPath) == "" || strings.TrimSpace(snapshot.Action) == "" ||
		strings.TrimSpace(snapshot.OriginModelName) == "" {
		return ErrAsyncBillingReservationInvariant
	}
	for _, value := range []float64{snapshot.ModelPrice, snapshot.ModelRatio, snapshot.GroupRatio} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return ErrAsyncBillingReservationInvariant
		}
	}
	if snapshot.UserGroupRatio != nil && (math.IsNaN(*snapshot.UserGroupRatio) || math.IsInf(*snapshot.UserGroupRatio, 0) || *snapshot.UserGroupRatio <= 0) {
		return ErrAsyncBillingReservationInvariant
	}
	for key, value := range snapshot.OtherRatios {
		if strings.TrimSpace(key) == "" || len(key) > 128 || !utf8.ValidString(key) ||
			math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return ErrAsyncBillingReservationInvariant
		}
	}
	if snapshot.QuotaClamp != nil && (snapshot.QuotaClamp.Clamped < 0 || snapshot.QuotaClamp.Clamped > common.MaxQuota) {
		return ErrAsyncBillingReservationInvariant
	}
	return nil
}

func freezeAsyncBillingAcceptanceIntent(
	reservation *AsyncBillingReservation,
	attemptSpec AsyncBillingAttemptSpec,
) ([]byte, string, error) {
	if reservation == nil || reservation.ID <= 0 || attemptSpec.AcceptanceIntent == nil ||
		validateAsyncBillingAuditSnapshot(attemptSpec.AcceptanceIntent.Audit) != nil {
		return nil, "", ErrAsyncBillingReservationInvariant
	}
	envelope := asyncBillingAcceptanceIntentEnvelope{
		ProtocolVersion: AsyncBillingAcceptanceIntentProtocol,
		Kind:            reservation.Kind,
		Audit:           attemptSpec.AcceptanceIntent.Audit,
	}
	switch reservation.Kind {
	case AsyncBillingKindTask:
		draft := attemptSpec.AcceptanceIntent.Task
		if draft == nil || draft.Platform == "" || strings.TrimSpace(draft.Action) == "" {
			return nil, "", ErrAsyncBillingReservationInvariant
		}
		envelope.Task = &asyncBillingTaskAcceptanceIntent{
			Platform: draft.Platform, Group: draft.Group, Action: draft.Action,
			Status: draft.Status, Progress: draft.Progress, SubmitTime: draft.SubmitTime,
			Properties: draft.Properties, BillingContext: draft.PrivateData.BillingContext,
		}
	case AsyncBillingKindMidjourney:
		draft := attemptSpec.AcceptanceIntent.Midjourney
		if draft == nil || strings.TrimSpace(draft.Action) == "" {
			return nil, "", ErrAsyncBillingReservationInvariant
		}
		envelope.Midjourney = &asyncBillingMidjourneyAcceptanceIntent{
			Code: draft.Code, Action: draft.Action, Prompt: draft.Prompt,
			SubmitTime: draft.SubmitTime, Status: draft.Status, Progress: draft.Progress,
			Group: draft.Group, Description: draft.Description,
		}
	default:
		return nil, "", ErrAsyncBillingReservationInvariant
	}
	payload, err := common.Marshal(envelope)
	if err != nil || len(payload) == 0 || len(payload) > maxAsyncBillingIntentBytes || !utf8.Valid(payload) {
		return nil, "", ErrAsyncBillingReservationInvariant
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

func thawAsyncBillingAcceptanceIntent(attempt *AsyncBillingAttempt) (*asyncBillingAcceptanceIntentEnvelope, error) {
	if attempt == nil || attempt.IntentProtocol != AsyncBillingAcceptanceIntentProtocol || len(attempt.IntentPayload) == 0 ||
		len(attempt.IntentPayload) > maxAsyncBillingIntentBytes || !utf8.Valid(attempt.IntentPayload) ||
		len(attempt.IntentPayloadHash) != sha256.Size*2 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	digest := sha256.Sum256(attempt.IntentPayload)
	if !strings.EqualFold(attempt.IntentPayloadHash, hex.EncodeToString(digest[:])) {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var envelope asyncBillingAcceptanceIntentEnvelope
	if err := common.Unmarshal(attempt.IntentPayload, &envelope); err != nil {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if envelope.ProtocolVersion != AsyncBillingAcceptanceIntentProtocol ||
		envelope.Kind == "" || validateAsyncBillingAuditSnapshot(envelope.Audit) != nil {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if (envelope.Kind == AsyncBillingKindTask) == (envelope.Task == nil) ||
		(envelope.Kind == AsyncBillingKindMidjourney) == (envelope.Midjourney == nil) {
		return nil, ErrAsyncBillingReservationInvariant
	}
	return &envelope, nil
}

func NormalizeAsyncBillingReplayHeaders(raw string) (http.Header, string, error) {
	headers := make(http.Header)
	if strings.TrimSpace(raw) == "" {
		return headers, "", nil
	}
	if len(raw) > maxAsyncBillingReplayHeadersBytes || !utf8.ValidString(raw) || strings.ContainsRune(raw, '\x00') {
		return nil, "", ErrAsyncBillingReservationInvariant
	}
	var source http.Header
	if err := common.UnmarshalJsonStr(raw, &source); err != nil || len(source) > 64 {
		return nil, "", ErrAsyncBillingReservationInvariant
	}
	for key, values := range source {
		if !httpguts.ValidHeaderFieldName(key) || len(key) > 128 || len(values) > 32 {
			return nil, "", ErrAsyncBillingReservationInvariant
		}
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		switch canonical {
		case "Authorization", "Connection", "Cookie", "Keep-Alive", "Proxy-Authenticate",
			"Proxy-Authorization", "Proxy-Connection", "Set-Cookie", "Te", "Trailer",
			"Transfer-Encoding", "Upgrade":
			return nil, "", ErrAsyncBillingReservationInvariant
		}
		for _, value := range values {
			if len(value) > 4096 || !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) ||
				strings.ContainsAny(value, "\r\n\x00") {
				return nil, "", ErrAsyncBillingReservationInvariant
			}
		}
		switch canonical {
		case "Content-Encoding", "Content-Length", "Content-Type":
			continue
		}
		if len(headers[canonical])+len(values) > 32 {
			return nil, "", ErrAsyncBillingReservationInvariant
		}
		headers[canonical] = append(headers[canonical], values...)
	}
	if len(headers) == 0 {
		return headers, "", nil
	}
	encoded, err := common.Marshal(headers)
	if err != nil || len(encoded) > maxAsyncBillingReplayHeadersBytes {
		return nil, "", ErrAsyncBillingReservationInvariant
	}
	return headers, string(encoded), nil
}

func normalizeAsyncBillingReplaySpec(spec AsyncBillingReplaySpec) (AsyncBillingReplaySpec, string, error) {
	if spec.StatusCode < http.StatusOK || spec.StatusCode >= http.StatusMultipleChoices ||
		len(spec.Body) > maxAsyncBillingReplayBodyBytes ||
		len(spec.HeadersJSON) > maxAsyncBillingReplayHeadersBytes || len(spec.ContentType) > 128 ||
		!utf8.ValidString(spec.ContentType) || !utf8.ValidString(spec.HeadersJSON) ||
		strings.ContainsAny(spec.ContentType, "\r\n\x00") || strings.ContainsRune(spec.HeadersJSON, '\x00') {
		return AsyncBillingReplaySpec{}, "", ErrAsyncBillingReservationInvariant
	}
	spec.ContentType = strings.TrimSpace(spec.ContentType)
	if spec.ContentType == "" {
		spec.ContentType = "application/json"
	}
	if mediaType, _, err := mime.ParseMediaType(spec.ContentType); err != nil || strings.TrimSpace(mediaType) == "" {
		return AsyncBillingReplaySpec{}, "", ErrAsyncBillingReservationInvariant
	}
	_, normalizedHeaders, err := NormalizeAsyncBillingReplayHeaders(spec.HeadersJSON)
	if err != nil {
		return AsyncBillingReplaySpec{}, "", err
	}
	spec.HeadersJSON = normalizedHeaders
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%d\n%s\n%s\n", spec.StatusCode, spec.ContentType, spec.HeadersJSON)
	_, _ = hasher.Write(spec.Body)
	return spec, hex.EncodeToString(hasher.Sum(nil)), nil
}

func legacyAsyncBillingReplayHash(spec AsyncBillingReplaySpec) string {
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%d\n%s\n%s\n", spec.StatusCode, spec.ContentType, spec.HeadersJSON)
	_, _ = hasher.Write(spec.Body)
	return hex.EncodeToString(hasher.Sum(nil))
}

func asyncBillingReplayFromReservation(reservation *AsyncBillingReservation) (*AsyncBillingReplayResponse, error) {
	if reservation == nil || !reservation.ReplayReady || reservation.ReplayProtocol != AsyncBillingReplayProtocol {
		return nil, ErrAsyncBillingReplayUnavailable
	}
	rawSpec := AsyncBillingReplaySpec{
		StatusCode: reservation.ReplayStatusCode, ContentType: reservation.ReplayContentType,
		HeadersJSON: reservation.ReplayHeadersJSON, Body: append([]byte(nil), reservation.ReplayBody...),
	}
	spec, hash, err := normalizeAsyncBillingReplaySpec(rawSpec)
	if err != nil {
		return nil, ErrAsyncBillingReplayUnavailable
	}
	legacySpec := rawSpec
	legacySpec.ContentType = spec.ContentType
	if !strings.EqualFold(hash, reservation.ReplayHash) &&
		!strings.EqualFold(legacyAsyncBillingReplayHash(legacySpec), reservation.ReplayHash) {
		return nil, ErrAsyncBillingReplayUnavailable
	}
	return &AsyncBillingReplayResponse{
		StatusCode: spec.StatusCode, ContentType: spec.ContentType,
		HeadersJSON: spec.HeadersJSON, Body: spec.Body,
	}, nil
}

func GetAsyncBillingReplayResponse(reservation *AsyncBillingReservation) (*AsyncBillingReplayResponse, error) {
	if reservation == nil ||
		(reservation.State != AsyncBillingReservationStateAccepted &&
			reservation.State != AsyncBillingReservationStateTerminal) {
		return nil, ErrAsyncBillingReplayUnavailable
	}
	return asyncBillingReplayFromReservation(reservation)
}

func synthesizeAsyncBillingManualReplay(
	reservation *AsyncBillingReservation,
	intent *asyncBillingAcceptanceIntentEnvelope,
) (AsyncBillingReplaySpec, error) {
	if reservation == nil || intent == nil || reservation.PublicTaskID == "" || intent.Kind != reservation.Kind {
		return AsyncBillingReplaySpec{}, ErrAsyncBillingReservationInvariant
	}
	if reservation.ReplayReady {
		replay, err := asyncBillingReplayFromReservation(reservation)
		if err != nil {
			return AsyncBillingReplaySpec{}, err
		}
		return AsyncBillingReplaySpec{
			StatusCode: replay.StatusCode, ContentType: replay.ContentType,
			HeadersJSON: replay.HeadersJSON, Body: replay.Body,
		}, nil
	}
	var payload any
	switch intent.Kind {
	case AsyncBillingKindTask:
		if intent.Task == nil {
			return AsyncBillingReplaySpec{}, ErrAsyncBillingReservationInvariant
		}
		if intent.Task.Platform == constant.TaskPlatformSuno {
			payload = dto.TaskResponse[string]{Code: "success", Data: reservation.PublicTaskID}
		} else {
			video := dto.NewOpenAIVideo()
			video.ID = reservation.PublicTaskID
			video.TaskID = reservation.PublicTaskID
			video.Model = intent.Audit.OriginModelName
			video.CreatedAt = time.Now().Unix()
			payload = video
		}
	case AsyncBillingKindMidjourney:
		payload = dto.MidjourneyResponse{Code: 1, Description: "manual provider reconciliation confirmed", Result: reservation.PublicTaskID}
	default:
		return AsyncBillingReplaySpec{}, ErrAsyncBillingReservationInvariant
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return AsyncBillingReplaySpec{}, err
	}
	return AsyncBillingReplaySpec{StatusCode: 200, ContentType: "application/json", Body: body}, nil
}

func validAsyncBillingHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
