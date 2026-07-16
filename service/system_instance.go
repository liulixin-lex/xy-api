package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

const systemInstanceReportInterval = 30 * time.Second

const (
	asyncBillingProtocolVersion  = 2
	asyncBillingFleetCacheTTL    = time.Second
	asyncBillingSchemaCacheTTL   = time.Minute
	asyncBillingFleetActiveLimit = 512
	asyncBillingFleetStableAfter = 35 * time.Second
)

var systemInstanceReporterOnce sync.Once

var systemInstanceIncarnation struct {
	sync.Once
	id string
}

var asyncBillingFleetGateCache struct {
	sync.Mutex
	ready           bool
	expiresAt       time.Time
	schemaReady     bool
	schemaExpiresAt time.Time
	candidateHash   string
	candidateSince  time.Time
}

type SystemInstanceInfo struct {
	SchemaVersion int                        `json:"schema_version"`
	Node          common.NodeIdentity        `json:"node"`
	Role          SystemInstanceRoleInfo     `json:"role"`
	Runtime       SystemInstanceRuntimeInfo  `json:"runtime"`
	Host          SystemInstanceHostInfo     `json:"host"`
	Resources     SystemInstanceResources    `json:"resources,omitempty"`
	Capabilities  SystemInstanceCapabilities `json:"capabilities"`
	Extra         map[string]any             `json:"extra,omitempty"`
}

type SystemInstanceCapabilities struct {
	AsyncBillingProtocol      int    `json:"async_billing_protocol"`
	AsyncBillingRolloutEpoch  string `json:"async_billing_rollout_epoch,omitempty"`
	AsyncBillingIncarnationID string `json:"async_billing_incarnation_id,omitempty"`
}

type SystemInstanceRoleInfo struct {
	IsMaster bool `json:"is_master"`
}

