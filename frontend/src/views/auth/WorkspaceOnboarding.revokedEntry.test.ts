/**
 * WorkspaceOnboarding 组件级测试（revoked-member-rejoin T01，TDD 红阶段先行）。
 *
 * 覆盖三件事：
 *  1. 撤销态（accessRevoked）渲染「查看邀请」入口并能打开 MyInvitationsDialog，
 *     同时保留「已被移除」提示卡；
 *  2. 撤销态不渲染任何创建空间入口（CreateTenantDialog 不挂载）；
 *  3. 正常用户视图零变化；接受邀请（hasValidTenant 变 true）经既有 watch
 *     跳转 /platform/knowledge-bases。
 *
 * 为什么这样断言：仓库组件测试基建 vueFixture 把 render 桩成 null，只驱动
 * script setup 的真实逻辑；模板门控（v-if / v-else）因此用 @vue/compiler-dom
 * 把组件真实模板编译成 render 函数，拿组件真实暴露的状态执行，对 vnode 树
 * 断言。这样能同时抓住「状态改了但模板仍把入口藏死」这类只在模板层出错的
 * 回归（正是本次 bug 的形态）。
 *
 * 无渲染实例时 resolveComponent 解析不到组件会回退为字符串名，正好当查找
 * 标记用（t-button / MyInvitationsDialog / CreateTenantDialog）；伴随的
 * [Vue warn] 属预期噪音，渲染时静音。
 *
 * 命名说明：Windows 工作区文件系统大小写不敏感，不能与既有的
 * workspaceOnboarding.test.ts（判定函数单测）同名共存，参照
 * useChatStreamHandler.pageButton.test.ts 的点后缀先例命名。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

const SFC = new URL('./WorkspaceOnboarding.vue', import.meta.url)
const INVITATIONS_KEY = 'auth.workspaceOnboarding.invitations'
const CREATE_KEY = 'auth.workspaceOnboarding.create'
const ENTER_ROUTE = '/platform/knowledge-bases'

type VNode = {
  type: unknown
  props?: Record<string, any> | null
  children: unknown
}

// 编译组件真实模板为 render 函数（与 Vite 构建同一套编译器）。
const { descriptor } = parseSfc(readFileSync(SFC, 'utf8'), { filename: 'WorkspaceOnboarding.vue' })
assert.ok(descriptor.template, 'WorkspaceOnboarding.vue 应包含模板')
const { code } = compile(descriptor.template.content, {
  mode: 'function',
  prefixIdentifiers: true,
  filename: 'WorkspaceOnboarding.vue',
})
const renderTemplate = new Function('Vue', code)(vue) as (ctx: any, cache: unknown[]) => VNode

/** 挂载真实 script setup（authStore / router / 登出接口打桩），返回组件状态。 */
async function setup(t: TestContext, auth: { canCreateTenant: boolean; accessRevoked: boolean }) {
  const f = fixture(t)
  const store = vue.reactive({
    canCreateTenant: auth.canCreateTenant,
    accessRevoked: auth.accessRevoked,
    hasValidTenant: false,
    pendingInvitationCount: 1,
    refreshFromAuthMe: async () => true,
    fetchPendingInvitationCount: async () => {},
  })
  const replaces: string[] = []
  const { vm } = f.component(SFC, {
    'vue-router': { useRouter: () => ({ replace: async (path: string) => { replaces.push(path) } }) },
    '@/stores/auth': { useAuthStore: () => store },
    '@/api/auth': { logout: async () => {} },
  })
  await flush() // 等 onMounted 里的 loadPolicy 走完
  return { vm, store, replaces }
}

/** 用组件当前状态执行模板 render，摊平成 vnode 列表（含插槽内容）。 */
function renderFrom(vm: any): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (k: string) => k
      return target[key]
    },
    set(target, key: string, value) {
      target[key] = value
      return true
    },
  })
  const originalWarn = console.warn
  console.warn = () => {}
  try {
    return flatten(renderTemplate(ctx, []))
  } finally {
    console.warn = originalWarn
  }
}

