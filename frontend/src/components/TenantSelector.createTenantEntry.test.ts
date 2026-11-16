/**
 * TenantSelector 的建空间入口门控（T11 / AC-44）。
 *
 * 产品口径：非系统管理员的 canCreateTenant 必须为 false，前端据此不渲染
 * 「新建空间」入口。这里锁定模板层门控，防止以后有人把 v-if 去掉、
 * 变成「前端可点、后端 403」（C4：隐藏不等于权限，但入口也必须有门）。
 *
 * 复用仓库既有的组件测试基建（vueFixture）：把 SFC 真实模板编译成
 * render 函数，挂载真实 script setup（authStore / 接口 / i18n 打桩），
 * 再用组件当前状态执行模板，对 vnode 树断言。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse as parseSfc } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../test-utils/vueFixture.ts'

const SFC = new URL('./TenantSelector.vue', import.meta.url)
const CREATE_ACTION_CLASS = 'tenant-create-action'
const CREATE_LABEL_CLASS = 'tenant-create-label'

type VNode = {
  type: unknown
  props?: Record<string, any> | null
  children: unknown
}

const { descriptor } = parseSfc(readFileSync(SFC, 'utf8'), { filename: 'TenantSelector.vue' })
assert.ok(descriptor.template, 'TenantSelector.vue 应包含模板')
const { code } = compile(descriptor.template.content, {
  mode: 'function',
  prefixIdentifiers: true,
  filename: 'TenantSelector.vue',
})
const renderTemplate = new Function('Vue', code)(vue) as (ctx: any, cache: unknown[]) => VNode

function makeStore(canCreateTenant: boolean) {
  return vue.reactive({
    canCreateTenant,
    selectedTenantId: 1,
    selectedTenantName: 'A',
    tenant: { id: 1, name: 'A' },
    user: { tenant_id: 1 },
    memberships: [],
    setAllTenants() {},
    setSelectedTenant() {},
    refreshFromAuthMe: async () => true,
  })
}

/** 挂载真实 script setup，打桩 authStore 与所有外部依赖。 */
async function setup(t: TestContext, canCreateTenant: boolean) {
  const f = fixture(t)
  const store = makeStore(canCreateTenant)
  const { vm } = f.component(SFC, {
    '@/stores/auth': { useAuthStore: () => store },
    '@/api/tenant': {
      searchTenants: async () => ({
        success: true,
        data: { items: [{ id: 1, name: 'A' }], total: 1 },
      }),
    },
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: { info() {}, error() {}, success() {} } },
    '@/utils/tenantSwitch': {
      navigateAfterTenantSwitch: () => {},
      persistLastActiveTenantPreference: async () => {},
      stashTenantSwitchToast: () => {},
    },
    '@/composables/useRoleLabel': { useRoleLabel: () => ({ formatRole: (value: string) => value }) },
  })
  await flush()
  // 打开下拉，让建空间入口所在的子树参与渲染。
  vm.showDropdown = true
  await flush()
  return { vm, store }
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

function renderFrom(vm: any): VNode[] {
  const ctx = new Proxy(vm, {
    get(target, key: string) {
      if (key === '$t') return (key: string) => key
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

function hasClass(node: VNode, className: string): boolean {
  const value = node.props?.class
  const matches = (raw: unknown): boolean =>
    typeof raw === 'string' && raw.split(/\s+/).includes(className)
  if (Array.isArray(value)) return value.some(matches)
  if (value && typeof value === 'object') return (value as Record<string, boolean>)[className] === true
  return matches(value)
}

function hasCreateEntry(nodes: VNode[]): boolean {
  return nodes.some(
    node => hasClass(node, CREATE_ACTION_CLASS) || hasClass(node, CREATE_LABEL_CLASS),
  )
}

test('canCreateTenant=false 时不渲染「新建空间」入口', async t => {
  const { vm } = await setup(t, false)
  const nodes = renderFrom(vm)
  assert.equal(
    hasCreateEntry(nodes),
    false,
    '非系统管理员（canCreateTenant=false）不得看到建空间入口',
  )
})

test('canCreateTenant=true 时渲染「新建空间」入口（对照）', async t => {
  const { vm } = await setup(t, true)
  const nodes = renderFrom(vm)
  assert.equal(
    hasCreateEntry(nodes),
    true,
    '系统管理员（canCreateTenant=true）应能看到建空间入口',
  )
})
