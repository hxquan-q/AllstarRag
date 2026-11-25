/**
 * T10 / H2：云之家同步面板（YunzhijiaSyncPanel.vue）渲染与交互。
 *
 * H2 按「操作记录/邀请管理」的 TDesign 规范重做视觉后，覆盖：
 * - 顶部状态 tag + 最近一次摘要（时间 · 触发 · 状态 · 耗时）与失败摘要；
 * - 预检 / 立即同步 / 刷新三个工具栏按钮；
 * - 一排统计项；异常 / 禁止重建用下划线 Tab 切换，两张表都用 t-table，
 *   空态用 t-empty；
 * - 预检结果放进 t-dialog（单独的 YunzhijiaPreviewDialog 组件）；
 * - enabled=false 的 info 提示；加载态、错误态（内联 alert + 重试）；
 * - 反馈收敛：预检 / 同步 / 解除失败走 MessagePlugin.error；
 * - 手动同步（120s 由 API 用例钉住；409 / 403 / 网络 / 500 / 超时提示；
 *   成功、失败、超时、409 后都刷新状态，不乐观显示成功；请求中禁用）；
 * - 解除 blocked（popconfirm 二次确认、成功刷新、失败提示）；
 * - 操作人 user_id → email/username 回填，未命中回退原始 id，空值显示 —。
 *
 * t() 打桩返回 key（带参数时附参数串），断言「哪个文案键 / 哪个值」被渲染；
 * 真实中文文案与 6 locale 键集由 i18n 用例与 check-i18n 保证。
 *
 * t-table 是全局注册组件，测试里用 TABLE_STUB 执行各列的 scoped slot，
 * 这样单元格内容（含操作人映射）才能进入渲染树被断言。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { h } from 'vue'

import { flush } from '../../test-utils/vueFixture.ts'
import { findByTag, mountRendered, settle, textOf, type T46Node } from '../user-management/t46SfcMount.ts'

const PANEL = new URL('./YunzhijiaSyncPanel.vue', import.meta.url)
const PREVIEW_DIALOG = new URL('./YunzhijiaPreviewDialog.vue', import.meta.url)
const MODE_TABS = new URL('./YunzhijiaModeTabs.vue', import.meta.url)
const NS = 'system.globalSettings.yunzhijiaSync'

const usedContexts = new WeakSet<object>()

/** 执行 t-table 各列 scoped slot 的替身，把单元格真实渲染进结构树。 */
const TABLE_STUB = {
  name: 'TableStub',
  props: {
    data: { type: Array, default: () => [] },
    pagination: { type: [Object, Boolean], default: undefined },
  },
  render(this: any) {
    const slots = this.$slots as Record<string, (props: { row: unknown; index: number }) => unknown>
    const cellSlots = Object.keys(slots).filter(
      (name) => name !== 'default' && typeof slots[name] === 'function',
    )
    const rows = (this.data as unknown[]).map((row, index) =>
      h(
        'tr-stub',
        cellSlots.map((name) =>
          h('td-stub', { slotName: name }, slots[name]!({ row, index }) as any),
        ),
      ),
    )
    return h('table-stub', { pagination: this.pagination }, rows)
  },
}

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
  /** listUsers 返回的成员列表；操作人映射用。 */
  users?: Array<Record<string, unknown>>
  listUsersError?: Error
  tenantId?: number | null
}

