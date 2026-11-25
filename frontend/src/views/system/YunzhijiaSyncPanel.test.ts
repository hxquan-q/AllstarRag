/**
 * T10：云之家同步面板（YunzhijiaSyncPanel.vue）渲染与交互。
 *
 * 覆盖票面验收：
 * - 状态卡（enabled / 最近运行 / 触发 / 状态 / 耗时 / 计数，updated 文案「已核对」）；
 * - 异常清单、blocked 清单；不渲染手机号 / ticket / token；
 * - enabled=false 提示；加载态、空态、错误态；
 * - 预检（只读结果、loading、失败提示）；
 * - 手动同步（120s 由 API 用例钉住；409 / 403 / 网络 / 500 / 超时提示；
 *   成功、失败、超时、409 后都刷新状态卡，不乐观显示成功；请求中禁用）；
 * - 解除 blocked（二次确认、成功刷新、失败提示）。
 *
 * t() 打桩返回 key（带参数时附参数串），断言「哪个文案键 / 哪个值」被渲染；
 * 真实中文文案与 6 locale 键集由 i18n 用例与 check-i18n 保证。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'

import { flush } from '../../test-utils/vueFixture.ts'
import { findByTag, mountRendered, settle, textOf, type T46Node } from '../user-management/t46SfcMount.ts'

const PANEL = new URL('./YunzhijiaSyncPanel.vue', import.meta.url)
const NS = 'system.globalSettings.yunzhijiaSync'

const usedContexts = new WeakSet<object>()

function makeI18n() {
  return {
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}|${JSON.stringify(params)}` : key,
      // enumLabel 只在键存在时翻译；unknown_* 用来验证回退原始值。
      te: (key: string) => !/\.(mystery|weird_kind)$/.test(key),
      locale: { value: 'zh-CN' },
    }),
  }
}

function blockedRow(overrides: Record<string, unknown> = {}) {
  return {
    open_id: 'oid-blocked-1',
    name: '王五',
    department: '人力资源部',
    blocked_at: '2026-11-20T02:00:00Z',
    blocked_by: 'u-admin-1',
    ...overrides,
  }
}

function statusPayload(overrides: Record<string, unknown> = {}) {
  return {
    enabled: true,
    latest: {
      id: 7,
      trigger: 'manual',
      status: 'success',
      started_at: '2026-11-25T03:00:00Z',
      finished_at: '2026-11-25T03:00:12Z',
      duration_ms: 12_345,
      active_count: 111,
      inactive_count: 22,
      created_count: 3,
      updated_count: 44,
      disabled_count: 5,
      migrated_count: 6,
      anomaly_count: 2,
      anomalies: [
        { kind: 'duplicate_name', name: '张三', detail: '同名 2 人' },
        { kind: 'weird_kind', name: '李四', detail: '未知类型' },
      ],
      error_summary: '上游超时一次',
    },
    blocked: [blockedRow()],
    ...overrides,
  }
}

function previewPayload() {
  return {
    active_count: 111,
    inactive_count: 22,
    tenant_default_count: 77,
    tenant_hr_count: 33,
    anomalies: [{ kind: 'duplicate_open_id', name: '赵六', detail: 'open_id 重复' }],
  }
}

type Responder = () => Promise<unknown>

interface PanelOptions {
  statuses?: Responder[]
  preview?: Responder
  sync?: Responder
  unblock?: Responder
}

function mountPanel(t: TestContext, options: PanelOptions = {}) {
  // 同一 test 内多次挂载会重复 enable mock timers；两次之间 reset。
  if (usedContexts.has(t)) t.mock.timers.reset()
  usedContexts.add(t)

  const calls = { status: 0, preview: 0, sync: 0, unblock: [] as string[] }
  const messages = { success: [] as string[], error: [] as string[] }
  const statuses = options.statuses ?? [async () => statusPayload()]

  const { root } = mountRendered(t, {
    component: PANEL,
    dependencies: {
      'vue-i18n': makeI18n(),
      'tdesign-vue-next': {
        MessagePlugin: {
          success: (message: string) => messages.success.push(message),
          error: (message: string) => messages.error.push(message),
        },
      },
      '@/api/system/yunzhijia': {
        getYunzhijiaSyncStatus: () => {
          const next = statuses[Math.min(calls.status, statuses.length - 1)]!
          calls.status += 1
          return next()
        },
        previewYunzhijiaSync: () => {
          calls.preview += 1
          return (options.preview ?? (async () => previewPayload()))()
        },
        runYunzhijiaSync: () => {
          calls.sync += 1
          return (options.sync ?? (async () => ({ id: 8, status: 'success' })))()
        },
        unblockYunzhijiaEmployee: (openId: string) => {
          calls.unblock.push(openId)
          return (options.unblock ?? (async () => ({ success: true })))()
        },
      },
    },
  })

  return { root, calls, messages }
}

function alerts(root: T46Node): string[] {
  return findByTag(root, 't-alert').map((node) => String(node.props.message ?? ''))
}

function buttons(root: T46Node): T46Node[] {
  return findByTag(root, 't-button')
}

/** header 三个按钮的固定顺序：预检、立即同步、刷新。 */
const PREVIEW_BUTTON = 0
const SYNC_BUTTON = 1
const REFRESH_BUTTON = 2

