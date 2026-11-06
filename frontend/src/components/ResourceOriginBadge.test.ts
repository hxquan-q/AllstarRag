/**
 * 资产来源徽章的创建者三态（T25 / AC-22）。
 *
 * T15 曾把 creator_name 缺失一律回退为「已注销用户」，但老数据 / 抓取数据也会
 * 缺 creator_name，导致误标。T25 起由后端明确返回 creator_deleted：
 *   - creator_name 有值            → 显示创建者名字
 *   - creator_deleted=true         → 显示「已注销用户」
 *   - 两者都没有（未注销 / 未知）   → 回退原来的「本空间」文案
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { fixture } from '../test-utils/vueFixture.ts'

const SFC = new URL('./ResourceOriginBadge.vue', import.meta.url)

function mountBadge(t: TestContext, props: Record<string, unknown>) {
  const f = fixture(t)
  const { vm } = f.component(SFC, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { Icon: {} },
    '@/stores/auth': {
      useAuthStore: () => ({ currentTenantName: 'Tenant A' }),
    },
  }, { variant: 'creator', ...props })
  return vm as any
}

test('creator 徽章有 creator_name 时展示创建者（AC-22）', (t) => {
  const vm = mountBadge(t, { creatorName: 'Alice', creatorDeleted: false })
  assert.equal(vm.displayText, 'Alice')
  assert.equal(vm.tooltipText, 'resourceOrigin.tenantTooltipWithCreator')
})

test('creator_deleted=true 时显示「已注销用户」（AC-22 / T25）', (t) => {
  const vm = mountBadge(t, { creatorName: '', creatorDeleted: true })
  assert.equal(vm.displayText, 'resourceOrigin.deletedUser')
  assert.equal(vm.tooltipText, 'resourceOrigin.deletedUserTooltip')
})

test('creator_name 缺失但未注销时回退「本空间」（T25）', (t) => {
  const vm = mountBadge(t, { creatorName: '', creatorDeleted: false })
  assert.equal(vm.displayText, 'resourceOrigin.tenant')
  assert.equal(vm.tooltipText, 'resourceOrigin.tenantTooltip')
})
