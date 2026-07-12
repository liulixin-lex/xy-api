package model

import (
	"context"
	"time"
)

const (
	routingAttemptMetricsMinimumWindow = time.Minute
	routingAttemptMetricsMaximumWindow = 30 * 24 * time.Hour
	routingAttemptMaximumPlatformCost  = 1_000_000_000.0
)

type RoutingAttemptWindowMetrics struct {
	FromTimeMs        int64                          `json:"from_time_ms"`
	ToTimeMs          int64                          `json:"to_time_ms"`
	PreCommitFailover RoutingPreCommitFailoverMetric `json:"pre_commit_failover_success_rate"`
	UnitPlatformCost  RoutingUnitPlatformCostMetric  `json:"unit_request_platform_cost"`
}

type RoutingPreCommitFailoverMetric struct {
	Known       bool    `json:"known"`
	Rate        float64 `json:"rate,omitempty"`
	Numerator   int64   `json:"numerator"`
	Denominator int64   `json:"denominator"`
	Covered     int64   `json:"covered"`
	Coverage    float64 `json:"coverage"`
}

type RoutingUnitPlatformCostMetric struct {
	Known               bool    `json:"known"`
	Value               float64 `json:"value,omitempty"`
	TotalPlatformCost   float64 `json:"total_platform_cost,omitempty"`
	RequestCount        int64   `json:"request_count"`
	SentAttempts        int64   `json:"sent_attempts"`
	KnownAttempts       int64   `json:"known_attempts"`
	UnknownAttempts     int64   `json:"unknown_attempts"`
	Coverage            float64 `json:"coverage"`
	Currency            string  `json:"currency,omitempty"`
	Unit                string  `json:"unit,omitempty"`
	DimensionConsistent bool    `json:"dimension_consistent"`
}

