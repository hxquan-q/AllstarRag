/**
 * UserMenu 宿主级用例（T11 / AC-28、AC-31、AC-32；T10 观察②收口）。
 *
 * 覆盖（票面验收 1/2/4/7）：
 * - 非超管：头像菜单身份行纯展示——不渲染可点击属性（role/tabindex）、
 *   无 is-clickable hover 反馈，click/enter/space/hover 全部无面板打开路径，
 *   身份行展示当前空间名 + 角色；面板以 superAdmin=false 挂载（AC-28）。
 * - 超管 memberships 为空（tenant_members 零记录）：切换入口仍可用
 *   （showTenantSwitcher 只看超管身份，不依赖 memberships 数量），面板
 *   open/superAdmin/currentTenantId 正确接线；UserMenu 不再从 memberships
 *   构造切换列表（AC-32：列表数据源在 TenantSwitchPanel 内走 searchTenants，
 *   其单测已实证，此处只锁宿主接线）。
 * - 切换链路保持（AC-31/验收 7）：挂真实 utils/tenantSwitch.ts（仅桩
 *   updateMyPreferences），selectTenant → setSelectedTenant + last_active
 *   持久化（切回 home 清零 / 切 peer 写目标 id）+ window.location 整页刷新。
 * - T10 观察②收口：宿主 hover 开合 180ms 延时（重进取消计时）、点击外部
 *   关闭（菜单内点击不误关 + tenant-submenu-floating 浮层命中契约）、
 *   面板 clearSearch 清空按钮（click + enter 路径）。
 *
 * 沿用仓库 vueFixture + 模板编译渲染模式（真实 script setup + 打桩依赖）；
 * 跨 VM realm 对象逐字段断言（T10 技术备注）。
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

const USER_MENU = new URL('./UserMenu.vue', import.meta.url)
const PANEL = new URL('./TenantSwitchPanel.vue', import.meta.url)
const USER_MENU_SOURCE = readFileSync(USER_MENU, 'utf8')
const PANEL_SOURCE = readFileSync(PANEL, 'utf8')
const TENANT_SWITCH_SOURCE = readFileSync(new URL('../utils/tenantSwitch.ts', import.meta.url), 'utf8')

function compileTemplate(file: URL, source: string, vueModule: Record<string, unknown> = vue) {
  const { descriptor } = parseSfc(source, { filename: file.pathname })
  assert.ok(descriptor.template, `${file.pathname} 应包含模板`)
  const { code } = compile(descriptor.template.content, {
    mode: 'function', prefixIdentifiers: true, filename: file.pathname,
  })
  return new Function('Vue', code)(vueModule) as (ctx: any, cache: unknown[]) => VNode
}

// 手动渲染无组件实例：未注册组件被解析成字符串元素后，normalizeChildren 的
// dev 路径会无参调用对象子节点的 default 插槽，v-slot="{ count, open }" 解构
// 即崩；用带身份的占位组件保留插槽结构（t36SpaceSwitch 同款）。
const resolvingVue = { ...vue, resolveComponent: (name: string) => ({ name }) }
const renderUserMenu = compileTemplate(USER_MENU, USER_MENU_SOURCE, resolvingVue)
const renderPanel = compileTemplate(PANEL, PANEL_SOURCE)

const isNamed = (node: VNode, name: string) =>
  typeof node.type === 'object' && node.type !== null && (node.type as any).name === name

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

function renderUserMenuFrom(vm: any): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (k: string) => k
      return target[key]
    },
    set(target, key, value) { target[key] = value; return true },
  })
  const originalWarn = console.warn
  console.warn = () => {}
  try { return flatten(renderUserMenu(ctx, [])) } finally { console.warn = originalWarn }
}

// 面板挂载返回的是 exposeProxy：props（open/superAdmin/currentTenantId）不在
// exposed 里，须经 $props 解析（TenantSwitchPanel.test.ts 同款代理）。
function renderPanelFrom(vm: any): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (k: string) => k
      const props = target.$props
      if (props && key in props) return props[key]
      return target[key]
    },
    set(target, key, value) { target[key] = value; return true },
  })
  const originalWarn = console.warn
  console.warn = () => {}
  try { return flatten(renderPanel(ctx, [])) } finally { console.warn = originalWarn }
}

function hasClass(node: VNode, className: string): boolean {
  const value = node.props?.class
  const matches = (raw: unknown): boolean =>
    typeof raw === 'string' && raw.split(/\s+/).includes(className)
  if (Array.isArray(value)) return value.some(matches)
  if (value && typeof value === 'object') return (value as Record<string, boolean>)[className] === true
  return matches(value)
}

/** 同一事件（如 keydown 的 enter/space）编译为 handler 数组，统一展开调用。 */
function handlers(value: unknown): Array<(event: any) => void> {
  return (Array.isArray(value) ? value : [value]) as Array<(event: any) => void>
}

