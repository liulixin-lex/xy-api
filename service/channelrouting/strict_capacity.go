package channelrouting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
)

const (
	CapacityModeRedisStrict CapacityMode = "redis_strict"
	CapacityModeRedisBlock  CapacityMode = "redis_block"

	strictCapacityWindow          = time.Minute
	strictCapacityMinimumLease    = time.Second
	strictCapacityMaximumLease    = 5 * time.Minute
	strictCapacityMaximumLifetime = 24 * time.Hour
	strictCapacityMaximumPools    = 128
	strictCapacityMaximumLeases   = 256
	strictCapacityMaximumFields   = 5_000
	strictCapacityMaximumIdentity = 128
	strictCapacityMaximumValue    = int64(1_000_000_000_000)
)

var (
	ErrStrictCapacityInvalid     = errors.New("invalid strict capacity request")
	ErrStrictCapacityUnavailable = errors.New("strict capacity redis is unavailable")
	ErrStrictCapacityExhausted   = errors.New("strict shared capacity exhausted")
	ErrStrictCapacityConflict    = errors.New("strict capacity configuration conflicts with active state")
	ErrStrictCapacityStateLimit  = errors.New("strict capacity redis state limit reached")
	ErrStrictCapacityLost        = errors.New("strict capacity reservation is unavailable")
	ErrStrictCapacityTransition  = errors.New("invalid strict capacity reservation transition")
)

type StrictCapacityKey struct {
	AccountID    int    `json:"account_id"`
	CredentialID int    `json:"credential_id"`
	Model        string `json:"model"`
}

type StrictCapacityDemand struct {
	RPM         int64 `json:"rpm"`
	InputTPM    int64 `json:"input_tpm"`
	OutputTPM   int64 `json:"output_tpm"`
	TotalTPM    int64 `json:"total_tpm"`
	Inflight    int64 `json:"inflight"`
	CostNanoUSD int64 `json:"cost_nano_usd"`
}

type StrictCapacityLimit = StrictCapacityDemand

type StrictCapacityPoolShare struct {
	PoolID                int `json:"pool_id"`
	GuaranteedBasisPoints int `json:"guaranteed_basis_points"`
	MaximumBasisPoints    int `json:"maximum_basis_points"`
}

type StrictCapacityRequest struct {
	Mode           CapacityMode              `json:"mode"`
	Key            StrictCapacityKey         `json:"key"`
	PoolID         int                       `json:"pool_id"`
	PolicyRevision uint64                    `json:"policy_revision"`
	Demand         StrictCapacityDemand      `json:"demand"`
	Limit          StrictCapacityLimit       `json:"limit"`
	PoolShares     []StrictCapacityPoolShare `json:"pool_shares"`
	LeaseTTL       time.Duration             `json:"lease_ttl"`
}

type StrictCapacityAdmission struct {
	Mode           CapacityMode              `json:"mode"`
	Key            StrictCapacityKey         `json:"key"`
	PoolID         int                       `json:"pool_id"`
	PolicyRevision uint64                    `json:"policy_revision"`
	CapacityEpoch  string                    `json:"capacity_epoch"`
	Demand         StrictCapacityDemand      `json:"demand"`
	Limit          StrictCapacityLimit       `json:"limit"`
	PoolShares     []StrictCapacityPoolShare `json:"pool_shares"`
	LeaseTTLMillis int64                     `json:"lease_ttl_ms"`
	LeaseExpiresMs int64                     `json:"lease_expires_ms"`
	NodeEpochID    string                    `json:"node_epoch_id,omitempty"`
	BlockLease     bool                      `json:"block_lease,omitempty"`
}

type StrictCapacityStats struct {
	Allowed       int64 `json:"allowed"`
	Denied        int64 `json:"denied"`
	Unavailable   int64 `json:"unavailable"`
	Committed     int64 `json:"committed"`
	Canceled      int64 `json:"canceled"`
	Released      int64 `json:"released"`
	Renewed       int64 `json:"renewed"`
	TransitionErr int64 `json:"transition_errors"`
	BlockAllowed  int64 `json:"block_allowed"`
	BlockLeases   int64 `json:"block_leases"`
	BlockRefills  int64 `json:"block_refills"`
	BlockFallback int64 `json:"block_fallback"`
	BlockExpired  int64 `json:"block_expired"`
	BlockFenced   int64 `json:"block_fenced"`
}

type strictCapacityAtomicStats struct {
	allowed       atomic.Int64
	denied        atomic.Int64
	unavailable   atomic.Int64
	committed     atomic.Int64
	canceled      atomic.Int64
	released      atomic.Int64
	renewed       atomic.Int64
	transitionErr atomic.Int64
	blockAllowed  atomic.Int64
	blockLeases   atomic.Int64
	blockRefills  atomic.Int64
	blockFallback atomic.Int64
	blockExpired  atomic.Int64
	blockFenced   atomic.Int64
}

type strictCapacityRedis interface {
	Eval(context.Context, string, []string, ...interface{}) *redis.Cmd
}

type StrictCapacityCoordinator struct {
	client strictCapacityRedis
	stats  strictCapacityAtomicStats
	blocks *strictCapacityBlockManager
}

var (
	defaultStrictCapacityMu          sync.Mutex
	defaultStrictCapacityClient      *redis.Client
	defaultStrictCapacityCoordinator *StrictCapacityCoordinator
	defaultStrictCapacityOverride    bool
)

type strictCapacityReservationState uint8