async function click(node: T46Node | undefined): Promise<void> {
  assert.ok(node, 'button must exist')
  const handler = node!.props.onClick as (() => unknown) | undefined
  assert.equal(typeof handler, 'function', 'button has a click handler')
  await handler!()
}

function deferred<T = unknown>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

// ---------------------------------------------------------------------------
// 状态卡 / 清单 / 空态 / enabled
// ---------------------------------------------------------------------------

test('renders the enabled status card, counts, anomalies and blocked list', async (t) => {
  const { root, calls } = mountPanel(t)
  await settle()
  const text = textOf(root)

  assert.equal(calls.status, 1, '挂载即拉取状态')

  // 标题 / 说明 / enabled 标签
  assert.ok(text.includes(`${NS}.title`))
  assert.ok(text.includes(`${NS}.description`))
  assert.ok(text.includes(`${NS}.enabled`))
  assert.ok(!text.includes(`${NS}.disabled`))

  // 最近运行：时间 / 触发 / 状态 / 耗时
  assert.ok(text.includes(`${NS}.latestTitle`))
  for (const field of ['startedAt', 'trigger', 'status', 'duration']) {
    assert.ok(text.includes(`${NS}.fields.${field}`), `missing field ${field}`)
  }
  assert.ok(text.includes(`${NS}.triggers.manual`))
  assert.ok(text.includes(`${NS}.statuses.success`))
  assert.ok(text.includes(`${NS}.durationValue|{"seconds":"12.3"}`), '耗时按秒保留一位小数')
  assert.ok(text.includes(`${NS}.errorSummary`))
  assert.ok(text.includes('上游超时一次'))

  // 计数：标签 + 值
  for (const field of [
    'activeCount', 'inactiveCount', 'createdCount', 'updatedCount',
    'disabledCount', 'migratedCount', 'anomalyCount',
  ]) {
    assert.ok(text.includes(`${NS}.fields.${field}`), `missing count label ${field}`)
  }
  for (const value of ['111', '22', '3', '44', '5', '6']) {
    assert.ok(text.includes(value), `missing count value ${value}`)
  }

  // 异常清单：已知 kind 翻译、未知 kind 回退原值、姓名与说明原样展示
  assert.ok(text.includes(`${NS}.anomaliesTitle`))
  assert.ok(text.includes(`${NS}.anomalyKinds.duplicate_name`))
  assert.ok(text.includes('weird_kind'), '未知异常类型回退原始 kind')
  assert.ok(text.includes('张三') && text.includes('同名 2 人'))

  // blocked 清单：列头 + 行数据（open_id 也用于解除操作）
  assert.ok(text.includes(`${NS}.blockedTitle`))
  for (const column of ['openId', 'name', 'department', 'blockedAt', 'blockedBy', 'actions']) {
    assert.ok(text.includes(`${NS}.blockedColumns.${column}`), `missing blocked column ${column}`)
  }
  assert.ok(text.includes('oid-blocked-1'))
  assert.ok(text.includes('王五'))
  assert.ok(text.includes('人力资源部'))
  assert.ok(text.includes('u-admin-1'))
})

test('the updated count label is rendered as 已核对, never 更新', async (t) => {
  const { root } = mountPanel(t)
  await settle()
  const text = textOf(root)

  assert.ok(text.includes(`${NS}.fields.updatedCount`), '计数卡使用 updatedCount 标签')
  assert.ok(!text.includes('更新'), '面板不得出现「更新」文案')
})

