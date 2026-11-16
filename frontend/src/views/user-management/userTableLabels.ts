import type { UserManagementStatus } from '@/api/user-management'

/**
 * 用户管理表格的角色/状态文案与时间格式（纯函数，不依赖 useI18n）。
 * T08：界面只显示 空间拥有者/普通管理员/游客 三档；后端已停止分配
 * contributor，残余行按普通管理员归并显示。
 * 由 UserTable 注入 t / locale，拆分后的单元格组件通过 props 复用。
 */
type T = (key: string) => string

export function roleLabel(role: string, t: T): string {
  if (!role) return '—'
  const labels: Record<string, string> = {
    system_admin: t('userManagement.roles.systemAdmin'),
    owner: t('userManagement.roles.owner'),
    admin: t('userManagement.roles.admin'),
    contributor: t('userManagement.roles.admin'),
    viewer: t('userManagement.roles.viewer'),
  }
  return labels[role] ?? role
}

export function statusLabel(status: UserManagementStatus, t: T): string {
  const labels: Record<UserManagementStatus, string> = {
    active: t('userManagement.status.active'),
    banned: t('userManagement.status.banned'),
    deleted: t('userManagement.status.deleted'),
  }
  return labels[status]
}

export function statusTheme(status: UserManagementStatus): 'success' | 'danger' | 'warning' {
  if (status === 'active') return 'success'
  if (status === 'banned') return 'danger'
  return 'warning'
}

export function formatDateTime(value: string, locale: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale || 'zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(date)
}