const (
	strictCapacityReservationPending strictCapacityReservationState = iota
	strictCapacityReservationCommitted
	strictCapacityReservationCanceled
	strictCapacityReservationReleased
)

type StrictCapacityReservation struct {
	coordinator *StrictCapacityCoordinator
	capacityKey string
	leaseKey    string
	token       string
	admission   StrictCapacityAdmission
	block       *strictCapacityBlockReservation

	mu    sync.Mutex
	state strictCapacityReservationState
}

func NewStrictCapacityCoordinator(client strictCapacityRedis) *StrictCapacityCoordinator {
	coordinator := &StrictCapacityCoordinator{client: client}
	coordinator.blocks = newStrictCapacityBlockManager(coordinator)
	return coordinator
}

func DefaultStrictCapacityCoordinator() *StrictCapacityCoordinator {
	var client *redis.Client
	if common.RedisEnabled {
		client = common.RDB
	}
	defaultStrictCapacityMu.Lock()
	defer defaultStrictCapacityMu.Unlock()
	if defaultStrictCapacityOverride && defaultStrictCapacityCoordinator != nil {
		return defaultStrictCapacityCoordinator
	}
	if defaultStrictCapacityCoordinator == nil || defaultStrictCapacityClient != client {
		defaultStrictCapacityClient = client
		defaultStrictCapacityCoordinator = NewStrictCapacityCoordinator(client)
	}
	return defaultStrictCapacityCoordinator
}

func SetDefaultStrictCapacityCoordinatorForTest(coordinator *StrictCapacityCoordinator) func() {
	defaultStrictCapacityMu.Lock()
	previousClient := defaultStrictCapacityClient
	previousCoordinator := defaultStrictCapacityCoordinator
	previousOverride := defaultStrictCapacityOverride
	defaultStrictCapacityClient = nil
	defaultStrictCapacityCoordinator = coordinator
	defaultStrictCapacityOverride = true
	defaultStrictCapacityMu.Unlock()
	return func() {
		defaultStrictCapacityMu.Lock()
		defaultStrictCapacityClient = previousClient
		defaultStrictCapacityCoordinator = previousCoordinator
		defaultStrictCapacityOverride = previousOverride
		defaultStrictCapacityMu.Unlock()
	}
}

func DefaultStrictCapacityStats() StrictCapacityStats {
	defaultStrictCapacityMu.Lock()
	coordinator := defaultStrictCapacityCoordinator
	defaultStrictCapacityMu.Unlock()
	return coordinator.Stats()
}

func (coordinator *StrictCapacityCoordinator) Stats() StrictCapacityStats {
	if coordinator == nil {
		return StrictCapacityStats{}
	}
	return StrictCapacityStats{
		Allowed: coordinator.stats.allowed.Load(), Denied: coordinator.stats.denied.Load(),
		Unavailable: coordinator.stats.unavailable.Load(), Committed: coordinator.stats.committed.Load(),
		Canceled: coordinator.stats.canceled.Load(), Released: coordinator.stats.released.Load(),
		Renewed:       coordinator.stats.renewed.Load(),
		TransitionErr: coordinator.stats.transitionErr.Load(),
		BlockAllowed:  coordinator.stats.blockAllowed.Load(),
		BlockLeases:   coordinator.stats.blockLeases.Load(),
		BlockRefills:  coordinator.stats.blockRefills.Load(),
		BlockFallback: coordinator.stats.blockFallback.Load(),
		BlockExpired:  coordinator.stats.blockExpired.Load(),
		BlockFenced:   coordinator.stats.blockFenced.Load(),
	}
}

