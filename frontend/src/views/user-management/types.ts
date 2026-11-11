import type { SystemAdminUser } from '@/api/system'
import type { TenantRole } from '@/api/tenant/members'
import type {
  UserManagementItem,
  UserManagementListScope,
  UserManagementMembership,
  UserManagementRole,
  UserManagementStatus,
} from '@/api/user-management'

/**
 * 左目录的类别（T08 收敛后）：空间拥有者(owner) / 普通管理员(admin) / 游客(viewer)，
 * 超管视图另加平台系统管理员；scope=all 再加「无空间」。
 * admin 与后端角色一一对应（单角色直查）。后端已停止分配 contributor，
 * 残余 contributor 行在界面按普通管理员归并显示，不再有独立「编辑」目录。
 * no_space 是超管全平台视图（scope=all）专属的「无空间」目录（T44）：
 * 无任何 active 成员关系的普通账号。
 */
export type DirectoryRole = 'owner' | 'admin' | 'viewer' | 'system_admin' | 'no_space'

/** 右表一行；只保留展示需要的字段（AC-32）。role 为空串表示该账号
 * 在当前视图里没有任何成员关系（scope=all 时可能出现）。 */
export interface UserRow {
  user_id: string
  email: string
  username: string
  role: UserManagementRole | ''
  status: UserManagementStatus
  created_at: string
  /** 多空间视图（scope=other/all）下的成员关系，用于「所属空间」列。 */
  memberships?: UserManagementMembership[]
}

/**
 * 左目录一项：类别、当前语言下的文案、该类别成员数。
 * count 为 null 表示该视图下计数不可信/不可用，不渲染徽标
 * （如后端未返回该桶计数的场景）。
 */
export interface DirectoryEntry {
  key: DirectoryRole
  label: string
  count: number | null
}

function normalizeStatus(status: string | undefined): UserManagementStatus {
  if (status === 'banned' || status === 'deleted') return status
  return 'active'
}

/** 后端空间成员行 → 表格行。 */
export function toUserRow(item: UserManagementItem): UserRow {
  const row: UserRow = {
    user_id: item.user_id,
    email: item.email,
    username: item.username,
    role: item.role,
    status: normalizeStatus(item.status),
    created_at: item.created_at,
  }
  if (item.memberships?.length) row.memberships = item.memberships
  return row
}

/**
 * 平台超管行 → 表格行。
 * 超管列表接口没有空间角色，角色固定为 system_admin；账号被停用时
 * 显示“已封禁”，其余显示“正常”。
 */
export function systemAdminToUserRow(admin: SystemAdminUser): UserRow {
  return {
    user_id: admin.id,
    email: admin.email,
    username: admin.username,
    role: 'system_admin',
    status: admin.is_active === false ? 'banned' : 'active',
    created_at: admin.created_at,
  }
}

/** 行内动作类型；与 UserActionDialogs 的 mode 对应（角色修改在表格内完成）。 */
export type UserActionKind = 'changeRole' | 'ban' | 'unban' | 'purge' | 'transfer'

/** 操作确认弹窗的类型。 */
export type UserActionMode = 'ban' | 'unban' | 'purge' | 'transfer'

/** 操作权限开关；来自 capabilities 投影，只做界面门控（C4）。 */
export interface UserActionPermissions {
  changeRole: boolean
  ban: boolean
  unban: boolean
  purge: boolean
  transfer: boolean
}

export interface ActionGate {
  allowed: boolean
  /** 不可用原因，指向 userManagement.tooltips.* 的文案键。 */
  reasonKey?: string
}

const TOOLTIP_NO_PERMISSION = 'userManagement.tooltips.noPermission'
const TOOLTIP_SELF = 'userManagement.tooltips.self'
const TOOLTIP_OWNER_PROTECTED = 'userManagement.tooltips.ownerProtected'
const TOOLTIP_SYSTEM_ADMIN = 'userManagement.tooltips.systemAdminProtected'
const TOOLTIP_TRANSFER_REQUIRED = 'userManagement.tooltips.transferRequired'
const TOOLTIP_TRANSFER_OWNER_ONLY = 'userManagement.tooltips.transferOwnerOnly'
const TOOLTIP_PEER_PROTECTED = 'userManagement.tooltips.peerProtected'

/**
 * 单行动作的前端门控：先看能力，再看目标形态与操作者身份。
 *
 * 注意这只是体验层：后端仍会逐接口校验（C4）。owner 行一律不提供
 * 角色下拉或注销：后端对 owner → 非 owner 一律 403，必须先走
 * transfer-owner 交接，前端不再按 ownerCount 提前放行。
 */
