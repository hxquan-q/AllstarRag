import type { TenantRole } from '@/api/tenant/members'

/**
 * 当前操作者能创建哪些角色的成员账号。
 *
 * 口径与后端 service.CanCreateRole 保持一致（T05 白名单）：
 * - Owner / Admin（含跨租户系统管理员）：admin / viewer
 * - owner / contributor 停止分配，选项里不再出现（枚举保留兼容存量数据）
 * - 其他角色：没有建号权限
 *
 * 这里只用于界面展示与选项过滤，真正能不能建以后端为准。
 */
export function allowedCreateRoles(
  actorRole: TenantRole | '' | null | undefined,
): TenantRole[] {
  switch (actorRole) {
    case 'owner':
    case 'admin':
      return ['admin', 'viewer']
    default:
      return []
  }
}

/** 是否显示建号入口：有至少一个可创建角色即视为有权限。 */
export function canCreateAccount(
  actorRole: TenantRole | '' | null | undefined,
): boolean {
  return allowedCreateRoles(actorRole).length > 0
}

/**
 * 能否重置目标成员的密码。
 *
 * 口径与后端 service.CanResetRole 保持一致（T05 后的角色矩阵，01-spec AC-2）：
 * - Owner（含跨租户系统管理员）：admin / viewer
 * - Admin：viewer（同级保护，不能重置另一个普通管理员）
 * - 其他角色：不能重置任何人
 * - 本人一律不能重置（走「修改密码」接口，需要旧密码）
 *
 * contributor 不再分配（存量迁移为 admin），后端 CanResetRole 对其一律
 * 拒绝，这里不再保留展示分支。同级或更高级的角色同样被排除。
 * 这里只决定 UI 是否展示入口，真正能不能重置以后端判定为准。
 */
export function canResetTarget(
  actorRole: TenantRole | '' | null | undefined,
  targetRole: TenantRole | '' | null | undefined,
  isSelf: boolean,
): boolean {
  if (isSelf) return false
  switch (actorRole) {
    case 'owner':
      return targetRole === 'admin' || targetRole === 'viewer'
    case 'admin':
      return targetRole === 'viewer'
    default:
      return false
  }
}