type SystemInstanceRuntimeInfo struct {
	Version   string `json:"version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	StartedAt int64  `json:"started_at"`
}

type SystemInstanceHostInfo struct {
	Hostname string `json:"hostname"`
}

type SystemInstanceResources struct {
	CPU     SystemInstanceResourceUsage  `json:"cpu"`
	Memory  SystemInstanceResourceUsage  `json:"memory"`
	Storage SystemInstanceStorageMetrics `json:"storage"`
}

type SystemInstanceResourceUsage struct {
	UsagePercent float64 `json:"usage_percent"`
}

type SystemInstanceStorageMetrics struct {
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

func StartSystemInstanceReporter() {
	systemInstanceReporterOnce.Do(func() {
		// Publish capabilities before serving can activate a fleet-gated protocol.
		reportSystemInstanceWithLog()
		gopool.Go(func() {
			ticker := time.NewTicker(systemInstanceReportInterval)
			defer ticker.Stop()
			for range ticker.C {
				reportSystemInstanceWithLog()
			}
		})
	})
}

func ReportCurrentSystemInstance() error {
	identity := common.GetNodeIdentity()
	hostname, hostnameErr := os.Hostname()
	if strings.TrimSpace(identity.Name) == "" {
		if hostnameErr != nil || strings.TrimSpace(hostname) == "" {
			return fmt.Errorf("system instance node name is empty")
		}
		identity.Name = hostname
		identity.Source = common.NodeNameSourceHostname
		identity.ManuallyConfigured = false
		identity.ShouldConfigureManually = true
	}
	systemStatus := common.GetSystemStatus()
	diskInfo := common.GetDiskSpaceInfo()
	info := SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          identity,
		Role: SystemInstanceRoleInfo{
			IsMaster: common.IsMasterNode,
		},
		Runtime: SystemInstanceRuntimeInfo{
			Version:   common.Version,
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
			StartedAt: common.StartTime,
		},
		Host: SystemInstanceHostInfo{
			Hostname: hostname,
		},
		Resources: SystemInstanceResources{
			CPU: SystemInstanceResourceUsage{
				UsagePercent: systemStatus.CPUUsage,
			},
			Memory: SystemInstanceResourceUsage{
				UsagePercent: systemStatus.MemoryUsage,
			},
			Storage: SystemInstanceStorageMetrics{
				TotalBytes:  diskInfo.Total,
				UsedBytes:   diskInfo.Used,
				FreeBytes:   diskInfo.Free,
				UsedPercent: diskInfo.UsedPercent,
			},
		},
		Capabilities: SystemInstanceCapabilities{
			AsyncBillingProtocol:      asyncBillingProtocolVersion,
			AsyncBillingRolloutEpoch:  asyncBillingRolloutEpoch(),
			AsyncBillingIncarnationID: asyncBillingIncarnationID(),
		},
	}
	return model.UpsertSystemInstance(
		identity.Name, info.Capabilities.AsyncBillingIncarnationID, info, common.StartTime, common.GetTimestamp(),
	)
}

// AsyncBillingV2FleetReady prevents v2 writers from activating while an old
// poller is still alive. Old instance payloads have no capability field and
// therefore fail closed until their heartbeat becomes stale.
func AsyncBillingV2FleetReady(ctx context.Context) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	asyncBillingFleetGateCache.Lock()
	defer asyncBillingFleetGateCache.Unlock()
	if !common.GetEnvOrDefaultBool("ASYNC_BILLING_V2_ENABLED", true) || common.BatchUpdateEnabled || model.DB == nil {
		resetAsyncBillingFleetCandidateLocked()
		asyncBillingFleetGateCache.expiresAt = time.Time{}
		if model.DB == nil {
			asyncBillingFleetGateCache.schemaReady = false
			asyncBillingFleetGateCache.schemaExpiresAt = time.Time{}
		}
		return false
	}
	if now.Before(asyncBillingFleetGateCache.expiresAt) {
		return asyncBillingFleetGateCache.ready
	}
	asyncBillingFleetGateCache.ready = false
	asyncBillingFleetGateCache.expiresAt = now.Add(asyncBillingFleetCacheTTL)
	if !asyncBillingFleetGateCache.schemaReady || !now.Before(asyncBillingFleetGateCache.schemaExpiresAt) {
		routingSchemaReady, err := model.RoutingSchemaReady(model.DB.WithContext(ctx))
		if err != nil || !routingSchemaReady {
			resetAsyncBillingFleetCandidateLocked()
			asyncBillingFleetGateCache.schemaReady = false
			asyncBillingFleetGateCache.schemaExpiresAt = now.Add(asyncBillingFleetCacheTTL)
			return false
		}
		asyncBillingSchemaReady, err := model.AsyncBillingV2SchemaReady(model.DB.WithContext(ctx))
		if err != nil || !asyncBillingSchemaReady {
			resetAsyncBillingFleetCandidateLocked()
			asyncBillingFleetGateCache.schemaReady = false
			asyncBillingFleetGateCache.schemaExpiresAt = now.Add(asyncBillingFleetCacheTTL)
			return false
		}
		asyncBillingFleetGateCache.schemaReady = true
		asyncBillingFleetGateCache.schemaExpiresAt = now.Add(asyncBillingSchemaCacheTTL)
	}
	instances, err := model.ListActiveSystemInstances(now.Unix(), asyncBillingFleetActiveLimit)
	if err != nil {
		resetAsyncBillingFleetCandidateLocked()
		return false
	}
	epoch := asyncBillingRolloutEpoch()
	fingerprint := strings.Builder{}
	for _, instance := range instances {
		if instance == nil {
			resetAsyncBillingFleetCandidateLocked()
			return false
		}
		var info SystemInstanceInfo
		if common.UnmarshalJsonStr(instance.Info, &info) != nil ||
			info.Capabilities.AsyncBillingProtocol < asyncBillingProtocolVersion ||
			info.Capabilities.AsyncBillingRolloutEpoch != epoch ||
			strings.TrimSpace(info.Capabilities.AsyncBillingIncarnationID) == "" {
			resetAsyncBillingFleetCandidateLocked()
			return false
		}
		_, _ = fmt.Fprintf(&fingerprint, "%s\x00%d\x00%d\x00%s\x00%s\n", instance.NodeName, instance.StartedAt,
			info.Capabilities.AsyncBillingProtocol, info.Capabilities.AsyncBillingRolloutEpoch,
			info.Capabilities.AsyncBillingIncarnationID)
	}
	if len(instances) == 0 {
		resetAsyncBillingFleetCandidateLocked()
		return false
	}
	digest := sha256.Sum256([]byte(fingerprint.String()))
	candidateHash := hex.EncodeToString(digest[:])
	if asyncBillingFleetGateCache.candidateHash != candidateHash {
		asyncBillingFleetGateCache.candidateHash = candidateHash
		asyncBillingFleetGateCache.candidateSince = now
		return false
	}
	stableAfter := asyncBillingFleetStableAfter
	if configured := common.GetEnvOrDefault("ASYNC_BILLING_V2_FLEET_STABLE_SECONDS", int(stableAfter/time.Second)); configured >= 0 {
		stableAfter = time.Duration(configured) * time.Second
	}
	if now.Sub(asyncBillingFleetGateCache.candidateSince) < stableAfter {
		return false
	}
	asyncBillingFleetGateCache.ready = true
	return true
}

func asyncBillingIncarnationID() string {
	systemInstanceIncarnation.Do(func() {
		value := make([]byte, 16)
		if _, err := rand.Read(value); err == nil {
			systemInstanceIncarnation.id = hex.EncodeToString(value)
			return
		}
		fallback := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", common.NodeName, common.StartTime, os.Getpid())))
		systemInstanceIncarnation.id = hex.EncodeToString(fallback[:16])
	})
	return systemInstanceIncarnation.id
}

func resetAsyncBillingFleetCandidateLocked() {
	asyncBillingFleetGateCache.ready = false
	asyncBillingFleetGateCache.candidateHash = ""
	asyncBillingFleetGateCache.candidateSince = time.Time{}
}

func asyncBillingRolloutEpoch() string {
	epoch := strings.TrimSpace(os.Getenv("ASYNC_BILLING_V2_ROLLOUT_EPOCH"))
	if epoch == "" {
		return "async-billing-v2"
	}
	return epoch
}

func reportSystemInstanceWithLog() {
	if err := ReportCurrentSystemInstance(); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("system instance report failed: %v", err))
	}
}
