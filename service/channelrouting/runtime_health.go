package channelrouting

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	runtimeHealthMaxEntries          = 100_000
	runtimeHealthMaintenanceInterval = 15 * time.Minute
	runtimeHealthMaintenanceRetry    = 30 * time.Second
	runtimeHealthMaintenanceLeaseTTL = time.Minute
	runtimeHealthMaintenanceLease    = "routing-v2-runtime-health-maintenance"
)

type RuntimeHealthStats struct {
	CredentialEntries       int    `json:"credential_entries"`
	AccountEntries          int    `json:"account_entries"`
	CredentialDirty         int    `json:"credential_dirty"`
	AccountDirty            int    `json:"account_dirty"`
	Evictions               int64  `json:"evictions"`
	CredentialOverflow      bool   `json:"credential_overflow"`
	AccountOverflow         bool   `json:"account_overflow"`
	CredentialOverflowDrops int64  `json:"credential_overflow_drops"`
	AccountOverflowDrops    int64  `json:"account_overflow_drops"`
	MaintenanceRuns         int64  `json:"maintenance_runs"`
	MaintenanceFailures     int64  `json:"maintenance_failures"`
	MaintenanceLastUnix     int64  `json:"maintenance_last_at"`
	MaintenanceLastError    string `json:"maintenance_last_error,omitempty"`
}

var runtimeHealth = struct {
	sync.RWMutex
	flushMu                sync.Mutex
	credentials            map[int]model.RoutingCredentialHealthState
	accounts               map[int]model.RoutingUpstreamAccountHealthState
	dirtyCredentials       map[int]model.RoutingCredentialHealthState
	dirtyAccounts          map[int]model.RoutingUpstreamAccountHealthState
	limit                  int
	evictions              int64
	credentialOverflow     bool
	accountOverflow        bool
	credentialOverflowDrop int64
	accountOverflowDrop    int64
	maintenanceNextMs      int64
	maintenanceRuns        int64
	maintenanceFailures    int64
	maintenanceLastUnix    int64
	maintenanceLastError   string
}{
	credentials:      make(map[int]model.RoutingCredentialHealthState),
	accounts:         make(map[int]model.RoutingUpstreamAccountHealthState),
	dirtyCredentials: make(map[int]model.RoutingCredentialHealthState),
	dirtyAccounts:    make(map[int]model.RoutingUpstreamAccountHealthState),
	limit:            runtimeHealthMaxEntries,
}

func CredentialRuntimeHealth(credentialID int) (model.RoutingCredentialHealthState, bool) {
	if credentialID <= 0 {
		return model.RoutingCredentialHealthState{}, false
	}
	runtimeHealth.RLock()
	defer runtimeHealth.RUnlock()
	state, ok := runtimeHealth.credentials[credentialID]
	return state, ok
}

func UpstreamAccountRuntimeHealth(accountID int) (model.RoutingUpstreamAccountHealthState, bool) {
	if accountID <= 0 {
		return model.RoutingUpstreamAccountHealthState{}, false
	}
	runtimeHealth.RLock()
	defer runtimeHealth.RUnlock()
	state, ok := runtimeHealth.accounts[accountID]
	return state, ok
}

func CredentialRuntimeBlocked(credentialID int, now time.Time) (string, bool) {
	if credentialID <= 0 {
		return "", false
	}
	if now.IsZero() {
		now = time.Now()
	}
	runtimeHealth.RLock()
	state, ok := runtimeHealth.credentials[credentialID]
	overflow := runtimeHealth.credentialOverflow
	runtimeHealth.RUnlock()
	if !ok {
		if overflow {
			return "credential_runtime_health_overflow", true
		}
		return "", false
	}
	nowMs := now.UnixMilli()
	if state.AuthFailure && (state.AuthFailureUntilMs <= 0 || state.AuthFailureUntilMs > nowMs) {
		return "credential_auth_failure", true
	}
	if state.CapacityLimited && state.CapacityCooldownUntilMs > nowMs {
		return "credential_capacity_cooldown", true
	}
	return "", false
}

