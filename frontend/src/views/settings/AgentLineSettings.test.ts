import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import type { TestContext } from 'node:test'
import { reactive } from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

/**
 * 客户设置「产线」分区（AC-1 / AC-2.1 / AC-6 / AC-8 / AC-10 / C4 / C6）。
 *
 * 组件依赖白名单见 test-utils/vueFixture.ts：新增的 @/ 依赖都要在这里补桩，
 * 否则整文件报 `Unexpected component dependency`。
 * 模板只做静态断言（fixture 把 render 置空），交互逻辑走 setup 暴露的状态。
 */

const source = readFileSync(new URL('./AgentLineSettings.vue', import.meta.url), 'utf8')

/** vm 里返回的对象/数组原型不同，比较前先转成宿主普通值。 */
function plain<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

const BUILTIN_AGENT_IDS = [
  'builtin-quick-answer',
  'builtin-smart-reasoning',
  'builtin-deep-researcher',
  'builtin-data-analyst',
  'builtin-knowledge-graph-expert',
  'builtin-document-assistant',
  'builtin-wiki-researcher',
]
const INTERNAL_AGENT_IDS = [
  'builtin-wiki-fixer',
  'builtin-skill-installer',
  'builtin-weknora-assistant',
]

function agent(id: string, name: string, description = '') {
  return { id, name, description, is_builtin: id.startsWith('builtin-'), config: {} }
}

function t(key: string, params?: Record<string, unknown>) {
  return params ? `${key}(${JSON.stringify(params)})` : key
}

interface SetupOptions {
  tenantId?: number
  boundByTenant?: Record<string, string[]>
  agents?: Array<ReturnType<typeof agent>>
  disabled?: string[]
  ensureAgentsError?: boolean
}

type Response = {
  success: boolean
  data?: { bound_agent_ids_by_tenant?: Record<string, string[]> }
  message?: string
  errorCode?: string
}

function setup(tc: TestContext, options: SetupOptions = {}) {
  const f = fixture(tc)
  const calls: Array<{ bound_agent_ids: string[] }> = []
  const toasts: string[] = []
  const auth = reactive({
    user: {
      id: 'user-1',
      preferences: { bound_agent_ids_by_tenant: options.boundByTenant ?? { '1': [] } },
    } as { id: string; preferences: { bound_agent_ids_by_tenant: Record<string, string[]> } },
    effectiveTenantId: options.tenantId ?? 1,
    setUser(next: { id: string; preferences: { bound_agent_ids_by_tenant: Record<string, string[]> } }) {
      auth.user = next
    },
  })
  const resources = reactive({
    agents: options.agents ?? [],
    disabledOwnAgentIds: options.disabled ?? [],
    ensureAgents: async () => {
      if (options.ensureAgentsError) throw new Error('agents unavailable')
    },
    isLoaded: () => true,
  })
  let respond: (patch: { bound_agent_ids: string[] }) => Response = (patch) => {
    const tenantKey = String(auth.effectiveTenantId)
    return {
      success: true,
      data: {
        bound_agent_ids_by_tenant: {
          ...auth.user.preferences.bound_agent_ids_by_tenant,
          [tenantKey]: patch.bound_agent_ids,
        },
      },
    }
  }
  const { vm, close } = f.component(new URL('./AgentLineSettings.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t }) },
    'tdesign-vue-next': { MessagePlugin: { success: (msg: string) => toasts.push(msg) } },
    '@/api/auth': {
      updateMyPreferences: async (patch: { bound_agent_ids: string[] }) => {
        calls.push(patch)
        return respond(patch)
      },
    },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/chatResources': { useChatResourcesStore: () => resources },
    '@/stores/productLine': { useProductLineStore: () => ({}) },
  })
  return {
    vm,
    auth,
    resources,
    calls,
    toasts,
    close,
    setResponse: (next: typeof respond) => {
      respond = next
    },
  }
}

test('binding an agent saves the whole list, clears the input and toasts success', async t => {
  const s = setup(t, {
    boundByTenant: { '1': ['agent-a'] },
    agents: [agent('agent-a', '法务助手'), agent('agent-b', '财务助手')],
  })
  await flush()
  s.vm.draftId = 'agent-b'
  await s.vm.bindAgent()
  await flush()

  assert.deepEqual(plain(s.calls),[{ bound_agent_ids: ['agent-a', 'agent-b'] }])
  assert.deepEqual(plain(s.vm.boundIds),['agent-a', 'agent-b'])
  assert.equal(s.vm.draftId, '', 'the input is cleared only after a successful save')
  assert.equal(s.vm.errorMessage, '')
  assert.ok(s.toasts.some((message) => message.includes('agentLineSettings.successBound')))
  s.close()
})