test('never renders mobile numbers, tickets or tokens even if the payload carries them', async (t) => {
  const leaky = statusPayload()
  Object.assign(leaky.latest!, {
    mobile: '13800000000',
    ticket: 'T-SECRET-TICKET-1',
    token: 'tok-secret-1',
    anomalies: [{ kind: 'duplicate_name', name: '张三', detail: '同名 2 人', mobile: '13800000001' }],
  })
  leaky.blocked = [blockedRow({ mobile: '13800000002', ticket: 'T-SECRET-TICKET-2', token: 'tok-secret-2' })]

  const { root } = mountPanel(t, { statuses: [async () => leaky] })
  await settle()
  const text = textOf(root)

  for (const secret of ['13800000000', '13800000001', '13800000002', 'T-SECRET-TICKET-1', 'T-SECRET-TICKET-2', 'tok-secret-1', 'tok-secret-2']) {
    assert.ok(!text.includes(secret), `must not render ${secret}`)
  }
  assert.ok(!/ticket/i.test(text), '界面不出现 ticket 字样')
})

test('enabled=false shows the 「登录未启用」 hint but keeps preview and manual sync usable', async (t) => {
  const { root } = mountPanel(t, { statuses: [async () => statusPayload({ enabled: false })] })
  await settle()
  const text = textOf(root)

  assert.ok(text.includes(`${NS}.disabled`))
  assert.ok(!text.includes(`${NS}.enabled`))
  assert.ok(alerts(root).includes(`${NS}.disabledHint`), 'enabled=false 必须提示登录未启用')

  const headerButtons = buttons(root)
  assert.equal(headerButtons[PREVIEW_BUTTON]!.props.disabled, false)
  assert.equal(headerButtons[SYNC_BUTTON]!.props.disabled, false)
})

test('shows empty states when there is no run and no blocked employee', async (t) => {
  const { root } = mountPanel(t, {
    statuses: [async () => ({ enabled: true, blocked: [] })],
  })
  await settle()
  const text = textOf(root)

  assert.ok(text.includes(`${NS}.noRuns`))
  assert.ok(text.includes(`${NS}.anomaliesEmpty`))
  assert.ok(text.includes(`${NS}.blockedEmpty`))
  assert.equal(findByTag(root, 'tbody').length, 0, '空态不渲染异常/blocked 表格')
})

test('shows the loading state before the status request resolves', async (t) => {
  const status = deferred()
  const { root } = mountPanel(t, { statuses: [() => status.promise] })
  await flush()

  assert.ok(textOf(root).includes(`${NS}.loading`))
  assert.equal(buttons(root)[REFRESH_BUTTON]!.props.loading, true)

  status.resolve(statusPayload())
  await settle()
  assert.ok(!textOf(root).includes(`${NS}.loading`))
})

// ---------------------------------------------------------------------------
// 错误提示：403 / 网络 / 未知
// ---------------------------------------------------------------------------

function statusError(status: number, message = '') {
  return () => Promise.reject(Object.assign(new Error(message), { status }))
}

test('a 403 on load shows the admin-permission hint', async (t) => {
  const { root } = mountPanel(t, { statuses: [statusError(403)] })
  await settle()

  assert.ok(alerts(root).includes(`${NS}.messages.forbidden`))
})

test('a network/unknown load error falls back to the localized loadFailed message', async (t) => {
  for (const responder of [
    () => Promise.reject(new Error('network down')),
    () => Promise.reject(Object.assign(new Error('boom'), { status: 500 })),
  ]) {
    const { root } = mountPanel(t, { statuses: [responder] })
    await settle()
    assert.ok(alerts(root).includes(`${NS}.loadFailed`))
  }
})

// ---------------------------------------------------------------------------
// 预检
// ---------------------------------------------------------------------------

test('preview posts once, renders the read-only counts and its anomaly list', async (t) => {
  const { root, calls } = mountPanel(t)
  await settle()

  await click(buttons(root)[PREVIEW_BUTTON])
  await settle()

  assert.equal(calls.preview, 1)
  const text = textOf(root)
  assert.ok(text.includes(`${NS}.previewTitle`))
  for (const key of ['previewActive', 'previewInactive', 'previewTenantDefault', 'previewTenantHR']) {
    assert.ok(text.includes(`${NS}.${key}`), `missing preview field ${key}`)
  }
  for (const value of ['77', '33']) assert.ok(text.includes(value))
  assert.ok(text.includes(`${NS}.previewAnomalies`))
  assert.ok(text.includes(`${NS}.anomalyKinds.duplicate_open_id`))
  assert.ok(text.includes('赵六'))
})