func GetRoutingAttemptWindowMetricsContext(
	ctx context.Context,
	fromTimeMs int64,
	toTimeMs int64,
) (RoutingAttemptWindowMetrics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil || fromTimeMs <= 0 || toTimeMs <= fromTimeMs {
		return RoutingAttemptWindowMetrics{}, ErrRoutingHedgeAttemptInvalid
	}
	window := time.Duration(toTimeMs-fromTimeMs) * time.Millisecond
	if window < routingAttemptMetricsMinimumWindow || window > routingAttemptMetricsMaximumWindow {
		return RoutingAttemptWindowMetrics{}, ErrRoutingHedgeAttemptInvalid
	}
	metrics := RoutingAttemptWindowMetrics{FromTimeMs: fromTimeMs, ToTimeMs: toTimeMs}
	logicRequests := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
		Where("state = ? AND completed_time_ms >= ? AND completed_time_ms < ?",
			RoutingHedgeAttemptStateCompleted, fromTimeMs, toTimeMs).
		Select(
			`request_key,
		 MAX(CASE WHEN execution_mode = ? AND will_retry = ? THEN 1 ELSE 0 END) AS had_precommit_failover,
		 MAX(CASE WHEN upstream_sent = ? AND final_attempt = ? AND result = ? THEN 1 ELSE 0 END) AS succeeded,
		 MAX(CASE WHEN final_attempt = ? THEN 1 ELSE 0 END) AS covered`,
			RoutingAttemptExecutionSerial, true, true, true, RoutingHedgeAttemptResultSuccess, true,
		).Group("request_key")
	var failover struct {
		Denominator int64
		Numerator   int64
		Covered     int64
	}
	if err := DB.WithContext(ctx).Table("(?) AS routing_logic_requests", logicRequests).
		Select(`COUNT(*) AS denominator,
			COALESCE(SUM(CASE WHEN succeeded = 1 THEN 1 ELSE 0 END), 0) AS numerator,
			COALESCE(SUM(CASE WHEN covered = 1 THEN 1 ELSE 0 END), 0) AS covered`).
		Where("had_precommit_failover = 1").Scan(&failover).Error; err != nil {
		return RoutingAttemptWindowMetrics{}, err
	}
	metrics.PreCommitFailover.Denominator = failover.Denominator
	metrics.PreCommitFailover.Numerator = failover.Numerator
	metrics.PreCommitFailover.Covered = failover.Covered
	if failover.Denominator > 0 {
		metrics.PreCommitFailover.Known = failover.Covered == failover.Denominator
		metrics.PreCommitFailover.Rate = float64(failover.Numerator) / float64(failover.Denominator)
		metrics.PreCommitFailover.Coverage = float64(failover.Covered) / float64(failover.Denominator)
	}

	var costCounts struct {
		RequestCount  int64
		SentAttempts  int64
		KnownAttempts int64
	}
	knownCostPredicate := `actual_cost_known = ? AND actual_cost >= ? AND actual_cost <= ? AND
		cost_currency <> ? AND cost_unit <> ?`
	if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
		Where("state = ? AND completed_time_ms >= ? AND completed_time_ms < ?",
			RoutingHedgeAttemptStateCompleted, fromTimeMs, toTimeMs).
		Select(
			`COUNT(DISTINCT CASE WHEN upstream_sent = ? THEN request_key ELSE NULL END) AS request_count,
		 COALESCE(SUM(CASE WHEN upstream_sent = ? THEN 1 ELSE 0 END), 0) AS sent_attempts,
		 COALESCE(SUM(CASE WHEN upstream_sent = ? AND `+knownCostPredicate+` THEN 1 ELSE 0 END), 0) AS known_attempts`,
			true,
			true,
			true, true, 0, routingAttemptMaximumPlatformCost, "", "",
		).Scan(&costCounts).Error; err != nil {
		return RoutingAttemptWindowMetrics{}, err
	}
	metric := &metrics.UnitPlatformCost
	metric.RequestCount = costCounts.RequestCount
	metric.SentAttempts = costCounts.SentAttempts
	metric.KnownAttempts = costCounts.KnownAttempts
	metric.UnknownAttempts = max(int64(0), costCounts.SentAttempts-costCounts.KnownAttempts)
	if metric.SentAttempts > 0 {
		metric.Coverage = float64(metric.KnownAttempts) / float64(metric.SentAttempts)
	}
	type costDimension struct {
		Currency string
		Unit     string
	}
	var dimensions []costDimension
	if costCounts.KnownAttempts > 0 {
		if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
			Where("state = ? AND completed_time_ms >= ? AND completed_time_ms < ?",
				RoutingHedgeAttemptStateCompleted, fromTimeMs, toTimeMs).
			Select("cost_currency AS currency, cost_unit AS unit").
			Where("upstream_sent = ? AND "+knownCostPredicate,
				true, true, 0, routingAttemptMaximumPlatformCost, "", "").
			Group("cost_currency, cost_unit").Order("cost_currency ASC").Order("cost_unit ASC").
			Limit(2).Scan(&dimensions).Error; err != nil {
			return RoutingAttemptWindowMetrics{}, err
		}
	}
	metric.DimensionConsistent = len(dimensions) <= 1
	if len(dimensions) == 1 {
		metric.Currency = dimensions[0].Currency
		metric.Unit = dimensions[0].Unit
	}
	metric.Known = metric.RequestCount > 0 && metric.SentAttempts > 0 &&
		metric.UnknownAttempts == 0 && len(dimensions) == 1
	if metric.Known {
		var total struct {
			Cost float64
		}
		if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
			Where("state = ? AND completed_time_ms >= ? AND completed_time_ms < ?",
				RoutingHedgeAttemptStateCompleted, fromTimeMs, toTimeMs).
			Select("COALESCE(SUM(actual_cost), 0) AS cost").
			Where("upstream_sent = ? AND "+knownCostPredicate+" AND cost_currency = ? AND cost_unit = ?",
				true, true, 0, routingAttemptMaximumPlatformCost, "", "", metric.Currency, metric.Unit).
			Scan(&total).Error; err != nil {
			return RoutingAttemptWindowMetrics{}, err
		}
		metric.TotalPlatformCost = total.Cost
		metric.Value = total.Cost / float64(metric.RequestCount)
	}
	return metrics, nil
}
