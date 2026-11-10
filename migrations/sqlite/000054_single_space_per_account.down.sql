-- Migration: 000054_single_space_per_account (down)
-- 回滚说明：本迁移只做角色降级与成员挂起，不硬删数据。
-- down 只删除唯一 owner 索引；数据回滚以迁移执行前的全库备份为基线。
-- 不要用 down 猜测恢复 contributor/admin 角色或 suspended 成员，统一走备份恢复。
DROP INDEX IF EXISTS idx_tenant_members_tenant_owner_unique;
