import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { productLineStorageKey, resolveLine } from '../brand/productLines.ts'

/**
 * 产线 store（AC-8 / 8.1 / 8.2 / 10 / AC-1 / C7）。
 *
 * 新模型下产线 = 当前工作区绑定的 agent 派生，不再是品牌配置。
 * store 通过 `@/` 别名引用 auth / chatResources / api，`tsx --test` 默认不解析
 * tsconfig.app 的 paths，所以沿用 settingsNavigation.test.ts 的做法：
 * 抽出真实 store 定义，用假的 auth / chatResources 和真实 Vue 响应式在 vm 里跑。
 */

const storeSource = readFileSync(new URL('./productLine.ts', import.meta.url), 'utf8')
const storeAst = ts.createSourceFile('productLine.ts', storeSource, ts.ScriptTarget.Latest, true)
const storeStatement = storeAst.statements.find(
  (statement) =>
    ts.isVariableStatement(statement) &&
    statement.declarationList.declarations.some(
      (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'useProductLineStore',
    ),
)
assert.ok(storeStatement, 'productLine.ts must define useProductLineStore')
const compiledStore = ts.transpileModule(
  storeStatement.getText(storeAst).replace(/^export\s+/, ''),
  { compilerOptions: { target: ts.ScriptTarget.ES2022 } },
).outputText

interface AgentShape {
  id: string
  name: string
  description?: string
}

interface StorageHarness {
  data: Map<string, string>
  writes: Array<{ key: string; value: string }>
  restore: () => void
}

interface StorageImpl {
  getItem: (key: string) => string | null
  setItem: (key: string, value: string) => void
  removeItem?: (key: string) => void
}

function installStorage(initial: Record<string, string> = {}, impl?: StorageImpl): StorageHarness {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const data = new Map(Object.entries(initial))
  const writes: Array<{ key: string; value: string }> = []
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: impl ?? {
      getItem: (key: string) => data.get(key) ?? null,
      setItem: (key: string, value: string) => {
        writes.push({ key, value })
        data.set(key, value)
      },
      removeItem: (key: string) => {
        data.delete(key)
      },
    },
  })
  return {
    data,
    writes,
    restore: () => {
      if (original) Object.defineProperty(globalThis, 'localStorage', original)
      else Reflect.deleteProperty(globalThis, 'localStorage')
    },
  }
}

interface Deferred {
  promise: Promise<void>
  resolve: () => void
  reject: (error: unknown) => void
}

