-- 回滚 000055：删除云之家员工目录与同步运行记录两张新表。
-- 账号本身（users / tenant_members）不受影响；回滚会丢失员工映射与运行历史。

DROP TABLE IF EXISTS yunzhijia_sync_runs;
DROP TABLE IF EXISTS yunzhijia_employees;