func UpstreamAccountRuntimeBlocked(accountID int, now time.Time) (string, bool) {
	if accountID <= 0 {
		return "", false
	}
	if now.IsZero() {
		now = time.Now()
	}
	runtimeHealth.RLock()
	state, ok := runtimeHealth.accounts[accountID]
	overflow := runtimeHealth.accountOverflow
	runtimeHealth.RUnlock()
	if !ok {
		if overflow {
			return "upstream_account_runtime_health_overflow", true
		}
		return "", false
	}
	if !state.ServingUnavailable {
		return "", false
	}
	if state.UnavailableUntilMs > 0 && state.UnavailableUntilMs <= now.UnixMilli() {
		return "", false
	}
	return "upstream_account_unavailable", true
}

func RecordCredentialAuthFailure(credentialID int, channelID int, reason string, until time.Time, now time.Time) {
	if credentialID <= 0 || channelID <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	untilMs, ok := runtimeHealthUntilMillis(until, true)
	if !ok {
		return
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	state, ok := credentialRuntimeHealthForMutationLocked(credentialID, channelID, now)
	if !ok {
		return
	}
	version, ok := nextRuntimeHealthVersion(state.AuthVersion, now)
	if !ok {
		markCredentialRuntimeHealthOverflowLocked()
		return
	}
	state.AuthFailure = true
	state.AuthFailureReason = boundedRuntimeHealthReason(reason)
	state.AuthFailureUntilMs = untilMs
	state.AuthVersion = version
	state.AuthUpdatedTimeMs = max(state.AuthUpdatedTimeMs, positiveRuntimeHealthTimeMs(now))
	state.UpdatedTimeMs = max(state.UpdatedTimeMs, state.AuthUpdatedTimeMs, state.CapacityUpdatedTimeMs)
	runtimeHealth.credentials[credentialID] = state
	runtimeHealth.dirtyCredentials[credentialID] = state
}

func ClearCredentialAuthFailure(credentialID int, channelID int, now time.Time) {
	if credentialID <= 0 || channelID <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	state, ok := credentialRuntimeHealthForMutationLocked(credentialID, channelID, now)
	if !ok {
		return
	}
	version, ok := nextRuntimeHealthVersion(state.AuthVersion, now)
	if !ok {
		markCredentialRuntimeHealthOverflowLocked()
		return
	}
	state.AuthFailure = false
	state.AuthFailureReason = ""
	state.AuthFailureUntilMs = 0
	state.AuthVersion = version
	state.AuthUpdatedTimeMs = max(state.AuthUpdatedTimeMs, positiveRuntimeHealthTimeMs(now))
	state.UpdatedTimeMs = max(state.UpdatedTimeMs, state.AuthUpdatedTimeMs, state.CapacityUpdatedTimeMs)
	runtimeHealth.credentials[credentialID] = state
	runtimeHealth.dirtyCredentials[credentialID] = state
}

func RecordCredentialCapacityCooldown(credentialID int, channelID int, statusCode int, until time.Time, now time.Time) {
	if credentialID <= 0 || channelID <= 0 || statusCode < 1 || statusCode > 599 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	untilMs, ok := runtimeHealthUntilMillis(until, false)
	if !ok {
		return
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	state, ok := credentialRuntimeHealthForMutationLocked(credentialID, channelID, now)
	if !ok {
		return
	}
	version, ok := nextRuntimeHealthVersion(state.CapacityVersion, now)
	if !ok {
		markCredentialRuntimeHealthOverflowLocked()
		return
	}
	state.CapacityLimited = true
	state.CapacityStatusCode = statusCode
	state.CapacityCooldownUntilMs = max(state.CapacityCooldownUntilMs, untilMs)
	state.CapacityVersion = version
	state.CapacityUpdatedTimeMs = max(state.CapacityUpdatedTimeMs, positiveRuntimeHealthTimeMs(now))
	state.UpdatedTimeMs = max(state.UpdatedTimeMs, state.AuthUpdatedTimeMs, state.CapacityUpdatedTimeMs)
	runtimeHealth.credentials[credentialID] = state
	runtimeHealth.dirtyCredentials[credentialID] = state
}

func ClearCredentialCapacityCooldown(credentialID int, channelID int, now time.Time) {
	if credentialID <= 0 || channelID <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	state, ok := credentialRuntimeHealthForMutationLocked(credentialID, channelID, now)
	if !ok {
		return
	}
	version, ok := nextRuntimeHealthVersion(state.CapacityVersion, now)
	if !ok {
		markCredentialRuntimeHealthOverflowLocked()
		return
	}
	state.CapacityLimited = false
	state.CapacityStatusCode = 0
	state.CapacityCooldownUntilMs = 0
	state.CapacityVersion = version
	state.CapacityUpdatedTimeMs = max(state.CapacityUpdatedTimeMs, positiveRuntimeHealthTimeMs(now))
	state.UpdatedTimeMs = max(state.UpdatedTimeMs, state.AuthUpdatedTimeMs, state.CapacityUpdatedTimeMs)
	runtimeHealth.credentials[credentialID] = state
	runtimeHealth.dirtyCredentials[credentialID] = state
}

func RecordUpstreamAccountUnavailable(accountID int, statusCode int, reason string, until time.Time, now time.Time) {
	if accountID <= 0 || statusCode < 1 || statusCode > 599 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	untilMs, ok := runtimeHealthUntilMillis(until, true)
	if !ok {
		return
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	state, ok := accountRuntimeHealthForMutationLocked(accountID, now)
	if !ok {
		return
	}
	version, ok := nextRuntimeHealthVersion(state.StateVersion, now)
	if !ok {
		markAccountRuntimeHealthOverflowLocked()
		return
	}
	state.ServingUnavailable = true
	state.StatusCode = statusCode
	state.Reason = boundedRuntimeHealthReason(reason)
	state.UnavailableUntilMs = untilMs
	state.StateVersion = version
	state.StateUpdatedTimeMs = max(state.StateUpdatedTimeMs, positiveRuntimeHealthTimeMs(now))
	state.UpdatedTimeMs = max(state.UpdatedTimeMs, state.StateUpdatedTimeMs)
	runtimeHealth.accounts[accountID] = state
	runtimeHealth.dirtyAccounts[accountID] = state
}

func ClearUpstreamAccountUnavailable(accountID int, now time.Time) {
	if accountID <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	state, ok := accountRuntimeHealthForMutationLocked(accountID, now)
	if !ok {
		return
	}
	version, ok := nextRuntimeHealthVersion(state.StateVersion, now)
	if !ok {
		markAccountRuntimeHealthOverflowLocked()
		return
	}
	state.ServingUnavailable = false
	state.StatusCode = 0
	state.Reason = ""
	state.UnavailableUntilMs = 0
	state.StateVersion = version
	state.StateUpdatedTimeMs = max(state.StateUpdatedTimeMs, positiveRuntimeHealthTimeMs(now))
	state.UpdatedTimeMs = max(state.UpdatedTimeMs, state.StateUpdatedTimeMs)
	runtimeHealth.accounts[accountID] = state
	runtimeHealth.dirtyAccounts[accountID] = state
}

func RefreshRuntimeHealthContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := FlushRuntimeHealthContext(ctx); err != nil {
		return err
	}
	if err := maintainRuntimeHealthContext(ctx, time.Now()); err != nil {
		common.SysError("maintain channel routing runtime health: " + common.SanitizeErrorMessage(err.Error()))
	}

	runtimeHealth.RLock()
	limit := runtimeHealth.limit
	runtimeHealth.RUnlock()
	credentialStates, err := model.ListRoutingCredentialHealthStatesContext(ctx, limit)
	if err != nil {
		if errors.Is(err, model.ErrRoutingRuntimeHealthLimitExceeded) {
			runtimeHealth.Lock()
			runtimeHealth.credentialOverflow = true
			runtimeHealth.Unlock()
		}
		return err
	}
	accountStates, err := model.ListRoutingUpstreamAccountHealthStatesContext(ctx, limit)
	if err != nil {
		if errors.Is(err, model.ErrRoutingRuntimeHealthLimitExceeded) {
			runtimeHealth.Lock()
			runtimeHealth.accountOverflow = true
			runtimeHealth.Unlock()
		}
		return err
	}

	runtimeHealth.Lock()
	rebuildCredentialRuntimeHealthLocked(credentialStates)
	rebuildAccountRuntimeHealthLocked(accountStates)
	runtimeHealth.Unlock()
	return ctx.Err()
}

func maintainRuntimeHealthContext(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := positiveRuntimeHealthTimeMs(now)
	runtimeHealth.Lock()
	if runtimeHealth.maintenanceNextMs > nowMs {
		runtimeHealth.Unlock()
		return nil
	}
	runtimeHealth.maintenanceNextMs = now.Add(runtimeHealthMaintenanceRetry).UnixMilli()
	runtimeHealth.Unlock()

	lease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		ctx,
		runtimeHealthMaintenanceLease,
		NodeEpochID(),
		int64(runtimeHealthMaintenanceLeaseTTL/time.Millisecond),
		int64(runtimeHealthMaintenanceInterval/time.Millisecond),
		false,
	)
	if err != nil {
		recordRuntimeHealthMaintenance(now, false, err)
		return err
	}
	if !acquired {
		runtimeHealth.Lock()
		runtimeHealth.maintenanceNextMs = now.Add(runtimeHealthMaintenanceInterval).UnixMilli()
		runtimeHealth.Unlock()
		return nil
	}

	maintenanceErr := model.PruneRoutingRuntimeHealthStatesContext(ctx)
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if maintenanceErr != nil {
		maintenanceErr = errors.Join(maintenanceErr, model.ReleaseRoutingControlLeaseContext(finishCtx, lease))
		recordRuntimeHealthMaintenance(now, false, maintenanceErr)
		return maintenanceErr
	}
	if err := model.CompleteRoutingControlLeaseContext(finishCtx, lease); err != nil {
		recordRuntimeHealthMaintenance(now, false, err)
		return err
	}
	recordRuntimeHealthMaintenance(now, true, nil)
	return nil
}

func recordRuntimeHealthMaintenance(now time.Time, succeeded bool, err error) {
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	runtimeHealth.maintenanceRuns++
	runtimeHealth.maintenanceLastUnix = now.Unix()
	if succeeded {
		runtimeHealth.maintenanceNextMs = now.Add(runtimeHealthMaintenanceInterval).UnixMilli()
		runtimeHealth.maintenanceLastError = ""
		return
	}
	runtimeHealth.maintenanceFailures++
	runtimeHealth.maintenanceNextMs = now.Add(runtimeHealthMaintenanceRetry).UnixMilli()
	if err == nil {
		runtimeHealth.maintenanceLastError = "runtime health maintenance failed"
		return
	}
	runtimeHealth.maintenanceLastError = boundedRuntimeHealthReason(err.Error())
}

func FlushRuntimeHealthContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeHealth.flushMu.Lock()
	defer runtimeHealth.flushMu.Unlock()

	runtimeHealth.RLock()
	credentials := make([]model.RoutingCredentialHealthState, 0, len(runtimeHealth.dirtyCredentials))
	for _, state := range runtimeHealth.dirtyCredentials {
		credentials = append(credentials, state)
	}
	accounts := make([]model.RoutingUpstreamAccountHealthState, 0, len(runtimeHealth.dirtyAccounts))
	for _, state := range runtimeHealth.dirtyAccounts {
		accounts = append(accounts, state)
	}
	runtimeHealth.RUnlock()

	sort.Slice(credentials, func(i, j int) bool { return credentials[i].CredentialID < credentials[j].CredentialID })
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].AccountID < accounts[j].AccountID })
	if err := model.FlushRoutingRuntimeHealthStatesContext(ctx, credentials, accounts); err != nil {
		return err
	}
	acknowledgeFlushedCredentialHealth(credentials)
	acknowledgeFlushedAccountHealth(accounts)
	return ctx.Err()
}

