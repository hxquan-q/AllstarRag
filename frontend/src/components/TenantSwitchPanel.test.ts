/**
 * TenantSwitchPanel 单测（T10 / AC-28、AC-29、AC-31、AC-32、AC-34，票面验收 3）。
 *
 * 覆盖：
 * - props 契约：open / anchor / superAdmin / currentTenantId 生效；
 *   superAdmin=false 时整个面板（含身份行与列表）不渲染（AC-28）。
 * - 超管数据源：打开即走 searchTenants（参数/分页大小）；300ms 防抖搜索，
 *   纯数字关键词同时按空间 ID 过滤；滚动近底加载下一页并追加（AC-32）。
 * - 当前空间高亮：is-current + aria-current + 「当前」徽标（AC-34）。
 * - 四态：加载 / 错误（内联，不与空态混淆）/ 空 / 搜索无结果（AC-34）。
 * - 列表行不显示角色（AC-29）；点击行 emit select、点当前行只 close、
 *   Escape 关闭、enter 键盘切换（AC-31）、created 链路与创建入口门控。
 *
 * 沿用仓库既有 vueFixture + 模板编译渲染模式（真实 script setup + 打桩依赖）。
 * 组件依赖须在此补桩：vue-i18n / tdesign / auth store / api tenant /
 * tenantSwitch / useRoleLabel（含 useHomeTenant）/ zoom；CreateTenantDialog
 * 由 vueFixture 的 .vue 兜底桩为空组件。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../test-utils/vueFixture.ts'

type VNode = { type: unknown; props?: Record<string, any> | null; children: unknown }
type SearchCall = { keyword?: string; tenant_id?: number; page: number; page_size: number }

const SFC = new URL('./TenantSwitchPanel.vue', import.meta.url)
const { descriptor } = parseSfc(readFileSync(SFC, 'utf8'), { filename: 'TenantSwitchPanel.vue' })
assert.ok(descriptor.template, 'TenantSwitchPanel.vue 应包含模板')
const { code } = compile(descriptor.template.content, {
  mode: 'function',
  prefixIdentifiers: true,
  filename: 'TenantSwitchPanel.vue',
})
const renderTemplate = new Function('Vue', code)(vue) as (ctx: any, cache: unknown[]) => VNode

const tenant = (id: number, name: string) => ({ id, name })
const ok = (items: Array<{ id: number; name: string }>, total = items.length) =>
  ({ success: true, data: { items, total } })
const DEFAULT_ITEMS = [tenant(1, 'A'), tenant(2, 'B')]

interface MountOptions {
  open?: boolean
  superAdmin?: boolean
  currentTenantId?: number | null
  anchor?: unknown
  respond?: (params: SearchCall, index: number) => unknown
  memberships?: any[]
  canCreateTenant?: boolean
}

/** 挂载真实 script setup；authStore / api / 切换工具全部打桩并记录调用。 */
async function mountPanel(t: TestContext, options: MountOptions = {}) {
  const f = fixture(t)
  const calls: SearchCall[] = []
  const respond = options.respond ?? (() => ok(DEFAULT_ITEMS))
  const api = {
    calls,
    searchTenants: async (params: SearchCall) => {
      calls.push({ ...params })
      const result = respond(params, calls.length)
      if (result instanceof Error) throw result
      return result
    },
  }
  const store = {
    selectedTenantName: 'A',
    tenant: { id: 1, name: 'A' },
    memberships: options.memberships ?? [],
    canCreateTenant: options.canCreateTenant ?? true,
    allTenants: [] as any[],
    selectedCalls: [] as Array<[number | null, string | null | undefined]>,
    refreshCount: 0,
    setAllTenants(items: any[]) { store.allTenants = items },
    setSelectedTenant(id: number | null, name?: string | null) { store.selectedCalls.push([id, name]) },
    refreshFromAuthMe: async () => { store.refreshCount += 1; return true },
  }
  const records = {
    closed: 0,
    selected: [] as any[],
    created: [] as any[],
    persisted: [] as Array<number | null>,
    stashed: [] as any[],
    navigated: 0,
  }
  const { vm } = f.component(SFC, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { info() {}, error() {}, success() {} } },
    '@/stores/auth': { useAuthStore: () => store },
    '@/api/tenant': api,
    '@/utils/tenantSwitch': {
      navigateAfterTenantSwitch: () => { records.navigated += 1 },
      persistLastActiveTenantPreference: (value: number | null) => {
        records.persisted.push(value)
        return Promise.resolve()
      },
      stashTenantSwitchToast: (payload: any) => { records.stashed.push(payload) },
    },
    // formatRole 打上 role: 前缀，便于断言「列表行不显示角色」（AC-29）。
    '@/composables/useRoleLabel': {
      useRoleLabel: () => ({ formatRole: (value: string) => `role:${value}` }),
      useHomeTenant: () => ({ homeTenantId: { value: null }, isHomeTenant: () => false }),
    },
    '@/utils/zoom': {
      getRootZoom: () => 1,
      rectToCssPx: (rect: any) => rect,
      cssViewportSize: () => ({ width: 1200, height: 900 }),
    },
  }, {
    open: options.open ?? true,
    anchor: (options.anchor ?? null) as any,
    superAdmin: options.superAdmin ?? true,
    currentTenantId: options.currentTenantId ?? 1,
    onClose: () => { records.closed += 1 },
    onSelect: (payload: any) => { records.selected.push(payload) },
    onCreated: (payload: any) => { records.created.push(payload) },
  })
  await flush()
  return { vm: vm as any, api, store, records }
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

