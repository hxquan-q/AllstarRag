-- Migration: 000137_single_space_per_account
-- 一号一空间（超管除外）+ 每空间唯一 owner 的存量数据收敛。
--
-- 原则：
--   - 只做角色降级/成员挂起，不硬删任何业务数据。
--   - 回滚以迁移执行前的 pg_dump 全库备份为基线（见 down.sql）。
--
-- 步骤（顺序执行）：
--   1. 写审计快照（audit_logs，tenant_id=0，actor=system）
--   2. 多 owner：每空间只留 1 条未删除 owner，其余降为 admin
--   3. contributor 成员 → admin
--   4. pending contributor 邀请 → revoked；其余 contributor/owner 邀请 → admin
--   5. 非超管多空间账号：保留 1 条 active/invited 成员，其余 suspended
--   6. 修正 users.tenant_id 指向保留空间
--   7. 建唯一 owner 部分索引

DO $$ BEGIN RAISE NOTICE '[Migration 000137] 开始 single-space-per-account 数据收敛'; END $$;

-- 1a. 审计：多 owner 降级（active 优先、joined_at 最早、id 最小者保留）。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'tenant_member',
       r.id::text, r.user_id, 'success',
       jsonb_build_object(
           'change', 'owner_demoted',
           'tenant_id', r.tenant_id,
           'before_role', 'owner',
           'after_role', 'admin'
       ),
       'platform', 'migration:single-space-per-account'
FROM (
    SELECT id, user_id, tenant_id,
           ROW_NUMBER() OVER (
               PARTITION BY tenant_id
               ORDER BY CASE WHEN status = 'active' THEN 1 ELSE 0 END DESC,
                        joined_at ASC,
                        id ASC
           ) AS rn
    FROM tenant_members
    WHERE role = 'owner' AND deleted_at IS NULL
) r
WHERE r.rn > 1;

-- 1b. 审计：contributor 成员升为 admin。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'tenant_member',
       tm.id::text, tm.user_id, 'success',
       jsonb_build_object(
           'change', 'contributor_to_admin',
           'tenant_id', tm.tenant_id,
           'before_role', 'contributor',
           'after_role', 'admin'
       ),
       'platform', 'migration:single-space-per-account'
FROM tenant_members tm
WHERE tm.role = 'contributor' AND tm.deleted_at IS NULL;

-- 1c. 审计：pending contributor 邀请撤销。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'tenant_invitation',
       ti.id::text, ti.invitee_user_id, 'success',
       jsonb_build_object(
           'change', 'invitation_revoked',
           'tenant_id', ti.tenant_id,
           'before_status', 'pending',
           'after_status', 'revoked'
       ),
       'platform', 'migration:single-space-per-account'
FROM tenant_invitations ti
WHERE ti.role = 'contributor' AND ti.status = 'pending' AND ti.deleted_at IS NULL;

-- 1d. 审计：历史 contributor/owner 邀请角色统一为 admin。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'tenant_invitation',
       ti.id::text, ti.invitee_user_id, 'success',
       jsonb_build_object(
           'change', 'invitation_role_normalized',
           'tenant_id', ti.tenant_id,
           'before_role', ti.role,
           'after_role', 'admin'
       ),
       'platform', 'migration:single-space-per-account'
FROM tenant_invitations ti
WHERE ti.role IN ('contributor', 'owner') AND ti.deleted_at IS NULL;

-- 1e. 审计：多空间账号的成员关系将被挂起。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'tenant_member',
       r.id::text, r.user_id, 'success',
       jsonb_build_object(
           'change', 'multi_space_suspended',
           'tenant_id', r.tenant_id,
           'before_status', r.status,
           'after_status', 'suspended'
       ),
       'platform', 'migration:single-space-per-account'
FROM (
    SELECT tm.id, tm.user_id, tm.tenant_id, tm.status,
           ROW_NUMBER() OVER (
               PARTITION BY tm.user_id
               ORDER BY CASE WHEN u.tenant_id IS NOT NULL
                              AND tm.tenant_id = u.tenant_id THEN 1 ELSE 0 END DESC,
                        CASE WHEN tm.status = 'active' THEN 1 ELSE 0 END DESC,
                        tm.joined_at ASC,
                        tm.id ASC
           ) AS rn
    FROM tenant_members tm
    JOIN users u ON u.id = tm.user_id
    WHERE tm.status IN ('active', 'invited')
      AND tm.deleted_at IS NULL
      AND NOT (COALESCE(u.is_system_admin, FALSE)
               OR COALESCE(u.can_access_all_tenants, FALSE))
      AND (SELECT COUNT(*)
             FROM tenant_members tm2
            WHERE tm2.user_id = tm.user_id
              AND tm2.status IN ('active', 'invited')
              AND tm2.deleted_at IS NULL) > 1
) r
WHERE r.rn > 1;

