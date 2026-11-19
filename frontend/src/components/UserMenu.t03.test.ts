/**
 * T03 测试先行红阶段：头像菜单删除「GitHub」「帮助与文档」（AC-1/AC-2）。
 *
 * 目标态（实现未动，本轮跑必红）：
 * - owner / 空间 admin / 系统管理员：两项 + 外层分隔线整块不再渲染，
 *   openGithub / openDocs / docsUrl 引用随删除清理；
 * - 客户（viewer）：现状已隐藏（宿主 v-if="!isCustomer"），本用例做无回归守卫（绿）。
 *
 * 断言在渲染 vnode 层（真实 script setup + 模板编译，沿 UserMenu.t11 先例），
 * 另配源码级零残留断言。这只是界面不渲染（UI gating only），
 * 真权限仍以后端为准（C4）；两项为纯外链，无后端拒绝点（调研 O1/O2）。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../test-utils/vueFixture.ts'

type VNode = { type: unknown; props?: Record<string, any> | null; children: unknown }

const USER_MENU = new URL('./UserMenu.vue', import.meta.url)
const USER_MENU_SOURCE = readFileSync(USER_MENU, 'utf8')

function compileTemplate(file: URL, source: string) {
  const { descriptor } = parseSfc(source, { filename: file.pathname })
  assert.ok(descriptor.template, `${file.pathname} 应包含模板`)
  const { code } = compile(descriptor.template.content, {
    mode: 'function', prefixIdentifiers: true, filename: file.pathname,
  })
  // 未注册组件解析成带名字的占位组件，保留 t-icon 等节点的 props（t11 同款）。
  const resolvingVue = { ...vue, resolveComponent: (name: string) => ({ name }) }
  return new Function('Vue', code)(resolvingVue) as (ctx: any, cache: unknown[]) => VNode
}

const renderUserMenu = compileTemplate(USER_MENU, USER_MENU_SOURCE)

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

function renderMenuNodes(vm: any): VNode[] {
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

const collectTexts = (nodes: VNode[]) =>
  nodes.map(n => n.children).filter(c => typeof c === 'string') as string[]

// ---------- 身份构造（research「三种身份的可复用判定」：三字段纯传参） ----------

interface Identity {
  label: string
  role: 'owner' | 'admin' | 'viewer'
  isSystemAdmin?: boolean
  canAccessAllTenants?: boolean
  isCustomer?: boolean
}

const IDENTITIES: Identity[] = [
  { label: 'owner', role: 'owner' },
  { label: 'admin', role: 'admin' },
  { label: '系统管理员', role: 'admin', isSystemAdmin: true, canAccessAllTenants: true },
  { label: '客户（viewer）', role: 'viewer', isCustomer: true },
]

async function mountUserMenu(t: TestContext, identity: Identity, reused?: ReturnType<typeof fixture>) {
  // 同一测试内多次挂载须复用 fixture：t.mock.timers.enable 二次调用会抛
  // 'MockTimers is already enabled'（t11 同款备注）。
  const f = reused ?? fixture(t)
  const store = {
    isLiteMode: false,
    canAccessAllTenants: identity.canAccessAllTenants ?? false,
    isSystemAdmin: identity.isSystemAdmin ?? false,
    memberships: [{ tenant_id: 1, tenant_name: 'A', role: identity.role }],
    selectedTenantName: 'A',
    tenant: { id: 1, name: 'A' },
    user: { username: 'u', email: 'u@example.com', avatar: '' },
    effectiveTenantId: 1,
    currentTenantRole: identity.role,
    hasRole: (min: string) => {
      const levels: Record<string, number> = { viewer: 10, contributor: 20, admin: 30, owner: 40 }
      return (levels[identity.role] ?? 0) >= (levels[min] ?? 0)
    },
    ensureAuthMe: async () => true,
    logout() {},
  }
  const uiStore = { sidebarCollapsed: false, openSettings() {} }
  const router = { pushes: [] as any[], push(to: any) { router.pushes.push(to) } }
  const { vm } = await f.component(USER_MENU, {
    'vue-router': { useRouter: () => router },
    '@/stores/ui': { useUIStore: () => uiStore },
    '@/stores/auth': { useAuthStore: () => store },
    '@/stores/capabilities': { useCapabilitiesStore: () => ({ can: () => false }) },
    'tdesign-vue-next': { MessagePlugin: { info() {}, error() {}, success() {} } },
    '@/api/auth': { logout: async () => ({ success: true }) },
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    '@/composables/useRoleLabel': {
      useRoleLabel: () => ({ formatRole: (value: string) => `role:${value}`, roleIcon: () => 'role-icon' }),
    },
    '@/config/contextualGuides': { openNewUserGuide() {} },
    '@/config/settingsAccess': { SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE: { models: 'admin' } },
    // 按身份驱动 isCustomer（真实判定=非超管/非跨空间且角色非 admin/owner）。
    '@/config/customerAccess': { isCustomerViewer: () => identity.isCustomer === true },
    // docsUrl 桩：红阶段组件还在 import；②删除后此桩闲置无害。
    '@/utils/docsUrl': { docsUrl: (path: string) => `docs:${path}` },
  })
  await flush()
  return vm as any
}

/** 打开菜单后收集全部渲染节点（$t 桩返回键名，文案断言用键名匹配）。 */
async function renderedMenu(t: TestContext, identity: Identity, reused?: ReturnType<typeof fixture>) {
  const vm = await mountUserMenu(t, identity, reused)
  vm.menuVisible = true
  await flush()
  return renderMenuNodes(vm)
}