function renderFrom(vm: any): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (k: string) => k
      // 模板里的 open/superAdmin 等从 props 取；其余走 setup 暴露的绑定。
      const props = target.$props
      if (props && key in props) return props[key]
      return target[key]
    },
    set(target, key, value) { target[key] = value; return true },
  })
  const originalWarn = console.warn
  console.warn = () => {}
  try { return flatten(renderTemplate(ctx, [])) } finally { console.warn = originalWarn }
}

function hasClass(node: VNode, className: string): boolean {
  const value = node.props?.class
  const matches = (raw: unknown): boolean =>
    typeof raw === 'string' && raw.split(/\s+/).includes(className)
  if (Array.isArray(value)) return value.some(matches)
  if (value && typeof value === 'object') return (value as Record<string, boolean>)[className] === true
  return matches(value)
}

const rowNodes = (vm: any) =>
  renderFrom(vm).filter(node => hasClass(node, 'tenant-submenu-item'))

// ---------- props 契约：superAdmin / open ----------

test('非超管（superAdmin=false）整个面板不渲染：无身份行、无列表数据行（AC-28/验收 3）', async t => {
  const m = await mountPanel(t, { superAdmin: false })
  const nodes = renderFrom(m.vm)
  assert.equal(nodes.some(n => hasClass(n, 'tenant-switch-panel')), false, '面板根节点不得渲染')
  assert.equal(nodes.some(n => hasClass(n, 'tenant-submenu-item')), false, '列表数据行不得渲染')
  assert.equal(nodes.some(n => hasClass(n, 'tenant-switch-current')), false, '当前空间身份行不得渲染')
  assert.equal(nodes.some(n => hasClass(n, 'tenant-switch-status')), false, '状态块不得渲染')
})

test('open=false 不渲染面板且不发起 searchTenants（props 契约 open）', async t => {
  const m = await mountPanel(t, { open: false })
  const nodes = renderFrom(m.vm)
  assert.equal(nodes.some(n => hasClass(n, 'tenant-switch-panel')), false)
  assert.equal(m.api.calls.length, 0, 'open=false 时不得拉取列表')
})

// ---------- 超管数据源：searchTenants / 搜索 / 分页 ----------

test('超管打开即按第 1 页拉取 searchTenants 并渲染列表（AC-32）', async t => {
  const m = await mountPanel(t)
  assert.equal(m.api.calls.length, 1)
  assert.deepEqual(m.api.calls[0], { keyword: undefined, tenant_id: undefined, page: 1, page_size: 20 })
  assert.deepEqual(m.vm.tenants.map((x: any) => x.id), [1, 2])
  assert.deepEqual(m.store.allTenants.map((x: any) => x.id), [1, 2],
    '列表应旁路写入 setAllTenants 供全局切换消费')
  assert.equal(rowNodes(m.vm).length, 2)
  assert.equal(m.store.refreshCount, 1, '打开时限流刷新 /auth/me')
})

test('anchor 生效：面板定位到锚点右侧 8px（props 契约 anchor）', async t => {
  const anchor = {
    getBoundingClientRect: () => ({ left: 10, right: 100, top: 50 }),
  } as unknown as HTMLElement
  const m = await mountPanel(t, { anchor })
  // panelStyle 生成自 VM realm，deepStrictEqual 原型不等，逐字段断言。
  assert.equal(m.vm.panelStyle.left, '108px', '面板左边距 = 锚点 right + 8px 间隙')
  assert.equal(m.vm.panelStyle.top, '50px', '面板顶边距 = 锚点 top（不低于 8px 边距）')
})