const collectStrings = (nodes: VNode[]) =>
  nodes.map(n => n.children).filter(c => typeof c === 'string') as string[]

// ---------- UserMenu 挂载（真实 script setup + 全量依赖打桩） ----------

interface AuthOptions {
  canAccessAllTenants?: boolean
  isSystemAdmin?: boolean
  memberships?: any[]
  effectiveTenantId?: number
}

function makeAuth(options: AuthOptions = {}) {
  return {
    isLiteMode: false,
    canAccessAllTenants: options.canAccessAllTenants ?? false,
    isSystemAdmin: options.isSystemAdmin ?? false,
    memberships: options.memberships ?? [],
    selectedTenantName: 'A',
    tenant: { id: 1, name: 'A' },
    user: { username: 'owner-user', email: 'owner@example.com', avatar: '' },
    effectiveTenantId: options.effectiveTenantId ?? 1,
    currentTenantRole: 'owner',
    hasRole: () => false,
    ensureAuthMe: async () => true,
    logout() {},
  }
}

async function mountUserMenu(t: TestContext, options: AuthOptions = {}, reused?: ReturnType<typeof fixture>) {
  // 同一测试内多次挂载须复用 fixture：t.mock.timers.enable 二次调用会抛
  // 'MockTimers is already enabled'。
  const f = reused ?? fixture(t)
  const store = makeAuth(options)
  const uiStore = { sidebarCollapsed: false, openSettings() {} }
  const router = { pushes: [] as any[], push(to: any) { router.pushes.push(to) } }
  const { vm } = f.component(USER_MENU, {
    'vue-router': { useRouter: () => router },
    '@/stores/ui': { useUIStore: () => uiStore },
    '@/stores/auth': { useAuthStore: () => store },
    '@/stores/capabilities': { useCapabilitiesStore: () => ({ can: () => false }) },
    'tdesign-vue-next': { MessagePlugin: { info() {}, error() {}, success() {} } },
    '@/api/auth': { logout: async () => ({ success: true }) },
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    // formatRole 带 role: 前缀，便于断言身份行展示的是角色标签。
    '@/composables/useRoleLabel': {
      useRoleLabel: () => ({
        formatRole: (value: string) => `role:${value}`,
        roleIcon: () => 'role-icon',
      }),
    },
    '@/config/contextualGuides': { openNewUserGuide() {} },
    '@/config/settingsAccess': { SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE: { models: 'admin' } },
    '@/config/customerAccess': { isCustomerViewer: () => false },
    '@/utils/docsUrl': { docsUrl: (path: string) => `docs:${path}` },
  })
  await flush()
  return { f, vm: vm as any, store }
}

// ---------- TenantSwitchPanel 挂载（真实 tenantSwitch.ts 可注入） ----------

interface PanelOptions {
  /** 复用外部 fixture（真实 tenantSwitch.ts 需与组件同 window realm）。 */
  f?: ReturnType<typeof fixture>
  /** 默认全桩；传真实模块时仅 updateMyPreferences 在 API 层打桩。 */
  switchModule?: Record<string, unknown>
  respond?: (params: SearchCall, index: number) => any
}