func (coordinator *StrictCapacityCoordinator) TryReserve(
	ctx context.Context,
	request StrictCapacityRequest,
) (*StrictCapacityReservation, error) {
	if coordinator == nil || coordinator.client == nil {
		if coordinator != nil {
			coordinator.stats.unavailable.Add(1)
		}
		return nil, ErrStrictCapacityUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeStrictCapacityRequest(request)
	if err != nil {
		return nil, err
	}
	if normalized.Mode == CapacityModeRedisBlock {
		return coordinator.blocks.tryReserve(ctx, normalized)
	}
	return coordinator.tryReserveAtomic(ctx, normalized)
}

func (coordinator *StrictCapacityCoordinator) tryReserveAtomic(
	ctx context.Context,
	normalized StrictCapacityRequest,
) (*StrictCapacityReservation, error) {
	token, err := newStrictCapacityToken()
	if err != nil {
		return nil, err
	}
	configHash := strictCapacityConfigurationHash(normalized)
	if configHash == "" {
		return nil, ErrStrictCapacityInvalid
	}
	epoch := strictCapacityEpoch(normalized.PolicyRevision, configHash)
	capacityKey, leaseKey := strictCapacityRedisKeys(normalized.Key)
	arguments := strictCapacityReserveArguments(normalized, token, configHash, epoch)
	result, err := coordinator.client.Eval(
		ctx, strictCapacityReserveScript, []string{capacityKey, leaseKey}, arguments...,
	).Result()
	if err != nil {
		coordinator.stats.unavailable.Add(1)
		return nil, fmt.Errorf("%w: %v", ErrStrictCapacityUnavailable, err)
	}
	code, leaseExpiresMs, ok := strictCapacityReserveResult(result)
	if !ok {
		coordinator.stats.unavailable.Add(1)
		return nil, ErrStrictCapacityUnavailable
	}
	switch code {
	case 1:
		if normalized.Mode == CapacityModeRedisBlock {
			coordinator.stats.blockLeases.Add(1)
		} else {
			coordinator.stats.allowed.Add(1)
		}
		return &StrictCapacityReservation{
			coordinator: coordinator, capacityKey: capacityKey, leaseKey: leaseKey, token: token,
			admission: StrictCapacityAdmission{
				Mode: normalized.Mode, Key: normalized.Key, PoolID: normalized.PoolID,
				PolicyRevision: normalized.PolicyRevision, CapacityEpoch: epoch,
				Demand: normalized.Demand, Limit: normalized.Limit,
				PoolShares:     append([]StrictCapacityPoolShare(nil), normalized.PoolShares...),
				LeaseTTLMillis: normalized.LeaseTTL.Milliseconds(),
				LeaseExpiresMs: leaseExpiresMs,
			},
			state: strictCapacityReservationPending,
		}, nil
	case 0:
		coordinator.stats.denied.Add(1)
		return nil, ErrStrictCapacityExhausted
	case 3:
		coordinator.stats.denied.Add(1)
		return nil, ErrStrictCapacityConflict
	case 4:
		coordinator.stats.unavailable.Add(1)
		return nil, ErrStrictCapacityStateLimit
	case 5:
		coordinator.stats.denied.Add(1)
		return nil, ErrStrictCapacityConflict
	default:
		coordinator.stats.unavailable.Add(1)
		return nil, ErrStrictCapacityUnavailable
	}
}

func (reservation *StrictCapacityReservation) Admission() StrictCapacityAdmission {
	if reservation == nil {
		return StrictCapacityAdmission{}
	}
	if reservation.block != nil {
		return reservation.block.admissionSnapshot()
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	admission := reservation.admission
	admission.PoolShares = append([]StrictCapacityPoolShare(nil), reservation.admission.PoolShares...)
	return admission
}

func (reservation *StrictCapacityReservation) Commit(ctx context.Context) error {
	if reservation != nil && reservation.block != nil {
		return reservation.block.commit(ctx)
	}
	return reservation.transition(ctx, strictCapacityCommitScript, strictCapacityReservationCommitted)
}

func (reservation *StrictCapacityReservation) Cancel(ctx context.Context) error {
	if reservation != nil && reservation.block != nil {
		return reservation.block.cancel(ctx)
	}
	return reservation.transition(ctx, strictCapacityCancelScript, strictCapacityReservationCanceled)
}

func (reservation *StrictCapacityReservation) Release(ctx context.Context) error {
	if reservation != nil && reservation.block != nil {
		return reservation.block.release(ctx)
	}
	return reservation.transition(ctx, strictCapacityReleaseScript, strictCapacityReservationReleased)
}

func (reservation *StrictCapacityReservation) Renew(ctx context.Context, leaseTTL time.Duration) error {
	if reservation != nil && reservation.block != nil {
		return reservation.block.renew(ctx, leaseTTL)
	}
	if reservation == nil || reservation.coordinator == nil || reservation.coordinator.client == nil ||
		leaseTTL < strictCapacityMinimumLease || leaseTTL > strictCapacityMaximumLease ||
		leaseTTL.Milliseconds() != reservation.admission.LeaseTTLMillis {
		return ErrStrictCapacityTransition
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state != strictCapacityReservationPending && reservation.state != strictCapacityReservationCommitted {
		reservation.coordinator.stats.transitionErr.Add(1)
		return ErrStrictCapacityTransition
	}
	idleTTL := max(leaseTTL*4, 2*strictCapacityWindow)
	if idleTTL > time.Hour {
		idleTTL = time.Hour
	}
	result, err := reservation.coordinator.client.Eval(
		ctx,
		strictCapacityRenewScript,
		[]string{reservation.capacityKey, reservation.leaseKey},
		reservation.token,
		leaseTTL.Milliseconds(),
		idleTTL.Milliseconds(),
	).Int64()
	if err != nil {
		reservation.coordinator.stats.unavailable.Add(1)
		return fmt.Errorf("%w: %v", ErrStrictCapacityUnavailable, err)
	}
	if result <= 0 {
		reservation.coordinator.stats.transitionErr.Add(1)
		return ErrStrictCapacityLost
	}
	reservation.admission.LeaseExpiresMs = result
	reservation.coordinator.stats.renewed.Add(1)
	return nil
}

func (reservation *StrictCapacityReservation) transition(
	ctx context.Context,
	script string,
	target strictCapacityReservationState,
) error {
	if reservation == nil || reservation.coordinator == nil || reservation.coordinator.client == nil {
		return ErrStrictCapacityTransition
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == target {
		return nil
	}
	valid := (reservation.state == strictCapacityReservationPending &&
		(target == strictCapacityReservationCommitted || target == strictCapacityReservationCanceled)) ||
		(reservation.state == strictCapacityReservationCommitted && target == strictCapacityReservationReleased)
	if !valid {
		reservation.coordinator.stats.transitionErr.Add(1)
		return ErrStrictCapacityTransition
	}
	result, err := reservation.coordinator.client.Eval(
		ctx, script, []string{reservation.capacityKey, reservation.leaseKey}, reservation.token,
	).Int64()
	if err != nil {
		reservation.coordinator.stats.unavailable.Add(1)
		return fmt.Errorf("%w: %v", ErrStrictCapacityUnavailable, err)
	}
	if result != 1 {
		reservation.coordinator.stats.transitionErr.Add(1)
		if result == 0 {
			return ErrStrictCapacityLost
		}
		return ErrStrictCapacityTransition
	}
	reservation.state = target
	if reservation.admission.Mode == CapacityModeRedisBlock {
		return nil
	}
	switch target {
	case strictCapacityReservationCommitted:
		reservation.coordinator.stats.committed.Add(1)
	case strictCapacityReservationCanceled:
		reservation.coordinator.stats.canceled.Add(1)
	case strictCapacityReservationReleased:
		reservation.coordinator.stats.released.Add(1)
	}
	return nil
}

func normalizeStrictCapacityRequest(request StrictCapacityRequest) (StrictCapacityRequest, error) {
	if request.Mode == "" {
		request.Mode = CapacityModeRedisStrict
	}
	request.Key.Model = strings.TrimSpace(request.Key.Model)
	if (request.Mode != CapacityModeRedisStrict && request.Mode != CapacityModeRedisBlock) ||
		request.PoolID <= 0 || request.PolicyRevision == 0 ||
		(request.Key.AccountID <= 0 && request.Key.CredentialID <= 0) ||
		len(request.Key.Model) > strictCapacityMaximumIdentity || !utf8.ValidString(request.Key.Model) ||
		request.LeaseTTL < strictCapacityMinimumLease || request.LeaseTTL > strictCapacityMaximumLease ||
		!validStrictCapacityDemand(request.Demand) || !validStrictCapacityLimit(request.Limit, request.Demand) ||
		len(request.PoolShares) == 0 || len(request.PoolShares) > strictCapacityMaximumPools {
		return StrictCapacityRequest{}, ErrStrictCapacityInvalid
	}
	request.PoolShares = append([]StrictCapacityPoolShare(nil), request.PoolShares...)
	sort.Slice(request.PoolShares, func(i, j int) bool { return request.PoolShares[i].PoolID < request.PoolShares[j].PoolID })
	guaranteedTotal := 0
	targetFound := false
	for index := range request.PoolShares {
		share := request.PoolShares[index]
		if share.PoolID <= 0 || share.GuaranteedBasisPoints < 0 || share.MaximumBasisPoints < 1 ||
			share.MaximumBasisPoints > 10_000 || share.GuaranteedBasisPoints > share.MaximumBasisPoints ||
			(index > 0 && request.PoolShares[index-1].PoolID == share.PoolID) {
			return StrictCapacityRequest{}, ErrStrictCapacityInvalid
		}
		guaranteedTotal += share.GuaranteedBasisPoints
		if guaranteedTotal > 10_000 {
			return StrictCapacityRequest{}, ErrStrictCapacityInvalid
		}
		if share.PoolID == request.PoolID {
			targetFound = true
		}
	}
	if !targetFound {
		return StrictCapacityRequest{}, ErrStrictCapacityInvalid
	}
	return request, nil
}

func validStrictCapacityDemand(demand StrictCapacityDemand) bool {
	values := []int64{demand.RPM, demand.InputTPM, demand.OutputTPM, demand.TotalTPM, demand.Inflight, demand.CostNanoUSD}
	positive := false
	for _, value := range values {
		if value < 0 || value > strictCapacityMaximumValue {
			return false
		}
		positive = positive || value > 0
	}
	return positive
}

func validStrictCapacityLimit(limit StrictCapacityLimit, demand StrictCapacityDemand) bool {
	limits := []int64{limit.RPM, limit.InputTPM, limit.OutputTPM, limit.TotalTPM, limit.Inflight, limit.CostNanoUSD}
	demands := []int64{demand.RPM, demand.InputTPM, demand.OutputTPM, demand.TotalTPM, demand.Inflight, demand.CostNanoUSD}
	for index, value := range limits {
		if value < 0 || value > strictCapacityMaximumValue ||
			(demands[index] > 0 && (value <= 0 || demands[index] > value)) {
			return false
		}
	}
	return true
}

func strictCapacityRedisKeys(key StrictCapacityKey) (string, string) {
	identity := strconv.Itoa(key.AccountID) + "\x00" + strconv.Itoa(key.CredentialID) + "\x00" + key.Model
	digest := sha256.Sum256([]byte(identity))
	tag := hex.EncodeToString(digest[:16])
	return "routing:v2:capacity:{" + tag + "}:state", "routing:v2:capacity:{" + tag + "}:leases"
}

func strictCapacityReserveArguments(
	request StrictCapacityRequest,
	token string,
	configHash string,
	epoch string,
) []interface{} {
	idleTTL := max(request.LeaseTTL*4, 2*strictCapacityWindow)
	if idleTTL > time.Hour {
		idleTTL = time.Hour
	}
	arguments := []interface{}{
		token,
		request.LeaseTTL.Milliseconds(),
		idleTTL.Milliseconds(),
		request.PoolID,
		request.PolicyRevision,
		request.Demand.RPM,
		request.Demand.InputTPM,
		request.Demand.OutputTPM,
		request.Demand.TotalTPM,
		request.Demand.Inflight,
		request.Demand.CostNanoUSD,
		request.Limit.RPM,
		request.Limit.InputTPM,
		request.Limit.OutputTPM,
		request.Limit.TotalTPM,
		request.Limit.Inflight,
		request.Limit.CostNanoUSD,
		len(request.PoolShares),
		configHash,
		epoch,
		strictCapacityMaximumLifetime.Milliseconds(),
	}
	for _, share := range request.PoolShares {
		arguments = append(arguments, share.PoolID, share.GuaranteedBasisPoints, share.MaximumBasisPoints)
	}
	return arguments
}

func strictCapacityConfigurationHash(request StrictCapacityRequest) string {
	payload, err := common.Marshal(struct {
		Version        string                    `json:"version"`
		Mode           CapacityMode              `json:"mode"`
		Key            StrictCapacityKey         `json:"key"`
		Limit          StrictCapacityLimit       `json:"limit"`
		PoolShares     []StrictCapacityPoolShare `json:"pool_shares"`
		LeaseTTLMillis int64                     `json:"lease_ttl_ms"`
	}{
		Version: "redis_token_bucket_v4", Mode: request.Mode, Key: request.Key, Limit: request.Limit,
		PoolShares: request.PoolShares, LeaseTTLMillis: request.LeaseTTL.Milliseconds(),
	})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func strictCapacityEpoch(revision uint64, configHash string) string {
	return strconv.FormatUint(revision, 10) + ":" + configHash
}

func strictCapacityReserveResult(result any) (int64, int64, bool) {
	items, ok := result.([]interface{})
	if !ok || len(items) < 1 {
		return 0, 0, false
	}
	code, ok := strictCapacityInt64(items[0])
	if !ok {
		return 0, 0, false
	}
	if code != 1 {
		return code, 0, true
	}
	if len(items) != 3 {
		return 0, 0, false
	}
	expires, ok := strictCapacityInt64(items[2])
	return code, expires, ok && expires > 0
}

func strictCapacityInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func newStrictCapacityToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

const strictCapacityReserveScript = `
-- strict_capacity_reserve_v2
local state = KEYS[1]
local leases = KEYS[2]
local token = ARGV[1]
local lease_ttl = tonumber(ARGV[2])
local idle_ttl = tonumber(ARGV[3])
local pool = ARGV[4]
local revision = ARGV[5]
local demand = {tonumber(ARGV[6]), tonumber(ARGV[7]), tonumber(ARGV[8]), tonumber(ARGV[9]), tonumber(ARGV[10]), tonumber(ARGV[11])}
local limits = {tonumber(ARGV[12]), tonumber(ARGV[13]), tonumber(ARGV[14]), tonumber(ARGV[15]), tonumber(ARGV[16]), tonumber(ARGV[17])}
local pool_count = tonumber(ARGV[18])
local config_hash = ARGV[19]
local epoch = ARGV[20]
local maximum_lifetime = tonumber(ARGV[21])
local dimensions = {'rpm', 'input', 'output', 'total', 'inflight', 'cost'}
local rate_dimensions = {1, 2, 3, 4, 6}
local refill_period = 60000
local activity_window = 60000
local maximum_leases = 256
local maximum_fields = 5000
local lease_fields = 12
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

local function number(field)
  return tonumber(redis.call('HGET', state, field) or '0')
end

local function revision_greater(left, right)
  if string.len(left) ~= string.len(right) then return string.len(left) > string.len(right) end
  return left > right
end

local function extend_ttl(key, ttl)
  local current = redis.call('PTTL', key)
  if current < 0 or current < ttl then redis.call('PEXPIRE', key, ttl) end
end

local function subtract(field, amount)
  if amount <= 0 then return end
  local current = number(field) - amount
  if current < 0 then current = 0 end
  redis.call('HSET', state, field, current)
end

local function refill(scope, capacity)
  if capacity <= 0 then return 0 end
  local tokens_field = scope .. ':tokens'
  local updated_field = scope .. ':updated'
  local stored_tokens = redis.call('HGET', state, tokens_field)
  local tokens = capacity
  if stored_tokens then tokens = tonumber(stored_tokens) or 0 end
  local updated = number(updated_field)
  if updated > 0 and now > updated then
    tokens = math.min(capacity, tokens + (now - updated) * capacity / refill_period)
  else
    tokens = math.min(capacity, tokens)
  end
  redis.call('HSET', state, tokens_field, tokens, updated_field, now)
  return tokens
end

local function epoch_prefix(epoch_id)
  return 'e:' .. epoch_id .. ':'
end

local function pool_capacity(epoch_id, pool_id, name)
  local prefix = epoch_prefix(epoch_id)
  local maximum = number(prefix .. 'p:' .. pool_id .. ':maximum')
  local epoch_limit = number(prefix .. 'limit:' .. name)
  return math.floor(epoch_limit * maximum / 10000)
end

local function refund(scope, capacity, amount)
  if amount <= 0 or capacity <= 0 then return end
  local tokens = refill(scope, capacity)
  redis.call('HSET', state, scope .. ':tokens', math.min(capacity, tokens + amount))
end

local function cleanup_epoch(epoch_id)
  if not epoch_id or epoch_id == '' then return end
  local prefix = epoch_prefix(epoch_id)
  if number(prefix .. 'leases') > 0 then return end
  if redis.call('HGET', state, 'active:epoch') == epoch_id then return end
  local fields = redis.call('HKEYS', state)
  for _, field in ipairs(fields) do
    if string.sub(field, 1, string.len(prefix)) == prefix then
      redis.call('HDEL', state, field)
    end
  end
end

local function remove_epoch_lease(epoch_id)
  if not epoch_id or epoch_id == '' then return end
  local field = epoch_prefix(epoch_id) .. 'leases'
  local remaining = math.max(0, number(field) - 1)
  redis.call('HSET', state, field, remaining)
  cleanup_epoch(epoch_id)
end

local expired = redis.call('ZRANGEBYSCORE', leases, '-inf', now, 'LIMIT', 0, maximum_leases)
for _, expired_token in ipairs(expired) do
  local prefix = 'r:' .. expired_token .. ':'
  local values = redis.call('HMGET', state,
    prefix .. 'pool', prefix .. 'state', prefix .. 'epoch', prefix .. 'rpm', prefix .. 'input',
    prefix .. 'output', prefix .. 'total', prefix .. 'inflight', prefix .. 'cost')
  if values[1] then
    local expired_pool = values[1]
    local expired_epoch = values[3]
    if values[2] == 'pending' then
      for _, dimension in ipairs(rate_dimensions) do
        local name = dimensions[dimension]
        local amount = tonumber(values[3 + dimension] or '0')
        local global_limit = number('limit:' .. name)
        refund('t:' .. name, global_limit, amount)
        refund(epoch_prefix(expired_epoch) .. 'p:' .. expired_pool .. ':' .. name,
          pool_capacity(expired_epoch, expired_pool, name), amount)
      end
    end
    local inflight = tonumber(values[8] or '0')
    subtract('t:inflight', inflight)
    subtract(epoch_prefix(expired_epoch) .. 'p:' .. expired_pool .. ':inflight', inflight)
    redis.call('HDEL', state,
      prefix .. 'pool', prefix .. 'state', prefix .. 'revision', prefix .. 'epoch', prefix .. 'expires', prefix .. 'max_expires',
      prefix .. 'rpm', prefix .. 'input', prefix .. 'output', prefix .. 'total', prefix .. 'inflight', prefix .. 'cost')
    remove_epoch_lease(expired_epoch)
  end
  redis.call('ZREM', leases, expired_token)
end

if redis.call('HEXISTS', state, 'r:' .. token .. ':state') == 1 then
  return {2}
end

local active_leases = tonumber(redis.call('ZCARD', leases))
if active_leases >= maximum_leases then return {4} end
if tonumber(redis.call('HLEN', state)) + 128 + pool_count + lease_fields > maximum_fields then return {4} end

local active_revision = redis.call('HGET', state, 'active:revision') or ''
local active_config = redis.call('HGET', state, 'active:config')
local previous_epoch = redis.call('HGET', state, 'active:epoch')
if active_revision ~= '' and revision_greater(active_revision, revision) then return {5} end
if active_revision == revision and active_config ~= config_hash then return {3} end
if active_revision == revision and previous_epoch and previous_epoch ~= epoch then return {3} end

if active_revision == '' or revision_greater(revision, active_revision) then
  for _, dimension in ipairs(rate_dimensions) do
    local name = dimensions[dimension]
    local old_limit = number('limit:' .. name)
    local old_tokens = 0
    if old_limit > 0 then old_tokens = refill('t:' .. name, old_limit) end
    local old_debt = math.max(0, old_limit - old_tokens)
    local new_limit = limits[dimension]
    redis.call('HSET', state,
      't:' .. name .. ':tokens', math.max(0, new_limit - old_debt),
      't:' .. name .. ':updated', now)
  end
  for dimension = 1, 6 do
    redis.call('HSET', state, 'limit:' .. dimensions[dimension], limits[dimension])
  end
  redis.call('HSET', state, 'active:revision', revision, 'active:config', config_hash, 'active:epoch', epoch)
  if previous_epoch and previous_epoch ~= epoch then cleanup_epoch(previous_epoch) end
end

local current_epoch_prefix = epoch_prefix(epoch)
local existing_epoch_config = redis.call('HGET', state, current_epoch_prefix .. 'config')
if existing_epoch_config and existing_epoch_config ~= config_hash then return {3} end
redis.call('HSET', state,
  current_epoch_prefix .. 'config', config_hash,
  current_epoch_prefix .. 'revision', revision)
for dimension = 1, 6 do
  redis.call('HSET', state, current_epoch_prefix .. 'limit:' .. dimensions[dimension], limits[dimension])
end

local target_maximum = nil
for index = 0, pool_count - 1 do
  local offset = 22 + index * 3
  local pool_id = ARGV[offset]
  redis.call('HSET', state, current_epoch_prefix .. 'p:' .. pool_id .. ':maximum', ARGV[offset + 2])
  if pool_id == pool then target_maximum = tonumber(ARGV[offset + 2]) end
end
if not target_maximum then return {3} end
redis.call('HSET', state, current_epoch_prefix .. 'p:' .. pool .. ':active', now)
extend_ttl(state, idle_ttl)

local global_available = {}
local target_available = {}
for _, dimension in ipairs(rate_dimensions) do
  local requested = demand[dimension]
  if requested > 0 then
    local name = dimensions[dimension]
    local global_limit = limits[dimension]
    local target_capacity = math.floor(global_limit * target_maximum / 10000)
    if requested > target_capacity then return {0} end
    local global_tokens = refill('t:' .. name, global_limit)
    local pool_tokens = refill(current_epoch_prefix .. 'p:' .. pool .. ':' .. name, target_capacity)
    if pool_tokens + 0.0000001 < requested then return {0} end

    local reserved_for_others = 0
    for index = 0, pool_count - 1 do
      local offset = 22 + index * 3
      local other_pool = ARGV[offset]
      local last_active = number(current_epoch_prefix .. 'p:' .. other_pool .. ':active')
      if other_pool ~= pool and last_active > 0 and now - last_active <= activity_window then
        local other_guarantee = math.floor(global_limit * tonumber(ARGV[offset + 1]) / 10000)
        local other_capacity = math.floor(global_limit * tonumber(ARGV[offset + 2]) / 10000)
        local other_tokens = refill(current_epoch_prefix .. 'p:' .. other_pool .. ':' .. name, other_capacity)
        local other_consumed = math.max(0, other_capacity - other_tokens)
        reserved_for_others = reserved_for_others + math.max(0, other_guarantee - other_consumed)
      end
    end
    if global_tokens - requested + 0.0000001 < reserved_for_others then return {0} end
    global_available[dimension] = global_tokens
    target_available[dimension] = pool_tokens
  end
end

local requested_inflight = demand[5]
if requested_inflight > 0 then
  local global_limit = limits[5]
  local total_used = number('t:inflight')
  local pool_used = number(current_epoch_prefix .. 'p:' .. pool .. ':inflight')
  local target_capacity = math.floor(global_limit * target_maximum / 10000)
  if pool_used + requested_inflight > target_capacity or total_used + requested_inflight > global_limit then
    return {0}
  end
  local reserved_for_others = 0
  for index = 0, pool_count - 1 do
    local offset = 22 + index * 3
    local other_pool = ARGV[offset]
    local last_active = number(current_epoch_prefix .. 'p:' .. other_pool .. ':active')
    if other_pool ~= pool and last_active > 0 and now - last_active <= activity_window then
      local other_used = number(current_epoch_prefix .. 'p:' .. other_pool .. ':inflight')
      local other_guarantee = math.floor(global_limit * tonumber(ARGV[offset + 1]) / 10000)
      reserved_for_others = reserved_for_others + math.max(0, other_guarantee - other_used)
    end
  end
  if global_limit - total_used - requested_inflight < reserved_for_others then return {0} end
end

for _, dimension in ipairs(rate_dimensions) do
  local requested = demand[dimension]
  if requested > 0 then
    local name = dimensions[dimension]
    redis.call('HSET', state, 't:' .. name .. ':tokens', global_available[dimension] - requested)
    redis.call('HSET', state, current_epoch_prefix .. 'p:' .. pool .. ':' .. name .. ':tokens', target_available[dimension] - requested)
  end
end
if requested_inflight > 0 then
  redis.call('HINCRBY', state, 't:inflight', requested_inflight)
  redis.call('HINCRBY', state, current_epoch_prefix .. 'p:' .. pool .. ':inflight', requested_inflight)
end
redis.call('HSET', state, current_epoch_prefix .. 'p:' .. pool .. ':active', now)

local max_expires = now + maximum_lifetime
local expires = math.min(now + lease_ttl, max_expires)
local prefix = 'r:' .. token .. ':'
redis.call('HSET', state,
  prefix .. 'pool', pool, prefix .. 'state', 'pending', prefix .. 'revision', revision, prefix .. 'epoch', epoch,
  prefix .. 'expires', expires, prefix .. 'max_expires', max_expires,
  prefix .. 'rpm', demand[1], prefix .. 'input', demand[2], prefix .. 'output', demand[3],
  prefix .. 'total', demand[4], prefix .. 'inflight', demand[5], prefix .. 'cost', demand[6])
redis.call('HINCRBY', state, current_epoch_prefix .. 'leases', 1)
redis.call('ZADD', leases, expires, token)
extend_ttl(state, idle_ttl)
extend_ttl(leases, idle_ttl)
return {1, now, expires}
`

const strictCapacityCommitScript = `
-- strict_capacity_commit_v2
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local prefix = 'r:' .. ARGV[1] .. ':'
local state_value = redis.call('HGET', KEYS[1], prefix .. 'state')
local expires = tonumber(redis.call('HGET', KEYS[1], prefix .. 'expires') or '0')
if not state_value or expires <= now then return 0 end
if state_value == 'committed' then return 1 end
if state_value ~= 'pending' then return 2 end
redis.call('HSET', KEYS[1], prefix .. 'state', 'committed')
return 1
`

const strictCapacityCancelScript = `
-- strict_capacity_cancel_v2
local state = KEYS[1]
local leases = KEYS[2]
local token = ARGV[1]
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local prefix = 'r:' .. token .. ':'
local state_value = redis.call('HGET', state, prefix .. 'state')
if not state_value then return 0 end
if state_value ~= 'pending' then return 2 end
local pool = redis.call('HGET', state, prefix .. 'pool')
local epoch = redis.call('HGET', state, prefix .. 'epoch')
if not pool or not epoch then return 0 end
local epoch_prefix = 'e:' .. epoch .. ':'
local dimensions = {'rpm', 'input', 'output', 'total', 'inflight', 'cost'}
local rate_dimensions = {1, 2, 3, 4, 6}

local function number(field)
  return tonumber(redis.call('HGET', state, field) or '0')
end

local function refill(scope, capacity)
  if capacity <= 0 then return 0 end
  local tokens_field = scope .. ':tokens'
  local updated_field = scope .. ':updated'
  local tokens = tonumber(redis.call('HGET', state, tokens_field) or capacity)
  local updated = number(updated_field)
  if updated > 0 and now > updated then
    tokens = math.min(capacity, tokens + (now - updated) * capacity / 60000)
  else
    tokens = math.min(capacity, tokens)
  end
  redis.call('HSET', state, tokens_field, tokens, updated_field, now)
  return tokens
end

local function refund(scope, capacity, amount)
  if amount <= 0 or capacity <= 0 then return end
  local tokens = refill(scope, capacity)
  redis.call('HSET', state, scope .. ':tokens', math.min(capacity, tokens + amount))
end

local maximum = number(epoch_prefix .. 'p:' .. pool .. ':maximum')
for _, dimension in ipairs(rate_dimensions) do
  local name = dimensions[dimension]
  local amount = number(prefix .. name)
  local global_limit = number('limit:' .. name)
  local pool_limit = math.floor(number(epoch_prefix .. 'limit:' .. name) * maximum / 10000)
  refund('t:' .. name, global_limit, amount)
  refund(epoch_prefix .. 'p:' .. pool .. ':' .. name, pool_limit, amount)
end

local inflight = number(prefix .. 'inflight')
local total_inflight = math.max(0, number('t:inflight') - inflight)
local pool_inflight = math.max(0, number(epoch_prefix .. 'p:' .. pool .. ':inflight') - inflight)
redis.call('HSET', state, 't:inflight', total_inflight, epoch_prefix .. 'p:' .. pool .. ':inflight', pool_inflight)
redis.call('HDEL', state,
  prefix .. 'pool', prefix .. 'state', prefix .. 'revision', prefix .. 'epoch', prefix .. 'expires', prefix .. 'max_expires',
  prefix .. 'rpm', prefix .. 'input', prefix .. 'output', prefix .. 'total', prefix .. 'inflight', prefix .. 'cost')
redis.call('ZREM', leases, token)
local epoch_leases = math.max(0, number(epoch_prefix .. 'leases') - 1)
redis.call('HSET', state, epoch_prefix .. 'leases', epoch_leases)
if epoch_leases == 0 and redis.call('HGET', state, 'active:epoch') ~= epoch then
  local fields = redis.call('HKEYS', state)
  for _, field in ipairs(fields) do
    if string.sub(field, 1, string.len(epoch_prefix)) == epoch_prefix then redis.call('HDEL', state, field) end
  end
end
return 1
`

const strictCapacityReleaseScript = `
-- strict_capacity_release_v2
local state = KEYS[1]
local leases = KEYS[2]
local token = ARGV[1]
local prefix = 'r:' .. token .. ':'
local state_value = redis.call('HGET', state, prefix .. 'state')
if not state_value then return 0 end
if state_value ~= 'committed' then return 2 end
local pool = redis.call('HGET', state, prefix .. 'pool')
local epoch = redis.call('HGET', state, prefix .. 'epoch')
if not pool or not epoch then return 0 end
local epoch_prefix = 'e:' .. epoch .. ':'
local inflight = tonumber(redis.call('HGET', state, prefix .. 'inflight') or '0')
local total_inflight = math.max(0, tonumber(redis.call('HGET', state, 't:inflight') or '0') - inflight)
local pool_inflight = math.max(0, tonumber(redis.call('HGET', state, epoch_prefix .. 'p:' .. pool .. ':inflight') or '0') - inflight)
redis.call('HSET', state, 't:inflight', total_inflight, epoch_prefix .. 'p:' .. pool .. ':inflight', pool_inflight)
redis.call('HDEL', state,
  prefix .. 'pool', prefix .. 'state', prefix .. 'revision', prefix .. 'epoch', prefix .. 'expires', prefix .. 'max_expires',
  prefix .. 'rpm', prefix .. 'input', prefix .. 'output', prefix .. 'total', prefix .. 'inflight', prefix .. 'cost')
redis.call('ZREM', leases, token)
local epoch_leases_field = epoch_prefix .. 'leases'
local epoch_leases = math.max(0, tonumber(redis.call('HGET', state, epoch_leases_field) or '0') - 1)
redis.call('HSET', state, epoch_leases_field, epoch_leases)
if epoch_leases == 0 and redis.call('HGET', state, 'active:epoch') ~= epoch then
  local fields = redis.call('HKEYS', state)
  for _, field in ipairs(fields) do
    if string.sub(field, 1, string.len(epoch_prefix)) == epoch_prefix then redis.call('HDEL', state, field) end
  end
end
return 1
`

const strictCapacityRenewScript = `
-- strict_capacity_renew_v2
local state = KEYS[1]
local leases = KEYS[2]
local token = ARGV[1]
local lease_ttl = tonumber(ARGV[2])
local idle_ttl = tonumber(ARGV[3])
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local prefix = 'r:' .. token .. ':'
local state_value = redis.call('HGET', state, prefix .. 'state')
local current_expires = tonumber(redis.call('HGET', state, prefix .. 'expires') or '0')
local max_expires = tonumber(redis.call('HGET', state, prefix .. 'max_expires') or '0')
local epoch = redis.call('HGET', state, prefix .. 'epoch')
if not state_value or not epoch or current_expires <= now or max_expires <= now then return 0 end
if state_value ~= 'pending' and state_value ~= 'committed' then return 0 end
local expires = math.min(now + lease_ttl, max_expires)
if expires <= now then return 0 end
local function extend_ttl(key, ttl)
  local current = redis.call('PTTL', key)
  if current < 0 or current < ttl then redis.call('PEXPIRE', key, ttl) end
end
redis.call('HSET', state, prefix .. 'expires', expires)
redis.call('ZADD', leases, expires, token)
extend_ttl(state, idle_ttl)
extend_ttl(leases, idle_ttl)
return expires
`