test('搜索 300ms 防抖后重查：纯数字关键词同时按空间 ID 过滤（AC-32）', async t => {
  const m = await mountPanel(t)
  assert.equal(m.api.calls.length, 1, '初始加载 1 次')
  m.vm.searchQuery = '100'
  m.vm.handleSearchInput()
  assert.equal(m.api.calls.length, 1, '防抖期内不得发请求')
  t.mock.timers.tick(300)
  await flush()
  assert.equal(m.api.calls.length, 2)
  assert.deepEqual(m.api.calls[1], { keyword: '100', tenant_id: 100, page: 1, page_size: 20 })
  // 普通关键词不做空间 ID 过滤，并重置回第 1 页。
  m.vm.searchQuery = 'dep'
  m.vm.handleSearchInput()
  t.mock.timers.tick(300)
  await flush()
  assert.equal(m.api.calls.length, 3)
  assert.deepEqual(m.api.calls[2], { keyword: 'dep', tenant_id: undefined, page: 1, page_size: 20 })
})

test('滚动接近底部加载下一页：page 递增、追加不覆盖已加载页（AC-32）', async t => {
  const m = await mountPanel(t, {
    respond: p => (p.page === 1
      ? ok([tenant(1, 'A'), tenant(2, 'B')], 5)
      : ok([tenant(3, 'C'), tenant(4, 'D')], 5)),
  })
  // 近底（600-550-100 < 50px）触发第 2 页。
  m.vm.listRef = { scrollHeight: 600, scrollTop: 550, clientHeight: 100 }
  m.vm.handleScroll()
  await flush()
  assert.equal(m.api.calls.length, 2)
  assert.equal(m.api.calls[1].page, 2)
  // 追加发生在 VM realm，先展开到测试 realm 再比较。
  assert.deepEqual([...m.vm.tenants].map((x: any) => x.id), [1, 2, 3, 4], '下一页应追加而非覆盖')
  // 远离底部不触发新请求。
  m.vm.listRef = { scrollHeight: 600, scrollTop: 100, clientHeight: 100 }
  m.vm.handleScroll()
  assert.equal(m.api.calls.length, 2)
})

// ---------- 当前空间高亮 / 不显示角色 ----------

test('当前空间高亮：is-current 类 + aria-current + 唯一「当前」徽标（AC-34）', async t => {
  const m = await mountPanel(t, { currentTenantId: 2 })
  assert.equal(m.vm.isCurrent(1), false)
  assert.equal(m.vm.isCurrent(2), true)
  const nodes = renderFrom(m.vm)
  const currentRow = nodes.find(
    n => hasClass(n, 'tenant-submenu-item') && hasClass(n, 'is-current'))
  assert.ok(currentRow, 'currentTenantId 对应行应有 is-current 选中态')
  assert.equal(currentRow!.props?.['aria-current'], 'true')
  assert.equal(
    nodes.filter(n => hasClass(n, 'tenant-submenu-item-badge')).length, 1,
    '「当前」徽标只出现一次')
})

test('列表行不显示角色：渲染文本不含任何角色标签（AC-29）', async t => {
  const m = await mountPanel(t, {
    memberships: [{ tenant_id: 2, tenant_name: 'B', role: 'admin' }],
  })
  const texts = renderFrom(m.vm)
    .map(n => n.children)
    .filter(c => typeof c === 'string') as string[]
  assert.equal(
    texts.some(text => text.includes('role:')), false,
    '角色只允许出现在切换 toast，不允许渲染进列表行')
})

// ---------- 列表四态（AC-34） ----------

test('首屏加载态：状态块显示 loading，不算错误', async t => {
  const m = await mountPanel(t, { respond: () => new Promise(() => {}) })
  assert.equal(m.vm.statusIsLoading, true)
  assert.equal(m.vm.listStatus, 'tenant.loading')
  const nodes = renderFrom(m.vm)
  const status = nodes.find(n => hasClass(n, 'tenant-switch-status'))
  assert.ok(status, '加载中应有状态块')
  assert.equal(hasClass(status!, 'is-error'), false)
  assert.equal(nodes.some(n => n.type === 't-loading'), true)
})

test('首屏失败渲染内联错误态，不与空态混淆（AC-34）', async t => {
  const m = await mountPanel(t, { respond: () => ({ success: false, message: 'backend down' }) })
  assert.equal(m.vm.statusIsError, true)
  assert.equal(m.vm.listStatus, 'backend down', '错误态展示后端 message')
  const status = renderFrom(m.vm).find(n => hasClass(n, 'tenant-switch-status'))!
  assert.equal(hasClass(status, 'is-error'), true)
  assert.equal(m.vm.tenants.length, 0)
})

test('空列表显示空态文案（AC-34）', async t => {
  const m = await mountPanel(t, { respond: () => ok([], 0) })
  assert.equal(m.vm.listStatus, 'tenant.switcher.empty')
  assert.equal(m.vm.statusIsError, false)
  assert.equal(rowNodes(m.vm).length, 0)
})

