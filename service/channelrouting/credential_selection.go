package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"

	"gorm.io/gorm"
)

const (
	credentialExclusionUnavailable = ExclusionReasonCredentialUnavailable
	credentialExclusionRequest     = ExclusionReasonCredentialRequest
)

var ErrPersistedCredentialUnavailable = errors.New("persisted routing credential is unavailable")

func (snapshot *runtimeSnapshot) selectCredential(
	member PoolMemberSnapshot,
	modelName string,
	seed int64,
	excluded map[int]struct{},
	requiredCredentialID int,
	now time.Time,
) (int, string) {
	if snapshot == nil || member.ID <= 0 || member.ChannelID <= 0 {
		return 0, credentialExclusionUnavailable
	}
	channel, exists := snapshot.channelByID[member.ChannelID]
	if !exists {
		return 0, credentialExclusionUnavailable
	}
	if len(member.CredentialIDs) == 0 {
		if channel.CredentialRequired {
			return 0, credentialExclusionUnavailable
		}
		return 0, ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	type rankedCredential struct {
		id       int
		inflight int64
		rank     uint64
	}
	var selected rankedCredential
	found := false
	requestExcluded := false
	for _, credentialID := range member.CredentialIDs {
		if requiredCredentialID > 0 && credentialID != requiredCredentialID {
			continue
		}
		credential, ok := snapshot.credentialByID[credentialID]
		if !ok || credential.ID != credentialID || credential.ChannelID != member.ChannelID || !credential.Operational {
			continue
		}
		if _, skip := excluded[credentialID]; skip {
			requestExcluded = true
			continue
		}
		if _, blocked := CredentialRuntimeBlocked(credentialID, now); blocked {
			continue
		}
		inflight := routingmetrics.StableInflightCount(routingmetrics.StableInflightKey{
			PoolMemberID: member.ID,
			CredentialID: credentialID,
			Model:        modelName,
		})
		candidate := rankedCredential{
			id:       credentialID,
			inflight: max(inflight, 0),
			rank:     credentialSelectionRank(seed, credentialID),
		}
		if !found || candidate.inflight < selected.inflight ||
			(candidate.inflight == selected.inflight && (candidate.rank < selected.rank ||
				(candidate.rank == selected.rank && candidate.id < selected.id))) {
			selected = candidate
			found = true
		}
	}
	if !found {
		if requestExcluded {
			return 0, credentialExclusionRequest
		}
		return 0, credentialExclusionUnavailable
	}
	return selected.id, ""
}

func (snapshot *runtimeSnapshot) hasOperationalCredential(member PoolMemberSnapshot) bool {
	channel, exists := snapshot.channelByID[member.ChannelID]
	if !exists {
		return false
	}
	if len(member.CredentialIDs) == 0 {
		return !channel.CredentialRequired
	}
	for _, credentialID := range member.CredentialIDs {
		credential, ok := snapshot.credentialByID[credentialID]
		if ok && credential.ChannelID == member.ChannelID && credential.Operational {
			return true
		}
	}
	return false
}

// ResolveCredentialKey binds a stable Credential ID to the current channel
// key material without retaining raw keys in the routing snapshot. The stored
// index is only a fast path; fingerprint verification keeps key reordering safe.
func ResolveCredentialKey(channel *model.Channel, credentialID int) (string, int, bool) {
	if channel == nil || channel.Id <= 0 || credentialID <= 0 {
		return "", 0, false
	}
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return "", 0, false
	}
	channelSnapshot, exists := snapshot.channelByID[channel.Id]
	if !exists || channelSnapshot.RoutingGeneration == "" ||
		(channel.RoutingGeneration != "" && channel.RoutingGeneration != channelSnapshot.RoutingGeneration) {
		return "", 0, false
	}
	credential, exists := snapshot.credentialByID[credentialID]
	if !exists || credential.ChannelID != channel.Id || !credential.Operational ||
		credential.Fingerprint == "" || credential.FingerprintVersion != model.RoutingCredentialFingerprintVersion ||
		credential.CurrentOccurrences != 1 {
		return "", 0, false
	}
	keys := channel.GetKeys()
	if len(keys) == 0 {
		return "", 0, false
	}
	matchedIndex := 0
	matchedKey := ""
	matches := 0
	for index, key := range keys {
		if !channelCredentialIndexEnabled(channel, index) || key == "" {
			continue
		}
		fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channelSnapshot.RoutingGeneration, key)
		if err != nil || fingerprint != credential.Fingerprint {
			continue
		}
		matchedIndex = index
		matchedKey = key
		matches++
	}
	if matches != 1 {
		return "", 0, false
	}
	if !channel.ChannelInfo.IsMultiKey {
		matchedIndex = model.RoutingMetricSingleKeyIndex
	}
	return matchedKey, matchedIndex, true
}