func RuntimeHealthRuntimeStats() RuntimeHealthStats {
	runtimeHealth.RLock()
	defer runtimeHealth.RUnlock()
	return RuntimeHealthStats{
		CredentialEntries:       len(runtimeHealth.credentials),
		AccountEntries:          len(runtimeHealth.accounts),
		CredentialDirty:         len(runtimeHealth.dirtyCredentials),
		AccountDirty:            len(runtimeHealth.dirtyAccounts),
		Evictions:               runtimeHealth.evictions,
		CredentialOverflow:      runtimeHealth.credentialOverflow,
		AccountOverflow:         runtimeHealth.accountOverflow,
		CredentialOverflowDrops: runtimeHealth.credentialOverflowDrop,
		AccountOverflowDrops:    runtimeHealth.accountOverflowDrop,
		MaintenanceRuns:         runtimeHealth.maintenanceRuns,
		MaintenanceFailures:     runtimeHealth.maintenanceFailures,
		MaintenanceLastUnix:     runtimeHealth.maintenanceLastUnix,
		MaintenanceLastError:    runtimeHealth.maintenanceLastError,
	}
}

func credentialRuntimeHealthForMutationLocked(credentialID int, channelID int, now time.Time) (model.RoutingCredentialHealthState, bool) {
	state, exists := runtimeHealth.credentials[credentialID]
	if exists {
		if state.ChannelID != channelID {
			markCredentialRuntimeHealthOverflowLocked()
			return model.RoutingCredentialHealthState{}, false
		}
		return state, true
	}
	if !reserveCredentialRuntimeHealthSlotLocked(now) {
		markCredentialRuntimeHealthOverflowLocked()
		return model.RoutingCredentialHealthState{}, false
	}
	return model.RoutingCredentialHealthState{CredentialID: credentialID, ChannelID: channelID}, true
}