function flatten(vnode: unknown, out: VNode[] = []): VNode[] {
  if (vnode == null || typeof vnode === 'boolean') return out
  if (Array.isArray(vnode)) {
    for (const item of vnode) flatten(item, out)
    return out
  }
  const node = vnode as VNode
  out.push(node)
  let children: unknown = node.children
  if (typeof children === 'function') children = children()
  if (Array.isArray(children)) {
    flatten(children, out)
  } else if (children && typeof children === 'object') {
    for (const slot of Object.values(children as Record<string, unknown>)) {
      if (typeof slot === 'function') flatten(slot(), out)
    }
  }
  return out
}

function textOf(nodes: VNode[]): string {
  return nodes
    .filter(node => node.type === vue.Text && typeof node.children === 'string')
    .map(node => node.children)
    .join(' ')
}

/** 按按钮文案里的 i18n key 找 t-button（$t 打桩为恒等，文案即 key）。 */
function findButton(nodes: VNode[], key: string): VNode | undefined {
  return nodes.find(
    node => node.type === 't-button' && textOf(flatten([node])).includes(key),
  )
}

function findComponent(nodes: VNode[], name: string): VNode | undefined {
  return nodes.find(node => node.type === name)
}

test('revoked state keeps the notice and opens the invitation entry', async t => {
  const { vm } = await setup(t, { canCreateTenant: false, accessRevoked: true })
  const nodes = renderFrom(vm)
  // 「已被移除」提示卡保留（AC-1 前半）
  assert.ok(
    nodes.some(node => node.props?.role === 'alert'),
    '撤销态应保留 role=alert 的「已被移除」提示卡',
  )
  // 「查看邀请」入口可见（AC-1 后半；红阶段预期失败）
  const entry = findButton(nodes, INVITATIONS_KEY)
  assert.ok(entry, '撤销态应渲染「查看邀请」按钮')
  // 入口可打开收件箱弹窗
  entry.props!.onClick()
  assert.equal(vm.invitationsVisible, true, '点击「查看邀请」应把 invitationsVisible 置真')
  const reopened = renderFrom(vm)
  const dialog = findComponent(reopened, 'MyInvitationsDialog')
  assert.ok(dialog, '撤销态应挂载 MyInvitationsDialog')
  assert.equal(dialog!.props!.visible, true, 'MyInvitationsDialog 应随入口打开')
  // 状态层契约：撤销用户放行邀请、锁死创建（红阶段预期失败）
  assert.deepEqual(vm.surfaces, { showCreate: false, showInvitations: true, showAccessRevoked: true })
})

test('revoked state keeps every create surface hidden', async t => {
  const { vm } = await setup(t, { canCreateTenant: true, accessRevoked: true })
  const nodes = renderFrom(vm)
  assert.ok(!findButton(nodes, CREATE_KEY), '撤销态不得渲染「创建空间」按钮')
  assert.ok(!findComponent(nodes, 'CreateTenantDialog'), '撤销态不得挂载 CreateTenantDialog')
})

test('normal users keep the create and invitation surfaces unchanged', async t => {
  const { vm } = await setup(t, { canCreateTenant: true, accessRevoked: false })
  const nodes = renderFrom(vm)
  assert.ok(findButton(nodes, CREATE_KEY), '正常可建户用户应保留「创建空间」按钮')
  assert.ok(findButton(nodes, INVITATIONS_KEY), '正常用户应保留「查看邀请」按钮')
  assert.ok(findComponent(nodes, 'CreateTenantDialog'), '正常用户应挂载 CreateTenantDialog')
  assert.ok(findComponent(nodes, 'MyInvitationsDialog'), '正常用户应挂载 MyInvitationsDialog')
  assert.deepEqual(vm.surfaces, { showCreate: true, showInvitations: true, showAccessRevoked: false })
})

test('accepting an invitation routes into the workspace via the existing watch', async t => {
  const { store, replaces } = await setup(t, { canCreateTenant: false, accessRevoked: true })
  store.hasValidTenant = true
  await flush()
  assert.deepEqual(replaces, [ENTER_ROUTE], 'hasValidTenant 变真后应跳转知识库页')
})
