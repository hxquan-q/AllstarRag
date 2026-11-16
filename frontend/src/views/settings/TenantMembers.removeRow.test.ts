import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { reactive, ref } from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

// CP3-FIX2 前端 known issue：DELETE /members 的 409 现在可能来自真正的
// last-owner，也可能来自目标角色并发变化（后端两者同为 code 1005，前端
// 无法可靠区分），removeRow 统一显示通用冲突文案，不再误报「最后 owner」。

type RemoveCall = { tenantId: number; userId: string }

async function removeRowFixture(t: TestContext, rejection: unknown | (() => unknown)) {
  const f = fixture(t)
  const errors: string[] = []
  const removeCalls: RemoveCall[] = []
  const auth = reactive({
    currentTenantId: 7,
    currentTenantRole: 'owner',
    canAccessAllTenants: false,
    user: { id: 'current-user' },
  })
  const { vm, close } = f.component(new URL('./TenantMembers.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: ref('zh-CN') }) },
    'tdesign-vue-next': {
      MessagePlugin: {
        success() {},
        info() {},
        error: (message: string) => errors.push(message),
      },
    },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/clipboard': { copyWithToast: async () => {} },
    '@/utils/docsUrl': { docsUrl: () => '' },
    '@/utils/memberRoles': {
      allowedCreateRoles: () => [],
      canCreateAccount: () => false,
      canResetTarget: () => false,
    },
    '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' } },
    '@/i18n/auditActionLabel': { auditActionLabel: () => '' },
    '@/api/tenant/members': {
      listMembers: async () => ({
        success: true,
        data: { members: [], total: 0, page: 1, page_size: 20 },
      }),
      removeMember: async (tenantId: number, userId: string) => {
        removeCalls.push({ tenantId, userId })
        throw typeof rejection === 'function' ? (rejection as () => unknown)() : rejection
      },
    },
    '@/api/tenant/invitations': {
      listTenantInvitations: async () => ({ success: true, data: { invitations: [], total: 0 } }),
    },
    '@/api/tenant/audit-log': {},
  })
  await flush()
  return { vm, close, errors, removeCalls }
}

function httpError(status: number, message: string) {
  return Object.assign(new Error(message), { status })
}

test('removeRow 收到 409 显示通用冲突文案，不再显示 lastOwner', async t => {
  const f = await removeRowFixture(t, httpError(409, 'tenant membership role changed'))

  await f.vm.removeRow({ user_id: 'u-target' })
  await flush()

  assert.deepEqual(f.removeCalls, [{ tenantId: 7, userId: 'u-target' }])
  assert.deepEqual(f.errors, ['userManagement.errors.conflict'])
  assert.equal(f.errors.includes('tenantMember.errors.lastOwner'), false)
})

test('removeRow 404 仍显示 notFound 文案（既有映射未变）', async t => {
  const f = await removeRowFixture(t, httpError(404, 'membership not found'))

  await f.vm.removeRow({ user_id: 'u-gone' })
  await flush()

  assert.deepEqual(f.errors, ['tenantMember.errors.notFound'])
})