function deferred(): Deferred {
  let resolve!: () => void
  let reject!: (error: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

interface HarnessOptions {
  userId?: string
  tenantId?: number | null
  boundByTenant?: Record<string, string[]>
  agents?: AgentShape[]
  disabledOwnAgentIds?: string[]
  agentsLoaded?: boolean
  storage?: Record<string, string>
  storageImpl?: StorageImpl
  samples?: Record<string, string[]>
  samplesError?: Error
}

interface StoreShape {
  lines: { value: Array<{ id: string; agentId: string; name: string; description: string; samples: string[] }> }
  ready: { value: boolean }
  currentLineId: { value: string }
  currentLine: { value: { id: string; name: string } | null }
  selectLine: (id: string) => void
  hydrate: () => void
}

function createStore(options: HarnessOptions = {}) {
  const storage = installStorage(options.storage, options.storageImpl)
  const authState = reactive<{
    user: { id: string; preferences: { bound_agent_ids_by_tenant: Record<string, string[]> } } | null
    tenantId: number | null
  }>({
    user: options.userId === ''
      ? null
      : {
          id: options.userId ?? 'user-1',
          preferences: { bound_agent_ids_by_tenant: options.boundByTenant ?? {} },
        },
    tenantId: options.tenantId === undefined ? 1 : options.tenantId,
  })
  const fakeAuthStore = {
    get currentUserId() {
      return authState.user?.id || ''
    },
    get effectiveTenantId() {
      return authState.tenantId
    },
    get user() {
      return authState.user
    },
  }
  const resources = reactive({
    agents: [...(options.agents ?? [])] as AgentShape[],
    disabledOwnAgentIds: [...(options.disabledOwnAgentIds ?? [])] as string[],
    loaded: options.agentsLoaded ?? true,
    ensureAgentsCalls: 0,
  })
  const ensure = deferred()
  const samplesRequests: string[] = []
  const fakeChatResources = {
    get agents() {
      return resources.agents
    },
    get disabledOwnAgentIds() {
      return resources.disabledOwnAgentIds
    },
    isLoaded: () => resources.loaded,
    ensureAgents: () => {
      resources.ensureAgentsCalls += 1
      return ensure.promise
    },
  }
  const defineStore = (_id: string, setup: () => unknown) => setup
  const stops: Array<() => void> = []
  const store = runInNewContext(`${compiledStore}\nuseProductLineStore()`, {
    localStorage: (globalThis as { localStorage?: unknown }).localStorage,
    // 显式传入宿主 console，让 M10 的 catch 告警能被测试捕获（vm 自带的 console 不共享实例）。
    console,
    computed,
    ref,
    watch: (source: unknown, callback: unknown, opts?: unknown) => {
      const stop = (watch as (...args: unknown[]) => () => void)(source, callback, opts)
      stops.push(stop)
      return stop
    },
    defineStore,
    productLineStorageKey,
    resolveLine,
    useAuthStore: () => fakeAuthStore,
    useChatResourcesStore: () => fakeChatResources,
    getSuggestedQuestions: async (agentId: string) => {
      samplesRequests.push(agentId)
      if (options.samplesError) throw options.samplesError
      const questions = options.samples?.[agentId] ?? []
      return { data: { questions: questions.map((question: string) => ({ question })) } }
    },
  }) as StoreShape
  return {
    store,
    storage,
    resources,
    authState,
    samplesRequests,
    ensureAgentsCalls: () => resources.ensureAgentsCalls,
    resolveAgents: () => ensure.resolve(),
    rejectAgents: (error: Error) => ensure.reject(error),
    close: () => {
      stops.forEach((stop) => stop())
      storage.restore()
    },
  }
}

async function flush(): Promise<void> {
  await new Promise<void>((resolve) => setImmediate(resolve))
  await nextTick()
  await new Promise<void>((resolve) => setImmediate(resolve))
  await nextTick()
}

const AGENT_A: AgentShape = { id: 'agent-a', name: '法务助手', description: '合同与合规' }
const AGENT_B: AgentShape = { id: 'agent-b', name: '财务助手', description: '报表问答' }

function defaultHarness(options: HarnessOptions = {}) {
  return createStore({
    userId: 'user-1',
    tenantId: 1,
    boundByTenant: { '1': ['agent-a', 'agent-b'] },
    agents: [AGENT_A, AGENT_B],
    agentsLoaded: true,
    ...options,
  })
}

test('lines are derived from bound agents: id/agentId/name/description come from the agent (AC-8)', async () => {
  const h = defaultHarness()
  try {
    await flush()
    assert.deepEqual(
      h.store.lines.value.map((line) => ({
        id: line.id,
        agentId: line.agentId,
        name: line.name,
        description: line.description,
      })),
      [
        { id: 'agent-a', agentId: 'agent-a', name: '法务助手', description: '合同与合规' },
        { id: 'agent-b', agentId: 'agent-b', name: '财务助手', description: '报表问答' },
      ],
    )
  } finally {
    h.close()
  }
})

test('line order follows the binding order, not the agents snapshot order (AC-8)', async () => {
  const h = defaultHarness({ boundByTenant: { '1': ['agent-b', 'agent-a'] } })
  try {
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-b', 'agent-a'])
  } finally {
    h.close()
  }
})

test('disabled agents are filtered out silently (AC-8)', async () => {
  const h = defaultHarness({ disabledOwnAgentIds: ['agent-a'] })
  try {
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-b'])
  } finally {
    h.close()
  }
})

test('deleted or out-of-workspace agents (missing from snapshot) are filtered out (AC-8)', async () => {
  const h = defaultHarness({ boundByTenant: { '1': ['agent-a', 'gone-agent', '', 'agent-b'] } })
  try {
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-a', 'agent-b'])
  } finally {
    h.close()
  }
})

test('no bindings means no lines (AC-8 / AC-10)', async () => {
  const h = defaultHarness({ boundByTenant: { '1': [] } })
  try {
    await flush()
    assert.deepEqual(h.store.lines.value, [])
    assert.equal(h.store.currentLineId.value, '')
    assert.equal(h.store.ready.value, true, 'agents loaded: empty state is legitimate')
  } finally {
    h.close()
  }
})

test('first visit defaults to the first bound line (AC-8.1)', async () => {
  const h = defaultHarness()
  try {
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-a')
    assert.equal(h.store.currentLine.value?.name, '法务助手')
  } finally {
    h.close()
  }
})

test('stored preference is read from the per-user key (AC-8.1)', async () => {
  const h = defaultHarness({ storage: { [productLineStorageKey('user-1')]: 'agent-b' } })
  try {
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-b')
  } finally {
    h.close()
  }
})

test('invalid stored id falls back to the first line and repairs storage (AC-8.1)', async () => {
  const key = productLineStorageKey('user-1')
  const h = defaultHarness({ storage: { [key]: 'unbound-agent' } })
  try {
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-a')
    assert.equal(h.storage.data.get(key), 'agent-a', 'bad value should be overwritten with the fallback')
  } finally {
    h.close()
  }
})

test('agents arriving asynchronously re-resolve the current line (AC-8.1 / AC-8.2)', async () => {
  const key = productLineStorageKey('user-1')
  const h = defaultHarness({
    agents: [],
    agentsLoaded: false,
    storage: { [key]: 'agent-b' },
  })
  try {
    await flush()
    assert.equal(h.store.lines.value.length, 0)
    assert.equal(h.store.currentLineId.value, '')
    assert.equal(h.store.ready.value, false)

    h.resources.agents = [AGENT_A, AGENT_B]
    h.resources.loaded = true
    h.resolveAgents()
    await flush()

    assert.equal(h.store.ready.value, true)
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-a', 'agent-b'])
    assert.equal(h.store.currentLineId.value, 'agent-b', 'stored selection is restored after agents arrive')
  } finally {
    h.close()
  }
})

test('the store bootstraps agents itself and flips ready only after success (AC-8.2 / AC-10)', async () => {
  const h = defaultHarness({ agents: [], agentsLoaded: false })
  try {
    await flush()
    assert.equal(h.ensureAgentsCalls(), 1, 'store must call ensureAgents without the chat page')
    assert.equal(h.store.ready.value, false, 'not ready while loading: no false empty state')

    h.resources.loaded = true
    h.resolveAgents()
    await flush()
    assert.equal(h.store.ready.value, true)
  } finally {
    h.close()
  }
})

test('a failed agents fetch keeps ready=false so the empty state stays hidden (AC-10)', async () => {
  const h = defaultHarness({ agents: [], agentsLoaded: false })
  try {
    await flush()
    h.rejectAgents(new Error('agents unavailable'))
    await flush()
    assert.equal(h.store.ready.value, false)
    assert.deepEqual(h.store.lines.value, [])
  } finally {
    h.close()
  }
})

test('switching workspace reads the other tenant key without merging (AC-1 / 5.1.1)', async () => {
  const h = defaultHarness({
    tenantId: 1,
    boundByTenant: { '1': ['agent-a'], '2': ['agent-b'] },
  })
  try {
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-a'])

    h.authState.tenantId = 2
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-b'], 'workspace 2 sees only its own bindings')
    assert.equal(h.store.currentLineId.value, 'agent-b')

    h.authState.tenantId = 1
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-a'], 'switching back does not merge lists')
  } finally {
    h.close()
  }
})

test('a line removed by unbinding makes the selection fall back to the first remaining one (AC-8.1)', async () => {
  const h = defaultHarness({ storage: { [productLineStorageKey('user-1')]: 'agent-b' } })
  try {
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-b')

    h.authState.user!.preferences.bound_agent_ids_by_tenant['1'] = ['agent-a']
    await flush()
    assert.deepEqual(h.store.lines.value.map((line) => line.id), ['agent-a'])
    assert.equal(h.store.currentLineId.value, 'agent-a', 'selected line re-resolves when its binding disappears')
  } finally {
    h.close()
  }
})

test('unbinding everything clears the selection (AC-8.1 / AC-10)', async () => {
  const h = defaultHarness()
  try {
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-a')

    h.authState.user!.preferences.bound_agent_ids_by_tenant['1'] = []
    await flush()
    assert.deepEqual(h.store.lines.value, [])
    assert.equal(h.store.currentLineId.value, '')
    assert.equal(h.store.currentLine.value, null)
  } finally {
    h.close()
  }
})

test('selectLine persists under the user key and never touches other storage keys (AC-8.1)', async () => {
  const key = productLineStorageKey('user-1')
  const h = defaultHarness()
  try {
    await flush()
    h.storage.writes.length = 0
    h.store.selectLine('agent-b')
    assert.equal(h.store.currentLineId.value, 'agent-b')
    assert.deepEqual(h.storage.writes, [{ key, value: 'agent-b' }])
  } finally {
    h.close()
  }
})

test('selectLine falls back to the first line for an unknown id', async () => {
  const h = defaultHarness()
  try {
    await flush()
    h.store.selectLine('agent-b')
    h.store.selectLine('does-not-exist')
    assert.equal(h.store.currentLineId.value, 'agent-a')
  } finally {
    h.close()
  }
})

test('switching accounts rehydrates from the new user key and does not leak preferences (AC-8.1)', async () => {
  const h = defaultHarness({
    storage: {
      [productLineStorageKey('user-1')]: 'agent-b',
      [productLineStorageKey('user-2')]: 'agent-a',
    },
  })
  try {
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-b')

    h.authState.user = {
      id: 'user-2',
      preferences: { bound_agent_ids_by_tenant: { '1': ['agent-a', 'agent-b'] } },
    }
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-a', 'user-2 keeps their own preference')

    h.authState.user = {
      id: 'user-3',
      preferences: { bound_agent_ids_by_tenant: { '1': ['agent-a', 'agent-b'] } },
    }
    await flush()
    assert.equal(h.store.currentLineId.value, 'agent-a', 'a brand-new user starts on the first line')
  } finally {
    h.close()
  }
})

test('logged-out store reads nothing, bootstraps nothing and writes nothing (AC-8.1)', async () => {
  const h = createStore({ userId: '', agents: [AGENT_A], agentsLoaded: true })
  try {
    await flush()
    assert.deepEqual(h.store.lines.value, [])
    assert.equal(h.store.currentLineId.value, '')
    assert.equal(h.ensureAgentsCalls(), 0, 'no agent fetch without a logged-in user')
    h.storage.writes.length = 0
    h.store.selectLine('agent-a')
    assert.deepEqual(h.storage.writes, [], 'no shared key without a user id')
  } finally {
    h.close()
  }
})

test('setItem failures (private mode / quota) keep the in-memory selection working (M10)', async () => {
  const originalWarn = console.warn
  const warnings: string[] = []
  console.warn = (...args: unknown[]) => {
    warnings.push(args.map((arg) => String(arg)).join(' '))
  }
  const h = defaultHarness({
    storageImpl: {
      getItem: () => null,
      setItem: () => {
        throw new Error('QuotaExceededError')
      },
    },
  })
  try {
    await flush()
    assert.doesNotThrow(() => h.store.selectLine('agent-b'))
    assert.equal(h.store.currentLineId.value, 'agent-b', 'memory state still switches when persistence fails')
    assert.ok(warnings.some((line) => line.includes('保存产线偏好失败')))
  } finally {
    h.close()
    console.warn = originalWarn
  }
})

test('sample questions are lazy-loaded from the agent, failures leave an empty list (AC-8)', async () => {
  const ok = defaultHarness({ samples: { 'agent-a': ['合同怎么审？', '合规风险？'] } })
  try {
    await flush()
    assert.deepEqual(ok.samplesRequests, ['agent-a', 'agent-b'])
    assert.deepEqual(ok.store.lines.value[0]?.samples, ['合同怎么审？', '合规风险？'])
  } finally {
    ok.close()
  }

  const failing = defaultHarness({ samplesError: new Error('suggested questions unavailable') })
  try {
    await flush()
    assert.deepEqual(failing.store.lines.value[0]?.samples, [], 'a failed sample request must not block the line')
    assert.deepEqual(failing.store.lines.value[1]?.samples, [])
  } finally {
    failing.close()
  }
})

test('productLine store is UI-only: no selectAgent/toggleAgent/session or KB switching (C7)', () => {
  assert.doesNotMatch(storeSource, /selectAgent\s*\(/)
  assert.doesNotMatch(storeSource, /toggleAgent\s*\(/)
  assert.doesNotMatch(storeSource, /selectedKnowledgeBases|selectedFiles|vectorstore|vector-store/i)
  assert.doesNotMatch(storeSource, /createSession|rebuildSession|resetSession/)
})

test('productLineStorageKey is namespaced per user', () => {
  assert.equal(productLineStorageKey('abc'), 'weknora_customer_line:abc')
  assert.notEqual(productLineStorageKey('abc'), productLineStorageKey('def'))
})