async function mountPanel(t: TestContext, options: PanelOptions = {}) {
  const f = options.f ?? fixture(t)
  const calls: SearchCall[] = []
  const respond = options.respond ?? (() => ({
    success: true,
    data: { items: [{ id: 1, name: 'A' }, { id: 2, name: 'B' }], total: 2 },
  }))
  const selected: Array<[number | null, string | null | undefined]> = []
  const store = {
    selectedTenantName: 'A',
    tenant: { id: 1, name: 'A' },
    memberships: [] as any[],
    canCreateTenant: true,
    allTenants: [] as any[],
    setAllTenants(items: any[]) { store.allTenants = items },
    setSelectedTenant(id: number | null, name?: string | null) { selected.push([id, name]) },
    refreshFromAuthMe: async () => true,
  }
  const { vm } = f.component(PANEL, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { info() {}, error() {}, success() {} } },
    '@/stores/auth': { useAuthStore: () => store },
    '@/api/tenant': {
      searchTenants: async (params: SearchCall) => {
        calls.push({ ...params })
        const result = respond(params, calls.length)
        if (result instanceof Error) throw result
        return result
      },
    },
    '@/utils/tenantSwitch': options.switchModule ?? {
      navigateAfterTenantSwitch() {},
      persistLastActiveTenantPreference: async () => {},
      stashTenantSwitchToast() {},
    },
    '@/composables/useRoleLabel': {
      useRoleLabel: () => ({ formatRole: (value: string) => value }),
      // home 空间 = 2：验证切回 home 清 last_active 的真实语义。
      useHomeTenant: () => ({ homeTenantId: { value: 2 }, isHomeTenant: (id: number) => id === 2 }),
    },
    '@/utils/zoom': {
      getRootZoom: () => 1,
      rectToCssPx: (rect: any) => rect,
      cssViewportSize: () => ({ width: 1200, height: 900 }),
    },
  }, { open: true, superAdmin: true, currentTenantId: 1 })
  await flush()
  return { f, vm: vm as any, calls, selected }
}

// ---------- AC-28：非超管身份行纯展示 ----------

test('T11 非超管：身份行纯展示——不可聚焦/不可点击/无 hover 反馈，任何交互都无面板打开路径（AC-28）', async t => {
  const { vm } = await mountUserMenu(t, {
    memberships: [{ tenant_id: 1, tenant_name: 'A', role: 'owner' }],
  })
  assert.equal(vm.showTenantSwitcher, false, '非超管 showTenantSwitcher 恒为 false')
  vm.menuVisible = true
  await flush()
  const nodes = renderUserMenuFrom(vm)
  const anchor = nodes.find(n => hasClass(n, 'dropdown-tenant-panel'))
  assert.ok(anchor, '下拉身份行应渲染（当前空间 + 角色展示）')
  assert.equal(anchor!.props?.role, undefined, '非超管不渲染 role="button"')
  assert.equal(anchor!.props?.tabindex, undefined, '非超管不渲染 tabindex（不可聚焦 = 无键盘路径）')
  assert.equal(hasClass(anchor!, 'is-clickable'), false, '非超管无 is-clickable（无 cursor/hover 反馈）')

  const anchorTexts = collectStrings(flatten(anchor))
  assert.ok(anchorTexts.includes('A'), '身份行显示当前空间名（selectedTenantName）')
  assert.ok(anchorTexts.includes('role:owner'), '身份行显示当前角色')

  // 无打开路径：click / enter / space / mouseenter 全部带 showTenantSwitcher 守卫（no-op）
  anchor!.props!.onClick?.()
  for (const handler of handlers(anchor!.props!.onKeydown)) {
    handler({ key: 'Enter', preventDefault() {} })
    handler({ key: ' ', preventDefault() {} })
  }
  anchor!.props!.onMouseenter?.()
  await flush()
  assert.equal(vm.tenantSubmenuOpen, false, '任何交互都不得打开面板')

  // 面板以 superAdmin=false 挂载（面板整体不渲染由 TenantSwitchPanel 单测实证）。
  // 手动渲染的 vnode 保留模板原样 kebab 键（super-admin），未走 props 归一化。
  const panel = nodes.find(n => isNamed(n, 'TenantSwitchPanel'))
  assert.equal(panel?.props?.['super-admin'], false, '宿主以 superAdmin=false 挂载面板')

  // swap 图标已随 T11 整体删除（样式类零残留）
  assert.equal(USER_MENU_SOURCE.includes('dropdown-tenant-panel-trail'), false,
    'swap 图标及其样式类应零残留（T11 删除）')
})