-- 2. 多 owner 降级：与 1a 同一排序规则，保留 rn=1，其余改 admin。
UPDATE tenant_members
SET role = 'admin', updated_at = CURRENT_TIMESTAMP
WHERE id IN (
    SELECT id FROM (
        SELECT id,
               ROW_NUMBER() OVER (
                   PARTITION BY tenant_id
                   ORDER BY CASE WHEN status = 'active' THEN 1 ELSE 0 END DESC,
                            joined_at ASC,
                            id ASC
               ) AS rn
        FROM tenant_members
        WHERE role = 'owner' AND deleted_at IS NULL
    ) ranked
    WHERE ranked.rn > 1
);

-- 3. contributor 成员 → admin。
UPDATE tenant_members
SET role = 'admin', updated_at = CURRENT_TIMESTAMP
WHERE role = 'contributor' AND deleted_at IS NULL;

-- 4a. pending contributor 邀请置为 revoked。
UPDATE tenant_invitations
SET status = 'revoked', responded_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE role = 'contributor' AND status = 'pending' AND deleted_at IS NULL;

-- 4b. 邀请表不再保留 contributor/owner 角色（含刚被撤销的 pending 邀请）。
UPDATE tenant_invitations
SET role = 'admin', updated_at = CURRENT_TIMESTAMP
WHERE role IN ('contributor', 'owner') AND deleted_at IS NULL;

-- 5. 非超管多空间账号：同一排序规则保留 rn=1，其余挂起（不硬删）。
UPDATE tenant_members
SET status = 'suspended', updated_at = CURRENT_TIMESTAMP
WHERE id IN (
    SELECT id FROM (
        SELECT tm.id,
               ROW_NUMBER() OVER (
                   PARTITION BY tm.user_id
                   ORDER BY CASE WHEN u.tenant_id IS NOT NULL
                                  AND tm.tenant_id = u.tenant_id THEN 1 ELSE 0 END DESC,
                            CASE WHEN tm.status = 'active' THEN 1 ELSE 0 END DESC,
                            tm.joined_at ASC,
                            tm.id ASC
               ) AS rn
        FROM tenant_members tm
        JOIN users u ON u.id = tm.user_id
        WHERE tm.status IN ('active', 'invited')
          AND tm.deleted_at IS NULL
          AND NOT (COALESCE(u.is_system_admin, FALSE)
                   OR COALESCE(u.can_access_all_tenants, FALSE))
          AND (SELECT COUNT(*)
                 FROM tenant_members tm2
                WHERE tm2.user_id = tm.user_id
                  AND tm2.status IN ('active', 'invited')
                  AND tm2.deleted_at IS NULL) > 1
    ) ranked
    WHERE ranked.rn > 1
);

-- 5b. 撤销跨空间 pending 邀请：invitee 已在另一个空间 active。
-- 只按 active 判定；invited-only 由接受时的服务层守卫负责（T04）。
-- 先写审计，再更新邀请状态。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'tenant_invitation',
       ti.id::text, ti.invitee_user_id, 'success',
       jsonb_build_object(
           'change', 'invitation_revoked',
           'reason', 'cross_space',
           'tenant_id', ti.tenant_id,
           'before_status', 'pending',
           'after_status', 'revoked'
       ),
       'platform', 'migration:single-space-per-account'
FROM tenant_invitations ti
JOIN users u ON u.id = ti.invitee_user_id
WHERE ti.status = 'pending' AND ti.deleted_at IS NULL
  AND ti.invitee_user_id <> ''
  AND NOT (COALESCE(u.is_system_admin, FALSE)
           OR COALESCE(u.can_access_all_tenants, FALSE))
  AND EXISTS (
      SELECT 1
      FROM tenant_members tm
      WHERE tm.user_id = ti.invitee_user_id
        AND tm.status = 'active'
        AND tm.deleted_at IS NULL
        AND tm.tenant_id <> ti.tenant_id
  );