func accountRuntimeHealthForMutationLocked(accountID int, now time.Time) (model.RoutingUpstreamAccountHealthState, bool) {
	if state, exists := runtimeHealth.accounts[accountID]; exists {
		return state, true
	}
	if !reserveAccountRuntimeHealthSlotLocked(now) {
		markAccountRuntimeHealthOverflowLocked()
		return model.RoutingUpstreamAccountHealthState{}, false
	}
	return model.RoutingUpstreamAccountHealthState{AccountID: accountID}, true
}

func reserveCredentialRuntimeHealthSlotLocked(now time.Time) bool {
	if len(runtimeHealth.credentials) < runtimeHealth.limit {
		return true
	}
	credentialID, unsafe := credentialRuntimeHealthEvictionCandidateLocked(now)
	if credentialID == 0 {
		return false
	}
	delete(runtimeHealth.credentials, credentialID)
	runtimeHealth.evictions++
	if unsafe {
		runtimeHealth.credentialOverflow = true
	}
	return true
}

func reserveAccountRuntimeHealthSlotLocked(now time.Time) bool {
	if len(runtimeHealth.accounts) < runtimeHealth.limit {
		return true
	}
	accountID, unsafe := accountRuntimeHealthEvictionCandidateLocked(now)
	if accountID == 0 {
		return false
	}
	delete(runtimeHealth.accounts, accountID)
	runtimeHealth.evictions++
	if unsafe {
		runtimeHealth.accountOverflow = true
	}
	return true
}

