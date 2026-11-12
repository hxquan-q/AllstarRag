/**
 * CP3-FIX3 C2 回归：交接候选必须与后端 AC-7 对齐（active + 普通管理员）。
 *
 * - 游客（viewer）不进候选；普通管理员（admin）进候选；
 * - 原空间拥有者由交接弹窗排除（transferOptions 过滤 target.user_id）；
 * - 六语言空态文案改为「先把目标提升为普通管理员」语义。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { fixture, flush } from '../../test-utils/vueFixture.ts'
import enUS from '../../i18n/locales/en-US.ts'
import jaJP from '../../i18n/locales/ja-JP.ts'
import koKR from '../../i18n/locales/ko-KR.ts'
import ruRU from '../../i18n/locales/ru-RU.ts'
import zhCN from '../../i18n/locales/zh-CN.ts'
import zhTW from '../../i18n/locales/zh-TW.ts'
import * as types from './types.ts'
import type { UserRow } from './types.ts'

const SFC = new URL('./UserManagement.vue', import.meta.url)
const COMPOSABLE = readFileSync(new URL('./useUserManagement.ts', import.meta.url), 'utf8')
const DIALOG = new URL('./UserActionDialogs.vue', import.meta.url)

interface CandidateItem {
  user_id: string
  email: string
  username: string
  role: string
  status: string
  created_at: string
}

function row(
  userID: string,
  role: string,
  status = 'active',
): CandidateItem {
  return {
    user_id: userID,
    email: `${userID}@example.com`,
    username: userID,
    role,
    status,
    created_at: '2026-11-05T10:00:00Z',
  }
}

interface PageSpec {
  items: CandidateItem[]
  total: number
}

async function mountPage(t: TestContext, pages: PageSpec[]) {
  const f = fixture(t)
  const calls: Array<{ page: number; pageSize: number }> = []
  const api = {
    listUsers: async (_tenantId: number, params: any) => {
      const page = params?.page ?? 1
      calls.push({ page, pageSize: params?.page_size })
      const spec = pages[page - 1] ?? { items: [], total: 0 }
      return {
        success: true,
        data: {
          items: spec.items,
          total: spec.total,
          role_counts: { viewer: 0, contributor: 0, admin: 0, owner: 0 },
        },
      }
    },
    listSystemAdmins: async () => ({ total: 0, admins: [] }),
    resolveUserManagementError: (error: unknown) => `resolved:${String(error)}`,
  }
  const auth = { effectiveTenantId: 7, currentUserId: 'u-owner' }
  const capabilities = {
    role: 'owner',
    isSystemAdmin: false,
    loaded: true,
    loading: false,
    can: (capability: string) => capability === 'user.view',
    refresh: async () => {},
  }
  const composable = f.evaluate(COMPOSABLE, {
    vue: await import('vue'),
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    '@/api/user-management': api,
    '@/api/tenant/members': {},
    '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/capabilities': { useCapabilitiesStore: () => capabilities },
    './types': types,
  })
  const { vm } = f.component(SFC, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {}, warning() {}, info() {} } },
    '@/api/tenant/members': { updateMemberRole: async () => ({ success: true }) },
    '@/api/system': {
      promoteUserToSystemAdmin: async () => ({ success: true }),
      revokeSystemAdmin: async () => ({ success: true }),
    },
    '@/api/user-management': api,
    './useUserManagement': composable,
    './types': types,
  })
  await flush()
  await flush()
  return { vm: vm as any, calls }
}

test('CP3-FIX3/C2 候选只保留 active 普通管理员：游客/拥有者/停用行都不进', async (t) => {
  const { vm } = await mountPage(t, [{
    items: [
      row('u-owner', 'owner'),
      row('u-viewer', 'viewer'),
      row('u-admin', 'admin'),
      row('u-banned-admin', 'admin', 'banned'),
    ],
    total: 4,
  }])

  const candidates = (await vm.loadTransferCandidates()) as UserRow[]
  assert.deepEqual(
    [...candidates].map((candidate) => candidate.user_id),
    ['u-admin'],
    '交接候选必须与后端 AC-7 一致：仅 active + admin',
  )
})

test('CP3-FIX3/C2 跨页收集后仍按角色过滤（不会因分页漏掉 admin）', async (t) => {
  const viewers: CandidateItem[] = Array.from({ length: 100 }, (_, index) =>
    row(`u-viewer-${index}`, 'viewer'))
  const { vm, calls } = await mountPage(t, [
    { items: viewers, total: 101 },
    { items: [row('u-admin-page2', 'admin')], total: 101 },
  ])

  const candidates = (await vm.loadTransferCandidates()) as UserRow[]
  assert.deepEqual([...candidates].map((candidate) => candidate.user_id), ['u-admin-page2'])
  assert.deepEqual(calls.filter((call) => call.pageSize === 100).map((call) => call.page), [1, 2])
})

test('CP3-FIX3/C2 没有普通管理员时返回空候选（触发新空态文案）', async (t) => {
  const { vm } = await mountPage(t, [{
    items: [row('u-owner', 'owner'), row('u-viewer', 'viewer')],
    total: 2,
  }])

  const candidates = (await vm.loadTransferCandidates()) as UserRow[]
  assert.deepEqual([...candidates], [], '没有 active admin 时交接弹窗应显示空态提示')
})

test('CP3-FIX3/C2 交接弹窗仍排除原拥有者，只提供普通管理员', async (t) => {
  const target: UserRow = {
    user_id: 'u-owner',
    email: 'owner@example.com',
    username: 'owner',
    role: 'owner',
    status: 'active',
    created_at: '2026-11-05T10:00:00Z',
  }
  const candidates: UserRow[] = [
    target,
    { ...target, user_id: 'u-admin', email: 'admin@example.com', username: 'admin', role: 'admin' },
  ]
  const f = fixture(t)
  const { vm } = f.component(DIALOG, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {}, warning() {}, info() {} } },
    '@/api/user-management': {
      banMember: async () => ({ success: true }),
      unbanMember: async () => ({ success: true }),
      purgeMember: async () => ({ success: true }),
      transferOwner: async () => ({ success: true }),
      resolveUserManagementError: (error: unknown) => String(error),
    },
  }, { tenantId: 7, target, mode: 'transfer', candidates })

  assert.deepEqual(
    [...(vm as any).transferOptions].map((option: any) => option.value),
    ['u-admin'],
    '原拥有者不能出现在交接候选里',
  )
})

test('CP3-FIX3/C2 六语言空态文案改为「先提升为普通管理员」语义', () => {
  const values: Array<[string, string]> = [
    ['zh-CN', (zhCN as any).userManagement.dialogs.transferNoCandidates],
    ['zh-TW', (zhTW as any).userManagement.dialogs.transferNoCandidates],
    ['en-US', (enUS as any).userManagement.dialogs.transferNoCandidates],
    ['ja-JP', (jaJP as any).userManagement.dialogs.transferNoCandidates],
    ['ko-KR', (koKR as any).userManagement.dialogs.transferNoCandidates],
    ['ru-RU', (ruRU as any).userManagement.dialogs.transferNoCandidates],
  ]
  const oldValues = new Set([
    '暂无可交接的成员',
    '暫無可交接的成員',
    'No members available to take over',
  ])
  const keywords: Record<string, string> = {
    'zh-CN': '普通管理员',
    'zh-TW': '一般管理員',
    'en-US': 'administrator',
    'ja-JP': '管理者',
    'ko-KR': '관리자',
    'ru-RU': 'администратор',
  }
  for (const [locale, value] of values) {
    assert.equal(typeof value, 'string', `${locale} 必须有 transferNoCandidates 文案`)
    assert.ok(value.length > 0, `${locale} 空态文案不能为空`)
    assert.ok(!oldValues.has(value), `${locale} 仍是旧的泛化文案：${value}`)
    assert.ok(
      value.toLowerCase().includes(keywords[locale]!.toLowerCase()),
      `${locale} 空态文案应指向普通管理员：${value}`,
    )
  }
})
