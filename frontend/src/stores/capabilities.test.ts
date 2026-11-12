import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import * as vue from 'vue'
import * as pinia from 'pinia'
import { fixture, flush } from '../test-utils/vueFixture.ts'

/**
 * T28：锁定 capabilities store 的租户隔离行为（F12）。
 * 只测 store 本身；request 与 auth store 用 mock，不访问真实后端。
 */

interface PendingCall {
  path: string
  resolve: (value: unknown) => void
  reject: (reason?: unknown) => void
}

interface CapabilitiesPayload {
  role: string
  is_system_admin: boolean
  capabilities: string[]
}

function setup(t: TestContext) {
  const f = fixture(t)
  const auth = { effectiveTenantId: 1 as number | null }
  const calls: PendingCall[] = []
  const get = (path: string) =>
    new Promise((resolve, reject) => {
      calls.push({ path, resolve, reject })
    })
  const { useCapabilitiesStore } = f.evaluate(
    readFileSync(new URL('./capabilities.ts', import.meta.url), 'utf8'),
    {
      vue,
      pinia,
      '@/utils/request': { get },
      '@/stores/auth': { useAuthStore: () => auth },
    },
  )
  const root = pinia.createPinia()
  pinia.setActivePinia(root)
  t.after(() => pinia.disposePinia(root))
  const store = useCapabilitiesStore()
  t.after(() => store.$dispose())
  return { store, auth, calls }
}

function ok(payload: CapabilitiesPayload) {
  return { success: true, data: payload }
}

test('refresh applies the current tenant capabilities', async t => {
  const { store, calls } = setup(t)
  const pending = store.refresh()
  assert.equal(calls.length, 1)
  calls[0].resolve(ok({ role: 'owner', is_system_admin: false, capabilities: ['user.view', 'user.ban'] }))
  await pending
  assert.equal(store.loaded, true)
  assert.equal(store.role, 'owner')
  assert.equal(store.isSystemAdmin, false)
  assert.equal(store.can('user.view'), true)
  assert.equal(store.can('user.ban'), true)
  assert.equal(store.can('user.purge'), false)
})

test('switching tenant clears old capabilities before the new response arrives', async t => {
  const { store, auth, calls } = setup(t)
  const first = store.refresh()
  calls[0].resolve(ok({ role: 'owner', is_system_admin: false, capabilities: ['user.view', 'user.ban'] }))
  await first
  assert.equal(store.can('user.view'), true)

  auth.effectiveTenantId = 2
  const second = store.refresh()
  // 新响应回来之前必须 fail-closed，不能沿用空间 1 的能力。
  assert.equal(store.can('user.view'), false)
  assert.equal(store.can('user.ban'), false)
  assert.equal(store.role, '')
  assert.equal(store.isSystemAdmin, false)
  assert.equal(store.loaded, false)
  assert.equal(store.loading, true)

  calls[1].resolve(ok({ role: 'viewer', is_system_admin: false, capabilities: [] }))
  await second
  assert.equal(store.loaded, true)
  assert.equal(store.role, 'viewer')
  assert.equal(store.can('user.view'), false)
})

test('late response from the previous tenant is discarded', async t => {
  const { store, auth, calls } = setup(t)
  const first = store.refresh()
  auth.effectiveTenantId = 2
  const second = store.refresh()
  assert.equal(calls.length, 2)

  // 空间 1 的响应迟到，不能写回空间 2。
  calls[0].resolve(ok({ role: 'owner', is_system_admin: false, capabilities: ['user.view', 'user.purge'] }))
  await flush()
  assert.equal(store.can('user.view'), false)
  assert.equal(store.can('user.purge'), false)
  assert.equal(store.role, '')
  assert.equal(store.loaded, false)

  calls[1].resolve(ok({ role: 'contributor', is_system_admin: false, capabilities: ['user.view'] }))
  await second
  await first
  assert.equal(store.can('user.view'), true)
  assert.equal(store.role, 'contributor')
  assert.equal(store.loaded, true)
})

test('reset clears tenant key and capabilities; next refresh applies again', async t => {
  const { store, auth, calls } = setup(t)
  const first = store.refresh()
  calls[0].resolve(ok({ role: 'owner', is_system_admin: true, capabilities: ['user.view', 'owner.provision'] }))
  await first
  assert.equal(store.can('owner.provision'), true)
  assert.equal(store.isSystemAdmin, true)

  store.reset()
  assert.equal(store.can('user.view'), false)
  assert.equal(store.role, '')
  assert.equal(store.isSystemAdmin, false)
  assert.equal(store.loaded, false)
  assert.equal(store.loading, false)

  // reset 后再登录同一空间：tenantKey 已清空，refresh 必须重新拉取并应用。
  const again = store.refresh()
  assert.equal(calls.length, 2)
  calls[1].resolve(ok({ role: 'viewer', is_system_admin: false, capabilities: ['user.view'] }))
  await again
  assert.equal(store.loaded, true)
  assert.equal(store.role, 'viewer')
  assert.equal(store.can('user.view'), true)
  assert.equal(store.can('owner.provision'), false)
})

test('repeated refresh within the same tenant reuses the in-flight request', async t => {
  const { store, calls } = setup(t)
  const first = store.refresh()
  const second = store.refresh()
  assert.equal(calls.length, 1, 'same tenant must not issue a second request')
  calls[0].resolve(ok({ role: 'owner', is_system_admin: false, capabilities: ['user.view'] }))
  await Promise.all([first, second])
  assert.equal(store.loaded, true)
  assert.equal(store.can('user.view'), true)

  // 已经加载完成后再次 refresh 才允许发新请求。
  const third = store.refresh()
  assert.equal(calls.length, 2)
  calls[1].resolve(ok({ role: 'owner', is_system_admin: false, capabilities: ['user.view'] }))
  await third
})

test('failed refresh keeps the previous capabilities (interface gate stays usable)', async t => {
  const { store, calls } = setup(t)
  const first = store.refresh()
  calls[0].resolve(ok({ role: 'owner', is_system_admin: false, capabilities: ['user.view', 'user.ban'] }))
  await first

  const second = store.refresh()
  calls[1].reject(new Error('network down'))
  await second
  assert.equal(store.can('user.view'), true)
  assert.equal(store.can('user.ban'), true)
  assert.equal(store.loaded, true)
  assert.equal(store.loading, false)
})
