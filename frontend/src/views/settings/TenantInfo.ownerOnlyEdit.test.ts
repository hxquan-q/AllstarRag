/**
 * T08 补充（single-space-per-account，队长 2026-11-11 拍板）：
 * 空间信息（名称/描述）编辑入口 owner-only 的守卫测试。
 *
 * 现状已是 owner-only：后端 routes_auth_tenant.go g.Owner() 放行 +
 * TenantInfo.vue:274 hasRole('owner') 前端门控（01-spec AC-5 拍板例外：
 * 空间信息修改保持 owner 专属；普通管理员可改的是模型/检索/存储等空间配置）。
 * 因此本文件是防回归基线（预期 PASS，非红点）；既有 TenantInfo.test.ts
 * 只覆盖数据刷新，未覆盖该守卫。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { reactive, ref } from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

// 与 stores/auth.ts 的 ROLE_LEVEL 同序（viewer<contributor<admin<owner）。
const LEVELS: Record<string, number> = { viewer: 10, contributor: 20, admin: 30, owner: 40 }

function mountTenantInfo(f: ReturnType<typeof fixture>, role: string) {
  const auth = reactive({
    currentTenantId: 7,
    currentTenantRole: role,
    hasRole: (min: string) => (LEVELS[role] ?? 0) >= (LEVELS[min] ?? 0),
  })
  const { vm } = f.component(new URL('./TenantInfo.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key, locale: ref('zh-CN') }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {}, warning() {}, info() {} }, DialogPlugin: {} },
    '@/api/auth': {
      getCurrentUser: async () => ({
        success: true,
        data: { tenant: { id: 7, name: 'Workspace', description: '' } },
      }),
    },
    '@/api/tenant': {},
    '@/api/tenant/members': { fetchAllTenantMembers: async () => [{ user_id: 'u1', role }] },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/composables/useRoleLabel': {
      useRoleLabel: () => ({ formatRole: String }),
      useHomeTenant: () => ({ homeTenantId: ref(7) }),
    },
    '@/utils/tenantSwitch': {},
  })
  return vm as any
}

test('T08 空间信息编辑入口仅 owner 可见，admin/viewer 不可编辑（守卫基线，AC-5 例外）', async (t) => {
  // fixture(t) 内部会 enable mock timers，一个 test 只能建一个 fixture，
  // 三种角色共用它分别挂载。
  const f = fixture(t)
  assert.equal(mountTenantInfo(f, 'owner').canEditTenant, true)
  assert.equal(mountTenantInfo(f, 'admin').canEditTenant, false)
  assert.equal(mountTenantInfo(f, 'viewer').canEditTenant, false)
  await flush()
})

test('T08 空间信息编辑按钮模板以 canEditTenant 守卫（源码断言，防回归）', () => {
  const source = readFileSync(new URL('./TenantInfo.vue', import.meta.url), 'utf8')
  const guarded = source.match(/v-if="canEditTenant"/g) ?? []
  assert.ok(guarded.length >= 2, '名称/描述两处编辑按钮都必须由 canEditTenant 守卫')
})
