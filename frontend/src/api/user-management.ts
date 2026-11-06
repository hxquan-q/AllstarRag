import { get, post } from '@/utils/request'
import i18n from '@/i18n'
import type { TenantMember, TenantRole } from '@/api/tenant/members'
import type { SystemAdminUser } from '@/api/system'

/**
 * 用户管理页的前端 API 客户端。
 *
 * 权限判断只在后端：这些函数不自行判断角色/能力，调用方用
 * capabilities store 的 can() 做界面门控，真正的放行由后端逐接口校验（C4）。
 * 后端错误统一抛出 { status, message } 形状（见 utils/request.ts），
 * 展示前请用 resolveUserManagementError 转成可读文案。
 */

export type UserManagementRole = TenantRole | 'system_admin'

/** 空间筛选口径：current=本空间；other=其他空间有关系的用户；all=全平台（T16）。 */
export type UserManagementListScope = 'current' | 'other' | 'all'

/** 多空间视图里一条成员关系：用户在哪个空间、以什么角色存在。 */
export interface UserManagementMembership {
  tenant_id: number | string
  tenant_name: string
  role: TenantRole
}

/** 列表行的状态：active=正常；banned=已封禁；deleted=账号已注销（墓碑）。 */
export type UserManagementStatus = 'active' | 'banned' | 'deleted'

export interface UserManagementItem {
  user_id: string
  email: string
  username: string
  role: TenantRole
  status: UserManagementStatus | string
  is_active: boolean
  created_at: string
  /** 仅 scope=other/all 返回；current 视图为空。 */
  memberships?: UserManagementMembership[]
}

/** 左侧角色目录的整空间计数，始终包含四个键（无成员为 0）。 */
export interface WorkspaceRoleCounts {
  viewer: number
  contributor: number
  admin: number
  owner: number
}

export interface UserManagementListResponse {
  success: boolean
  data?: {
    items: UserManagementItem[]
    total: number
    role_counts: WorkspaceRoleCounts
  }
  message?: string
}

export interface UserManagementListParams {
  /** 空间筛选；缺省即 current。other/all 仅系统管理员可调（后端 403 把关）。 */
  scope?: UserManagementListScope
  /** 角色筛选；缺省表示全部角色。支持逗号分隔多值（如 contributor,admin）。 */
  role?: TenantRole | string
  /** 按邮箱/用户名模糊筛选。 */
  q?: string
  page?: number
  page_size?: number
}

function buildListQuery(params: UserManagementListParams): string {
  const u = new URLSearchParams()
  if (params.scope) u.set('scope', params.scope)
  if (params.role) u.set('role', params.role)
  const q = params.q?.trim()
  if (q) u.set('q', q)
  if (params.page != null && params.page > 0) u.set('page', String(params.page))
  if (params.page_size != null && params.page_size > 0) u.set('page_size', String(params.page_size))
  const qs = u.toString()
  return qs ? `?${qs}` : ''
}

export interface BanMemberRequest {
  reason?: string
}

export interface MemberStatusChangeResponse {
  success: boolean
  data?: {
    user: {
      id: string
      email: string
      username: string
      is_active: boolean
      status?: string
    }
    member: TenantMember
  }
  message?: string
}

export interface PurgeMemberRequest {
  /** 必须与目标账号当前邮箱一致，否则后端返回 400。 */
  confirm_email: string
  reason?: string
}

export interface PurgeMemberResponse {
  success: boolean
  data?: { user_id: string }
  message?: string
}

export interface TransferOwnerRequest {
  /** 省略表示把调用者自己持有的 owner 身份交出去。 */
  from_user_id?: string
  to_user_id: string
}

export interface SimpleResponse {
  success: boolean
  message?: string
}

export interface CreateCoOwnerRequest {
  username: string
  email: string
  /** 必填；后端对空密码返回 400（不会生成随机密码）。 */
  password: string
}

export interface CreateCoOwnerResponse {
  success: boolean
  data?: {
    user: {
      id: string
      email: string
      username: string
    }
  }
  message?: string
}

export interface ProvisionOwnerSpaceRequest {
  /** 省略时后端按用户名生成空间名。 */
  space_name?: string
}

export interface ProvisionOwnerSpaceResponse {
  success: boolean
  data?: {
    tenant: {
      id: number | string
      name: string
      status?: string
    }
  }
  message?: string
}

export interface CreateOwnerInviteRequest {
  email?: string
  space_name?: string
  /** 邀请链接有效期（天）；省略时后端默认 7 天。 */
  expires_in_days?: number
}

export interface CreateOwnerInviteResponse {
  success: boolean
  data?: {
    invite_url: string
    tenant_id: number | string
    expires_at: string
  }
  message?: string
}

export interface SystemAdminListResponse {
  total: number
  admins: SystemAdminUser[]
}

/**
 * 分页列出本空间可管理的成员（含已封禁账号，墓碑用户不出现）。
 * Backend: GET /api/v1/tenants/:id/user-management（Owner+ / 系统管理员）。
 */
