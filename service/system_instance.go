package service

import (
	"context"
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
	"github.com/google/uuid"
)

const systemInstanceReportInterval = 30 * time.Second

var systemInstanceReporterOnce sync.Once
var systemInstanceID = uuid.NewString()

type SystemInstanceInfo struct {
	SchemaVersion int                       `json:"schema_version"`
	Node          common.NodeIdentity       `json:"node"`
	Role          SystemInstanceRoleInfo    `json:"role"`
	Runtime       SystemInstanceRuntimeInfo `json:"runtime"`
	Payment       PaymentRuntimeInfo        `json:"payment"`
	Host          SystemInstanceHostInfo    `json:"host"`
	Resources     SystemInstanceResources   `json:"resources,omitempty"`
	Extra         map[string]any            `json:"extra,omitempty"`
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
		// Register synchronously before payment workers begin claiming work. The
		// recurring refresh remains asynchronous.
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

func currentSystemInstanceInventoryKey(nodeName string) string {
	runes := []rune(strings.TrimSpace(nodeName))
	// SystemInstance.NodeName is varchar(128). UUID plus separator consumes 37
	// characters, leaving 91 for the operator-visible logical node name.
	if len(runes) > 91 {
		runes = runes[:91]
	}
	return string(runes) + "@" + systemInstanceID
}

func currentSystemInstanceIdentity() (common.NodeIdentity, string, error) {
	identity := common.GetNodeIdentity()
	hostname, hostnameErr := os.Hostname()
	if strings.TrimSpace(identity.Name) == "" {
		if hostnameErr != nil || strings.TrimSpace(hostname) == "" {
			return common.NodeIdentity{}, "", fmt.Errorf("system instance node name is empty")
		}
		identity.Name = hostname
		identity.Source = common.NodeNameSourceHostname
		identity.ManuallyConfigured = false
		identity.ShouldConfigureManually = true
	}
	return identity, hostname, nil
}

func ReportCurrentSystemInstance() error {
	identity, hostname, err := currentSystemInstanceIdentity()
	if err != nil {
		return err
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
		Payment: currentPaymentRuntimeInfo(),
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
	}
	inventoryKey := currentSystemInstanceInventoryKey(identity.Name)
	if err := model.UpsertSystemInstance(inventoryKey, info, common.StartTime, common.GetTimestamp()); err != nil {
		return err
	}
	return reportPaymentClusterRedisLease(inventoryKey)
}

func UnregisterCurrentSystemInstance() error {
	identity, _, err := currentSystemInstanceIdentity()
	if err != nil {
		return err
	}
	inventoryKey := currentSystemInstanceInventoryKey(identity.Name)
	if _, err = model.DeleteSystemInstance(inventoryKey); err != nil {
		return err
	}
	return unregisterPaymentClusterRedisLease(inventoryKey)
}

func reportSystemInstanceWithLog() {
	if err := ReportCurrentSystemInstance(); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("system instance report failed: %v", err))
	}
}