// ResolvePersistedCredentialKey resolves a stateful request's credential from
// the database identity. It is intentionally separate from the snapshot-only
// hot path because task continuation already performs database I/O.
func ResolvePersistedCredentialKey(ctx context.Context, channel *model.Channel, credentialID int) (string, int, error) {
	if channel == nil || channel.Id <= 0 || channel.RoutingGeneration == "" || credentialID <= 0 {
		return "", 0, ErrPersistedCredentialUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var credential model.RoutingCredentialRef
	err := model.DB.WithContext(ctx).Where("id = ? AND channel_id = ?", credentialID, channel.Id).First(&credential).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", 0, ErrPersistedCredentialUnavailable
		}
		return "", 0, fmt.Errorf("load persisted routing credential: %w", err)
	}
	if !credential.Active || credential.ChannelGeneration != channel.RoutingGeneration ||
		credential.Fingerprint == "" || credential.FingerprintVersion != model.RoutingCredentialFingerprintVersion ||
		credential.CurrentOccurrences != 1 {
		return "", 0, ErrPersistedCredentialUnavailable
	}

	matchedIndex := 0
	matchedKey := ""
	matches := 0
	for index, key := range channel.GetKeys() {
		if !channelCredentialIndexEnabled(channel, index) || key == "" {
			continue
		}
		fingerprint, fingerprintErr := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, key)
		if fingerprintErr != nil {
			return "", 0, fmt.Errorf("fingerprint persisted routing credential: %w", fingerprintErr)
		}
		if fingerprint != credential.Fingerprint {
			continue
		}
		matchedIndex = index
		matchedKey = key
		matches++
	}
	if matches != 1 {
		return "", 0, ErrPersistedCredentialUnavailable
	}
	if !channel.ChannelInfo.IsMultiKey {
		matchedIndex = model.RoutingMetricSingleKeyIndex
	}
	return matchedKey, matchedIndex, nil
}

// ResolvePersistedCredentialID binds current key material to its stable ID so
// newly-created stateful tasks never persist an array position as identity.
func ResolvePersistedCredentialID(ctx context.Context, channel *model.Channel, key string) (int, error) {
	if channel == nil || channel.Id <= 0 || channel.RoutingGeneration == "" || key == "" {
		return 0, ErrPersistedCredentialUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, key)
	if err != nil {
		return 0, fmt.Errorf("fingerprint current routing credential: %w", err)
	}
	var credential model.RoutingCredentialRef
	err = model.DB.WithContext(ctx).
		Where("channel_id = ? AND channel_generation = ? AND fingerprint = ?", channel.Id, channel.RoutingGeneration, fingerprint).
		First(&credential).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrPersistedCredentialUnavailable
		}
		return 0, fmt.Errorf("load current routing credential: %w", err)
	}
	if !credential.Active || credential.FingerprintVersion != model.RoutingCredentialFingerprintVersion || credential.CurrentOccurrences != 1 {
		return 0, ErrPersistedCredentialUnavailable
	}
	return credential.ID, nil
}

func ResolveUpstreamAccountID(group string, channelID int, modelName string) (int, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || group == "" || channelID <= 0 || modelName == "" {
		return 0, false
	}
	poolID, exists := snapshot.poolByGroup[group]
	if !exists {
		return 0, false
	}
	memberID, exists := snapshot.memberByPoolChannel[poolChannelKey{PoolID: poolID, ChannelID: channelID}]
	if !exists {
		return 0, false
	}
	observation, exists := snapshot.modelByMemberModel[memberModelKey{memberID: memberID, model: modelName}]
	if !exists || observation.upstreamAccountID <= 0 {
		return 0, false
	}
	return observation.upstreamAccountID, true
}

func channelCredentialIndexEnabled(channel *model.Channel, index int) bool {
	if channel == nil || index < 0 {
		return false
	}
	if !channel.ChannelInfo.IsMultiKey {
		return index == 0
	}
	if channel.ChannelInfo.MultiKeyStatusList == nil {
		return true
	}
	status, exists := channel.ChannelInfo.MultiKeyStatusList[index]
	return !exists || status == common.ChannelStatusEnabled
}

func credentialSelectionRank(seed int64, credentialID int) uint64 {
	payload := strconv.FormatInt(seed, 10) + "\x00" + strconv.Itoa(credentialID)
	sum := sha256.Sum256([]byte(payload))
	return binary.BigEndian.Uint64(sum[:8])
}