export async function listUsers(
  tenantId: number | string,
  params: UserManagementListParams = {},
): Promise<UserManagementListResponse> {
  return (await get(
    `/api/v1/tenants/${tenantId}/user-management${buildListQuery(params)}`,
  )) as unknown as UserManagementListResponse
}

/**
 * 封禁账号：禁止登录并吊销全部 token，保留成员关系，可解封。
 * Backend: POST /api/v1/tenants/:id/members/:user_id/ban。
 */
export async function banMember(
  tenantId: number | string,
  userId: string,
  reason?: string,
): Promise<MemberStatusChangeResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/members/${userId}/ban`,
    { reason: reason ?? '' } satisfies BanMemberRequest,
  )) as unknown as MemberStatusChangeResponse
}

/**
 * 解封账号：恢复登录能力，历史 token 不恢复（需重新登录）。
 * Backend: POST /api/v1/tenants/:id/members/:user_id/unban。
 */
export async function unbanMember(
  tenantId: number | string,
  userId: string,
  reason?: string,
): Promise<MemberStatusChangeResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/members/${userId}/unban`,
    { reason: reason ?? '' } satisfies BanMemberRequest,
  )) as unknown as MemberStatusChangeResponse
}

/**
 * 彻底删除（墓碑化）账号：清空个人数据并保留其资产，不可恢复。
 * 必须先输入目标账号邮箱确认。
 * Backend: POST /api/v1/tenants/:id/members/:user_id/purge。
 */
export async function purgeMember(
  tenantId: number | string,
  userId: string,
  body: PurgeMemberRequest,
): Promise<PurgeMemberResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/members/${userId}/purge`,
    body,
  )) as unknown as PurgeMemberResponse
}

/**
 * 空间交接：把 owner 身份交给本空间成员；普通管理员只能交出自己持有的
 * owner 身份，系统管理员可指定 from_user_id。
 * Backend: POST /api/v1/tenants/:id/transfer-owner。
 */
export async function transferOwner(
  tenantId: number | string,
  body: TransferOwnerRequest,
): Promise<SimpleResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/transfer-owner`,
    body,
  )) as unknown as SimpleResponse
}

/**
 * 新建 co-owner 账号并直接加入本空间（role=owner）。
 * Backend: POST /api/v1/tenants/:id/owners/create。
 */
export async function createCoOwner(
  tenantId: number | string,
  body: CreateCoOwnerRequest,
): Promise<CreateCoOwnerResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/owners/create`,
    body,
  )) as unknown as CreateCoOwnerResponse
}

/**
 * 系统管理员把已有账号设为普通管理员：无独立空间则新建，有则复用（幂等）。
 * Backend: POST /api/v1/system/admin/users/:user_id/owner-space。
 */
export async function provisionOwnerSpace(
  userId: string,
  body: ProvisionOwnerSpaceRequest = {},
): Promise<ProvisionOwnerSpaceResponse> {
  return (await post(
    `/api/v1/system/admin/users/${userId}/owner-space`,
    body,
  )) as unknown as ProvisionOwnerSpaceResponse
}

/**
 * 系统管理员生成普通管理员邀请：创建 pending 空间 + owner 角色邀请链接，
 * 接受后才建立成员关系。
 * Backend: POST /api/v1/system/admin/owners/invite。
 */
export async function createOwnerInvite(
  body: CreateOwnerInviteRequest = {},
): Promise<CreateOwnerInviteResponse> {
  return (await post(
    '/api/v1/system/admin/owners/invite',
    body,
  )) as unknown as CreateOwnerInviteResponse
}

/**
 * 列出平台系统管理员（供超管视图的「超管」目录使用）。
 * Backend: GET /api/v1/system/admin/list（SystemAdmin only）。
 * 响应不使用 { success, data } 包裹，直接返回 { total, admins }。
 */
export async function listSystemAdmins(
  params: { offset?: number; limit?: number } = {},
): Promise<SystemAdminListResponse> {
  const u = new URLSearchParams()
  if (params.offset != null && params.offset >= 0) u.set('offset', String(params.offset))
  if (params.limit != null && params.limit > 0) u.set('limit', String(params.limit))
  const qs = u.toString()
  return (await get(
    `/api/v1/system/admin/list${qs ? `?${qs}` : ''}`,
  )) as unknown as SystemAdminListResponse
}

const STATUS_ERROR_KEYS: Record<number, string> = {
  400: 'userManagement.errors.invalidRequest',
  401: 'userManagement.errors.noPermission',
  403: 'userManagement.errors.noPermission',
  404: 'userManagement.errors.notFound',
  409: 'userManagement.errors.conflict',
  422: 'userManagement.errors.invalidRequest',
}

/**
 * 把后端错误转成可读文案。只按 HTTP 状态映射，不把错误码/原始英文
 * 直接展示给用户；未知情况回退到通用失败提示。
 */
export function resolveUserManagementError(error: unknown): string {
  const status = (error as { status?: number } | null)?.status
  const key = status != null ? STATUS_ERROR_KEYS[status] : undefined
  return i18n.global.t(key ?? 'userManagement.errors.unknown')
}
