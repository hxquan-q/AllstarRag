package types

import "time"

// YunzhijiaEmployeeStatus 是员工目录记录在本地同步视角下的状态。
//   - active：上游标记在职；
//   - inactive：上游标记离职。
//
// 本地账号是否可用还要结合 disabled_by_sync 与 blocked 判定，
// 不能只看这里的状态。
type YunzhijiaEmployeeStatus string

const (
	// YunzhijiaEmployeeStatusActive 表示上游员工接口标记为在职。
	YunzhijiaEmployeeStatusActive YunzhijiaEmployeeStatus = "active"
	// YunzhijiaEmployeeStatusInactive 表示上游员工接口标记为离职。
	YunzhijiaEmployeeStatusInactive YunzhijiaEmployeeStatus = "inactive"
)

// YunzhijiaEmployee 是云之家员工目录与本地账号的映射行。
//
// 一个 open_id 只对应一行；user_id 在本地账号创建成功后回填，
// 未开号时为空。手机号只在员工表保存，仅系统管理员可见，
// 不写入 users 表。
type YunzhijiaEmployee struct {
	ID uint64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// OpenID 是云之家员工唯一标识，也是本表唯一键。
	OpenID string `json:"open_id" gorm:"column:open_id;type:varchar(64);not null;uniqueIndex:uq_yunzhijia_employees_open_id"`
	// Name 用于 ticket 姓名匹配；同名会拒绝登录，不做自动消歧。
	Name string `json:"name" gorm:"type:varchar(255);not null;index:idx_yunzhijia_employees_name_status,priority:1"`
	// Department 保留上游部门路径原文，用于判断是否属于人力空间。
	Department string `json:"department" gorm:"type:varchar(500);not null;default:''"`
	// Mobile 可为空；存但不展示，不参与登录。
	Mobile *string `json:"-" gorm:"type:varchar(32)"`
	// TenantID 是该员工当前应归属的本地空间（默认 10024，人力 10022）。
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_yunzhijia_employees_tenant_status,priority:1"`
	// UserID 指向本地 users.id，未开号时为空。
	UserID *string `json:"user_id,omitempty" gorm:"column:user_id;type:varchar(36);index:idx_yunzhijia_employees_user_id"`
	// Status 记录上游在职/离职状态。禁/启用本地账号由 Service 依据它决定。
	Status YunzhijiaEmployeeStatus `json:"status" gorm:"type:varchar(20);not null;default:'active';index:idx_yunzhijia_employees_name_status,priority:2;index:idx_yunzhijia_employees_tenant_status,priority:2"`
	// DisabledBySync 只表示“账号是被同步因离职停用的”。管理员封禁或
	// 其它人工停用必须置 false；再入职自动恢复只认 true。
	DisabledBySync bool `json:"disabled_by_sync" gorm:"not null;default:false"`
	// Blocked 为 true 时登录与同步都跳过，且禁止重建本地账号；
	// 只有管理员显式解除后才会在下一轮同步重建。
	Blocked bool `json:"blocked" gorm:"not null;default:false"`
	// BlockedAt / BlockedBy 记录删除或禁止重建的时间与操作人。
	BlockedAt *time.Time `json:"blocked_at,omitempty"`
	BlockedBy *string    `json:"blocked_by,omitempty" gorm:"type:varchar(36)"`
	// LastSyncedAt 是最近一次从上游看到该员工的同步时间。
	LastSyncedAt time.Time `json:"last_synced_at" gorm:"not null"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 固定表名，避免 GORM 复数命名带来歧义。
func (YunzhijiaEmployee) TableName() string { return "yunzhijia_employees" }

// YunzhijiaSyncTrigger 是一次同步运行由谁触发。
type YunzhijiaSyncTrigger string

const (
	YunzhijiaSyncTriggerStartup YunzhijiaSyncTrigger = "startup"
	YunzhijiaSyncTriggerCron    YunzhijiaSyncTrigger = "cron"
	YunzhijiaSyncTriggerManual  YunzhijiaSyncTrigger = "manual"
	// YunzhijiaSyncTriggerPreview 是管理员预检：只抓取/校验/计数，
	// 不写员工记录与账号。Latest 查询会排除这类记录。
	YunzhijiaSyncTriggerPreview YunzhijiaSyncTrigger = "preview"
)

// YunzhijiaSyncStatus 是一次同步运行的最终状态。
type YunzhijiaSyncStatus string

const (
	YunzhijiaSyncStatusRunning YunzhijiaSyncStatus = "running"
	YunzhijiaSyncStatusSuccess YunzhijiaSyncStatus = "success"
	YunzhijiaSyncStatusFailed  YunzhijiaSyncStatus = "failed"
)

// YunzhijiaAccountResult 描述一轮同步对单个员工账号实际执行的动作，
// 供 Run 累计 AC-14 的开号/更新/停用/迁移计数。
type YunzhijiaAccountResult string

const (
	// YunzhijiaAccountResultNone 表示本次没有改动账号（blocked、已停用等）。
	YunzhijiaAccountResultNone YunzhijiaAccountResult = "none"
	// YunzhijiaAccountResultCreated 表示本轮新建了本地账号。
	YunzhijiaAccountResultCreated YunzhijiaAccountResult = "created"
	// YunzhijiaAccountResultUpdated 表示本轮核对过的已有在职账号（含无变化），
	// 以及再入职恢复或补成员关系。
	YunzhijiaAccountResultUpdated YunzhijiaAccountResult = "updated"
	// YunzhijiaAccountResultDisabled 表示离职员工账号被停用。
	YunzhijiaAccountResultDisabled YunzhijiaAccountResult = "disabled"
	// YunzhijiaAccountResultMigrated 表示在职员工被迁移到另一个空间。
	YunzhijiaAccountResultMigrated YunzhijiaAccountResult = "migrated"
)

// YunzhijiaSyncRun 记录一次同步运行的结果，供管理端状态页读取。
// Anomalies 用 JSON 数组保存逐条异常，失败时 ErrorSummary 存脱敏摘要。
type YunzhijiaSyncRun struct {
	ID        uint64               `json:"id" gorm:"primaryKey;autoIncrement"`
	Trigger   YunzhijiaSyncTrigger `json:"trigger" gorm:"column:trigger;type:varchar(16);not null"`
	Status    YunzhijiaSyncStatus  `json:"status" gorm:"type:varchar(16);not null"`
	StartedAt time.Time            `json:"started_at" gorm:"not null"`
	// FinishedAt 在运行结束时写入；running 状态为空。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// 拉取数量
	ActiveCount   int `json:"active_count" gorm:"not null;default:0"`
	InactiveCount int `json:"inactive_count" gorm:"not null;default:0"`
	// 收敛计数
	CreatedCount  int `json:"created_count" gorm:"not null;default:0"`
	UpdatedCount  int `json:"updated_count" gorm:"not null;default:0"`
	DisabledCount int `json:"disabled_count" gorm:"not null;default:0"`
	MigratedCount int `json:"migrated_count" gorm:"not null;default:0"`
	AnomalyCount  int `json:"anomaly_count" gorm:"not null;default:0"`
	// Anomalies 是 []YunzhijiaSyncAnomaly 的 JSON 文本。
	// Postgres 用 JSONB，SQLite 存 TEXT。
	Anomalies JSON `json:"anomalies,omitempty"`
	// ErrorSummary 是整轮失败的脱敏摘要，不包含 ticket 等凭证。
	ErrorSummary string    `json:"error_summary" gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 固定表名，避免 GORM 复数命名带来歧义。
func (YunzhijiaSyncRun) TableName() string { return "yunzhijia_sync_runs" }

// YunzhijiaSyncAnomaly 是同步过程中记入运行记录的单条异常。
// 不写入任何 ticket；Detail 由调用方保证已脱敏。
type YunzhijiaSyncAnomaly struct {
	// Kind 取下面的 YunzhijiaAnomaly* 常量之一。
	Kind   string `json:"kind"`
	OpenID string `json:"open_id,omitempty"`
	Name   string `json:"name,omitempty"`
	UserID string `json:"user_id,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// 同步异常类型（01-spec §10.7 清单）。
const (
	YunzhijiaAnomalyDuplicateName     = "duplicate_name"
	YunzhijiaAnomalyDuplicateOpenID   = "duplicate_open_id"
	YunzhijiaAnomalyUnknownDepartment = "unknown_department"
	YunzhijiaAnomalyProvisionFailed   = "provision_failed"
	YunzhijiaAnomalyBlockedSkipped    = "blocked_skipped"
	YunzhijiaAnomalyBannedSkipped     = "banned_skipped"
	YunzhijiaAnomalyLocalUserMissing  = "local_user_missing"
	// YunzhijiaAnomalyLocalActiveMissing 表示本地在职员工既不在本轮
	// active 也不在 inactive 名单，已按离职收敛。
	YunzhijiaAnomalyLocalActiveMissing = "local_active_missing"
)
