-- 回滚 000136：删除注册配额两列。
-- 注意：旧代码不校验配额，回滚后设置过上限（含已满额）的 pending 链接会变成不限人数；
-- 如需保留限额意图，请在回滚前手动撤销这些链接。

ALTER TABLE tenant_invitations DROP COLUMN IF EXISTS max_registrations;
ALTER TABLE tenant_invitations DROP COLUMN IF EXISTS registered_count;