export function gateUserAction(
  kind: UserActionKind,
  row: UserRow,
  currentUserId: string,
  permissions: UserActionPermissions,
  isSystemAdmin: boolean,
  _ownerCount: number,
): ActionGate {
  if (kind === 'transfer') {
    if (!permissions.transfer) return { allowed: false, reasonKey: TOOLTIP_NO_PERMISSION }
    if (row.role !== 'owner') return { allowed: false, reasonKey: TOOLTIP_TRANSFER_OWNER_ONLY }
    if (!isSystemAdmin && row.user_id !== currentUserId) {
      return { allowed: false, reasonKey: TOOLTIP_OWNER_PROTECTED }
    }
    return { allowed: true }
  }

  if (!permissions[kind]) return { allowed: false, reasonKey: TOOLTIP_NO_PERMISSION }
  if (row.role === 'system_admin') return { allowed: false, reasonKey: TOOLTIP_SYSTEM_ADMIN }
  if (row.user_id === currentUserId) return { allowed: false, reasonKey: TOOLTIP_SELF }
  // CP2 同级保护（T08/AC-12b）：操作者按能力投影刻画 —— 拥有成员治理能力但
  // 没有 space.transfer，在 T02 后的能力矩阵下唯一对应普通管理员（owner 与
  // 系统管理员的矩阵都含 transfer），禁止写第二套角色判断。普通管理员不能
  // 对同级 admin 执行治理动作；迁移残留的 contributor 行已按普通管理员归并
  // 显示，同样受保护。后端 member_target_protection 仍逐接口兜底（C4）。
  const isRegularAdminOperator = !isSystemAdmin && !permissions.transfer
  if (isRegularAdminOperator && (row.role === 'admin' || row.role === 'contributor')) {
    return { allowed: false, reasonKey: TOOLTIP_PEER_PROTECTED }
  }
  if (row.role !== 'owner') return { allowed: true }
  // owner 行不提供角色下拉：后端对 owner → 非 owner 一律 403，
  // 只能先通过「交接」把所有权交给其他成员。
  if (kind === 'changeRole') {
    return { allowed: false, reasonKey: TOOLTIP_TRANSFER_REQUIRED }
  }
  if (!isSystemAdmin) return { allowed: false, reasonKey: TOOLTIP_OWNER_PROTECTED }
  // 超管处理 owner：注销前必须交接。
  if (kind === 'purge') return { allowed: false, reasonKey: TOOLTIP_TRANSFER_REQUIRED }
  return { allowed: true }
}

/**
 * 角色下拉可选项（T08/AC-11/13）：只分配 游客(viewer) / 普通管理员(admin)；
 * 空间拥有者(owner) 的提升仅超管操作者可选（交接/设 owner 路径）。
 * contributor 停止分配后不再出现在选项里。
 */
export function roleChangeOptions(isSystemAdmin: boolean): TenantRole[] {
  return isSystemAdmin ? ['viewer', 'admin', 'owner'] : ['viewer', 'admin']
}

/**
 * 多空间视图（scope=other/all）的封禁/解封/注销走平台接口（T35），
 * 不携带当前 tenantId；当前空间视图继续用空间内成员接口。
 */
export function usesPlatformAccountActions(scope: UserManagementListScope): boolean {
  return scope !== 'current'
}

/** 改角色/交接是空间内操作，只在当前空间视图可用（Q44）。 */
export function canManageMembershipInScope(scope: UserManagementListScope): boolean {
  return scope === 'current'
}

/** 「切换到该空间」的候选目标。 */
export interface TenantSwitchTarget {
  tenantId: number
  tenantName: string | null
  label: string
}

/**
 * 从行内成员关系解析可切换的空间：
 * - 排除当前空间（切过去没有意义）；
 * - 同一空间只保留一条；
 * - 非法 id 直接丢弃。
 */
export function resolveTenantSwitchTargets(
  memberships: UserManagementMembership[] | undefined,
  currentTenantId?: number | null,
): TenantSwitchTarget[] {
  const targets: TenantSwitchTarget[] = []
  const seen = new Set<number>()
  const currentId = currentTenantId == null ? null : Number(currentTenantId)
  for (const membership of memberships ?? []) {
    const tenantId = Number(membership.tenant_id)
    if (!Number.isFinite(tenantId) || tenantId <= 0) continue
    if (currentId !== null && tenantId === currentId) continue
    if (seen.has(tenantId)) continue
    seen.add(tenantId)
    const tenantName = membership.tenant_name || null
    targets.push({ tenantId, tenantName, label: tenantName || `#${tenantId}` })
  }
  return targets
}
