/**
 * 用户管理右表（T07 / T08 / AC-26、AC-27、AC-29、AC-31）。
 *
 * 覆盖：
 * - 列定义（账号 / 角色 / 状态 / 创建时间 / 操作；多空间视图多一列「所属空间」）；
 * - loading / empty / error（含重试入口）三态；
 * - 行内动作门控（owner 目标、自操作、超管目标、交接）；
 * - 角色下拉选项（超管与非超管一致：游客/普通管理员；owner 只经交接/开通产生，T09/AC-11）。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture } from '../../test-utils/vueFixture.ts'
import { gateUserAction, roleChangeOptions, type UserActionPermissions, type UserRow } from './types.ts'

const SFC = new URL('./UserTable.vue', import.meta.url)
const { descriptor } = parseSfc(readFileSync(SFC, 'utf8'), { filename: 'UserTable.vue' })
assert.ok(descriptor.template)
const { code } = compile(descriptor.template.content, {
  mode: 'function', prefixIdentifiers: true, filename: 'UserTable.vue',
})
const renderTemplate = new Function('Vue', code)(vue) as (ctx: any, cache: unknown[]) => unknown

const PERMISSIONS: UserActionPermissions = {
  changeRole: true, ban: true, unban: true, purge: true, transfer: true,
}

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
  const { vm } = f.component(SFC, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) },
    './types': { gateUserAction, roleChangeOptions },
  }, { ...baseProps, ...props })
  return vm as any
}

type VNode = { type: unknown; props?: Record<string, any> | null; children: unknown }

function flatten(vnode: unknown, out: VNode[] = []): VNode[] {
  if (vnode == null || typeof vnode === 'boolean') return out
  if (Array.isArray(vnode)) { for (const item of vnode) flatten(item, out); return out }
  const node = vnode as VNode
  out.push(node)
  let children: unknown = node.children
  if (typeof children === 'function') children = children()
  if (Array.isArray(children)) flatten(children, out)
  else if (children && typeof children === 'object') {
    for (const slot of Object.values(children as Record<string, unknown>)) {
      // 只展开无参插槽；t-table 的列插槽按 { row } 解构，渲染器未提供行时跳过。
      if (typeof slot === 'function' && slot.length === 0) flatten(slot(), out)
    }
  }
  return out
}

function renderFrom(vm: any): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (k: string) => k
      // mounted vm 不直接转发 props；模板里的 items/error/total 等从 props 取。
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

const hasType = (nodes: VNode[], type: string) => nodes.some((node) => node.type === type)

test('右表列顺序为 账号/角色/状态/创建时间/操作（AC-26）', (t) => {
  const vm = mountTable(t)
  assert.deepEqual([...vm.columns].map((column: any) => column.colKey), [
    'account', 'role', 'status', 'created_at', 'actions',
  ])
  assert.deepEqual([...vm.columns].map((column: any) => column.title), [
    'userManagement.columns.account',
    'userManagement.columns.role',
    'userManagement.columns.status',
    'userManagement.columns.createdAt',
    'userManagement.columns.actions',
  ])
})

test('多空间视图在账号列后插入所属空间列（Q34/AC-26）', (t) => {
  const vm = mountTable(t, { scope: 'other' })
  assert.deepEqual([...vm.columns].map((column: any) => column.colKey), [
    'account', 'memberships', 'role', 'status', 'created_at', 'actions',
  ])
})

test('加载态显示 loading（AC-31）', (t) => {
  const vm = mountTable(t, { loading: true })
  const nodes = renderFrom(vm)
  assert.equal(hasType(nodes, 't-loading'), true)
  assert.equal(nodes.some((node) => node.children === 'userManagement.states.loading'), true)
})

test('空表显示空态（AC-27/31）', (t) => {
  const vm = mountTable(t)
  const nodes = renderFrom(vm)
  assert.equal(hasType(nodes, 't-empty'), true)
})

test('错误态显示错误提示与重试按钮（AC-31）', async (t) => {
  const vm = mountTable(t, { error: 'boom' })
  assert.equal(vm.props.error, 'boom')
  const nodes = renderFrom(vm)
  const alert = nodes.find((node) => node.type === 't-alert')
  assert.ok(alert, 'error state must render t-alert')
  assert.equal(alert!.props?.theme, 'error')
  assert.equal(alert!.props?.message, 'boom')
  assert.equal(hasType(nodes, 't-button'), true, 'retry button must be rendered')
})

test('角色下拉：非超管操作者只看到 游客/普通管理员（T08/AC-11）', (t) => {
  const vm = mountTable(t)
  assert.deepEqual([...vm.roleOptions].map((option: any) => option.value),
    ['viewer', 'admin'])
})

test('角色下拉：超管与非超管一致，owner 只经交接/开通产生（T09/AC-11）', (t) => {
  const vm = mountTable(t, { isSystemAdmin: true })
  assert.deepEqual([...vm.roleOptions].map((option: any) => option.value),
    ['viewer', 'admin'])
})

test('行内动作门控：owner 目标 / 自操作 / 超管目标（AC-4/5/7/29）', (t) => {
  const vm = mountTable(t)
  const ownerTarget: UserRow = {
    user_id: 'u-other-owner', email: 'o@example.com', username: 'o',
    role: 'owner', status: 'active', created_at: '',
  }
  assert.equal(vm.gate('purge', ownerTarget).allowed, false)
  assert.equal(vm.gate('purge', ownerTarget).reasonKey, 'userManagement.tooltips.ownerProtected')

  const self: UserRow = { ...ownerTarget, user_id: 'u-owner' }
  const selfGate = gateUserAction('ban', self, 'u-owner', PERMISSIONS, false, 2)
  assert.equal(selfGate.allowed, false)
  assert.equal(selfGate.reasonKey, 'userManagement.tooltips.self')

  const sysAdmin: UserRow = {
    user_id: 'u-sa', email: 'sa@example.com', username: 'sa',
    role: 'system_admin', status: 'active', created_at: '',
  }
  assert.equal(vm.roleGate(sysAdmin).allowed, false)
  assert.equal(gateUserAction('ban', sysAdmin, 'u-owner', PERMISSIONS, false, 2).allowed, false)
})

test('超管视图中 owner 行允许封禁；注销/降级一律先交接（AC-8/13，F2）', (t) => {
  const vm = mountTable(t, { isSystemAdmin: true, currentUserId: 'u-admin' })
  const ownerTarget: UserRow = {
    user_id: 'u-other-owner', email: 'o@example.com', username: 'o',
    role: 'owner', status: 'active', created_at: '',
  }
  assert.equal(gateUserAction('ban', ownerTarget, 'u-admin', PERMISSIONS, true, 0).allowed, true)
  assert.equal(vm.gate('purge', ownerTarget).reasonKey, 'userManagement.tooltips.transferRequired')
  // F2：owner 行角色下拉一律禁用并提示先交接；ownerCount=2 不再放行超管降级
  // （后端对 owner → 非 owner 一律 403）。
  assert.equal(vm.gate('changeRole', ownerTarget).allowed, false)
  assert.equal(vm.roleGate(ownerTarget).reasonKey, 'userManagement.tooltips.transferRequired')
})

test('封禁行状态决定按钮语义为解封（AC-17）', (t) => {
  const vm = mountTable(t)
  const banned: UserRow = {
    user_id: 'u-banned', email: 'b@example.com', username: 'b',
    role: 'viewer', status: 'banned', created_at: '',
  }
  const actionCell = readFileSync(new URL('./UserActionsCell.vue', import.meta.url), 'utf8')
  assert.match(actionCell, /row\.status === 'banned'[\s\S]*?userManagement\.actions\.unban/)
  assert.equal(gateUserAction('unban', banned, 'u-owner', PERMISSIONS, false, 2).allowed, true)
})
