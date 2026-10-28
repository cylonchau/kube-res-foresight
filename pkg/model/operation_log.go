package model

import "time"

// OperationLog is an append-only record of every mutating (POST/PUT/DELETE/
// PATCH) request handled behind JWTAuthMiddleware/CasbinMiddleware — the
// admin console's "系统管理/操作日志" page. It intentionally does not cover
// GET requests (too noisy, and not "an operation" in the conventional sense
// for a Chinese admin console) or the login endpoints themselves, which
// already have their own narrower record in LoginAuditLog.
//
// ClientType is how the four filterable sources in the UI ("页面日志" /
// "tma日志" / "api日志" / "CLI日志") are told apart: "page" for the web admin
// console, "api" for calls authenticated
// with an access-key/secret developer credential rather than an
// interactive login, and "cli" for a command-line client using that developer
// credential with X-kube-resource-fcst-Client: cli (see middleware.resolveClientType).
type OperationLog struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`
	// RequestID is the identifier of the operation: the caller's X-Request-Id when it sent one, else generated. It is also
	// returned in the X-Request-Id response header, so a caller can quote it when it asks what happened to a request.
	RequestID string `gorm:"type:varchar(64);index" json:"request_id" column_setting:"true"`
	// ClientType is the kind of caller: page (the web console), api (a developer credential), cli, or operator.
	ClientType string `gorm:"type:varchar(16);index" json:"client_type" column_setting:"true"`
	// ClientName names the caller where it says so (the operator's Pod, the CLI's host); empty for the web console.
	ClientName string `gorm:"type:varchar(128)" json:"client_name" column_setting:"true"`
	UserID     uint   `gorm:"index" json:"user_id" column_setting:"false"`
	Username   string `gorm:"type:varchar(128);index" json:"username" column_setting:"true"`
	Method     string `gorm:"type:varchar(10)" json:"method" column_setting:"true"`
	Path       string `gorm:"type:varchar(255);index" json:"path" column_setting:"true"`
	// Module is the first path segment after /api/v1/ (e.g. "users",
	// "modules", "tasks") — a cheap, curation-free grouping for the
	// filter UI without needing a hand-maintained action catalog.
	Module     string `gorm:"type:varchar(64);index" json:"module" column_setting:"true"`
	StatusCode int    `gorm:"index" json:"status_code" column_setting:"true"`
	ClientIP   string `gorm:"type:varchar(64)" json:"client_ip" column_setting:"true"`
	UserAgent  string `gorm:"type:varchar(255)" json:"user_agent" column_setting:"false"`
	// Params is the raw querystring; Body is the request body (JSON/form
	// only, capped, secret-shaped fields redacted) — see
	// middleware.OperationLogMiddleware for the capture/redaction rules.
	Params     string `gorm:"type:text" json:"params" column_setting:"false"`
	Body       string `gorm:"type:text" json:"body" column_setting:"false"`
	DurationMs int64  `json:"duration_ms" column_setting:"true"`
	ErrorMsg   string `gorm:"type:varchar(500)" json:"error_msg" column_setting:"false"`
}

func (OperationLog) TableName() string { return "operation_log" }

func init() { RegisterModel(&OperationLog{}) }
