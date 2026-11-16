import type { TenantInvitation } from '@/api/tenant/invitations'

/**
 * 邀请管理 Tab 的纯展示工具（无 i18n、无组件状态），
 * 从 InviteManagement.vue 拆出来保持该文件在 400 行以内。
 */

/** 后端未配置 FRONTEND_BASE_URL 时返回相对路径，复制前拼成绝对地址。 */
export function absoluteInviteURL(raw: string): string {
  if (!raw) return ''
  if (/^https?:\/\//i.test(raw)) return raw
  const origin = window.location.origin
  return raw.startsWith('/') ? origin + raw : `${origin}/${raw}`
}

/** 后端不回传裸 token；从 invite_url 里解析出来截断展示。 */
export function shortToken(row: TenantInvitation): string {
  const url = row.invite_url || ''
  if (!url) return '—'
  try {
    const token = new URL(url, window.location.origin).searchParams.get('token') || ''
    return token ? `${token.slice(0, 8)}…` : '—'
  } catch {
    return '—'
  }
}

/** 被邀请人显示名：用户名 > 邮箱 > 裸 ID。 */
export function inviteeName(row: TenantInvitation): string {
  return row.invitee_name || row.invitee_email || row.invitee_user_id || '—'
}

/** 邀请时间列的本地化格式；解析失败原样返回。 */
export function formatDateTime(value: string, locale: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale || 'zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}
