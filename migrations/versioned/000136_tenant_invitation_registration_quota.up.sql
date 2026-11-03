-- Migration: 000136_tenant_invitation_registration_quota
-- 给共享邀请链接增加注册配额字段：
--   max_registrations  最多可注册人数，NULL 表示不限；
--   registered_count   已注册人数，默认 0。
-- 只统计通过链接成功创建的新账号，已有账号登录加入不计数。
-- 存量链接 max_registrations=NULL、registered_count=0，无需回填。

DO $$ BEGIN RAISE NOTICE '[Migration 000136] Adding tenant_invitations registration quota columns'; END $$;

ALTER TABLE tenant_invitations
    ADD COLUMN IF NOT EXISTS max_registrations INTEGER,
    ADD COLUMN IF NOT EXISTS registered_count INTEGER NOT NULL DEFAULT 0;

DO $$ BEGIN RAISE NOTICE '[Migration 000136] Done'; END $$;