// ---------- 矩阵：身份 × 两项入口不渲染 ----------

for (const identity of IDENTITIES) {
  const expectRed = identity.isCustomer !== true
  test(`T03 ${identity.label}：头像菜单不渲染「帮助与文档」与「GitHub」（AC-1/AC-2，${expectRed ? '预期红' : '现状守卫'}）`, async t => {
    const nodes = await renderedMenu(t, identity)
    const texts = collectTexts(nodes)
    assert.equal(
      texts.includes('general.helpAndDocs'), false,
      `「帮助与文档」不得渲染（$t 键 general.helpAndDocs），实际渲染文本：${texts.join('|')}`,
    )
    assert.equal(texts.includes('common.github'), false, '「GitHub」菜单项不得渲染（$t 键 common.github）')
    assert.equal(
      nodes.some(n => n.props?.name === 'logo-github'), false,
      'logo-github 图标节点不得出现',
    )
    assert.equal(
      nodes.some(n => n.props?.title === 'common.githubStarTip'), false,
      'GitHub 项的 title 提示（common.githubStarTip）不得出现',
    )
  })
}

// ---------- 源码零残留（整块删除，含分隔线与处理函数） ----------

test('T03 UserMenu.vue 源码零残留：openGithub/openDocs/docsUrl/两项文案键全清（AC-1/AC-2）', () => {
  for (const residue of ['openGithub', 'openDocs', 'docsUrl', 'general.helpAndDocs', 'common.github', 'logo-github']) {
    assert.equal(
      USER_MENU_SOURCE.includes(residue), false,
      `UserMenu.vue 仍含 ${residue}（应随整块删除清理）`,
    )
  }
})

// ---------- 守卫：相邻功能不受删除波及（现状绿，实现后仍须绿） ----------

test('T03 守卫：系统管理入口仍仅超管渲染、登出保留（C6 清单外不动）', async t => {
  // 同一测试内两次挂载复用一个 fixture（见 mountUserMenu 备注）。
  const f = fixture(t)
  // 超管：系统管理入口照常渲染（v-if="authStore.isSystemAdmin" 先例保留）。
  const sysNodes = await renderedMenu(t, IDENTITIES[2]!, f)
  assert.ok(
    sysNodes.some(n => n.type !== null && typeof n.type === 'object'),
    '超管菜单正常渲染',
  )
  const sysTexts = collectTexts(sysNodes)
  assert.ok(sysTexts.includes('settings.navGroups.systemAdministration'), '超管保留系统管理入口')
  assert.ok(sysTexts.includes('auth.logout'), '登出入口保留')

  // owner：系统管理入口不渲染（既有 gating 无回归），个人设置保留。
  const ownerNodes = await renderedMenu(t, IDENTITIES[0]!, f)
  const ownerTexts = collectTexts(ownerNodes)
  assert.equal(ownerTexts.includes('settings.navGroups.systemAdministration'), false, 'owner 不见系统管理入口（既有行为）')
  assert.ok(ownerTexts.includes('general.allSettings'), 'owner 保留「全部设置」入口')
})