test('搜索无结果显示 noMatch 而非空态（AC-34）', async t => {
  const m = await mountPanel(t, {
    respond: p => (p.keyword === 'zzz' ? ok([], 0) : ok(DEFAULT_ITEMS)),
  })
  m.vm.searchQuery = 'zzz'
  m.vm.handleSearchInput()
  t.mock.timers.tick(300)
  await flush()
  assert.equal(m.vm.listStatus, 'tenant.noMatch')
  assert.equal(m.vm.statusIsError, false)
})

// ---------- 切换调用与关闭 ----------

test('点击空间行 emit select 并走切换链路（emit 在链路执行前发出）（验收 3）', async t => {
  const m = await mountPanel(t, {
    currentTenantId: 1,
    memberships: [{ tenant_id: 2, tenant_name: 'B', role: 'admin' }],
  })
  m.vm.selectTenant(m.vm.tenants[1])
  // payload 生成自组件所在的 VM realm，deepStrictEqual 原型不等，逐字段断言。
  assert.equal(m.records.selected.length, 1)
  assert.equal(m.records.selected[0].tenantId, 2)
  assert.equal(m.records.selected[0].name, 'B')
  assert.deepEqual(m.store.selectedCalls, [[2, 'B']], '写 override 让请求带 X-Tenant-ID')
  assert.equal(m.records.closed >= 1, true, '切换时面板请求关闭')
  assert.deepEqual(m.records.stashed.map((s: any) => s.name), ['B'])
  assert.equal(m.records.stashed[0].role, 'role:admin', '有 membership 时 toast 带角色')
  await flush()
  assert.deepEqual(m.records.persisted, [2], '持久化 last_active 为目标空间')
  assert.equal(m.records.navigated, 1, '切换后整页刷新')
})

test('点当前空间行只收起面板：不发 select、不走切换链路（对照）', async t => {
  const m = await mountPanel(t, { currentTenantId: 1 })
  m.vm.selectTenant(m.vm.tenants[0])
  assert.equal(m.records.selected.length, 0)
  assert.equal(m.store.selectedCalls.length, 0)
  assert.equal(m.records.persisted.length, 0)
  assert.equal(m.records.navigated, 0)
  assert.equal(m.records.closed, 1)
})

// ---------- 键盘可达（AC-31） ----------

test('键盘可达：列表行 enter 触发切换（AC-31）', async t => {
  const m = await mountPanel(t, { currentTenantId: 1 })
  const targetRow = rowNodes(m.vm).find(n => !hasClass(n, 'is-current'))
  assert.ok(targetRow?.props?.onKeydown, '空间行应绑定 keydown 处理器')
  // enter/space 两个 handler 编译成数组；传入 Enter 事件只有 enter 分支命中。
  const handlers = targetRow!.props!.onKeydown
  for (const handler of (Array.isArray(handlers) ? handlers : [handlers])) {
    handler({ key: 'Enter', preventDefault() {} })
  }
  assert.equal(m.records.selected.length, 1)
  assert.equal(m.records.selected[0].tenantId, 2)
  assert.equal(m.records.selected[0].name, 'B')
})

test('搜索框 Escape 请求关闭面板（AC-31）', async t => {
  const m = await mountPanel(t)
  const input = renderFrom(m.vm).find(n => n.type === 'input')
  assert.ok(input?.props?.onKeydown, '搜索框应绑定 keydown 处理器')
  input!.props!.onKeydown({ key: 'Escape' })
  assert.equal(m.records.closed, 1)
})

// ---------- 创建入口与 created 链路 ----------

test('canCreateTenant=true 渲染「新建空间」入口（对照）', async t => {
  const m = await mountPanel(t, { canCreateTenant: true })
  assert.equal(renderFrom(m.vm).some(n => hasClass(n, 'tenant-submenu-create')), true)
})

test('canCreateTenant=false 不渲染「新建空间」入口', async t => {
  const m = await mountPanel(t, { canCreateTenant: false })
  assert.equal(renderFrom(m.vm).some(n => hasClass(n, 'tenant-submenu-create')), false)
})

test('新建空间成功 emit created 并切到新空间（emit 在链路前发出）', async t => {
  const m = await mountPanel(t)
  await m.vm.onTenantCreated({ id: 9, name: 'New' })
  assert.equal(m.records.created.length, 1)
  assert.equal(m.records.created[0].tenantId, 9)
  assert.equal(m.records.created[0].name, 'New')
  assert.deepEqual(m.store.selectedCalls, [[9, 'New']])
  await flush()
  assert.deepEqual(m.records.persisted, [9])
  assert.equal(m.records.navigated, 1)
})
