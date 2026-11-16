/**
 * T36 / AC-2：多空间视图的「切换到该空间」与表格门控。
 *
 * - SwitchSpaceButton 零/单/多目标渲染与切换调用，目标解析排除当前空间、去重、丢弃非法 id。
 * - UserActionsCell 多空间视图不渲染交接项、渲染切换按钮；scope=current 行为不变。
 * - UserTable 多空间视图角色单元格走 switchSpaceRequired tooltip（无下拉）。
 * 沿用 vueFixture + 模板编译渲染，验证真实组件。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'
import * as types from './types.ts'
import { resolveTenantSwitchTargets, type UserActionPermissions, type UserRow } from './types.ts'

type VNode = { type: unknown; props?: Record<string, any> | null; children: unknown }

function renderTemplate(file: URL) {
  const { descriptor } = parseSfc(readFileSync(file, 'utf8'), { filename: file.pathname })
  assert.ok(descriptor.template)
  const { code } = compile(descriptor.template.content, {
    mode: 'function', prefixIdentifiers: true, filename: file.pathname,
  })
  return new Function('Vue', code)(vue) as (ctx: any, cache: unknown[]) => unknown
}

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

function renderFrom(vm: any, render: (ctx: any, cache: unknown[]) => unknown): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (k: string) => k
      const props = target.props
      if (props && key in props) return props[key]
      return target[key]
    },
    set(target, key: string, value: unknown) { target[key] = value; return true },
  })
  const originalWarn = console.warn
  console.warn = () => {}
  try { return flatten(render(ctx, [])) } finally { console.warn = originalWarn }
}

const hasType = (nodes: VNode[], type: string) => nodes.some((node) => node.type === type)

// ---------- SwitchSpaceButton ----------

const SWITCH_BUTTON = new URL('./SwitchSpaceButton.vue', import.meta.url)
const SWITCH_SPACE_SOURCE = readFileSync(new URL('./switchSpace.ts', import.meta.url), 'utf8')
const renderSwitch = renderTemplate(SWITCH_BUTTON)

const membership = (tenant_id: number | string, tenant_name = '', role = 'viewer') =>
  ({ tenant_id, tenant_name, role }) as any

function mountSwitch(
  t: TestContext,
  memberships: any[],
  effectiveTenantId = 7,
) {
  const f = fixture(t)
  // 执行真实 switchSpace.ts，只桩掉租户切换工具，验证 setSelectedTenant 链路。
  const selected: any[] = []
  const persisted: any[] = []
  let navigated = 0
  const auth = {
    effectiveTenantId,
    user: { tenant_id: String(effectiveTenantId) },
    setSelectedTenant: (tenantId: number | null, tenantName?: string | null) => {
      selected.push([tenantId, tenantName])
    },
  }
  const switchModule = f.evaluate(SWITCH_SPACE_SOURCE, {
    '@/utils/tenantSwitch': {
      persistLastActiveTenantPreference: (value: unknown) => {
        persisted.push(value)
        return Promise.resolve()
      },
      navigateAfterTenantSwitch: () => { navigated += 1 },
    },
  })
  const { vm } = f.component(SWITCH_BUTTON, {
    '@/stores/auth': { useAuthStore: () => auth },
    './types': types,
    './switchSpace': switchModule,
  }, { memberships, label: 'userManagement.actions.switchSpace' })
  return {
    vm: vm as any,
    selected,
    persisted,
    navigated: () => navigated,
  }
}

test('T36 切换目标解析：排除当前空间、同空间去重、非法 id 丢弃', () => {
  const targets = resolveTenantSwitchTargets([
    membership(8, 'Other A'),
    membership(8, 'Duplicate A'),
    membership(0, 'Zero'),
    membership(-3, 'Negative'),
    membership('abc', 'Not a number'),
    membership(7, 'Current'),
    membership(9, ''),
  ], 7)
  assert.deepEqual(targets, [
    { tenantId: 8, tenantName: 'Other A', label: 'Other A' },
    { tenantId: 9, tenantName: null, label: '#9' },
  ])
  assert.deepEqual(resolveTenantSwitchTargets(undefined, 7), [])
  assert.deepEqual(resolveTenantSwitchTargets([], 7), [])
})

test('T36 SwitchSpaceButton 零目标不渲染按钮', (t) => {
  const { vm } = mountSwitch(t, [membership(7, 'Current')])
  assert.deepEqual(vm.targets, [])
  const nodes = renderFrom(vm, renderSwitch)
  assert.equal(hasType(nodes, 't-button'), false)
  assert.equal(hasType(nodes, 't-dropdown'), false)
})

test('T36 SwitchSpaceButton 单目标渲染按钮并直接切换', async (t) => {
  const mount = mountSwitch(t, [
    membership(7, 'Current'),
    membership(8, 'Other A'),
  ])
  const { vm } = mount
  assert.deepEqual(vm.targets.map((target: any) => target.tenantId), [8])
  const nodes = renderFrom(vm, renderSwitch)
  assert.equal(hasType(nodes, 't-button'), true)
  assert.equal(hasType(nodes, 't-dropdown'), false)

  vm.switchToFirst()
  await flush()
  assert.deepEqual(mount.selected, [[8, 'Other A']], '调用 setSelectedTenant(目标空间)')
  assert.deepEqual(mount.persisted, [8], '持久化 last_active 为目标空间')
  assert.equal(mount.navigated(), 1, '切换后触发整页刷新')
})

test('T36 SwitchSpaceButton 多目标渲染下拉并切换到所选空间', async (t) => {
  const mount = mountSwitch(t, [
    membership(7, 'Current'),
    membership(8, 'Other A'),
    membership(9, ''),
  ])
  const { vm } = mount
  assert.deepEqual(vm.targets.map((target: any) => target.label), ['Other A', '#9'])
  const nodes = renderFrom(vm, renderSwitch)
  assert.equal(hasType(nodes, 't-dropdown'), true)

  vm.switchTo(vm.targets[1])
  await flush()
  assert.deepEqual(mount.selected, [[9, null]])
  assert.deepEqual(mount.persisted, [9])
  assert.equal(mount.navigated(), 1)
})

// ---------- UserActionsCell ----------

const ACTION_CELL = new URL('./UserActionsCell.vue', import.meta.url)

// 手动渲染时没有组件实例，Vue 会把未注册标签解析成字符串元素，导致插槽
// 被当成普通 children 展平。这里用带身份的占位组件保留插槽结构，便于断言
// 「多空间视图是否渲染交接项」。
const resolvingVue = {
  ...vue,
  resolveComponent: (name: string) => ({ name }),
}
const renderCell = (() => {
  const { descriptor } = parseSfc(readFileSync(ACTION_CELL, 'utf8'), { filename: ACTION_CELL.pathname })
  assert.ok(descriptor.template)
  const { code } = compile(descriptor.template.content, {
    mode: 'function', prefixIdentifiers: true, filename: ACTION_CELL.pathname,
  })
  return new Function('Vue', code)(resolvingVue) as (ctx: any, cache: unknown[]) => unknown
})()
const isNamed = (node: VNode, name: string) =>
  typeof node.type === 'object' && node.type !== null && (node.type as any).name === name
const SWITCH_STUB = { name: 'SwitchSpaceButton' }

const PERMISSIONS: UserActionPermissions = {
  changeRole: true, ban: true, unban: true, purge: true, transfer: true,
}

const OWNER_ROW: UserRow = {
  user_id: 'u-owner-target',
  email: 'owner@example.com',
  username: 'owner',
  role: 'owner',
  status: 'active',
  created_at: '',
}

function mountCell(t: TestContext, scope: 'current' | 'other' | 'all') {
  const f = fixture(t)
  const { vm } = f.component(ACTION_CELL, {
    './types': types,
    './SwitchSpaceButton.vue': { default: SWITCH_STUB },
  }, {
    row: OWNER_ROW,
    currentUserId: 'u-self',
    isSystemAdmin: true,
    permissions: PERMISSIONS,
    ownerCount: 2,
    canResetPassword: false,
    scope,
    t: (key: string) => key,
  })
  return vm as any
}

// 组件标签由编译器解析为字符串名（无当前实例时 resolveComponent 返回名称）。
const hasText = (nodes: VNode[], text: string) =>
  nodes.some((node) => node.children === text)

test('T36 多空间视图 UserActionsCell 无交接项、有切换按钮', (t) => {
  const vm = mountCell(t, 'other')
  const nodes = renderFrom(vm, renderCell)
  assert.equal(hasText(nodes, 'userManagement.actions.transfer'), false, '多空间视图不得渲染交接入口')
  assert.equal(nodes.some((node) => isNamed(node, 'SwitchSpaceButton')), true, '应渲染切换到该空间按钮')
})

test('T36 回归 scope=current UserActionsCell 保留交接项、无切换按钮', (t) => {
  const vm = mountCell(t, 'current')
  const nodes = renderFrom(vm, renderCell)
  assert.equal(hasText(nodes, 'userManagement.actions.transfer'), true, '当前空间视图交接入口不变')
  assert.equal(nodes.some((node) => isNamed(node, 'SwitchSpaceButton')), false, '当前空间不显示切换按钮')
})

// ---------- UserTable ----------

const TABLE = new URL('./UserTable.vue', import.meta.url)

const baseProps = {
  items: [] as UserRow[],
  total: 0,
  page: 1,
  pageSize: 20,
  loading: false,
  error: '',
  permissions: PERMISSIONS,
  currentUserId: 'u-owner',
  isSystemAdmin: false,
  ownerCount: 2,
  scope: 'current' as 'current' | 'other' | 'all',
}

function mountTable(t: TestContext, props: Partial<typeof baseProps> = {}) {
  const f = fixture(t)
  const { vm } = f.component(TABLE, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) },
    './types': types,
  }, { ...baseProps, ...props })
  return vm as any
}

const viewer: UserRow = {
  user_id: 'u-viewer', email: 'v@example.com', username: 'v',
  role: 'viewer', status: 'active', created_at: '',
}

test('T36 多空间视图（other）角色单元格走 switchSpaceRequired 且无下拉', (t) => {
  const vm = mountTable(t, { scope: 'other' })
  assert.equal(vm.roleGate(viewer).reasonKey, 'userManagement.tooltips.switchSpaceRequired')
  assert.equal(vm.canChangeRole(viewer), false)
  const actions = vm.columns.find((column: any) => column.colKey === 'actions')
  assert.equal(actions.width, 200)
})

test('T36 多空间视图（all）角色单元格走 switchSpaceRequired 且无下拉', (t) => {
  const vm = mountTable(t, { scope: 'all' })
  assert.equal(vm.roleGate(viewer).reasonKey, 'userManagement.tooltips.switchSpaceRequired')
  assert.equal(vm.canChangeRole(viewer), false)
  const actions = vm.columns.find((column: any) => column.colKey === 'actions')
  assert.equal(actions.width, 200)
})

test('T36 回归 scope=current 角色门控与操作列宽度不变', (t) => {
  const vm = mountTable(t, { scope: 'current' })
  assert.equal(vm.roleGate(viewer).allowed, true)
  assert.equal(vm.roleGate(viewer).reasonKey, undefined)
  assert.equal(vm.canChangeRole(viewer), true)
  const actions = vm.columns.find((column: any) => column.colKey === 'actions')
  assert.equal(actions.width, 120)

  // 既有 owner / 自操作门控语义不变。
  const ownerRow: UserRow = { ...viewer, user_id: 'u-other-owner', role: 'owner' }
  assert.equal(vm.roleGate(ownerRow).reasonKey, 'userManagement.tooltips.transferRequired')
  const selfRow: UserRow = { ...viewer, user_id: 'u-owner' }
  assert.equal(vm.roleGate(selfRow).reasonKey, 'userManagement.tooltips.self')
})