func credentialRuntimeHealthEvictionCandidateLocked(now time.Time) (int, bool) {
	var safeID, unsafeID int
	var safeUpdated, unsafeUpdated int64 = math.MaxInt64, math.MaxInt64
	for credentialID, state := range runtimeHealth.credentials {
		if _, dirty := runtimeHealth.dirtyCredentials[credentialID]; dirty {
			continue
		}
		updated := max(state.UpdatedTimeMs, state.AuthUpdatedTimeMs, state.CapacityUpdatedTimeMs)
		if credentialStateBlocksAt(state, now) {
			if updated < unsafeUpdated || (updated == unsafeUpdated && (unsafeID == 0 || credentialID < unsafeID)) {
				unsafeID = credentialID
				unsafeUpdated = updated
			}
			continue
		}
		if updated < safeUpdated || (updated == safeUpdated && (safeID == 0 || credentialID < safeID)) {
			safeID = credentialID
			safeUpdated = updated
		}
	}
	if safeID != 0 {
		return safeID, false
	}
	return unsafeID, unsafeID != 0
}

func accountRuntimeHealthEvictionCandidateLocked(now time.Time) (int, bool) {
	var safeID, unsafeID int
	var safeUpdated, unsafeUpdated int64 = math.MaxInt64, math.MaxInt64
	for accountID, state := range runtimeHealth.accounts {
		if _, dirty := runtimeHealth.dirtyAccounts[accountID]; dirty {
			continue
		}
		updated := max(state.UpdatedTimeMs, state.StateUpdatedTimeMs)
		if accountStateBlocksAt(state, now) {
			if updated < unsafeUpdated || (updated == unsafeUpdated && (unsafeID == 0 || accountID < unsafeID)) {
				unsafeID = accountID
				unsafeUpdated = updated
			}
			continue
		}
		if updated < safeUpdated || (updated == safeUpdated && (safeID == 0 || accountID < safeID)) {
			safeID = accountID
			safeUpdated = updated
		}
	}
	if safeID != 0 {
		return safeID, false
	}
	return unsafeID, unsafeID != 0
}

