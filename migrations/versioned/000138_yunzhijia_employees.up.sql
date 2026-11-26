-- Migration: 000138_yunzhijia_employees
-- 云之家免登录的基础数据：员工目录 + 夜间同步运行记录。
-- 只新增表/索引，不改现有表；users / tenant_members 不受影响。
--
-- disabled_by_sync 语义（01-spec §7.4）：只表示“由同步因离职停用”，
-- 管理员封禁/人工停用必须为 false；再入职自动恢复只认 true。
-- blocked=true 优先级最高：登录与同步都跳过，且禁止重建本地账号。

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Creating yunzhijia_employees and yunzhijia_sync_runs'; END $$;

CREATE TABLE IF NOT EXISTS yunzhijia_employees (
    id               BIGSERIAL PRIMARY KEY,
    open_id          VARCHAR(64)  NOT NULL,
    name             VARCHAR(255) NOT NULL,
    department       VARCHAR(500) NOT NULL DEFAULT '',
    mobile           VARCHAR(32),
    tenant_id        BIGINT       NOT NULL,
    user_id          VARCHAR(36),
    status           VARCHAR(20)  NOT NULL DEFAULT 'active',
    disabled_by_sync BOOLEAN      NOT NULL DEFAULT FALSE,
    blocked          BOOLEAN      NOT NULL DEFAULT FALSE,
    blocked_at       TIMESTAMPTZ,
    blocked_by       VARCHAR(36),
    last_synced_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
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
    id              BIGSERIAL PRIMARY KEY,
    trigger         VARCHAR(16) NOT NULL,
    status          VARCHAR(16) NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at     TIMESTAMPTZ,
    active_count    INTEGER     NOT NULL DEFAULT 0,
    inactive_count  INTEGER     NOT NULL DEFAULT 0,
    created_count   INTEGER     NOT NULL DEFAULT 0,
    updated_count   INTEGER     NOT NULL DEFAULT 0,
    disabled_count  INTEGER     NOT NULL DEFAULT 0,
    migrated_count  INTEGER     NOT NULL DEFAULT 0,
    anomaly_count   INTEGER     NOT NULL DEFAULT 0,
    anomalies       JSONB,
    error_summary   TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_yunzhijia_sync_runs_started_at
    ON yunzhijia_sync_runs (started_at DESC);

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Done'; END $$;
