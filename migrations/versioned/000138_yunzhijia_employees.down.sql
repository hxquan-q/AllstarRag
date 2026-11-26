-- 回滚 000138：删除云之家员工目录与同步运行记录两张新表。
-- 两张表均为本 feature 新增，无其它模块依赖；回滚会丢失已同步的
-- 员工映射与运行历史，账号本身（users / tenant_members）不受影响。

DROP TABLE IF EXISTS yunzhijia_sync_runs;
DROP TABLE IF EXISTS yunzhijia_employees;
