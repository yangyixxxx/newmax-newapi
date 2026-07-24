package model

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PerfMetricChannel stores aggregated relay performance metrics broken down by
// channel. It runs in parallel to PerfMetric (which aggregates by model only)
// so the existing model-level success-rate feature is never affected.
//
// channel_id = 0 is a sentinel meaning "unattributed": historical failures that
// were recorded before per-channel metrics existed (and therefore cannot be tied
// to a specific channel) are backfilled here so totals still reconcile with the
// model-level numbers without falsely blaming any real channel.
type PerfMetricChannel struct {
	Id             int    `json:"id" gorm:"primaryKey"`
	ModelName      string `json:"model_name" gorm:"size:128;uniqueIndex:idx_perfch_model_group_ch_bucket,priority:1"`
	Group          string `json:"group" gorm:"column:group;size:64;uniqueIndex:idx_perfch_model_group_ch_bucket,priority:2"`
	ChannelId      int    `json:"channel_id" gorm:"uniqueIndex:idx_perfch_model_group_ch_bucket,priority:3"`
	BucketTs       int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_perfch_model_group_ch_bucket,priority:4;index:idx_perfch_bucket_ts"`
	RequestCount   int64  `json:"-" gorm:"default:0"`
	SuccessCount   int64  `json:"-" gorm:"default:0"`
	TotalLatencyMs int64  `json:"-" gorm:"default:0"`
	TtftSumMs      int64  `json:"-" gorm:"default:0"`
	TtftCount      int64  `json:"-" gorm:"default:0"`
	OutputTokens   int64  `json:"-" gorm:"default:0"`
	GenerationMs   int64  `json:"-" gorm:"default:0"`
}

func (PerfMetricChannel) TableName() string {
	return "perf_metrics_channel"
}

func UpsertPerfMetricChannel(metric *PerfMetricChannel) error {
	if metric == nil || metric.RequestCount == 0 {
		return nil
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "model_name"},
			{Name: "group"},
			{Name: "channel_id"},
			{Name: "bucket_ts"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"request_count":    gorm.Expr("perf_metrics_channel.request_count + ?", metric.RequestCount),
			"success_count":    gorm.Expr("perf_metrics_channel.success_count + ?", metric.SuccessCount),
			"total_latency_ms": gorm.Expr("perf_metrics_channel.total_latency_ms + ?", metric.TotalLatencyMs),
			"ttft_sum_ms":      gorm.Expr("perf_metrics_channel.ttft_sum_ms + ?", metric.TtftSumMs),
			"ttft_count":       gorm.Expr("perf_metrics_channel.ttft_count + ?", metric.TtftCount),
			"output_tokens":    gorm.Expr("perf_metrics_channel.output_tokens + ?", metric.OutputTokens),
			"generation_ms":    gorm.Expr("perf_metrics_channel.generation_ms + ?", metric.GenerationMs),
		}),
	}).Create(metric).Error
}

type PerfMetricChannelSummary struct {
	ChannelId      int   `json:"channel_id"`
	RequestCount   int64 `json:"request_count"`
	SuccessCount   int64 `json:"success_count"`
	TotalLatencyMs int64 `json:"total_latency_ms"`
	OutputTokens   int64 `json:"output_tokens"`
	GenerationMs   int64 `json:"generation_ms"`
}

// GetPerfMetricsChannelSummaryAll aggregates channel-level metrics over a window.
// When modelName is non-empty results are scoped to that model.
func GetPerfMetricsChannelSummaryAll(startTs int64, endTs int64, groups []string, modelName string) ([]PerfMetricChannelSummary, error) {
	var summaries []PerfMetricChannelSummary
	query := DB.Model(&PerfMetricChannel{}).
		Select("channel_id, SUM(request_count) as request_count, SUM(success_count) as success_count, SUM(total_latency_ms) as total_latency_ms, SUM(output_tokens) as output_tokens, SUM(generation_ms) as generation_ms").
		Where("bucket_ts >= ? AND bucket_ts <= ?", startTs, endTs)
	if modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	if groups != nil {
		if len(groups) == 0 {
			return summaries, nil
		}
		query = query.Where(commonGroupCol+" IN ?", groups)
	}
	err := query.
		Group("channel_id").
		Having("SUM(request_count) > 0").
		Find(&summaries).Error
	return summaries, err
}

func DeletePerfMetricsChannelBefore(cutoffTs int64) error {
	if cutoffTs <= 0 {
		return nil
	}
	return DB.Where("bucket_ts < ?", cutoffTs).Delete(&PerfMetricChannel{}).Error
}
