/**
 * T46 红阶段（二）：KeepAlive 双 Tab 切回刷新。
 *
 * 现状缺陷：InviteManagement / OperationLog 在 UserManagement 里包在
 * `<KeepAlive>` 内用 v-if 切换，切走只缓存不销毁；切回不触发 onMounted，
 * 组件又没有 onActivated → 邀请管理看不到新发的邀请、操作记录停在旧数据，
 * 只能刷新页面绕过。
 *
 * 目标行为（03-tickets/T46.md 契约）：
 * - 增加 onActivated(() => void load())，并用标记跳过与 onMounted 重叠的
 *   首次激活，避免双请求；
 * - tenantId watch、防抖/竞态序号语义不变（T36/T43 保护网另跑既有用例）。
 *
 * 基建：t46SfcMount 的 KeepAlive 宿主挂载（与 UserManagement 同构的
 * 切走/切回），两组件分别断言。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mountInKeepAlive, settle } from './t46SfcMount.ts'

const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)
const INVITES = new URL('./InviteManagement.vue', import.meta.url)

const i18n = {
  useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: { value: 'zh-CN' } }),
}

const message = () => ({
  MessagePlugin: { success() {}, error() {}, warning() {}, info() {} },
})

const memberRow = (id: number) => ({
  id,
  action: 'rbac.member_banned',
  created_at: '2026-11-10T00:00:00Z',
})

/** KeepAlive 宿主挂载 OperationLog；pages 按请求序返回审计响应。 */
function mountLogInTabs(t: TestContext, pages: Array<{ data: any[]; next_cursor: number }>) {
  const calls: any[][] = []
  const mounted = mountInKeepAlive(t, {
    component: OPERATIONS,
    dependencies: {
      'vue-i18n': i18n,
      '@/api/tenant/audit-log': {
        listAuditLog: async (...args: any[]) => {
          calls.push(args)
          const page = pages[Math.min(calls.length - 1, pages.length - 1)]!
          return { success: true, data: page.data, next_cursor: page.next_cursor }
        },
      },
      '@/api/user-management': {
        listUsers: async () => ({ data: { items: [] } }),
        resolveUserManagementError: (error: unknown) => String(error),
      },
      '@/i18n/auditActionLabel': { auditActionLabel: (_t: unknown, action: string) => action },
      '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' } },
    },
    props: { tenantId: 7 },
  })
  return { ...mounted, calls }
}

/** KeepAlive 宿主挂载 InviteManagement；rounds 按轮次（每轮 pending+all 两请求）返回邀请列表。 */
function mountInvitesInTabs(t: TestContext, rounds: any[][]) {
  const calls = { list: [] as any[][] }
  const mounted = mountInKeepAlive(t, {
    component: INVITES,
    dependencies: {
      'vue-i18n': i18n,
      'tdesign-vue-next': message(),
      '@/api/tenant/invitations': {
        listTenantInvitations: async (...args: any[]) => {
          calls.list.push(args)
          // 每轮 load 发 pending + all 两个请求；按已发请求数折算轮次。
          const round = Math.min(Math.floor((calls.list.length - 1) / 2), rounds.length - 1)
          const rows = rounds[round]!
          return { success: true, data: { invitations: rows, total: rows.length } }
        },
        createInviteLink: async () => ({ success: true }),
        revokeInvitation: async () => ({ success: true }),
      },
      '@/api/user-management': { resolveUserManagementError: (error: unknown) => String(error) },
      '@/utils/clipboard': { copyWithToast: async () => {} },
      './inviteHelpers': {
        absoluteInviteURL: (u: string) => u,
        formatDateTime: (v: string) => v,
        inviteeName: () => '',
        shortToken: (v: string) => v,
      },
    },
    props: { tenantId: 7, canInviteOwner: false },
  })
  return { ...mounted, calls }
}

test('T46-A1 OperationLog 切走再切回应重新拉取最新数据（onActivated）', async (t) => {
  const { vm, away, back, calls } = mountLogInTabs(t, [
    { data: [memberRow(1)], next_cursor: 0 },
    { data: [memberRow(1), memberRow(9)], next_cursor: 0 },
  ])
  await settle()

  // 首次进入（mounted 与首次 activated 重叠）：只发一次请求。
  assert.equal(calls.length, 1, '首次进入只发一次请求，不得双请求')
  assert.deepEqual(
    (vm().entries as any[]).map((row) => row.id),
    [1],
    '首屏数据写回',
  )

  // 切走再切回：实例必须还是同一个（KeepAlive 状态保留，F24 不回归）。
  const before = vm()
  await away()
  await back()
  assert.equal(vm(), before, '切回仍是同一实例（KeepAlive 缓存未重建）')

  // 切回应重新发起请求拉取最新数据。
  assert.equal(calls.length, 2, '切回 Tab 应重新发起请求')

  // 新数据写回（重新拉取的语义：最新记录出现）。
  await settle()
  assert.ok(
    (vm().entries as any[]).some((row) => row.id === 9),
    '切回拉取的最新数据写回',
  )
})

test('T46-A3 InviteManagement 切走再切回应重新拉取最新邀请（onActivated）', async (t) => {
  const directInvite = { id: 5, is_share_link: false, invitee_email: 'new@x.y', status: 'pending' }
  const { vm, away, back, calls } = mountInvitesInTabs(t, [
    [],
    [directInvite],
  ])
  await settle()

  // 首次进入单轮：pending + all 两个请求，不得因 activated 重叠翻倍。
  assert.equal(calls.list.length, 2, '首次进入只发一轮请求（pending+all），不得双请求')

  // 本地状态标记：切走再切回后不得丢失（KeepAlive 状态保留，F24 不回归）。
  const before = vm()
  vm().createForm.role = 'viewer'
  await away()
  await back()
  assert.equal(vm(), before, '切回仍是同一实例（KeepAlive 缓存未重建）')
  assert.equal(vm().createForm.role, 'viewer', '本地状态保留，组件未被重建')

  // 切回应重新拉取：第二轮（pending+all）请求发出。
  assert.equal(calls.list.length, 4, '切回 Tab 应重新发起一轮请求')

  // 新邀请写回：第二轮返回的直接邀请出现在列表。
  await settle()
  assert.ok(
    (vm().directInvites as any[]).some((row) => row.id === 5),
    '切回拉取到新发的邀请',
  )
})