test('an unbound row shows the agent name from the snapshot and falls back to the id', async t => {
  const s = setup(t, {
    boundByTenant: { '1': ['agent-a', 'ghost-agent'] },
    agents: [agent('agent-a', '法务助手', '合同与合规')],
  })
  await flush()
  assert.deepEqual(
    s.vm.boundRows.map((row: { id: string; name: string }) => ({ id: row.id, name: row.name })),
    [
      { id: 'agent-a', name: '法务助手' },
      { id: 'ghost-agent', name: 'ghost-agent' },
    ],
  )
  s.close()
})

test('unbinding removes the agent from the saved list', async t => {
  const s = setup(t, { boundByTenant: { '1': ['agent-a', 'agent-b'] } })
  await flush()
  await s.vm.unbindAgent('agent-a')
  await flush()

  assert.deepEqual(plain(s.calls),[{ bound_agent_ids: ['agent-b'] }])
  assert.deepEqual(plain(s.vm.boundIds),['agent-b'])
  assert.ok(s.toasts.some((message) => message.includes('agentLineSettings.successUnbound')))
  s.close()
})

test('unbinding the last agent leaves an empty list and an empty state (AC-10)', async t => {
  const s = setup(t, { boundByTenant: { '1': ['agent-a'] } })
  await flush()
  await s.vm.unbindAgent('agent-a')
  await flush()

  assert.deepEqual(plain(s.vm.boundIds),[])
  assert.deepEqual(plain(s.vm.boundRows), [])
  // 空态由模板条件承担（fixture 不渲染模板，这里做源码断言）。
  assert.match(source, /v-if="boundRows\.length === 0"[\s\S]*?agentLineSettings\.empty/)
  s.close()
})

test('local pre-checks show the five friendly errors without calling the API', async t => {
  const s = setup(t, {
    boundByTenant: { '1': ['agent-a'] },
    agents: [agent('agent-a', 'A')],
  })
  await flush()

  void s.vm.bindAgent()
  assert.equal(s.vm.errorMessage, 'agentLineSettings.errorEmpty')

  s.vm.draftId = 'agent-a'
  await s.vm.bindAgent()
  assert.equal(s.vm.errorMessage, 'agentLineSettings.errorDuplicate')

  s.vm.draftId = 'builtin-wiki-fixer'
  await s.vm.bindAgent()
  assert.equal(s.vm.errorMessage, 'agentLineSettings.errorInternal')

  const twenty = Array.from({ length: 20 }, (_, index) => `agent-${index}`)
  s.auth.user.preferences.bound_agent_ids_by_tenant['1'] = twenty
  s.vm.draftId = 'agent-over-limit'
  await s.vm.bindAgent()
  assert.ok(s.vm.errorMessage.startsWith('agentLineSettings.errorLimit'))
  assert.match(s.vm.errorMessage, /20/)

  assert.deepEqual(plain(s.calls),[], 'local pre-checks must not hit the server')
  s.close()
})

test('server errors map to the five readable messages', async t => {
  const cases: Array<[string, string]> = [
    ['agent id cannot be empty', 'agentLineSettings.errorEmpty'],
    ['agent is internal and cannot be bound', 'agentLineSettings.errorInternal'],
    ['agent is disabled in this workspace', 'agentLineSettings.errorDisabled'],
    ['cannot bind more than 20 agents', 'agentLineSettings.errorLimit'],
    ['agent does not exist', 'agentLineSettings.errorNotFound'],
    ['agent cannot be bound to this workspace', 'agentLineSettings.errorNotFound'],
    ['unexpected backend failure', 'agentLineSettings.errorUnknown'],
  ]
  const s = setup(t, { boundByTenant: { '1': [] } })
  await flush()

  s.setResponse(() => ({ success: false, message: 'x' }))
  for (const [message, expected] of cases) {
    s.setResponse(() => ({ success: false, message }))
    const ok = await s.vm.saveBoundIds(['agent-a'])
    assert.equal(ok, false)
    assert.ok(s.vm.errorMessage.startsWith(expected), `${message} -> ${s.vm.errorMessage}`)
  }
  s.close()
})

test('server errors prefer the stable errorCode over message substrings (F15 / F5)', async t => {
  // message 是与映射无关的文案：按 code 必须映射到对应本地化 key，
  // 而不是落进 errorUnknown 兜底（措辞调整不得破坏前端映射）。
  const codes: Array<[string, string]> = [
    ['bound_agent_empty', 'agentLineSettings.errorEmpty'],
    ['bound_agent_internal', 'agentLineSettings.errorInternal'],
    ['bound_agent_disabled', 'agentLineSettings.errorDisabled'],
    ['bound_agent_limit', 'agentLineSettings.errorLimit'],
    ['bound_agent_not_found', 'agentLineSettings.errorNotFound'],
  ]
  const s = setup(t, { boundByTenant: { '1': [] } })
  await flush()

  for (const [code, expected] of codes) {
    s.setResponse(() => ({ success: false, message: 'unrelated text', errorCode: code }))
    const ok = await s.vm.saveBoundIds(['agent-a'])
    assert.equal(ok, false)
    assert.ok(s.vm.errorMessage.startsWith(expected), `${code} -> ${s.vm.errorMessage}`)
  }
  // 未知 code 仍退回 message 子串兜底（旧后端兼容路径）。
  s.setResponse(() => ({ success: false, message: 'unexpected backend failure', errorCode: 'bound_agent_weird' }))
  await s.vm.saveBoundIds(['agent-a'])
  assert.ok(s.vm.errorMessage.startsWith('agentLineSettings.errorUnknown'))
  s.close()
})

