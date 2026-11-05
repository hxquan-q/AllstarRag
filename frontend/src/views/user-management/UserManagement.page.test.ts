/**
 * 用户管理页面壳（T07 / T09 / AC-2、AC-26、AC-27、AC-31、AC-41）。
 *
 * 复用仓库既有 vueFixture，并直接用 `evaluate` 执行真实的
 * `useUserManagement.ts`，只把 auth / capabilities / HTTP 客户端打桩，
 * 因此这里验证的是真实目录、计数与请求参数，而不是测试内复制的一份逻辑。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'
import * as types from './types.ts'

const SFC = new URL('./UserManagement.vue', import.meta.url)
const COMPOSABLE = readFileSync(new URL('./useUserManagement.ts', import.meta.url), 'utf8')

const { descriptor: PAGE_DESCRIPTOR } = parseSfc(readFileSync(SFC, 'utf8'), {
  filename: 'UserManagement.vue',
})
assert.ok(PAGE_DESCRIPTOR.template)
const { code: PAGE_RENDER_CODE } = compile(PAGE_DESCRIPTOR.template.content, {
  mode: 'function', prefixIdentifiers: true, filename: 'UserManagement.vue',
})
const renderPage = new Function('Vue', PAGE_RENDER_CODE)(vue) as (ctx: any, cache: unknown[]) => unknown

type VNode = { type: unknown; props?: Record<string, any> | null; children: unknown }

function flattenNodes(vnode: unknown, out: VNode[] = []): VNode[] {
  if (vnode == null || typeof vnode === 'boolean' || typeof vnode === 'string') return out
  if (Array.isArray(vnode)) { for (const item of vnode) flattenNodes(item, out); return out }
  const node = vnode as VNode
  out.push(node)
  let children: unknown = node.children
  if (typeof children === 'function' && children.length === 0) children = children()
  if (Array.isArray(children)) flattenNodes(children, out)
  else if (children && typeof children === 'object') {
    for (const slot of Object.values(children as Record<string, unknown>)) {
      if (typeof slot === 'function' && slot.length === 0) flattenNodes(slot(), out)
    }
  }
  return out
}

function renderToolbar(vm: any): string[] {
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
  try {
    const nodes = flattenNodes(renderPage(ctx, []))
    const texts: string[] = []
    for (const node of nodes) {
      if (typeof node.children === 'string') texts.push(node.children)
      if (Array.isArray(node.children)) {
        for (const child of node.children) {
          if (typeof child === 'string') texts.push(child)
          else if (child && typeof child === 'object' && typeof (child as VNode).children === 'string') {
            texts.push((child as VNode).children as string)
          }
        }
      }
    }
    return texts
  } finally {
    console.warn = originalWarn
  }
}

const ROLE_COUNTS = { viewer: 5, contributor: 4, admin: 3, owner: 2 }

interface MountOptions {
  role?: string
  isSystemAdmin?: boolean
  capabilities?: string[]
  loaded?: boolean
  loading?: boolean
  listUsersResult?: unknown
  listUsersError?: Error
  systemAdmins?: (params: { offset: number; limit: number }) => { total: number; admins: unknown[] }
}

function makeAuth() {
  return { effectiveTenantId: 7, currentUserId: 'u-owner' }
}

function makeCapabilities(options: MountOptions) {
  const granted = new Set(options.capabilities ?? [])
  return {
    role: options.role ?? 'owner',
    isSystemAdmin: options.isSystemAdmin ?? false,
    loaded: options.loaded ?? true,
    loading: options.loading ?? false,
    can: (capability: string) => granted.has(capability),
    refresh: async () => {},
  }
}

async function mountPage(t: TestContext, options: MountOptions = {}) {
  const f = fixture(t)
  const calls = {
    listUsers: [] as Array<{ tenantId: number; params: any }>,
    listSystemAdmins: [] as any[],
    promote: [] as any[],
    revoke: [] as any[],
  }
  const api = {
    listUsers: async (tenantId: number, params: any) => {
      calls.listUsers.push({ tenantId, params })
      if (options.listUsersError) throw options.listUsersError
      return options.listUsersResult ?? {
        success: true,
        data: {
          items: [{ user_id: 'u1', email: 'a@example.com', username: 'a', role: 'owner', status: 'active', created_at: '2026-11-05T10:00:00Z' }],
          total: 2,
          role_counts: ROLE_COUNTS,
        },
      }
    },
    listSystemAdmins: async (params: any) => {
      calls.listSystemAdmins.push(params)
      return options.systemAdmins?.(params) ?? { total: 0, admins: [] }
    },
    resolveUserManagementError: (error: unknown) =>
      `resolved:${error instanceof Error ? error.message : String(error)}`,
  }
  const composable = f.evaluate(COMPOSABLE, {
    vue,
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
      promoteUserToSystemAdmin: async (...args: any[]) => { calls.promote.push(args); return { success: true } },
      revokeSystemAdmin: async (...args: any[]) => { calls.revoke.push(args); return { success: true } },
    },
    '@/api/user-management': api,
    './useUserManagement': composable,
    './types': types,
  })
  await flush()
  await flush()
  return { vm: vm as any, calls }
}

test('owner 左目录为四类，计数来自 role_counts（AC-2/41）', async (t) => {
  const { vm, calls } = await mountPage(t, {
    capabilities: ['user.view', 'user.owner.add', 'user.create'],
  })
  assert.deepEqual([...vm.directoryEntries].map((entry: any) => entry.key),
    ['owner', 'admin', 'contributor', 'viewer'])
  assert.deepEqual([...vm.directoryEntries].map((entry: any) => entry.count), [2, 3, 4, 5])
  assert.equal(vm.selectedRole, 'owner')
  assert.equal(vm.total, 2)
  assert.equal([...vm.rows].length, 1)
  assert.equal(calls.listUsers[0].tenantId, 7)
  assert.equal(calls.listUsers[0].params.role, 'owner')
  assert.equal(calls.listUsers[0].params.page, 1)
  assert.equal(calls.listUsers[0].params.page_size, 20)
  assert.equal(calls.listUsers[0].params.q, undefined)
})

test('切换目录触发对应 role 查询并回到第一页（AC-27）', async (t) => {
  const { vm, calls } = await mountPage(t, {
    capabilities: ['user.view', 'user.owner.add'],
  })
  const before = calls.listUsers.length
  vm.page = 3
  vm.selectRole('admin')
  await flush()
  assert.equal(vm.selectedRole, 'admin')
  assert.equal(vm.page, 1)
  const last = calls.listUsers.at(-1)!
  assert.equal(last.params.role, 'admin')
  assert.equal(last.params.page, 1)
  assert.ok(calls.listUsers.length > before)
})

test('超管左目录额外包含「超管」且排在最前（AC-2/41/42）', async (t) => {
  const { vm, calls } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'admin',
    capabilities: [
      'user.view', 'user.ban', 'user.unban', 'user.purge',
      'user.role.change', 'user.owner.add', 'space.transfer', 'user.create', 'user.invite',
    ],
    systemAdmins: (params) => params.limit === 1
      // 计数探针只关心 total；完整列表从 offset=0 开始拉，offset=1 返回空即结束。
      ? { total: 6, admins: [] }
      : (params.offset === 0
        ? {
          total: 6,
          admins: [{
            id: 'sa1', email: 'sa@example.com', username: 'sa', is_active: true,
            is_system_admin: true, created_at: '2026-11-05T10:00:00Z', updated_at: '',
          }],
        }
        : { total: 6, admins: [] }),
  })
  assert.deepEqual([...vm.directoryEntries].map((entry: any) => entry.key),
    ['system_admin', 'owner', 'admin', 'contributor', 'viewer'])
  assert.equal([...vm.directoryEntries][0].count, 6)
  assert.ok(calls.listSystemAdmins.length >= 1)

  vm.selectRole('system_admin')
  await flush()
  assert.equal(vm.selectedRole, 'system_admin')
  assert.equal([...vm.rows][0].role, 'system_admin')
  assert.equal(vm.total, 1)
  assert.equal(vm.isSystemAdmin, true)
})

test('无 user.view 能力时不渲染页面内容、不请求列表（AC-1/31）', async (t) => {
  const { vm, calls } = await mountPage(t, {
    capabilities: [],
  })
  assert.equal(vm.canView, false)
  assert.equal(calls.listUsers.length, 0)
  assert.deepEqual([...vm.rows], [])
  assert.equal(vm.total, 0)
})

test('能力拉取中显示 pending 状态（AC-31）', async (t) => {
  const { vm } = await mountPage(t, {
    loaded: false,
    loading: true,
    capabilities: [],
  })
  assert.equal(vm.capabilitiesPending, true)
})

test('列表失败时进入 error 状态并清空行（AC-31）', async (t) => {
  const { vm } = await mountPage(t, {
    capabilities: ['user.view'],
    listUsersError: new Error('boom'),
  })
  assert.equal(vm.error, 'resolved:boom')
  assert.deepEqual([...vm.rows], [])
  assert.equal(vm.total, 0)
  assert.equal(vm.loading, false)
})

test('工具栏「新增账号」入口按 user.create / user.invite 门控（T13 / C4）', async (t) => {
  const { vm } = await mountPage(t, {
    capabilities: ['user.view', 'user.create'],
  })
  assert.equal(vm.canAddUser, true)
  assert.equal(vm.canCreateUser, true)
  assert.equal(vm.canInviteUser, false)
  assert.equal(vm.canManageOwners, false)
})

test('工具栏 owner 管理入口按 user.owner.add 门控（T08 / C4）', async (t) => {
  const { vm } = await mountPage(t, {
    capabilities: ['user.view', 'user.owner.add'],
  })
  assert.equal(vm.canManageOwners, true)
  assert.equal(vm.canAddUser, false)
})

test('owner 工具栏显示「新建/邀请 co-owner」（T08/AC-9）', async (t) => {
  const { vm } = await mountPage(t, {
    capabilities: ['user.view', 'user.owner.add'],
  })
  const texts = renderToolbar(vm)
  assert.ok(texts.includes('userManagement.actions.createCoOwner'))
  assert.ok(texts.includes('userManagement.actions.inviteCoOwner'))
  assert.equal(texts.includes('userManagement.actions.provisionOwnerSpace'), false)
  assert.equal(texts.includes('userManagement.actions.inviteOwner'), false)
})

test('超管可提升/撤销系统管理员并刷新列表（AC-42）', async (t) => {
  const { vm, calls } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    capabilities: ['user.view'],
  })
  const before = calls.listUsers.length
  await vm.onPromoteSystemAdmin({ user_id: 'u1' })
  await vm.onRevokeSystemAdmin({ user_id: 'u2' })
  assert.equal(calls.promote[0]?.[0]?.user_id, 'u1')
  assert.equal(calls.revoke[0]?.[0], 'u2')
  assert.ok(calls.listUsers.length > before, 'reload must re-query the current list')
})

test('超管工具栏显示「设 owner/邀请 owner」（T08/AC-9）', async (t) => {
  const { vm } = await mountPage(t, {
    isSystemAdmin: true,
    role: 'viewer',
    capabilities: ['user.view', 'user.owner.add'],
  })
  const texts = renderToolbar(vm)
  assert.ok(texts.includes('userManagement.actions.provisionOwnerSpace'))
  assert.ok(texts.includes('userManagement.actions.inviteOwner'))
  assert.equal(texts.includes('userManagement.actions.createCoOwner'), false)
})