func acknowledgeFlushedCredentialHealth(states []model.RoutingCredentialHealthState) {
	if len(states) == 0 {
		return
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	for index := range states {
		flushed := states[index]
		current, ok := runtimeHealth.dirtyCredentials[flushed.CredentialID]
		if ok && current.AuthVersion <= flushed.AuthVersion && current.CapacityVersion <= flushed.CapacityVersion {
			delete(runtimeHealth.dirtyCredentials, flushed.CredentialID)
		}
	}
}

func acknowledgeFlushedAccountHealth(states []model.RoutingUpstreamAccountHealthState) {
	if len(states) == 0 {
		return
	}
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	for index := range states {
		flushed := states[index]
		current, ok := runtimeHealth.dirtyAccounts[flushed.AccountID]
		if ok && current.StateVersion <= flushed.StateVersion {
			delete(runtimeHealth.dirtyAccounts, flushed.AccountID)
		}
	}
}

func rebuildCredentialRuntimeHealthLocked(states []model.RoutingCredentialHealthState) {
	next := make(map[int]model.RoutingCredentialHealthState, min(runtimeHealth.limit, len(states)+len(runtimeHealth.dirtyCredentials)))
	overflow := false
	dirtyIDs := make([]int, 0, len(runtimeHealth.dirtyCredentials))
	for credentialID := range runtimeHealth.dirtyCredentials {
		dirtyIDs = append(dirtyIDs, credentialID)
	}
	sort.Ints(dirtyIDs)
	for _, credentialID := range dirtyIDs {
		if len(next) >= runtimeHealth.limit {
			runtimeHealth.credentialOverflowDrop++
			overflow = true
			break
		}
		next[credentialID] = runtimeHealth.dirtyCredentials[credentialID]
	}
	for index := range states {
		state := states[index]
		if current, ok := next[state.CredentialID]; ok {
			if current.ChannelID != state.ChannelID {
				overflow = true
				continue
			}
			next[state.CredentialID] = mergeCredentialRuntimeHealth(current, state)
			continue
		}
		if len(next) >= runtimeHealth.limit {
			overflow = true
			runtimeHealth.evictions++
			continue
		}
		next[state.CredentialID] = state
	}
	runtimeHealth.credentials = next
	runtimeHealth.credentialOverflow = overflow
}

func rebuildAccountRuntimeHealthLocked(states []model.RoutingUpstreamAccountHealthState) {
	next := make(map[int]model.RoutingUpstreamAccountHealthState, min(runtimeHealth.limit, len(states)+len(runtimeHealth.dirtyAccounts)))
	overflow := false
	dirtyIDs := make([]int, 0, len(runtimeHealth.dirtyAccounts))
	for accountID := range runtimeHealth.dirtyAccounts {
		dirtyIDs = append(dirtyIDs, accountID)
	}
	sort.Ints(dirtyIDs)
	for _, accountID := range dirtyIDs {
		if len(next) >= runtimeHealth.limit {
			runtimeHealth.accountOverflowDrop++
			overflow = true
			break
		}
		next[accountID] = runtimeHealth.dirtyAccounts[accountID]
	}
	for index := range states {
		state := states[index]
		if current, ok := next[state.AccountID]; ok {
			next[state.AccountID] = mergeAccountRuntimeHealth(current, state)
			continue
		}
		if len(next) >= runtimeHealth.limit {
			overflow = true
			runtimeHealth.evictions++
			continue
		}
		next[state.AccountID] = state
	}
	runtimeHealth.accounts = next
	runtimeHealth.accountOverflow = overflow
}

func mergeCredentialRuntimeHealth(current model.RoutingCredentialHealthState, incoming model.RoutingCredentialHealthState) model.RoutingCredentialHealthState {
	if incoming.AuthVersion > current.AuthVersion {
		current.AuthFailure = incoming.AuthFailure
		current.AuthFailureReason = incoming.AuthFailureReason
		current.AuthFailureUntilMs = incoming.AuthFailureUntilMs
		current.AuthVersion = incoming.AuthVersion
		current.AuthUpdatedTimeMs = incoming.AuthUpdatedTimeMs
	}
	if incoming.CapacityVersion > current.CapacityVersion {
		current.CapacityLimited = incoming.CapacityLimited
		current.CapacityStatusCode = incoming.CapacityStatusCode
		current.CapacityCooldownUntilMs = incoming.CapacityCooldownUntilMs
		current.CapacityVersion = incoming.CapacityVersion
		current.CapacityUpdatedTimeMs = incoming.CapacityUpdatedTimeMs
	}
	current.UpdatedTimeMs = max(current.UpdatedTimeMs, current.AuthUpdatedTimeMs, current.CapacityUpdatedTimeMs)
	return current
}

func mergeAccountRuntimeHealth(current model.RoutingUpstreamAccountHealthState, incoming model.RoutingUpstreamAccountHealthState) model.RoutingUpstreamAccountHealthState {
	if incoming.StateVersion > current.StateVersion {
		current.ServingUnavailable = incoming.ServingUnavailable
		current.StatusCode = incoming.StatusCode
		current.Reason = incoming.Reason
		current.UnavailableUntilMs = incoming.UnavailableUntilMs
		current.StateVersion = incoming.StateVersion
		current.StateUpdatedTimeMs = incoming.StateUpdatedTimeMs
	}
	current.UpdatedTimeMs = max(current.UpdatedTimeMs, current.StateUpdatedTimeMs)
	return current
}

func credentialStateBlocksAt(state model.RoutingCredentialHealthState, now time.Time) bool {
	nowMs := now.UnixMilli()
	return state.AuthFailure && (state.AuthFailureUntilMs <= 0 || state.AuthFailureUntilMs > nowMs) ||
		state.CapacityLimited && state.CapacityCooldownUntilMs > nowMs
}

func accountStateBlocksAt(state model.RoutingUpstreamAccountHealthState, now time.Time) bool {
	return state.ServingUnavailable && (state.UnavailableUntilMs <= 0 || state.UnavailableUntilMs > now.UnixMilli())
}

func nextRuntimeHealthVersion(current int64, now time.Time) (int64, bool) {
	candidate := now.UnixNano()
	if candidate <= 0 {
		candidate = 1
	}
	if candidate > current {
		return candidate, true
	}
	if current == math.MaxInt64 {
		return 0, false
	}
	return current + 1, true
}

func positiveRuntimeHealthTimeMs(now time.Time) int64 {
	if nowMs := now.UnixMilli(); nowMs > 0 {
		return nowMs
	}
	return 1
}

func runtimeHealthUntilMillis(until time.Time, zeroAsIndefinite bool) (int64, bool) {
	if until.IsZero() {
		return math.MaxInt64, zeroAsIndefinite
	}
	seconds := until.Unix()
	milliseconds := int64(until.Nanosecond() / int(time.Millisecond))
	maximumSeconds := int64(math.MaxInt64 / int64(time.Second/time.Millisecond))
	maximumMilliseconds := int64(math.MaxInt64 % int64(time.Second/time.Millisecond))
	if seconds > maximumSeconds || (seconds == maximumSeconds && milliseconds > maximumMilliseconds) {
		return math.MaxInt64, true
	}
	if seconds < 0 {
		return 0, false
	}
	untilMs := seconds*int64(time.Second/time.Millisecond) + milliseconds
	if untilMs <= 0 {
		return 0, false
	}
	return untilMs, true
}

func boundedRuntimeHealthReason(reason string) string {
	reason = strings.ToValidUTF8(reason, "")
	reason = strings.TrimSpace(common.SanitizeErrorMessage(reason))
	if len(reason) <= 256 {
		return reason
	}
	end := 256
	for end > 0 && !utf8.ValidString(reason[:end]) {
		end--
	}
	return strings.TrimSpace(reason[:end])
}

func markCredentialRuntimeHealthOverflowLocked() {
	runtimeHealth.credentialOverflow = true
	runtimeHealth.credentialOverflowDrop++
}

func markAccountRuntimeHealthOverflowLocked() {
	runtimeHealth.accountOverflow = true
	runtimeHealth.accountOverflowDrop++
}

func setRuntimeHealthLimitForTest(limit int) {
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	if limit < 1 || limit > runtimeHealthMaxEntries {
		limit = runtimeHealthMaxEntries
	}
	runtimeHealth.limit = limit
}

func resetRuntimeHealthForTest() {
	runtimeHealth.flushMu.Lock()
	defer runtimeHealth.flushMu.Unlock()
	runtimeHealth.Lock()
	defer runtimeHealth.Unlock()
	runtimeHealth.credentials = make(map[int]model.RoutingCredentialHealthState)
	runtimeHealth.accounts = make(map[int]model.RoutingUpstreamAccountHealthState)
	runtimeHealth.dirtyCredentials = make(map[int]model.RoutingCredentialHealthState)
	runtimeHealth.dirtyAccounts = make(map[int]model.RoutingUpstreamAccountHealthState)
	runtimeHealth.limit = runtimeHealthMaxEntries
	runtimeHealth.evictions = 0
	runtimeHealth.credentialOverflow = false
	runtimeHealth.accountOverflow = false
	runtimeHealth.credentialOverflowDrop = 0
	runtimeHealth.accountOverflowDrop = 0
	runtimeHealth.maintenanceNextMs = 0
	runtimeHealth.maintenanceRuns = 0
	runtimeHealth.maintenanceFailures = 0
	runtimeHealth.maintenanceLastUnix = 0
	runtimeHealth.maintenanceLastError = ""
}
