import type { SystemAdminUser } from '@/api/system'
import type { TenantRole } from '@/api/tenant/members'
import type {
  UserManagementItem,
  UserManagementMembership,
  UserManagementRole,
  UserManagementStatus,
} from '@/api/user-management'

/**
 * 左目录的类别（Q37 简化后）：普通管理员(owner) / 编辑(editor) / 游客(viewer)，
 * 超管视图另加平台系统管理员。
 * editor 是前端展示口径：聚合后端的 contributor 与 admin 两类账号，
 * 后端 admin 角色保留不删，已有 admin 账号归入「编辑」栏且权限不变。
 */
export type DirectoryRole = 'owner' | 'editor' | 'viewer' | 'system_admin'

/** 编辑类包含的后端角色；目录计数与列表查询都按这两个角色聚合。 */
export const EDITOR_BACKEND_ROLES: TenantRole[] = ['contributor', 'admin']

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

/** 左目录一项：类别、当前语言下的文案、该类别成员数。 */
export interface DirectoryEntry {
  key: DirectoryRole
  label: string
  count: number
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

/** 新增/邀请普通管理员的入口类型。 */
export type AddOwnerMode = 'createCoOwner' | 'inviteCoOwner' | 'provision' | 'inviteOwner'

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
 * 角色下拉可选项（Q37 简化后）：只提供 游客(viewer) / 编辑(contributor)，
 * 超管操作者额外可把成员提升为 普通管理员(owner)（co-owner）。
 * 后端 admin 角色不再出现在 UI 选项里；「编辑」统一落到 contributor。
 */
export function roleChangeOptions(isSystemAdmin: boolean): TenantRole[] {
  return isSystemAdmin ? ['viewer', 'contributor', 'owner'] : ['viewer', 'contributor']
}