UPDATE tenant_invitations
SET status = 'revoked', responded_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id IN (
    SELECT ti.id
    FROM tenant_invitations ti
    JOIN users u ON u.id = ti.invitee_user_id
    WHERE ti.status = 'pending' AND ti.deleted_at IS NULL
      AND ti.invitee_user_id <> ''
      AND NOT (COALESCE(u.is_system_admin, FALSE)
               OR COALESCE(u.can_access_all_tenants, FALSE))
      AND EXISTS (
          SELECT 1
          FROM tenant_members tm
          WHERE tm.user_id = ti.invitee_user_id
            AND tm.status = 'active'
            AND tm.deleted_at IS NULL
            AND tm.tenant_id <> ti.tenant_id
      )
);

-- 5c. 审计：users.tenant_id 修正逐账号快照（AC-25 / N16）。
-- 与第 6 步使用同一保留行排序与同一修正条件；记录旧/新 home。
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_role, action, target_type, target_id,
    target_user_id, outcome, details, scope_type, scope_id
)
SELECT 0, 'system', 'system_admin', 'system.membership_migrated', 'user',
       u.id, u.id, 'success',
       jsonb_build_object(
           'change', 'home_tenant_repaired',
           'before_tenant_id', COALESCE(u.tenant_id, 0),
           'after_tenant_id', kept.tenant_id
       ),
       'platform', 'migration:single-space-per-account'
FROM users u
JOIN (
    SELECT tm.user_id, tm.tenant_id,
           ROW_NUMBER() OVER (
               PARTITION BY tm.user_id
               ORDER BY CASE WHEN u.tenant_id IS NOT NULL
                              AND tm.tenant_id = u.tenant_id THEN 1 ELSE 0 END DESC,
                        CASE WHEN tm.status = 'active' THEN 1 ELSE 0 END DESC,
                        tm.joined_at ASC,
                        tm.id ASC
           ) AS rn
    FROM tenant_members tm
    JOIN users u ON u.id = tm.user_id
    WHERE tm.status IN ('active', 'invited')
      AND tm.deleted_at IS NULL
      AND NOT (COALESCE(u.is_system_admin, FALSE)
               OR COALESCE(u.can_access_all_tenants, FALSE))
) kept ON kept.user_id = u.id
WHERE kept.rn = 1
  AND (u.tenant_id IS NULL
       OR u.tenant_id = 0
       OR u.tenant_id <> kept.tenant_id);

-- 6. users.tenant_id 指向保留下来的空间。
-- 第 5 步挂起后，多空间账号只剩 1 条 active/invited 成员，这里按它修正 home；
-- 单空间账号若 home 与唯一成员不一致也一并修正（AC-19）。
UPDATE users
SET tenant_id = kept.tenant_id,
    updated_at = CURRENT_TIMESTAMP
FROM (
    SELECT tm.user_id, tm.tenant_id,
           ROW_NUMBER() OVER (
               PARTITION BY tm.user_id
               ORDER BY CASE WHEN u.tenant_id IS NOT NULL
                              AND tm.tenant_id = u.tenant_id THEN 1 ELSE 0 END DESC,
                        CASE WHEN tm.status = 'active' THEN 1 ELSE 0 END DESC,
                        tm.joined_at ASC,
                        tm.id ASC
           ) AS rn
    FROM tenant_members tm
    JOIN users u ON u.id = tm.user_id
    WHERE tm.status IN ('active', 'invited')
      AND tm.deleted_at IS NULL
      AND NOT (COALESCE(u.is_system_admin, FALSE)
               OR COALESCE(u.can_access_all_tenants, FALSE))
) kept
WHERE users.id = kept.user_id
  AND kept.rn = 1
  AND (users.tenant_id IS NULL
       OR users.tenant_id = 0
       OR users.tenant_id <> kept.tenant_id);

-- 7. 每空间最多 1 条未删除 owner。
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_members_tenant_owner_unique
    ON tenant_members(tenant_id)
    WHERE role = 'owner' AND deleted_at IS NULL;

DO $$ BEGIN RAISE NOTICE '[Migration 000137] single-space-per-account 数据收敛完成'; END $$;