// ---------- AC-32：超管 memberships 为空仍可切换 ----------

test('T11 超管 memberships 为空（tenant_members 零记录）：入口仍可用且列表不经 memberships（AC-32）', async t => {
  const f = fixture(t)
  const { vm } = await mountUserMenu(t, { canAccessAllTenants: true, memberships: [] }, f)
  assert.equal(vm.showTenantSwitcher, true, '切换门控只看超管身份，与 memberships 数量无关')
  const isSystemAdminOnly = await mountUserMenu(t, { isSystemAdmin: true, memberships: [] }, f)
  assert.equal(isSystemAdminOnly.vm.showTenantSwitcher, true, 'isSystemAdmin 单独成立同样放行')

  vm.menuVisible = true
  await flush()
  let nodes = renderUserMenuFrom(vm)
  const anchor = nodes.find(n => hasClass(n, 'dropdown-tenant-panel'))
  assert.ok(anchor, '超管下拉身份行渲染')
  assert.equal(anchor!.props!.role, 'button')
  assert.equal(anchor!.props!.tabindex, 0)
  assert.equal(hasClass(anchor!, 'is-clickable'), true, '超管锚点可点击（hover 反馈）')
  assert.equal(
    nodes.some(n => hasClass(n, 'dropdown-tenant-panel-trail')),
    false,
    'swap 图标已删（T11），不渲染 trail 类图标',
  )

  anchor!.props!.onClick!()
  assert.equal(vm.tenantSubmenuOpen, true, '点击锚点打开面板')
  nodes = renderUserMenuFrom(vm)
  const panel = nodes.find(n => isNamed(n, 'TenantSwitchPanel'))
  assert.ok(panel, '超管挂载 TenantSwitchPanel')
  assert.equal(panel!.props!.open, true, '打开状态传入面板')
  assert.equal(panel!.props!['super-admin'], true, '超管身份传入面板')
  assert.equal(panel!.props!['current-tenant-id'], 1, '当前空间 id 来自 authStore.effectiveTenantId')

  // select/created 链路接线：面板切换动作连同下拉菜单一起收起
  assert.equal(typeof panel!.props!.onSelect, 'function', '@select 接 handlePanelSwitchAction')
  vm.menuVisible = true
  panel!.props!.onSelect!({ tenantId: 2, name: 'B' })
  assert.equal(vm.tenantSubmenuOpen, false, '切换动作收起面板')
  assert.equal(vm.menuVisible, false, '切换动作连同下拉菜单收起')

  // 数据源契约：UserMenu 不再从 memberships 构造切换列表（旧 switchableMemberships
  // 已随 T10 迁出）；超管列表由 TenantSwitchPanel 内部走 searchTenants。
  assert.equal(USER_MENU_SOURCE.includes('switchableMemberships'), false,
    '宿主不得从 memberships 构造切换列表（AC-32：不得把全部空间写入 memberships）')
})

// ---------- AC-31：切换链路保持（真实 tenantSwitch.ts） ----------

