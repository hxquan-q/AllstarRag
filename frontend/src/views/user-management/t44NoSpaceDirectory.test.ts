/**
 * T44 / Codex 复核 ①③：超管「无空间」目录 + 多空间编辑计数隐藏。
 *
 * - 系统管理员且 scope=all 时左目录新增 no_space 条目（文案键
 *   userManagement.directory.noSpace），计数取响应 no_membership_count；
 *   选中该目录的请求带 without_membership=true 且不带 role。
 * - scope=current/other 不渲染 no_space；非超管任何情况下都没有该目录。
 * - scope=other/all 时「编辑」目录不渲染人数徽标（count=null），
 *   scope=current 保持数字（Codex 复核 ③：多空间双计误导，用户裁决隐藏）。
 * - 6 语言目录文案键齐全；API 客户端序列化 without_membership；
 *   RoleDirectory 对 count=null 不渲染徽标。
 *
 * 复用 UserManagement.page.test.ts 的 vueFixture + evaluate 模式，
 * 验证真实 useUserManagement 脚本，而不是测试内复制的逻辑。
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

const SFC = new URL('./UserManagement.vue', import.meta.url)
const COMPOSABLE = readFileSync(new URL('./useUserManagement.ts', import.meta.url), 'utf8')
const API_SOURCE = readFileSync(new URL('../../api/user-management.ts', import.meta.url), 'utf8')
const DIRECTORY_SOURCE = readFileSync(new URL('./RoleDirectory.vue', import.meta.url), 'utf8')

const ROLE_COUNTS = { viewer: 5, contributor: 4, admin: 3, owner: 2 }

interface T44MountOptions {
  isSystemAdmin?: boolean
  role?: string
  capabilities?: string[]
  /** 列表响应 data（可带 no_membership_count）。 */
  listData?: Record<string, unknown>
}

function makeAuth() {
  return { effectiveTenantId: 7, currentUserId: 'u-owner' }
}

function makeCapabilities(options: T44MountOptions) {
  const granted = new Set(options.capabilities ?? ['user.view'])
  return {
    role: options.role ?? 'owner',
    isSystemAdmin: options.isSystemAdmin ?? false,
    loaded: true,
    loading: false,
    can: (capability: string) => granted.has(capability),
    refresh: async () => {},
  }
}

async function mountPage(t: TestContext, options: T44MountOptions = {}) {
  const f = fixture(t)
  const calls = { listUsers: [] as Array<{ tenantId: number; params: any }> }
  const api = {
    listUsers: async (tenantId: number, params: any) => {
      calls.listUsers.push({ tenantId, params })
      return {
        success: true,
        data: options.listData ?? {
          items: [{ user_id: 'u1', email: 'a@example.com', username: 'a', role: 'owner', status: 'active', created_at: '2026-11-05T10:00:00Z' }],
          total: 2,
          role_counts: ROLE_COUNTS,
        },
      }
    },
    listSystemAdmins: async () => ({ total: 0, admins: [] }),
    resolveUserManagementError: (error: unknown) => `resolved:${String(error)}`,
  }
  const composable = f.evaluate(COMPOSABLE, {
    vue: await import('vue'),
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    '@/api/user-management': api,
    '@/api/tenant/members': {},
    '@/stores/auth': { useAuthStore: () => makeAuth() },
    '@/stores/capabilities': { useCapabilitiesStore: () => makeCapabilities(options) },
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

function entryKeys(vm: any): string[] {
  return [...vm.directoryEntries].map((entry: any) => entry.key)
}

function findEntry(vm: any, key: string): any {
  return [...vm.directoryEntries].find((entry: any) => entry.key === key)
}

// ---------- ① 无空间目录渲染条件与计数 ----------

test('T44 超管 scope=all 目录含 no_space，计数用 no_membership_count', async (t) => {
  const { vm } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    listData: {
      items: [],
      total: 0,
      role_counts: ROLE_COUNTS,
      no_membership_count: 3,
    },
  })
  vm.selectScope('all')
  await flush()
  assert.deepEqual(entryKeys(vm), ['system_admin', 'owner', 'editor', 'viewer', 'no_space'])
  const noSpace = findEntry(vm, 'no_space')
  assert.ok(noSpace, 'scope=all 必须渲染无空间目录')
  assert.equal(noSpace.count, 3, '无空间徽标计数取响应 no_membership_count')
  assert.equal(noSpace.label, 'userManagement.directory.noSpace')
})

test('T44 scope=current/other 不渲染 no_space 条目', async (t) => {
  const { vm } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    listData: {
      items: [], total: 0, role_counts: ROLE_COUNTS, no_membership_count: 3,
    },
  })
  // 默认 current：无 no_space
  assert.equal(findEntry(vm, 'no_space'), undefined, 'current 视图没有无空间目录')
  vm.selectScope('other')
  await flush()
  assert.equal(findEntry(vm, 'no_space'), undefined, 'other 视图同样没有无空间目录')
  assert.deepEqual(entryKeys(vm), ['system_admin', 'owner', 'editor', 'viewer'])
})

test('T44 非超管任何 scope 都没有 no_space，编辑计数保持数字', async (t) => {
  const { vm } = await mountPage(t, { isSystemAdmin: false })
  assert.deepEqual(entryKeys(vm), ['owner', 'editor', 'viewer'])
  // 非超管 selectScope('all') 被界面门控拒绝（后端仍有 403 兜底，C4）。
  vm.selectScope('all')
  await flush()
  assert.equal(vm.scope, 'current')
  assert.equal(findEntry(vm, 'no_space'), undefined)
  assert.equal(typeof findEntry(vm, 'editor').count, 'number')
})

