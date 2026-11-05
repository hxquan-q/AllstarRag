/**
 * 新增账号 / co-owner / owner 弹窗（T08 / T13 / AC-9、AC-12、AC-28、AC-30）。
 *
 * 覆盖：
 * - AddUserDialog：创建/邀请两 tab、角色只允许 viewer/contributor/admin、
 *   两个 tab 分别调用现有建号/邀请接口；
 * - AddOwnerDialog：owner 与超管的四种模式（新建/邀请 co-owner、设 owner、
 *   邀请 owner），以及提交参数。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture } from '../../test-utils/vueFixture.ts'
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

test('AddUserDialog 角色只允许 viewer/contributor/admin（AC-28/30）', (t) => {
  const { vm } = mountAddUser(t)
  assert.deepEqual([...vm.allowedRoles], ['admin', 'contributor', 'viewer'])
  assert.equal([...vm.allowedRoles].includes('owner' as any), false)
  assert.deepEqual([...vm.roleOptions].map((option: any) => option.value),
    ['admin', 'contributor', 'viewer'])
})

test('AddUserDialog 系统管理员同样看不到 owner 选项（AC-30）', (t) => {
  const { vm } = mountAddUser(t, { isSystemAdmin: true, actorRole: '' })
  assert.deepEqual([...vm.allowedRoles], ['admin', 'contributor', 'viewer'])
})

test('AddUserDialog 同时有创建/邀请能力时渲染两个 tab（T13）', (t) => {
  const { vm } = mountAddUser(t)
  const tabs = addUserRender.render(vm).filter((node) => node.type === 't-tab-panel')
  assert.deepEqual(tabs.map((node) => node.props?.value), ['create', 'invite'])
})

test('AddUserDialog 只有邀请能力时只渲染邀请 tab（T13）', (t) => {
  const { vm } = mountAddUser(t, { canCreate: false, canInvite: true })
  const tabs = addUserRender.render(vm).filter((node) => node.type === 't-tab-panel')
  assert.deepEqual(tabs.map((node) => node.props?.value), ['invite'])
})

test('AddUserDialog 创建账号调用现有建号接口（T13）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => true }
  vm.form.username = ' bob '
  vm.form.email = ' bob@example.com '
  vm.form.password = 'secret123'
  vm.form.role = 'admin'
  await vm.submit()
  assert.equal(calls.create.length, 1)
  const [tenantId, payload] = calls.create[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.username, 'bob')
  assert.equal(payload.email, 'bob@example.com')
  assert.equal(payload.password, 'secret123')
  assert.equal(payload.role, 'admin')
  assert.equal(vm.form.password, '', 'password must be cleared after submit')
})

test('AddUserDialog 邀请 tab 调用现有邀请接口（T13）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => true }
  vm.activeTab = 'invite'
  vm.form.email = 'invite@example.com'
  vm.form.role = 'contributor'
  await vm.submit()
  assert.equal(calls.create.length, 0)
  assert.equal(calls.invite.length, 1)
  const [tenantId, payload] = calls.invite[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.email, 'invite@example.com')
  assert.equal(payload.role, 'contributor')
})

test('AddUserDialog 表单校验失败时不调用接口（T13）', async (t) => {
  const { vm, calls } = mountAddUser(t)
  vm.formRef = { validate: async () => false }
  await vm.submit()
  assert.equal(calls.create.length, 0)
  assert.equal(calls.invite.length, 0)
})

const ADD_OWNER = new URL('./AddOwnerDialog.vue', import.meta.url)

function mountAddOwner(t: TestContext, mode: string) {
  const calls = {
    coOwner: [] as any[],
    ownerInvite: [] as any[],
    provision: [] as any[],
    invitations: [] as any[],
  }
  const f = fixture(t)
  const { vm } = f.component(ADD_OWNER, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {}, warning() {}, info() {} } },
    '@/api/tenant/invitations': {
      createInvitation: async (...args: any[]) => { calls.invitations.push(args); return { success: true } },
    },
    '@/api/user-management': {
      createCoOwner: async (...args: any[]) => { calls.coOwner.push(args); return { success: true } },
      createOwnerInvite: async (...args: any[]) => {
        calls.ownerInvite.push(args)
        return { success: true, data: { invite_url: 'https://invite.example.com/tok' } }
      },
      provisionOwnerSpace: async (...args: any[]) => { calls.provision.push(args); return { success: true } },
      resolveUserManagementError: (error: unknown) => String(error),
    },
    '@/api/system': { listAllUsers: async () => ({ users: [] }) },
  }, { visible: true, tenantId: 7, mode })
  return { vm: vm as any, calls }
}

test('AddOwnerDialog owner 分支：新建 co-owner 的标题与规则（AC-9）', (t) => {
  const { vm } = mountAddOwner(t, 'createCoOwner')
  assert.equal(vm.title, 'userManagement.addOwner.createTitle')
  assert.deepEqual(Object.keys(vm.rules).sort(), ['email', 'password', 'username'])
})

test('AddOwnerDialog owner 分支：邀请 co-owner 的标题与规则（AC-9）', (t) => {
  const { vm } = mountAddOwner(t, 'inviteCoOwner')
  assert.equal(vm.title, 'userManagement.addOwner.inviteCoOwnerTitle')
  assert.deepEqual(Object.keys(vm.rules), ['email'])
})

test('AddOwnerDialog 超管分支：设 owner 的标题与规则（AC-9/11）', (t) => {
  const { vm } = mountAddOwner(t, 'provision')
  assert.equal(vm.title, 'userManagement.dialogs.provisionTitle')
  assert.deepEqual(Object.keys(vm.rules), ['targetUserId'])
})

test('AddOwnerDialog 超管分支：邀请 owner 的标题与规则（AC-9/11）', (t) => {
  const { vm } = mountAddOwner(t, 'inviteOwner')
  assert.equal(vm.title, 'userManagement.dialogs.inviteTitle')
  assert.deepEqual(Object.keys(vm.rules), ['email'])
})

test('AddOwnerDialog 有效期只接受正整数（AC-30b）', (t) => {
  const { vm } = mountAddOwner(t, 'inviteOwner')
  vm.form.expiresInDays = '3'
  assert.equal(vm.positiveDays(), 3)
  vm.form.expiresInDays = '3.9'
  assert.equal(vm.positiveDays(), 3)
  vm.form.expiresInDays = '0'
  assert.equal(vm.positiveDays(), undefined)
  vm.form.expiresInDays = ''
  assert.equal(vm.positiveDays(), undefined)
})

test('AddOwnerDialog 新建 co-owner 调用 createCoOwner（AC-9）', async (t) => {
  const { vm, calls } = mountAddOwner(t, 'createCoOwner')
  vm.formRef = { validate: async () => true }
  vm.form.username = 'co'
  vm.form.email = 'co@example.com'
  vm.form.password = 'secret123'
  await vm.submitCreateCoOwner()
  assert.equal(calls.coOwner.length, 1)
  const [tenantId, payload] = calls.coOwner[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.username, 'co')
  assert.equal(payload.email, 'co@example.com')
})

test('AddOwnerDialog 邀请 owner 生成链接并回填（AC-9/11）', async (t) => {
  const { vm, calls } = mountAddOwner(t, 'inviteOwner')
  vm.formRef = { validate: async () => true }
  vm.form.email = 'owner@example.com'
  vm.form.spaceName = 'Owner Space'
  vm.form.expiresInDays = '3'
  await vm.submitInviteOwner()
  assert.equal(calls.ownerInvite.length, 1)
  const [payload] = calls.ownerInvite[0]!
  assert.equal(payload.email, 'owner@example.com')
  assert.equal(payload.space_name, 'Owner Space')
  assert.equal(payload.expires_in_days, 3)
  assert.equal(vm.generatedLink, 'https://invite.example.com/tok')
})

test('AddOwnerDialog 设 owner 调用 provisionOwnerSpace（AC-9/11）', async (t) => {
  const { vm, calls } = mountAddOwner(t, 'provision')
  vm.formRef = { validate: async () => true }
  vm.form.targetUserId = 'u-target'
  vm.form.spaceName = 'Space'
  await vm.submitProvision()
  assert.equal(calls.provision.length, 1)
  const [targetId, payload] = calls.provision[0]!
  assert.equal(targetId, 'u-target')
  assert.equal(payload.space_name, 'Space')
})

test('AddOwnerDialog 邀请 co-owner 复用邀请接口且角色为 owner（AC-9）', async (t) => {
  const { vm, calls } = mountAddOwner(t, 'inviteCoOwner')
  vm.formRef = { validate: async () => true }
  vm.form.email = 'co@example.com'
  await vm.submitInviteCoOwner()
  assert.equal(calls.invitations.length, 1)
  const [tenantId, payload] = calls.invitations[0]!
  assert.equal(tenantId, 7)
  assert.equal(payload.email, 'co@example.com')
  assert.equal(payload.role, 'owner')
})
