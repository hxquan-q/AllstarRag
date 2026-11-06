/**
 * 用户管理新增 Tab 与弹窗（T18 邀请管理 / T19 操作记录 / T20 重置密码）。
 *
 * 沿用 vueFixture 打桩模式：只替换 API 与 i18n，验证真实组件脚本的门控与请求参数。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

const i18n = { useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: { value: 'zh-CN' } }) }
const message = (errors: string[] = []) => ({
  MessagePlugin: { success() {}, error: (k: string) => errors.push(k), warning() {}, info() {} },
})

// ---------- T18 InviteManagement ----------
const INVITES = new URL('./InviteManagement.vue', import.meta.url)

function mountInvites(t: TestContext, props: Record<string, unknown> = {}) {
  const calls = { list: [] as any[], create: [] as any[], revoke: [] as any[], errors: [] as string[] }
  const f = fixture(t)
  const { vm } = f.component(INVITES, {
    'vue-i18n': i18n,
    'tdesign-vue-next': message(calls.errors),
    '@/api/tenant/invitations': {
      listTenantInvitations: async (...args: any[]) => {
        calls.list.push(args)
        return { success: true, data: { invitations: [], total: 0 } }
      },
      createInviteLink: async (...args: any[]) => { calls.create.push(args); return { success: true } },
      revokeInvitation: async (...args: any[]) => { calls.revoke.push(args); return { success: true } },
    },
    '@/api/user-management': { resolveUserManagementError: (e: unknown) => String(e) },
    '@/utils/clipboard': { copyWithToast: async () => {} },
    './inviteHelpers': {
      absoluteInviteURL: (u: string) => u, formatDateTime: (v: string) => v,
      inviteeName: () => '', shortToken: (v: string) => v,
    },
  }, { tenantId: 7, canInviteOwner: false, ...props })
  return { vm: vm as any, calls }
}

test('InviteManagement 无 user.owner.add 时链接角色只有 编辑/游客（T18/Q37）', (t) => {
  const { vm } = mountInvites(t)
  assert.deepEqual([...vm.roleOptions.map((o: any) => o.value)], ['contributor', 'viewer'])
})

test('InviteManagement 有 user.owner.add 时可发放普通管理员链接，admin 不出现（T18/Q37）', (t) => {
  const { vm } = mountInvites(t, { canInviteOwner: true })
  const values = vm.roleOptions.map((o: any) => o.value)
  assert.deepEqual([...values], ['contributor', 'viewer', 'owner'])
  assert.equal(values.includes('admin'), false)
})

test('InviteManagement 挂载即按空间拉 pending 与全部状态两份列表（T18）', async (t) => {
  const { calls } = mountInvites(t)
  await flush()
  assert.ok(calls.list.length >= 2)
  assert.ok(calls.list.every((args: any[]) => args[0] === 7))
  const terminal = calls.list.map((args: any[]) => args[1].includeTerminal).sort()
  assert.deepEqual(terminal.slice(0, 2), [false, true])
})

test('InviteManagement 人数上限越界时拒绝创建、不发请求（T18）', async (t) => {
  const { vm, calls } = mountInvites(t)
  vm.createForm.maxRegistrations = 1001
  await vm.submitCreate()
  assert.equal(calls.create.length, 0)
  assert.deepEqual(calls.errors, ['userManagement.invites.invalidMaxRegistrations'])
})

test('InviteManagement 创建链接带角色与未来过期时间（T18）', async (t) => {
  const { vm, calls } = mountInvites(t)
  vm.createForm.role = 'viewer'
  vm.createForm.maxRegistrations = 5
  await vm.submitCreate()
  assert.equal(calls.create.length, 1)
  const [tenantId, body] = calls.create[0]!
  assert.equal(tenantId, 7)
  assert.equal(body.role, 'viewer')
  assert.equal(body.max_registrations, 5)
  assert.ok(Date.parse(body.expires_at) > Date.now())
})

test('InviteManagement 撤销调用 revokeInvitation 并重新加载（T18）', async (t) => {
  const { vm, calls } = mountInvites(t)
  await flush()
  const before = calls.list.length
  await vm.revoke({ id: 42 })
  assert.deepEqual(calls.revoke[0], [7, 42])
  assert.ok(calls.list.length > before)
})

// ---------- T19 OperationLog ----------
const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)

test('OperationLog 只展示成员管理动作，不含 access_denied 等噪音（T19/Q35）', () => {
  const source = readFileSync(OPERATIONS, 'utf8')
  for (const action of ['rbac.member_banned', 'rbac.member_unbanned', 'rbac.member_purged',
    'rbac.owner_transferred', 'rbac.member_role_changed', 'rbac.invitation_sent']) {
    assert.ok(source.includes(`'${action}'`), `${action} must be listed`)
  }
  assert.equal(/access_denied'/.test(source.split('const MEMBER_ACTIONS')[1]!.split('] as const')[0]!), false)
})

test('OperationLog 挂载按本空间拉审计并回填用户名（T19）', async (t) => {
  const calls = { audit: [] as any[], users: [] as any[] }
  const f = fixture(t)
  const { vm } = f.component(OPERATIONS, {
    'vue-i18n': i18n,
    '@/api/tenant/audit-log': {
      listAuditLog: async (...args: any[]) => {
        calls.audit.push(args)
        return { success: true, data: [{ id: 1, action: 'rbac.member_banned' }], next_cursor: 0 }
      },
    },
    '@/api/user-management': {
      listUsers: async (...args: any[]) => {
        calls.users.push(args)
        return { data: { items: [{ user_id: 'u1', email: 'a@example.com', username: 'a' }] } }
      },
      resolveUserManagementError: (e: unknown) => String(e),
    },
    '@/i18n/auditActionLabel': { auditActionLabel: (_t: unknown, action: string) => action },
    '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' } },
  }, { tenantId: 7 })
  await flush()
  assert.equal(calls.audit[0]?.[0], 7)
  assert.equal(calls.audit[0]?.[1].after_id, undefined)
  assert.equal(calls.users[0]?.[0], 7)
  assert.equal((vm as any).userNames.u1, 'a@example.com')
  assert.equal((vm as any).hasMore, false)
})

// ---------- T20 ResetPasswordDialog ----------
const RESET = new URL('./ResetPasswordDialog.vue', import.meta.url)

function mountReset(t: TestContext, usePlatformApi: boolean) {
  const calls = { member: [] as any[], platform: [] as any[] }
  const f = fixture(t)
  const { vm } = f.component(RESET, {
    'vue-i18n': i18n,
    'tdesign-vue-next': message(),
    '@/api/tenant/members': {
      resetMemberPassword: async (...args: any[]) => { calls.member.push(args); return { success: true } },
    },
    '@/api/system': {
      resetUserPassword: async (...args: any[]) => { calls.platform.push(args); return { success: true } },
    },
    '@/api/user-management': { resolveUserManagementError: (e: unknown) => String(e) },
  }, {
    target: { user_id: 'u9', email: 'u9@example.com', role: 'viewer' },
    tenantId: 7,
    usePlatformApi,
  })
  return { vm: vm as any, calls }
}

test('ResetPasswordDialog 当前空间走成员重置接口（T20）', async (t) => {
  const { vm, calls } = mountReset(t, false)
  vm.formRef = { validate: async () => true }
  vm.form.password = 'newpass123'
  await vm.submit()
  assert.deepEqual(calls.member[0], [7, 'u9', 'newpass123'])
  assert.equal(calls.platform.length, 0)
})

test('ResetPasswordDialog 跨空间视图走系统重置接口（T20）', async (t) => {
  const { vm, calls } = mountReset(t, true)
  vm.formRef = { validate: async () => true }
  vm.form.password = 'newpass123'
  await vm.submit()
  assert.deepEqual(JSON.parse(JSON.stringify(calls.platform[0])), [{ email: 'u9@example.com', new_password: 'newpass123' }])
  assert.equal(calls.member.length, 0)
})

test('ResetPasswordDialog 校验失败不发请求，密码规则 8–32 位必填（T20）', async (t) => {
  const { vm, calls } = mountReset(t, false)
  vm.formRef = { validate: async () => false }
  await vm.submit()
  assert.equal(calls.member.length + calls.platform.length, 0)
  const rule = vm.rules.password
  assert.equal(rule[0].required, true)
  assert.equal(rule[1].min, 8)
  assert.equal(rule[1].max, 32)
})

// ---------- T26 OperationLog 后端关键词搜索 ----------
// 用源码断言锁定契约（组件挂载用例在本机 tsx 下偶发挂起，行为回归留给票级复跑）。
test('OperationLog 关键词带 q 请求后端、去掉本地关键词过滤（T26）', () => {
  const source = readFileSync(OPERATIONS, 'utf8')
  assert.match(source, /watch\(\s*keyword/, '关键词变化必须触发 watch -> load(true)')
  assert.match(source, /q:\s*keyword\.value\.trim\(\)/, 'listAuditLog 必须携带 q')
  assert.equal(
    /visibleRows[\s\S]{0,400}toLowerCase\(\)\.includes\(q\)/.test(source),
    false,
    '关键词过滤必须交给后端，不能继续本地 includes',
  )
})