test('T11 切换链路保持：setSelectedTenant + last_active 持久化 + window.location 整页刷新（AC-31/验收 7，真实 tenantSwitch.ts）', async t => {
  const f = fixture(t)
  // fixture 的 window 类型不含 location；注入后持有引用直接断言（真实
  // navigateAfterTenantSwitch 写的就是这个 window.location.href）。
  const location = { href: 'about:blank' }
  Object.assign(f.window, { location })
  const preferences: any[] = []
  const switchModule = f.evaluate(TENANT_SWITCH_SOURCE, {
    '@/api/auth': {
      updateMyPreferences: async (payload: any) => {
        preferences.push(payload)
        return { success: true }
      },
    },
  })
  const m = await mountPanel(t, { f, switchModule })
  assert.equal(m.calls.length, 1, '初始加载 1 次')

  // 切到 home 空间（id=2）：last_active 清零（null → 0），名称取目标空间名。
  m.vm.selectTenant(m.vm.tenants[1])
  await flush()
  assert.equal(m.selected.length, 1)
  assert.equal(m.selected[0][0], 2)
  assert.equal(m.selected[0][1], 'B', 'setSelectedTenant(2, "B")：请求带 X-Tenant-ID')
  assert.equal(preferences.length, 1)
  assert.equal(preferences[0].last_active_tenant_id, 0, '切回 home 清除 last_active 偏好（0）')
  assert.equal(location.href, '/platform/knowledge-bases',
    '整页刷新：window.location.href 跳 KB 列表（full navigation，非 SPA 路由）')

  // 切到普通空间（id=3）：last_active 持久化为目标空间 id。
  m.vm.selectTenant({ id: 3, name: 'C' })
  await flush()
  assert.equal(m.selected.length, 2)
  assert.equal(m.selected[1][0], 3)
  assert.equal(m.selected[1][1], 'C')
  assert.equal(preferences.length, 2)
  assert.equal(preferences[1].last_active_tenant_id, 3, '切换到 peer 空间持久化目标 id')
  assert.equal(location.href, '/platform/knowledge-bases')
})

// ---------- T10 观察②：宿主 hover 开合 180ms ----------

test('T10观察② 宿主 hover 开合：180ms 延时关闭、重进取消计时（锚点与面板事件同源）', async t => {
  const { vm } = await mountUserMenu(t, { canAccessAllTenants: true })
  vm.menuVisible = true
  await flush()
  const nodes = renderUserMenuFrom(vm)
  const anchor = nodes.find(n => hasClass(n, 'dropdown-tenant-panel'))
  const panel = nodes.find(n => isNamed(n, 'TenantSwitchPanel'))
  assert.ok(anchor)
  assert.ok(panel)
  // 事件接线保留模板原样键：pointer-enter → onPointerEnter（未走归一化）。
  assert.equal(typeof panel!.props?.onPointerEnter, 'function', '面板 pointer-enter 接宿主 showTenantSubmenu')
  assert.equal(typeof panel!.props?.onPointerLeave, 'function', '面板 pointer-leave 接宿主 scheduleHideTenantSubmenu')

  anchor!.props!.onMouseenter!()
  assert.equal(vm.tenantSubmenuOpen, true, '锚点 hover 直接打开（超管）')

  panel!.props!.onPointerLeave!()
  t.mock.timers.tick(179)
  assert.equal(vm.tenantSubmenuOpen, true, '180ms 容忍期内不关闭（指针桥缝隙）')
  t.mock.timers.tick(1)
  assert.equal(vm.tenantSubmenuOpen, false, '离开 180ms 后关闭')

  panel!.props!.onPointerEnter!()
  panel!.props!.onPointerLeave!()
  t.mock.timers.tick(100)
  panel!.props!.onPointerEnter!()
  t.mock.timers.tick(300)
  assert.equal(vm.tenantSubmenuOpen, true, '关闭计时被重进取消，面板保持打开')
})

// ---------- T10 观察②：点击外部关闭 ----------

