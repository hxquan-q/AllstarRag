/**
 * 新增账号弹窗（T09 / T13 / T20 / AC-11、AC-13）。
 *
 * 覆盖：
 * - AddUserDialog（T20 合并后唯一入口）：创建/邀请两 tab + 超管「平台账号」tab；
 *   角色只提供 普通管理员(admin) / 游客(viewer)，默认 admin（T09/AC-11 白名单）；
 *   owner 只能由开通/交接产生，contributor 停止分配；建号/邀请统一走
 *   createMemberAccount / createInvitation，createCoOwner 已删除。
 * - PlatformAccountForm（超管专属）：开通独立空间（账号成为该空间的空间拥有者）/
 *   新建无空间平台账号（生成密码只显示一次）；「邀请新普通管理员」模式已删（T09）。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'
import { allowedCreateRoles } from '../../utils/memberRoles.ts'

function renderHelpers(file: URL) {
  const { descriptor } = parseSfc(readFileSync(file, 'utf8'), { filename: file.pathname })
  assert.ok(descriptor.template)
  const { code } = compile(descriptor.template.content, {
    mode: 'function', prefixIdentifiers: true, filename: file.pathname,
  })
  const renderTemplate = new Function('Vue', code)(vue) as (ctx: any, cache: unknown[]) => unknown

  type VNode = { type: unknown; props?: Record<string, any> | null; children: unknown }
  function flatten(vnode: unknown, out: VNode[] = []): VNode[] {
    if (vnode == null || typeof vnode === 'boolean') return out
    if (Array.isArray(vnode)) { for (const item of vnode) flatten(item, out); return out }
    const node = vnode as VNode
    out.push(node)
    let children: unknown = node.children
    if (typeof children === 'function' && children.length === 0) children = children()
    if (Array.isArray(children)) flatten(children, out)
    else if (children && typeof children === 'object') {
      for (const slot of Object.values(children as Record<string, unknown>)) {
        if (typeof slot === 'function' && slot.length === 0) flatten(slot(), out)
      }
    }
    return out
  }

  function render(vm: any): VNode[] {
    const ctx = new Proxy(vm, {
      get(target, key: string) {
        if (key === '$t') return (k: string) => k
        const props = target.props
        if (props && key in props) return props[key]
        return target[key]
      },
      set(target, key: string, value) { target[key] = value; return true },
    })
    const originalWarn = console.warn
    console.warn = () => {}
    try { return flatten(renderTemplate(ctx, [])) } finally { console.warn = originalWarn }
  }
  return { render }
}

const ADD_USER = new URL('./AddUserDialog.vue', import.meta.url)
const addUserRender = renderHelpers(ADD_USER)

function mountAddUser(t: TestContext, props: Record<string, unknown> = {}) {
  const calls = {
    create: [] as any[],
    invite: [] as any[],
    errors: [] as string[],
  }
  const f = fixture(t)
  const { vm } = f.component(ADD_USER, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': {
      MessagePlugin: {
        success() {},
        error: (key: string) => calls.errors.push(key),
        warning() {},
        info() {},
      },
    },
    '@/api/tenant/members': {
      createMemberAccount: async (...args: any[]) => { calls.create.push(args); return { success: true } },
    },
    '@/api/tenant/invitations': {
      createInvitation: async (...args: any[]) => { calls.invite.push(args); return { success: true } },
    },
    '@/api/user-management': {
      resolveUserManagementError: (error: unknown) => String(error),
    },
    '@/utils/memberRoles': { allowedCreateRoles },
  }, {
    visible: true,
    tenantId: 7,
    actorRole: 'owner',
    isSystemAdmin: false,
    canCreate: true,
    canInvite: true,
    ...props,
  })
  return { vm: vm as any, calls }
}

test('AddUserDialog 角色为 普通管理员/游客，不含 owner/contributor（T09/AC-11）', (t) => {
  const { vm } = mountAddUser(t)
  assert.deepEqual([...vm.allowedRoles], ['admin', 'viewer'])
  assert.equal([...vm.allowedRoles].includes('owner' as any), false)
  assert.equal([...vm.allowedRoles].includes('contributor' as any), false)
  assert.deepEqual([...vm.roleOptions].map((option: any) => option.value),
    ['admin', 'viewer'])
})

test('AddUserDialog 游客操作者无可选角色（T09 fail-closed）', (t) => {
  const { vm } = mountAddUser(t, { actorRole: 'viewer' })
  assert.deepEqual([...vm.allowedRoles], [])
})

test('AddUserDialog 系统管理员角色同样只有 普通管理员/游客（T09/AC-11）', (t) => {
  const { vm } = mountAddUser(t, { isSystemAdmin: true, actorRole: '' })
  assert.deepEqual([...vm.allowedRoles], ['admin', 'viewer'])
})

test('AddUserDialog 系统管理员旁路空间角色，选项不随 actorRole 收窄（T09）', (t) => {
  const { vm } = mountAddUser(t, { isSystemAdmin: true, actorRole: 'viewer' })
  assert.deepEqual([...vm.allowedRoles], ['admin', 'viewer'])
})

test('AddUserDialog 默认角色为普通管理员（admin）（T09/AC-11）', (t) => {
  const { vm } = mountAddUser(t)
  assert.equal(vm.form.role, 'admin')
})

test('AddUserDialog 非超管渲染 创建/邀请 两个 tab，不渲染平台账号 tab（T13/T20）', (t) => {
  const { vm } = mountAddUser(t)
  const tabs = addUserRender.render(vm).filter((node) => node.type === 't-tab-panel')
  assert.deepEqual(tabs.map((node) => node.props?.value), ['create', 'invite'])
})

test('AddUserDialog 超管额外渲染「平台账号」tab（T20）', (t) => {
  const { vm } = mountAddUser(t, { isSystemAdmin: true, actorRole: '' })
  const tabs = addUserRender.render(vm).filter((node) => node.type === 't-tab-panel')
  assert.deepEqual(tabs.map((node) => node.props?.value), ['create', 'invite', 'platform'])
})

test('AddUserDialog 只有邀请能力时只渲染邀请 tab（T13）', (t) => {
  const { vm } = mountAddUser(t, { canCreate: false, canInvite: true })
  const tabs = addUserRender.render(vm).filter((node) => node.type === 't-tab-panel')
  assert.deepEqual(tabs.map((node) => node.props?.value), ['invite'])
})

test('AddUserDialog 创建游客账号调用 createMemberAccount（T13/T09）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => true }
  vm.form.username = ' bob '
  vm.form.email = ' bob@example.com '
  vm.form.password = 'secret123'
  vm.form.role = 'viewer'
  await vm.submit()
  assert.equal(calls.create.length, 1)
  const [tenantId, payload] = calls.create[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.username, 'bob')
  assert.equal(payload.email, 'bob@example.com')
  assert.equal(payload.password, 'secret123')
  assert.equal(payload.role, 'viewer')
  assert.equal(vm.form.password, '', 'password must be cleared after submit')
})

test('AddUserDialog 创建普通管理员统一走 createMemberAccount，createCoOwner 已删（T09）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => true }
  vm.form.username = 'co'
  vm.form.email = 'co@example.com'
  vm.form.password = 'secret123'
  vm.form.role = 'admin'
  await vm.submit()
  assert.equal(calls.create.length, 1)
  const [tenantId, payload] = calls.create[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.username, 'co')
  assert.equal(payload.email, 'co@example.com')
  assert.equal(payload.password, 'secret123')
  assert.equal(payload.role, 'admin')
  assert.equal(vm.form.password, '')
  // createCoOwner 死函数已随 T09 微补删除，组件不得再引用。
  assert.equal(readFileSync(ADD_USER, 'utf8').includes('createCoOwner'), false)
})

test('AddUserDialog 创建 tab 的密码为必填（后端要求非空密码）', (t) => {
  const { vm } = mountAddUser(t)
  vm.form.role = 'admin'
  const rules = vm.rules.password
  assert.ok(Array.isArray(rules))
  assert.ok(rules.some((rule: any) => rule.required === true))
})

test('AddUserDialog 邀请 tab 调用现有邀请接口（T13）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => true }
  vm.activeTab = 'invite'
  vm.form.email = 'invite@example.com'
  vm.form.role = 'viewer'
  await vm.submit()
  assert.equal(calls.create.length, 0)
  assert.equal(calls.invite.length, 1)
  const [tenantId, payload] = calls.invite[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.email, 'invite@example.com')
  assert.equal(payload.role, 'viewer')
})

test('AddUserDialog 邀请普通管理员复用邀请接口且 role=admin（T09/AC-11）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => true }
  vm.activeTab = 'invite'
  vm.form.email = 'co@example.com'
  vm.form.role = 'admin'
  await vm.submit()
  assert.equal(calls.invite.length, 1)
  const [tenantId, payload] = calls.invite[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.role, 'admin')
})

test('AddUserDialog 表单校验失败时不调用接口（T13）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => false }
  await vm.submit()
  assert.equal(calls.create.length + calls.invite.length, 0)
})

test('AddUserDialog 平台账号 tab 下弹窗 confirm 不触发空间建号（T20）', async (t) => {
  const { vm, calls } = mountAddUser(t, { isSystemAdmin: true, actorRole: '' })
  vm.formRef = { validate: async () => true }
  vm.activeTab = 'platform'
  await vm.submit()
  assert.equal(calls.create.length + calls.invite.length, 0)
})

const PLATFORM = new URL('./PlatformAccountForm.vue', import.meta.url)

function mountPlatform(t: TestContext, systemUserResult: Record<string, unknown> = {}) {
  const calls = {
    provision: [] as any[],
    systemUser: [] as any[],
    listAll: [] as any[],
    emitted: [] as string[],
  }
  const f = fixture(t)
  const { vm } = f.component(PLATFORM, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {}, warning() {}, info() {} } },
    '@/api/user-management': {
      provisionOwnerSpace: async (...args: any[]) => { calls.provision.push(args); return { success: true } },
      resolveUserManagementError: (error: unknown) => String(error),
    },
    '@/api/system': {
      listAllUsers: async (...args: any[]) => {
        calls.listAll.push(args)
        return {
          users: [
            { id: 'u-1', username: 'alice', email: 'alice@example.com', is_active: true },
            { id: 'u-2', username: '', email: 'banned@example.com', is_active: false },
          ],
        }
      },
      createSystemUser: async (...args: any[]) => { calls.systemUser.push(args); return systemUserResult },
    },
  }, { visible: true })
  return { vm: vm as any, calls }
}

test('PlatformAccountForm 打开即加载候选人，封禁账号不出现在候选里（AC-11）', async (t) => {
  const { vm, calls } = mountPlatform(t)
  await flush()
  assert.equal(calls.listAll.length, 1)
  // 参数对象来自 vm 沙箱（另一个 realm），按 JSON 结构比较。
  assert.equal(JSON.stringify(calls.listAll[0]![0]), JSON.stringify({ offset: 0, limit: 200 }))
  assert.deepEqual(vm.candidateOptions.map((o: any) => o.value), ['u-1'])
  assert.equal(vm.candidatesLoaded, true)
})

test('PlatformAccountForm 开通独立空间规则只要求 targetUserId，提交调 provisionOwnerSpace（T09/AC-11）', async (t) => {
  const { vm, calls } = mountPlatform(t)
  await flush()
  assert.equal(vm.mode, 'provision')
  assert.deepEqual(Object.keys(vm.rules), ['targetUserId'])
  vm.formRef = { validate: async () => true }
  vm.form.targetUserId = 'u-1'
  vm.form.spaceName = ' Space '
  await vm.submit()
  assert.equal(calls.provision.length, 1)
  const [targetId, payload] = calls.provision[0]!
  assert.equal(targetId, 'u-1')
  assert.equal(payload.space_name, 'Space')
})

test('PlatformAccountForm 已删除「邀请新普通管理员」模式，只剩 开通独立空间/新建平台账号（T09/AC-11）', () => {
  const source = readFileSync(PLATFORM, 'utf8')
  assert.equal(source.includes('inviteOwner'), false, 'inviteOwner 模式必须删除（后端路由 T03 已 404）')
  assert.equal(source.includes('createOwnerInvite'), false, 'createOwnerInvite 调用必须删除')
  assert.equal(source.includes('positiveDays'), false, 'positiveDays 随 inviteOwner 模式删除')
  assert.equal(source.includes('expiresInDays'), false, 'expiresInDays 表单字段随 inviteOwner 模式删除')
  // 模式单选只剩 provision / platformUser。
  assert.match(source, /value="provision"/)
  assert.match(source, /value="platformUser"/)
})

test('PlatformAccountForm 新建平台账号：密码留空不下发，生成密码只回显一次（T20）', async (t) => {
  const { vm, calls } = mountPlatform(t, { generated_password: 'Gen#Pass123' })
  vm.mode = 'platformUser'
  await flush()
  assert.deepEqual(Object.keys(vm.rules).sort(), ['email', 'password', 'username'])
  assert.equal(vm.rules.password.some((rule: any) => rule.required === true), false,
    '平台账号密码可留空，由后端生成')
  vm.formRef = { validate: async () => true }
  vm.form.username = ' plat '
  vm.form.email = ' plat@example.com '
  await vm.submit()
  assert.equal(calls.systemUser.length, 1)
  const [payload] = calls.systemUser[0]!
  assert.equal(payload.username, 'plat')
  assert.equal(payload.email, 'plat@example.com')
  assert.equal('password' in payload, false)
  assert.equal(vm.resultText, 'Gen#Pass123')
})

test('PlatformAccountForm 新建平台账号填写密码时下发并在提交后清空', async (t) => {
  const { vm, calls } = mountPlatform(t, {})
  vm.mode = 'platformUser'
  await flush()
  vm.formRef = { validate: async () => true }
  vm.form.username = 'plat'
  vm.form.email = 'plat@example.com'
  vm.form.password = 'secret123'
  await vm.submit()
  const [payload] = calls.systemUser[0]!
  assert.equal(payload.password, 'secret123')
  assert.equal(vm.form.password, '')
  assert.equal(vm.resultText, '')
})

test('PlatformAccountForm 校验失败时不调用任何接口', async (t) => {
  const { vm, calls } = mountPlatform(t)
  await flush()
  vm.formRef = { validate: async () => false }
  await vm.submit()
  assert.equal(calls.provision.length + calls.systemUser.length, 0)
})