// ---------- ① 选中无空间目录的请求参数 ----------

test('T44 选中 no_space 请求带 without_membership=true 且不带 role', async (t) => {
  const { vm, calls } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    listData: {
      items: [
        { user_id: 'u-probe', email: 'probe@example.com', username: 'probe', role: '', status: 'active', is_active: true, created_at: '2026-11-10T08:00:00Z' },
      ],
      total: 1,
      role_counts: ROLE_COUNTS,
      no_membership_count: 1,
    },
  })
  vm.selectScope('all')
  await flush()
  const before = calls.listUsers.length
  vm.selectRole('no_space')
  await flush()
  assert.equal(vm.selectedRole, 'no_space')
  assert.ok(calls.listUsers.length > before, '选中目录必须触发加载')
  const last = calls.listUsers.at(-1)!
  assert.equal(last.params.scope, 'all')
  assert.equal(last.params.without_membership, true)
  assert.equal(last.params.role, undefined, '无空间过滤不携带角色参数')
  assert.equal(last.params.page, 1)
  // 行映射：无空间账号 role 为空串。
  assert.equal([...vm.rows][0].user_id, 'u-probe')
  assert.equal([...vm.rows][0].role, '')
})

test('T44 scope 切走 all 时 no_space 选中态回退 owner，不再发无空间请求', async (t) => {
  const { vm, calls } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    listData: { items: [], total: 0, role_counts: ROLE_COUNTS, no_membership_count: 1 },
  })
  vm.selectScope('all')
  await flush()
  vm.selectRole('no_space')
  await flush()
  vm.selectScope('current')
  await flush()
  assert.equal(vm.scope, 'current')
  assert.equal(vm.selectedRole, 'owner', '目录消失后选中态必须回退，不能停留在 no_space')
  const last = calls.listUsers.at(-1)!
  assert.equal(last.params.scope, 'current')
  assert.equal(last.params.without_membership, undefined)
  assert.equal(last.params.role, 'owner')
})

test('T44 非法入口：scope=current 时选 no_space 无效且不发请求', async (t) => {
  const { vm, calls } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
  })
  const before = calls.listUsers.length
  vm.selectRole('no_space')
  await flush()
  assert.equal(vm.selectedRole, 'owner', '无空间目录不存在时选中必须被拒绝')
  assert.equal(calls.listUsers.length, before)
})

// ---------- ③ 多空间视图编辑目录徽标隐藏 ----------

test('T44 scope=other/all 编辑目录徽标隐藏（count=null），current 保持数字', async (t) => {
  const { vm } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    listData: { items: [], total: 0, role_counts: ROLE_COUNTS },
  })
  // current：编辑徽标保持数字（回归保护）。
  assert.equal(findEntry(vm, 'editor').count, 7)
  vm.selectScope('other')
  await flush()
  assert.strictEqual(findEntry(vm, 'editor').count, null,
    'other 视图编辑计数是 contributor+admin 去重前相加，会双计，必须隐藏')
  vm.selectScope('all')
  await flush()
  assert.strictEqual(findEntry(vm, 'editor').count, null, 'all 视图同样隐藏')
  // owner / viewer / system_admin 徽标不受影响。
  assert.equal(findEntry(vm, 'owner').count, 2)
  assert.equal(findEntry(vm, 'viewer').count, 5)
  assert.equal(findEntry(vm, 'system_admin').count, 0)
})

test('T44 RoleDirectory 渲染层：count=null 不渲染人数徽标', () => {
  assert.match(
    DIRECTORY_SOURCE,
    /v-if="entry\.count != null"/,
    '徽标 span 必须以 entry.count != null 守卫，null 时不渲染空徽标',
  )
})

// ---------- API 客户端 ----------

test('T44 API 客户端序列化 without_membership 查询参数', (t) => {
  const f = fixture(t)
  const urls: string[] = []
  const api = f.evaluate(API_SOURCE, {
    '@/utils/request': {
      get: async (url: string) => { urls.push(url); return { success: true, data: {} } },
      post: async () => ({}),
    },
    '@/i18n': { __esModule: true, default: { global: { t: (key: string) => key } } },
  }) as any

  void api.listUsers(7, { scope: 'all', without_membership: true, page: 1 })
  void api.listUsers(7, { scope: 'all' })
  assert.equal(urls.length, 2)
  assert.match(urls[0], /[?&]without_membership=true/, '带过滤时必须序列化 without_membership=true')
  assert.match(urls[0], /[?&]scope=all/)
  assert.equal(/[?&]without_membership=/.test(urls[1]!), false,
    '未设置时不携带该参数')
})

// ---------- 6 语言文案 ----------

const BUNDLES: Record<string, any> = {
  'zh-CN': zhCN,
  'zh-TW': zhTW,
  'en-US': enUS,
  'ja-JP': jaJP,
  'ko-KR': koKR,
  'ru-RU': ruRU,
}

function lookup(bundle: any, key: string): unknown {
  return key.split('.').reduce<any>(
    (node, part) => (node == null ? undefined : node[part]),
    bundle,
  )
}

for (const [locale, bundle] of Object.entries(BUNDLES)) {
  test(`T44 目录文案键 userManagement.directory.noSpace 齐全（${locale}）`, () => {
    const value = lookup(bundle, 'userManagement.directory.noSpace')
    assert.equal(typeof value, 'string', `${locale} 必须定义该键`)
    assert.ok(String(value).trim().length > 0, `${locale} 文案不得为空`)
  })
}