test('preview with no anomalies shows the empty hint instead of a table', async (t) => {
  const { root } = mountPanel(t, {
    preview: async () => ({ ...previewPayload(), anomalies: [] }),
  })
  await settle()

  await click(buttons(root)[PREVIEW_BUTTON])
  await settle()
  const text = textOf(root)

  assert.ok(text.includes(`${NS}.previewTitle`))
  assert.ok(text.includes(`${NS}.anomaliesEmpty`))
  assert.equal(findByTag(root, 'tbody').length, 2, '仅最近运行异常表与 blocked 表存在，预检异常为空不建表')
})

test('preview disables the action buttons while running and re-enables afterwards', async (t) => {
  const pending = deferred()
  const { root } = mountPanel(t, { preview: () => pending.promise })
  await settle()

  const previewClick = click(buttons(root)[PREVIEW_BUTTON])
  await flush()
  let header = buttons(root)
  assert.equal(header[PREVIEW_BUTTON]!.props.loading, true)
  assert.equal(header[SYNC_BUTTON]!.props.disabled, true, '预检中禁用立即同步')
  assert.equal(header[REFRESH_BUTTON]!.props.disabled, true, '预检中禁用刷新')

  pending.resolve(previewPayload())
  await previewClick
  await settle()
  header = buttons(root)
  assert.equal(header[PREVIEW_BUTTON]!.props.loading, false)
  assert.equal(header[SYNC_BUTTON]!.props.disabled, false)
})

test('a 403 on preview shows the admin-permission hint; other errors show previewFailed', async (t) => {
  for (const [responder, expected] of [
    [statusError(403), `${NS}.messages.forbidden`],
    [statusError(500), `${NS}.messages.previewFailed`],
    [() => Promise.reject(new Error('offline')), `${NS}.messages.previewFailed`],
  ] as const) {
    const { root } = mountPanel(t, { preview: responder })
    await settle()
    await click(buttons(root)[PREVIEW_BUTTON])
    await settle()
    assert.ok(alerts(root).includes(expected), `expected ${expected}`)
    assert.ok(!textOf(root).includes(`${NS}.previewTitle`), '失败时不展示预检结果')
  }
})

// ---------------------------------------------------------------------------
// 手动同步
// ---------------------------------------------------------------------------

test('manual sync shows a success toast and refreshes the status without optimistic updates', async (t) => {
  const refreshed = statusPayload({
    latest: {
      ...(statusPayload().latest as object),
      created_count: 999,
    },
  })
  const pending = deferred()
  const { root, calls, messages } = mountPanel(t, {
    statuses: [async () => statusPayload(), async () => refreshed],
    sync: () => pending.promise,
  })
  await settle()

  const syncClick = click(buttons(root)[SYNC_BUTTON])
  await flush()
  assert.equal(buttons(root)[SYNC_BUTTON]!.props.loading, true)
  assert.equal(buttons(root)[PREVIEW_BUTTON]!.props.disabled, true, '同步中禁用预检')
  assert.equal(calls.status, 1, '同步未完成前不刷新状态')

  pending.resolve({ id: 8, status: 'success' })
  await syncClick
  await settle()

  assert.deepEqual(messages.success, [`${NS}.messages.syncSuccess`])
  assert.equal(calls.sync, 1)
  assert.equal(calls.status, 2, '成功后重新拉取状态')
  assert.ok(textOf(root).includes('999'), '界面展示刷新后的计数，而非乐观写死')
  assert.equal(buttons(root)[SYNC_BUTTON]!.props.loading, false)
})

/** 构造一份把最近运行状态改成指定值的状态响应（用于验证 finally 刷新后的卡片）。 */
function statusWithRunStatus(runStatus: 'running' | 'failed'): Record<string, unknown> {
  return statusPayload({
    latest: { ...(statusPayload().latest as object), status: runStatus },
  })
}

test('a 409 during manual sync shows the running hint and still refreshes the status card', async (t) => {
  const { root, calls, messages } = mountPanel(t, {
    statuses: [async () => statusPayload(), async () => statusWithRunStatus('running')],
    sync: statusError(409),
  })
  await settle()

  await click(buttons(root)[SYNC_BUTTON])
  await settle()

  assert.deepEqual(messages.success, [], '409 不提示成功')
  assert.ok(alerts(root).includes(`${NS}.messages.running`), '409 运行中提示仍在')
  assert.equal(calls.status, 2, '409 后 finally 仍刷新一次状态')
  const text = textOf(root)
  assert.ok(text.includes(`${NS}.statuses.running`), '状态卡显示后端返回的运行中')
  assert.ok(!text.includes(`${NS}.statuses.success`), '不保留上一轮的成功态')
  assert.equal(buttons(root)[SYNC_BUTTON]!.props.loading, false)
})

