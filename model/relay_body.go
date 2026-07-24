package model

import (
	"github.com/QuantumNous/new-api/common"
)

// RelayBody 保存每次中转请求的完整请求体与响应体（原文，不脱敏）。
//
// 设计要点：
//   - 独立于 logs 表：logs 是计费热表，保持精简；正文体量大、按 append-only 写入，
//     单独成表便于独立归档/迁移（可随 LOG_DB 一起迁到 ClickHouse）。
//   - 落在 LOG_DB（与 logs 同一日志库），复用 New API 现成的日志库路由。
//   - 冗余 token_name / model_name / user_id：无需 join logs 即可直接按用户/模型/时间检索
//     （token_name = <邮箱>_keyN，一眼定位到人）。
//   - request_id 与 logs.request_id 对齐，需要计费口径时可回 join。
//   - RequestBody/ResponseBody 用 text：Postgres 大字段自动走 TOAST 压缩+行外存储，
//     既可直接肉眼/LIKE 检索，又不额外占行内空间。
//   - Truncated：响应体超出上限（防单请求 OOM）时置真，标记该行正文不完整。
type RelayBody struct {
	Id               int64  `json:"id"`
	RequestId        string `json:"request_id" gorm:"index:idx_relay_body_request_id;default:''"`
	UserId           int    `json:"user_id" gorm:"index:idx_relay_body_user"`
	TokenId          int    `json:"token_id" gorm:"default:0"`
	TokenName        string `json:"token_name" gorm:"index:idx_relay_body_token_created,priority:1;default:''"`
	ChannelId        int    `json:"channel_id" gorm:"default:0"`
	ModelName        string `json:"model_name" gorm:"index:idx_relay_body_model;default:''"`
	RequestPath  string `json:"request_path" gorm:"default:''"`
	IsStream     bool   `json:"is_stream"`
	StatusCode   int    `json:"status_code" gorm:"default:0"`
	RequestBody  string `json:"request_body" gorm:"type:text"`
	ResponseBody string `json:"response_body" gorm:"type:text"`
	Truncated    bool   `json:"truncated"`
	CreatedAt    int64  `json:"created_at" gorm:"bigint;index:idx_relay_body_token_created,priority:2;index:idx_relay_body_created"`
}

func (RelayBody) TableName() string { return "relay_bodies" }

// RecordRelayBody 异步安全的正文落库（调用方通常已在 gopool 协程内）。
// best-effort：写失败只记日志，绝不影响主请求链路。
func RecordRelayBody(rb *RelayBody) {
	if err := LOG_DB.Create(rb).Error; err != nil {
		common.SysError("failed to record relay body: " + err.Error())
	}
}