test('a failed save keeps the typed id so the user can retry (AC-6)', async t => {
  const s = setup(t, { boundByTenant: { '1': ['agent-a'] } })
  await flush()
  s.setResponse(() => ({ success: false, message: 'agent does not exist' }))
  s.vm.draftId = 'agent-typo'
  await s.vm.bindAgent()
  await flush()

  assert.equal(s.vm.draftId, 'agent-typo', 'failed save must not clear the input')
  assert.deepEqual(plain(s.vm.boundIds),['agent-a'], 'failed save must not change the list')
  assert.ok(s.vm.errorMessage.includes('agentLineSettings.errorNotFound'))
  s.close()
})

test('candidate list keeps the seven builtin agents and drops the three internal ones (AC-2.1 / C4)', async t => {
  const s = setup(t, {
    boundByTenant: { '1': [] },
    agents: [
      ...BUILTIN_AGENT_IDS.map((id) => agent(id, id)),
      ...INTERNAL_AGENT_IDS.map((id) => agent(id, id)),
    ],
  })
  await flush()

  const values = s.vm.candidateOptions.map((option: { value: string }) => option.value)
  assert.deepEqual(values, BUILTIN_AGENT_IDS)
  for (const internal of INTERNAL_AGENT_IDS) {
    assert.equal(values.includes(internal), false, `${internal} must not be offered`)
  }
  s.close()
})

test('candidates exclude bound and disabled agents as well', async t => {
  const s = setup(t, {
    boundByTenant: { '1': ['builtin-quick-answer', 'agent-bound'] },
    agents: [
      ...BUILTIN_AGENT_IDS.map((id) => agent(id, id)),
      ...INTERNAL_AGENT_IDS.map((id) => agent(id, id)),
      agent('agent-bound', '已绑定'),
      agent('agent-custom', '自定义'),
      agent('agent-disabled', '已停用'),
    ],
    disabled: ['builtin-smart-reasoning', 'agent-disabled'],
  })
  await flush()

  const values = s.vm.candidateOptions.map((option: { value: string }) => option.value)
  assert.deepEqual(values, [
    'builtin-deep-researcher',
    'builtin-data-analyst',
    'builtin-knowledge-graph-expert',
    'builtin-document-assistant',
    'builtin-wiki-researcher',
    'agent-custom',
  ])
  s.close()
})

test('the bound list follows the active workspace, not the previous one (AC-1)', async t => {
  const s = setup(t, {
    tenantId: 1,
    boundByTenant: { '1': ['agent-a'], '2': ['agent-b'] },
    agents: [agent('agent-a', 'A'), agent('agent-b', 'B')],
  })
  await flush()
  assert.deepEqual(plain(s.vm.boundIds),['agent-a'])

  s.auth.effectiveTenantId = 2
  await flush()
  assert.deepEqual(plain(s.vm.boundIds),['agent-b'], 'workspace 2 must not see workspace 1 bindings')

  s.auth.effectiveTenantId = 1
  await flush()
  assert.deepEqual(plain(s.vm.boundIds),['agent-a'])
  s.close()
})

test('binding in another workspace writes only that workspace key (AC-1)', async t => {
  const s = setup(t, {
    tenantId: 2,
    boundByTenant: { '1': ['agent-a'], '2': [] },
    agents: [agent('agent-a', 'A'), agent('agent-b', 'B')],
  })
  await flush()
  s.vm.draftId = 'agent-b'
  await s.vm.bindAgent()
  await flush()

  assert.deepEqual(plain(s.auth.user.preferences.bound_agent_ids_by_tenant), {
    '1': ['agent-a'],
    '2': ['agent-b'],
  })
  s.close()
})

test('a failed agents fetch is surfaced as a status instead of silently hiding candidates', async t => {
  const s = setup(t, { boundByTenant: { '1': [] }, ensureAgentsError: true })
  await flush()
  await flush()
  assert.equal(s.vm.loadFailed, true)
  assert.match(source, /v-else-if="loadFailed"[\s\S]*?agentLineSettings\.loadFailed/)
  s.close()
})

test('the component has no admin entry points and is only wired into the customer settings view (C6)', () => {
  assert.doesNotMatch(source, /知识库|管理智能体|共享给我/)
  const settings = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')
  assert.match(settings, /currentSection === 'agentlines' && isCustomerViewerActive/)
})