/**
 * 手动同步失败（500 / 网络 / 120s 超时）：finally 都刷新一次状态卡，
 * 卡片显示后端最新状态（failed），且不乐观提示成功。
 */
async function assertSyncFailureRefreshes(
  t: TestContext,
  responder: Responder,
  expectedAlert: string,
): Promise<void> {
  const { root, calls, messages } = mountPanel(t, {
    statuses: [async () => statusPayload(), async () => statusWithRunStatus('failed')],
    sync: responder,
  })
  await settle()

  await click(buttons(root)[SYNC_BUTTON])
  await settle()

  assert.deepEqual(messages.success, [], '失败不提示成功')
  assert.ok(alerts(root).includes(expectedAlert), `expected ${expectedAlert}`)
  assert.equal(calls.status, 2, '失败后 finally 仍刷新一次状态')
  const text = textOf(root)
  assert.ok(text.includes(`${NS}.statuses.failed`), '状态卡显示失败的运行状态')
  assert.ok(!text.includes(`${NS}.statuses.success`), '不显示上一轮的成功态')
  assert.equal(buttons(root)[SYNC_BUTTON]!.props.loading, false)
}

test('a 500 during manual sync refreshes the status card and shows the failure', async (t) => {
  await assertSyncFailureRefreshes(t, statusError(500), `${NS}.messages.syncFailed`)
})

test('a network error during manual sync refreshes the status card and shows the failure', async (t) => {
  await assertSyncFailureRefreshes(t, () => Promise.reject(new Error('offline')), `${NS}.messages.syncFailed`)
})

test('a 120s timeout during manual sync refreshes the status card and shows the failure', async (t) => {
  const timeout = Object.assign(new Error('timeout of 120000ms exceeded'), { code: 'ECONNABORTED' })
  await assertSyncFailureRefreshes(t, () => Promise.reject(timeout), `${NS}.messages.syncFailed`)
})

test('manual sync maps 403 and network/unknown failures to localized messages', async (t) => {
  for (const [responder, expected] of [
    [statusError(403), `${NS}.messages.forbidden`],
    [statusError(500), `${NS}.messages.syncFailed`],
    [() => Promise.reject(new Error('offline')), `${NS}.messages.syncFailed`],
  ] as const) {
    const { root, messages } = mountPanel(t, { sync: responder })
    await settle()
    await click(buttons(root)[SYNC_BUTTON])
    await settle()
    assert.deepEqual(messages.success, [])
    assert.ok(alerts(root).includes(expected), `expected ${expected}`)
  }
})

// ---------------------------------------------------------------------------
// 解除 blocked
// ---------------------------------------------------------------------------

test('unblock requires the popconfirm step and only calls the API on confirm', async (t) => {
  const { root, calls, messages } = mountPanel(t, {
    statuses: [async () => statusPayload(), async () => statusPayload({ blocked: [] })],
  })
  await settle()

  const popconfirms = findByTag(root, 't-popconfirm')
  assert.equal(popconfirms.length, 1, '每个 blocked 行一个二次确认')

  const pop = popconfirms[0]!
  assert.ok(String(pop.props.content).includes('王五'), '确认文案带姓名')
  assert.ok(String(pop.props.content).includes(`${NS}.unblockConfirm`))

  // 行内按钮本身没有点击处理器：必须先经过 popconfirm。
  const rowButton = buttons(root)[3]
  assert.ok(rowButton, 'blocked 行内有解除按钮')
  assert.equal(rowButton!.props.onClick, undefined)
  assert.deepEqual(calls.unblock, [])

  await (pop.props.onConfirm as () => Promise<void>)()
  await settle()

  assert.deepEqual(calls.unblock, ['oid-blocked-1'])
  assert.deepEqual(messages.success, [`${NS}.messages.unblockSuccess`])
  assert.equal(calls.status, 2, '解除成功后刷新状态')
})

test('unblock failure reports 403 or a localized fallback and does not refresh', async (t) => {
  for (const [responder, expected] of [
    [statusError(403), `${NS}.messages.forbidden`],
    [statusError(500), `${NS}.messages.unblockFailed`],
    [() => Promise.reject(new Error('offline')), `${NS}.messages.unblockFailed`],
  ] as const) {
    const { root, calls, messages } = mountPanel(t, { unblock: responder })
    await settle()

    const pop = findByTag(root, 't-popconfirm')[0]!
    await (pop.props.onConfirm as () => Promise<void>)()
    await settle()

    assert.deepEqual(messages.success, [])
    assert.deepEqual(messages.error, [expected])
    assert.equal(calls.status, 1, '失败不刷新状态')
  }
})
