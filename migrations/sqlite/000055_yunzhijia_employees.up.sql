-- Migration: 000055_yunzhijia_employees
-- 云之家免登录的基础数据：员工目录 + 夜间同步运行记录。
-- 只新增表/索引，不改现有表；SQLite 无 JSONB，anomalies 存 TEXT。

CREATE TABLE IF NOT EXISTS yunzhijia_employees (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    open_id          VARCHAR(64)  NOT NULL,
    name             VARCHAR(255) NOT NULL,
    department       VARCHAR(500) NOT NULL DEFAULT '',
    mobile           VARCHAR(32),
    tenant_id        BIGINT       NOT NULL,
    user_id          VARCHAR(36),
    status           VARCHAR(20)  NOT NULL DEFAULT 'active',
    disabled_by_sync BOOLEAN      NOT NULL DEFAULT 0,
    blocked          BOOLEAN      NOT NULL DEFAULT 0,
    blocked_at       DATETIME,
    blocked_by       VARCHAR(36),
    last_synced_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at       DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_yunzhijia_employees_open_id
    ON yunzhijia_employees (open_id);
CREATE INDEX IF NOT EXISTS idx_yunzhijia_employees_name_status
    ON yunzhijia_employees (name, status);
CREATE INDEX IF NOT EXISTS idx_yunzhijia_employees_user_id
    ON yunzhijia_employees (user_id);
CREATE INDEX IF NOT EXISTS idx_yunzhijia_employees_tenant_status
    ON yunzhijia_employees (tenant_id, status);

CREATE TABLE IF NOT EXISTS yunzhijia_sync_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger         VARCHAR(16) NOT NULL,
    status          VARCHAR(16) NOT NULL,
    started_at      DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at     DATETIME,
    active_count    INTEGER     NOT NULL DEFAULT 0,
    inactive_count  INTEGER     NOT NULL DEFAULT 0,
    created_count   INTEGER     NOT NULL DEFAULT 0,
    updated_count   INTEGER     NOT NULL DEFAULT 0,
    disabled_count  INTEGER     NOT NULL DEFAULT 0,
    migrated_count  INTEGER     NOT NULL DEFAULT 0,
    anomaly_count   INTEGER     NOT NULL DEFAULT 0,
    anomalies       TEXT,
    error_summary   TEXT        NOT NULL DEFAULT '',
    created_at      DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_yunzhijia_sync_runs_started_at
    ON yunzhijia_sync_runs (started_at DESC);