function mountPanel(t: TestContext, options: PanelOptions = {}) {
  // 同一 test 内多次挂载会重复 enable mock timers；两次之间 reset。
  if (usedContexts.has(t)) t.mock.timers.reset()
  usedContexts.add(t)

  const calls = {
    status: 0,
    preview: 0,
    sync: 0,
    unblock: [] as string[],
    listUsers: [] as Array<{ tenantId: number; params: unknown }>,
  }
  const messages = { success: [] as string[], error: [] as string[] }
  const statuses = options.statuses ?? [async () => statusPayload()]

  const { root } = mountRendered(t, {
    component: PANEL,
    componentFiles: {
      './YunzhijiaPreviewDialog.vue': PREVIEW_DIALOG,
      './YunzhijiaModeTabs.vue': MODE_TABS,
    },
    components: { 't-table': TABLE_STUB },
    props: options.tenantId === undefined ? {} : { tenantId: options.tenantId },
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
      '@/api/user-management': {
        listUsers: async (tenantId: number, params: unknown) => {
          calls.listUsers.push({ tenantId, params })
          if (options.listUsersError) throw options.listUsersError
          return { success: true, data: { items: options.users ?? [], total: options.users?.length ?? 0 } }
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

/** 空态 t-empty 的 description（t-empty 未注册，文案在 props 上）。 */
function emptyDescriptions(root: T46Node): string[] {
  return findByTag(root, 't-empty').map((node) => String(node.props.description ?? ''))
}

/** 取某一列所有单元格的文本（TABLE_STUB 渲染的 td-stub）。 */
function columnTexts(root: T46Node, slotName: string): string[] {
  return findByTag(root, 'td-stub')
    .filter((node) => node.props.slotName === slotName)
    .map((node) => textOf(node))
}

/** 工具条右侧按钮行。 */
function toolbarActions(root: T46Node): T46Node | undefined {
  return findByTag(root, 'div').find((node) =>
    String(node.props.class ?? '').includes('yzj-sync__actions'),
  )
}

/** 预检提示行（工具条下方的独立一行）。 */
function previewHintNode(root: T46Node): T46Node | undefined {
  return findByTag(root, 'p').find((node) => textOf(node).includes(`${NS}.previewHint`))
}

/** 两张表的 pagination 配置（每个表一个）。 */
function tablePagination(root: T46Node): Array<Record<string, unknown>> {
  return findByTag(root, 'table-stub').map(
    (node) => (node.props.pagination ?? {}) as Record<string, unknown>,
  )
}

/** 切换异常 / 禁止重建分段（下划线 Tab）。 */
async function setTableMode(root: T46Node, mode: 'anomalies' | 'blocked'): Promise<void> {
  const testId = mode === 'anomalies' ? 'yzj-sync-mode-anomalies' : 'yzj-sync-mode-blocked'
  const label = mode === 'anomalies' ? `${NS}.anomaliesTitle` : `${NS}.blockedTitle`
  const tab = findByTag(root, 'button').find(
    (node) =>
      node.props['data-testid'] === testId
      || node.props.dataTestid === testId
      || textOf(node).includes(label),
  )
  assert.ok(tab, '异常/禁止重建 Tab 必须存在')
  const handler = tab!.props.onClick as (() => unknown) | undefined
  assert.equal(typeof handler, 'function', 'Tab 必须有 click 处理器')
  await handler!()
  await settle(2)
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

function statusError(status: number, message = '') {
  return () => Promise.reject(Object.assign(new Error(message), { status }))
}

// ---------------------------------------------------------------------------
// 状态 tag / 最近摘要 / 统计 / 两张表
// ---------------------------------------------------------------------------

test('renders the status tag, latest summary, stats and the anomalies table by default', async (t) => {
  const { root, calls } = mountPanel(t)
  await settle()
  const text = textOf(root)

  assert.equal(calls.status, 1, '挂载即拉取状态')

  // 说明压成一行 muted 小字；不再有 h3 大标题。
  assert.ok(text.includes(`${NS}.description`))
  assert.equal(findByTag(root, 'h3').length, 0, 'H2 不再渲染 h3 大标题')

  // 状态 tag：enabled 用 success，disabled 不出现。
  const tags = findByTag(root, 't-tag')
  assert.ok(tags.some((tag) => textOf(tag).includes(`${NS}.enabled`)))
  assert.ok(!text.includes(`${NS}.disabled`))

  // 最近一次：时间 · 触发 · 状态 · 耗时；失败摘要单列。
  assert.ok(text.includes(`${NS}.latestTitle`))
  assert.ok(text.includes(`${NS}.triggers.manual`))
  assert.ok(text.includes(`${NS}.statuses.success`))
  assert.ok(text.includes(`${NS}.durationValue|{"seconds":"12.3"}`), '耗时按秒保留一位小数')
  assert.ok(text.includes(`${NS}.errorSummary`))
  assert.ok(text.includes('上游超时一次'))

  // 统计项：标签 + 值，0 不是这里的分支（值都非 0）。
  for (const field of [
    'activeCount', 'inactiveCount', 'createdCount', 'updatedCount',
    'disabledCount', 'migratedCount', 'anomalyCount',
  ]) {
    assert.ok(text.includes(`${NS}.fields.${field}`), `missing count label ${field}`)
  }
  for (const value of ['111', '22', '3', '44', '5', '6']) {
    assert.ok(text.includes(value), `missing count value ${value}`)
  }

  // 默认展示异常表；分段标签带数量。
  assert.ok(text.includes(`${NS}.anomaliesTitle`))
  assert.ok(text.includes(`${NS}.blockedTitle`))
  assert.equal(findByTag(root, 'table-stub').length, 1, '同一时间只展示一张表')
  assert.equal(tablePagination(root)[0]!.defaultPageSize, 10, '异常表前端分页 10 条')
  assert.equal(tablePagination(root)[0]!.defaultCurrent, 1, '异常表分页从第 1 页开始')
  assert.equal(tablePagination(root)[0]!.total, 2, '异常表分页 total 必须与行数一致')
  assert.ok(text.includes(`${NS}.anomalyKinds.duplicate_name`))
  assert.ok(text.includes('weird_kind'), '未知异常类型回退原始 kind')
  assert.ok(text.includes('张三') && text.includes('同名 2 人'))
})

test('the mode tabs switch between the anomalies and blocked tables', async (t) => {
  const { root } = mountPanel(t)
  await settle()

  // 异常表默认选中。
  assert.deepEqual(columnTexts(root, 'kind').length, 2)

  await setTableMode(root, 'blocked')
  const text = textOf(root)
  assert.ok(text.includes('oid-blocked-1'))
  assert.ok(text.includes('王五'))
  assert.ok(text.includes('人力资源部'))
  assert.deepEqual(columnTexts(root, 'blocked_by'), ['u-admin-1'])
  // 姓名列下挂 muted open_id。
  const nameCell = columnTexts(root, 'name').find((cell) => cell.includes('王五'))
  assert.ok(nameCell && nameCell.includes('oid-blocked-1'))
  assert.equal(columnTexts(root, 'kind').length, 0, '切到禁止重建后不再渲染异常表')
  assert.equal(findByTag(root, 'table-stub').length, 1)
  assert.equal(tablePagination(root)[0]!.defaultPageSize, 10, '禁止重建表前端分页 10 条')
  assert.equal(tablePagination(root)[0]!.defaultCurrent, 1, '禁止重建表分页从第 1 页开始')
  assert.equal(tablePagination(root)[0]!.total, 1, '禁止重建表分页 total 必须与行数一致')

  await setTableMode(root, 'anomalies')
  assert.equal(columnTexts(root, 'kind').length, 2)
})

test('the updated count label is rendered as 已核对, never 更新', async (t) => {
  const { root } = mountPanel(t)
  await settle()
  const text = textOf(root)

  assert.ok(text.includes(`${NS}.fields.updatedCount`), '计数项使用 updatedCount 标签')
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
  let text = textOf(root)
  for (const secret of ['13800000000', '13800000001', '13800000002', 'T-SECRET-TICKET-1', 'T-SECRET-TICKET-2', 'tok-secret-1', 'tok-secret-2']) {
    assert.ok(!text.includes(secret), `must not render ${secret}`)
  }

  await setTableMode(root, 'blocked')
  text = textOf(root)
  for (const secret of ['13800000002', 'T-SECRET-TICKET-2', 'tok-secret-2']) {
    assert.ok(!text.includes(secret), `must not render ${secret} in blocked table`)
  }
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
  assert.equal(headerButtons[PREVIEW_BUTTON]!.props.loading, false, '预检按钮在空闲态不转圈')
  assert.equal(headerButtons[REFRESH_BUTTON]!.props.variant, 'outline', '刷新按钮为 outline 带文字')
})

test('shows t-empty states for no run, no anomalies and no blocked employee', async (t) => {
  const { root } = mountPanel(t, {
    statuses: [async () => ({ enabled: true, blocked: [] })],
  })
  await settle()
  const text = textOf(root)

  assert.ok(text.includes(`${NS}.noRuns`))
  assert.equal(findByTag(root, 'table-stub').length, 0, '无最近运行时不建表')
  assert.deepEqual(emptyDescriptions(root), [`${NS}.anomaliesEmpty`])
  // 7 个统计项在没有最近运行时都显示占位 —。
  assert.equal(findByTag(root, 'dd').filter((node) => textOf(node) === '—').length, 7)

  await setTableMode(root, 'blocked')
  assert.deepEqual(emptyDescriptions(root), [`${NS}.blockedEmpty`])
  assert.equal(findByTag(root, 'table-stub').length, 0)
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

test('a 403 on load shows the admin-permission hint inline', async (t) => {
  const { root } = mountPanel(t, { statuses: [statusError(403)] })
  await settle()

  assert.ok(alerts(root).includes(`${NS}.messages.forbidden`))
  assert.ok(findByTag(root, 't-alert').length >= 1, '加载失败走内联 alert')
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
// 预检（t-dialog）
// ---------------------------------------------------------------------------

test('preview posts once and opens a t-dialog with read-only counts and its anomaly list', async (t) => {
  const { root, calls } = mountPanel(t)
  await settle()

  const dialogBefore = findByTag(root, 't-dialog')[0]
  assert.ok(dialogBefore, '预检结果用 t-dialog 承载')
  assert.equal(dialogBefore!.props.visible, false)

  await click(buttons(root)[PREVIEW_BUTTON])
  await settle()

  assert.equal(calls.preview, 1)
  const dialog = findByTag(root, 't-dialog')[0]!
  assert.equal(dialog.props.visible, true)
  assert.equal(dialog.props.header, `${NS}.previewTitle`)
  const text = textOf(dialog)
  for (const key of ['previewActive', 'previewInactive', 'previewTenantDefault', 'previewTenantHR']) {
    assert.ok(text.includes(`${NS}.${key}`), `missing preview field ${key}`)
  }
  for (const value of ['77', '33']) assert.ok(text.includes(value))
  assert.ok(text.includes(`${NS}.previewAnomalies`))
  assert.ok(text.includes(`${NS}.anomalyKinds.duplicate_open_id`))
  assert.ok(text.includes('赵六'))
  assert.equal(findByTag(dialog, 'td-stub').filter((node) => node.props.slotName === 'kind').length, 1)
  assert.equal(emptyDescriptions(dialog).length, 0)
})

test('preview with no anomalies shows t-empty inside the dialog', async (t) => {
  const { root } = mountPanel(t, {
    preview: async () => ({ ...previewPayload(), anomalies: [] }),
  })
  await settle()

  await click(buttons(root)[PREVIEW_BUTTON])
  await settle()

  const dialog = findByTag(root, 't-dialog')[0]!
  assert.equal(dialog.props.visible, true)
  assert.deepEqual(emptyDescriptions(dialog), [`${NS}.anomaliesEmpty`])
  assert.equal(findByTag(dialog, 'td-stub').length, 0)
  // 面板本身仍显示最近一次运行的异常表，不受预检影响。
  assert.equal(findByTag(root, 'table-stub').length, 1)
})

test('preview shows the hint and disables the action buttons while running, then re-enables', async (t) => {
  const pending = deferred()
  const { root } = mountPanel(t, { preview: () => pending.promise })
  await settle()

  const previewClick = click(buttons(root)[PREVIEW_BUTTON])
  await flush()
  let header = buttons(root)
  const hint = previewHintNode(root)
  assert.ok(hint, '预检进行中显示 1–2 分钟提示')
  // H2 收尾：提示移到工具条下方单独一行，不再挤在按钮行里。
  const actions = toolbarActions(root)
  assert.ok(actions, '按钮行存在')
  assert.ok(!textOf(actions!).includes(`${NS}.previewHint`), '提示不得挤进按钮行')
  const panelRoot = findByTag(root, 'div').find((node) =>
    String(node.props.class ?? '') === 'yzj-sync',
  )
  assert.ok(panelRoot, '面板根节点存在')
  assert.equal(hint!.parent, panelRoot, '提示必须是面板根节点的直接子元素')
  const toolbar = panelRoot!.children.find(
    (node) => node.tag === 'div' && String(node.props.class ?? '').includes('yzj-sync__toolbar'),
  )
  assert.ok(toolbar, '工具条存在')
  assert.ok(
    panelRoot!.children.indexOf(hint!) > panelRoot!.children.indexOf(toolbar!),
    '提示行必须排在工具条之后',
  )
  assert.equal(header[PREVIEW_BUTTON]!.props.loading, true)
  assert.equal(header[SYNC_BUTTON]!.props.disabled, true, '预检中禁用立即同步')
  assert.equal(header[REFRESH_BUTTON]!.props.disabled, true, '预检中禁用刷新')

  pending.resolve(previewPayload())
  await previewClick
  await settle()
  header = buttons(root)
  assert.equal(header[PREVIEW_BUTTON]!.props.loading, false)
  assert.equal(header[SYNC_BUTTON]!.props.disabled, false)
  assert.ok(!textOf(root).includes(`${NS}.previewHint`))
})

test('preview failures go through MessagePlugin.error and never open the dialog', async (t) => {
  for (const [responder, expected] of [
    [statusError(403), `${NS}.messages.forbidden`],
    [statusError(500), `${NS}.messages.previewFailed`],
    [() => Promise.reject(new Error('offline')), `${NS}.messages.previewFailed`],
  ] as const) {
    const { root, messages } = mountPanel(t, { preview: responder })
    await settle()
    await click(buttons(root)[PREVIEW_BUTTON])
    await settle()
    assert.deepEqual(messages.error, [expected])
    assert.equal(findByTag(root, 't-dialog')[0]!.props.visible, false, '失败时不展示预检结果')
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

/** 构造一份把最近运行状态改成指定值的状态响应（用于验证 finally 刷新后的摘要）。 */
function statusWithRunStatus(runStatus: 'running' | 'failed'): Record<string, unknown> {
  return statusPayload({
    latest: { ...(statusPayload().latest as object), status: runStatus },
  })
}

test('a 409 during manual sync shows the running hint and still refreshes the status', async (t) => {
  const { root, calls, messages } = mountPanel(t, {
    statuses: [async () => statusPayload(), async () => statusWithRunStatus('running')],
    sync: statusError(409),
  })
  await settle()

  await click(buttons(root)[SYNC_BUTTON])
  await settle()

  assert.deepEqual(messages.success, [], '409 不提示成功')
  assert.deepEqual(messages.error, [`${NS}.messages.running`], '409 运行中提示走 MessagePlugin.error')
  assert.equal(calls.status, 2, '409 后 finally 仍刷新一次状态')
  const text = textOf(root)
  assert.ok(text.includes(`${NS}.statuses.running`), '摘要显示后端返回的运行中')
  assert.ok(!text.includes(`${NS}.statuses.success`), '不保留上一轮的成功态')
  assert.equal(buttons(root)[SYNC_BUTTON]!.props.loading, false)
  assert.equal(findByTag(root, 't-loading').length, 1, '运行中显示 loading')
})

/**
 * 手动同步失败（500 / 网络 / 120s 超时）：finally 都刷新一次状态，
 * 摘要显示后端最新状态（failed），且不乐观提示成功。
 */
async function assertSyncFailureRefreshes(
  t: TestContext,
  responder: Responder,
  expected: string,
): Promise<void> {
  const { root, calls, messages } = mountPanel(t, {
    statuses: [async () => statusPayload(), async () => statusWithRunStatus('failed')],
    sync: responder,
  })
  await settle()

  await click(buttons(root)[SYNC_BUTTON])
  await settle()

  assert.deepEqual(messages.success, [], '失败不提示成功')
  assert.deepEqual(messages.error, [expected], `expected ${expected}`)
  assert.equal(calls.status, 2, '失败后 finally 仍刷新一次状态')
  const text = textOf(root)
  assert.ok(text.includes(`${NS}.statuses.failed`), '摘要显示失败的运行状态')
  assert.ok(!text.includes(`${NS}.statuses.success`), '不显示上一轮的成功态')
  assert.equal(buttons(root)[SYNC_BUTTON]!.props.loading, false)
}

test('a 500 during manual sync refreshes the status and shows the failure toast', async (t) => {
  await assertSyncFailureRefreshes(t, statusError(500), `${NS}.messages.syncFailed`)
})

test('a network error during manual sync refreshes the status and shows the failure toast', async (t) => {
  await assertSyncFailureRefreshes(t, () => Promise.reject(new Error('offline')), `${NS}.messages.syncFailed`)
})

test('a 120s timeout during manual sync refreshes the status and shows the failure toast', async (t) => {
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
    assert.deepEqual(messages.error, [expected])
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
  await setTableMode(root, 'blocked')

  const popconfirms = findByTag(root, 't-popconfirm')
  assert.equal(popconfirms.length, 1, '每个 blocked 行一个二次确认')

  const pop = popconfirms[0]!
  assert.ok(String(pop.props.content).includes('王五'), '确认文案带姓名')
  assert.ok(String(pop.props.content).includes(`${NS}.unblockConfirm`))

  // 行内按钮本身没有点击处理器：必须先经过 popconfirm。
  const rowButton = findByTag(pop, 't-button')[0]
  assert.ok(rowButton, 'blocked 行内有解除按钮')
  assert.equal(rowButton!.props.onClick, undefined)
  assert.deepEqual(calls.unblock, [])

  await (pop.props.onConfirm as () => Promise<void>)()
  await settle()

  assert.deepEqual(calls.unblock, ['oid-blocked-1'])
  assert.deepEqual(messages.success, [`${NS}.messages.unblockSuccess`])
  assert.equal(calls.status, 2, '解除成功后刷新状态')
})

test('unblock failure reports 403 or a localized fallback via MessagePlugin.error and does not refresh', async (t) => {
  for (const [responder, expected] of [
    [statusError(403), `${NS}.messages.forbidden`],
    [statusError(500), `${NS}.messages.unblockFailed`],
    [() => Promise.reject(new Error('offline')), `${NS}.messages.unblockFailed`],
  ] as const) {
    const { root, calls, messages } = mountPanel(t, { unblock: responder })
    await settle()
    await setTableMode(root, 'blocked')

    const pop = findByTag(root, 't-popconfirm')[0]!
    await (pop.props.onConfirm as () => Promise<void>)()
    await settle()

    assert.deepEqual(messages.success, [])
    assert.deepEqual(messages.error, [expected])
    assert.equal(calls.status, 1, '失败不刷新状态')
  }
})

// ---------------------------------------------------------------------------
// 操作人映射（H2）
// ---------------------------------------------------------------------------

test('blocked_by maps user_id to email/username, falls back to the raw id and shows — when empty', async (t) => {
  const users = [
    { user_id: 'u-admin-1', email: 'admin@example.com', username: 'admin' },
    { user_id: 'u-admin-2', email: '', username: 'ops-user' },
  ]
  const blocked = [
    blockedRow({ open_id: 'oid-email', name: '甲', blocked_by: 'u-admin-1' }),
    blockedRow({ open_id: 'oid-username', name: '乙', blocked_by: 'u-admin-2' }),
    blockedRow({ open_id: 'oid-unknown', name: '丙', blocked_by: 'u-unknown' }),
    blockedRow({ open_id: 'oid-empty', name: '丁', blocked_by: '' }),
  ]
  const { root, calls } = mountPanel(t, {
    tenantId: 7,
    users,
    statuses: [async () => statusPayload({ blocked })],
  })
  await settle()
  await setTableMode(root, 'blocked')

  // params 在 vm 内创建，跨 realm 的 deepEqual 会因原型不同失败，逐字段断言。
  assert.equal(calls.listUsers.length, 1)
  assert.equal(calls.listUsers[0]!.tenantId, 7)
  assert.equal((calls.listUsers[0]!.params as { page?: number }).page, 1)
  assert.equal((calls.listUsers[0]!.params as { page_size?: number }).page_size, 100)
  assert.deepEqual(columnTexts(root, 'blocked_by'), [
    'admin@example.com',
    'ops-user',
    'u-unknown',
    '—',
  ])
})

test('a failing listUsers does not break the panel and keeps raw ids', async (t) => {
  const { root } = mountPanel(t, {
    tenantId: 7,
    listUsersError: new Error('offline'),
    statuses: [async () => statusPayload({ blocked: [blockedRow({ blocked_by: 'u-admin-1' })] })],
  })
  await settle()
  await setTableMode(root, 'blocked')

  assert.deepEqual(columnTexts(root, 'blocked_by'), ['u-admin-1'])
  assert.equal(alerts(root).length, 0, '映射失败不影响面板展示，也不额外弹错')
})

test('without tenantId the operator map is not requested', async (t) => {
  const { root, calls } = mountPanel(t)
  await settle()
  await setTableMode(root, 'blocked')

  assert.deepEqual(calls.listUsers, [])
  assert.deepEqual(columnTexts(root, 'blocked_by'), ['u-admin-1'])
})
