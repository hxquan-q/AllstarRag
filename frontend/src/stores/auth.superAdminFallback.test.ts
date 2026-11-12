import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import * as vue from 'vue'
import * as pinia from 'pinia'
import { fixture } from '../test-utils/vueFixture.ts'

/**
 * CP3-FIX4：currentTenantRole 的跨空间超管 fallback 防回归。
 * 后端 auth.go resolveTenantRole step2 有两个独立分支：is_system_admin
 * 无条件给临时 Admin；can_access_all_tenants 需 crossTenantSwitch。
 * 前端 fallback 曾只认 canAccessAllTenants，is_system_admin-only 超管
 * 切到非成员空间后角色解析为空（8084 演练实证，设置分区全部隐藏）。
 * 本文件锁定两条：a) is_system_admin-only 超管 fallback='admin'；
 * b) 两标志均 false 时保持空串（原行为回归）。
 * 只测 store 本身；依赖全部 mock，不访问真实后端。
 */

function setup(t: TestContext) {
  const f = fixture(t)
  const { useAuthStore } = f.evaluate(
    readFileSync(new URL('./auth.ts', import.meta.url), 'utf8'),
    {
      vue,
      pinia,
      '@/api/auth': { userInfoFromApi: (u: unknown) => u },
      '@/i18n': { __esModule: true, default: { global: { t: (key: string) => key } } },
      '@/composables/useFont': { reloadFontFromStorage() {} },
      '@/composables/useTheme': { reloadThemeFromStorage() {} },
      '@/composables/preferenceStorage': { resetMigrationLatch() {} },
      '@/api/agent': { BUILTIN_QUICK_ANSWER_ID: 'builtin-quick-answer' },
      '@/stores/chatResources': { useChatResourcesStore: () => ({ invalidate() {} }) },
      '@/stores/editorResources': { useEditorResourcesStore: () => ({ invalidate() {} }) },
      '@/stores/organization': { useOrganizationStore: () => ({ clearState() {} }) },
      '@/stores/sequencedRefresh': {
        createSequencedRefresh: () => ({
          refresh: () => Promise.resolve(false),
          share: () => Promise.resolve(false),
          invalidate() {},
        }),
      },
      '@/stores/capabilities': {
        useCapabilitiesStore: () => ({ refresh: () => {}, reset() {} }),
      },
    },
  )
  const root = pinia.createPinia()
  pinia.setActivePinia(root)
  t.after(() => pinia.disposePinia(root))
  const store: any = useAuthStore()
  t.after(() => store.$dispose())
  return { store }
}

// 模拟「切换到非成员空间」：home=10000 有成员行，selected=10001 无行。
function switchToForeignTenant(store: any, isSystemAdmin: boolean) {
  store.setUser({
    id: 'u-sys',
    username: 'sysadmin',
    email: 'sys@example.local',
    tenant_id: '10000',
    can_access_all_tenants: false,
    is_system_admin: isSystemAdmin,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  })
  store.setMemberships([{ tenant_id: 10000, tenant_name: 'Home', role: 'owner' }])
  store.setSelectedTenant(10001, 'Foreign')
}

test('is_system_admin-only super admin gets temporary admin role in a non-member tenant', t => {
  const { store } = setup(t)
  switchToForeignTenant(store, true)
  // 控制断言：确认两标志各自的真实值，避免用例因 mock 失真而假绿。
  assert.equal(store.isSystemAdmin, true)
  assert.equal(store.canAccessAllTenants, false)
  // 前端 fallback 必须与后端 step2 的 is_system_admin 分支对齐。
  assert.equal(store.currentTenantRole, 'admin')
  assert.equal(store.hasRole('admin'), true)
})

test('user without either super flag keeps empty role in a non-member tenant', t => {
  const { store } = setup(t)
  switchToForeignTenant(store, false)
  assert.equal(store.isSystemAdmin, false)
  assert.equal(store.canAccessAllTenants, false)
  // 原行为回归：非超管绝不能从 fallback 拿到任何角色。
  assert.equal(store.currentTenantRole, '')
  assert.equal(store.hasRole('viewer'), false)
})
