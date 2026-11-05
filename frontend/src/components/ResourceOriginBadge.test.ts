/**
 * 资产来源徽章的墓碑回退（T03 / AC-22）。
 *
 * 彻底删除采用墓碑化，资产 creator_id 仍指向墓碑行。后端回填 creator_name 时
 * 查不到账号（或老数据缺失），前端显示「已注销用户」而不是「本空间」，
 * 保证资产列表不会出现空白来源，也不误导用户以为资源由本空间创建。
 * T15 起回退文案由 resourceOrigin.tenant 改为 resourceOrigin.deletedUser。
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
  const vm = mountBadge(t, { creatorName: 'Alice' })
  assert.equal(vm.displayText, 'Alice')
  assert.equal(vm.tooltipText, 'resourceOrigin.tenantTooltipWithCreator')
})

test('墓碑/已删除账号（creator_name 缺失）显示「已注销用户」（AC-22 / T15）', (t) => {
  const vm = mountBadge(t, { creatorName: '' })
  // 账号已被墓碑化时不能回退成「本空间」，否则会误导用户以为资源由本空间创建。
  assert.equal(vm.displayText, 'resourceOrigin.deletedUser')
  assert.equal(vm.tooltipText, 'resourceOrigin.deletedUserTooltip')
})