test('T10观察② 点击外部关闭：菜单内点击不误关，外部点击连面板一起收起（tenant-submenu-floating 命中契约）', async t => {
  const { vm } = await mountUserMenu(t, { canAccessAllTenants: true })
  vm.menuVisible = true
  vm.tenantSubmenuOpen = true
  const insideMenu = { name: 'inside-menu' }
  vm.menuRef = { contains: (node: unknown) => node === insideMenu } as any
  vm.handleClickOutside({ target: insideMenu })
  assert.equal(vm.menuVisible, true, '菜单内部点击不关闭')
  assert.equal(vm.tenantSubmenuOpen, true, '菜单内部点击不收起子菜单')

  vm.handleClickOutside({ target: { name: 'outside' } })
  assert.equal(vm.menuVisible, false, '外部点击关闭菜单')
  assert.equal(vm.tenantSubmenuOpen, false, '外部点击连同子菜单一起关闭')

  // 浮层命中路径：fixture 的 document 桩固定 querySelector→null，无法注入命中
  // 节点做行为断言；锁源码契约防止类名/选择器漂移（T10 观察②收口）。
  assert.ok(USER_MENU_SOURCE.includes("document.querySelector('.tenant-submenu-floating')"),
    '宿主按 .tenant-submenu-floating 查询浮层面板')
  assert.ok(USER_MENU_SOURCE.includes('tenantFloating.contains(target)'),
    '命中浮层（点击在面板内）时不得关闭')
  assert.ok(PANEL_SOURCE.includes('tenant-submenu-floating tenant-switch-panel'),
    'TenantSwitchPanel 根节点保留 tenant-submenu-floating 类（Teleport 命中前提）')
})

// ---------- T10 观察②：面板 clearSearch 清空按钮 ----------

test('T10观察② 面板 clearSearch：清空按钮 click/enter 清词并立即重查第 1 页（不经防抖）', async t => {
  const m = await mountPanel(t, {
    respond: p => (p.keyword === 'zzz'
      ? { success: true, data: { items: [], total: 0 } }
      : { success: true, data: { items: [{ id: 1, name: 'A' }, { id: 2, name: 'B' }], total: 2 } }),
  })
  assert.equal(m.calls.length, 1, '初始加载')
  m.vm.searchQuery = 'zzz'
  m.vm.handleSearchInput()
  t.mock.timers.tick(300)
  await flush()
  assert.equal(m.calls.length, 2, '防抖搜索命中')

  const clear = renderPanelFrom(m.vm).find(n => hasClass(n, 'tenant-switch-clear'))
  assert.ok(clear, '有搜索词时渲染清空按钮')
  assert.equal(typeof clear!.props?.onClick, 'function')

  clear!.props!.onClick!()
  assert.equal(m.vm.searchQuery, '', '清空搜索词')
  await flush()
  assert.equal(m.calls.length, 3, 'clearSearch 立即重查（不等待防抖）')
  assert.deepEqual(m.calls[2], { keyword: undefined, tenant_id: undefined, page: 1, page_size: 20 })
  assert.equal([...m.vm.tenants].length, 2, '列表恢复默认结果')
  assert.equal(
    renderPanelFrom(m.vm).some(n => hasClass(n, 'tenant-switch-clear')),
    false,
    '清空后按钮随 v-if 消失',
  )

  // enter 键盘路径（AC-31 同族：role=button tabindex=0）
  m.vm.searchQuery = 'zzz'
  const clearAgain = renderPanelFrom(m.vm).find(n => hasClass(n, 'tenant-switch-clear'))
  assert.ok(clearAgain, '再次输入后清空按钮复现')
  for (const handler of handlers(clearAgain!.props!.onKeydown)) {
    handler({ key: 'Enter', preventDefault() {} })
  }
  assert.equal(m.vm.searchQuery, '', 'enter 同样触发清空')
  await flush()
  assert.equal(m.calls.length, 4)
  assert.deepEqual(m.calls[3], { keyword: undefined, tenant_id: undefined, page: 1, page_size: 20 })
})
