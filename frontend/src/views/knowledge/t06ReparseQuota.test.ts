/**
 * T06 票级验证：失败重试额度耗尽（后端 429 RETRY_LIMIT_EXHAUSTED）三入口人话卡片。
 *
 * 覆盖：
 * A. 时间线失败卡：429 → `.kp-quota-card` 渲染、zh-CN 文案逐字、隐藏重试按钮、无复制按钮；
 * B. 对照组：非额度耗尽错误不出现卡片，失败卡重试按钮保持原样；
 * C. 恢复语义：recover_at 到点（假时钟）撤卡、重试按钮回来；
 * D. 无持久状态：源码零 localStorage/sessionStorage + 新挂载无卡片；
 * E. 文档行「重建知识」：submitReparse 429 → 页内 `.doc-reparse-quota` 警示区文案；
 * E2. 对照组：submitReparse 普通失败仍走 MessagePlugin.error（条件 5 零变化）；
 * F. 批量：200 + data.skipped → 被拒文档点名、成功数=reparse_count、乐观状态排除被拒；
 * G. 批量全被拒：不提示成功、保留选择与批量模式；
 * H. 批量无 skipped：行为与改动前一致（数量回退 ids.length）；
 * U. 解析工具：摊平对象/后端信封两种入参、skipped 只认额度耗尽原因。
 *
 * 挂载沿用 t46SfcMount 的「完整编译模板 + 结构 renderer」先例；与 t46 的差别是
 * 依赖解析补了 require 回退（相对 TS 模块真实加载、.vue 占位），因为两个组件
 * 的 import 面较宽。文案断言使用真实 zh-CN bundle，不做键名打桩。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test, { type TestContext } from 'node:test'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import zhCN from '../../i18n/locales/zh-CN.ts'
import * as knowledgeTrace from '../../utils/knowledgeTrace.ts'
import * as processingStall from '../../utils/knowledgeProcessingStall.ts'
import * as processingStatus from '../../utils/knowledgeProcessingStatus.ts'
import * as traceAxis from '../../utils/traceAxis.ts'
import {
  RETRY_LIMIT_EXHAUSTED,
  detectReparseQuotaExhausted,
  readReparseQuotaSkips,
} from '../../utils/reparseQuota.ts'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

const TIMELINE = new URL('../../components/knowledge-processing-timeline.vue', import.meta.url)
const KNOWLEDGE_BASE = new URL('./KnowledgeBase.vue', import.meta.url)
const TIMELINE_SOURCE = readFileSync(TIMELINE, 'utf8')
const KB_SOURCE = readFileSync(KNOWLEDGE_BASE, 'utf8')
const QUOTA_UTIL_SOURCE = readFileSync(new URL('../../utils/reparseQuota.ts', import.meta.url), 'utf8')

// ---------- 真实 zh-CN 文案（不做键名打桩，断言逐字文案） ----------

type Params = Record<string, unknown> | undefined

function message(locale: Record<string, unknown>, key: string): string | undefined {
  const raw = key.split('.').reduce<unknown>((node, part) => (
    node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined
  ), locale)
  return typeof raw === 'string' ? raw : undefined
}

function zhText(key: string, params?: Params): string {
  const raw = message(zhCN as unknown as Record<string, unknown>, key)
  if (raw === undefined) return key
  return raw.replace(/\{(\w+)\}/g, (_, name: string) => String(params?.[name] ?? ''))
}

const locale = vue.ref('zh-CN')
const realI18n = { useI18n: () => ({ t: zhText, locale }) }
const messageStub = () => ({ success() {}, error() {}, info() {}, warning() {} })

// ---------- 结构 renderer（t46SfcMount 同款，本文件自带以复用 require 回退） ----------

interface Node {
  tag: string
  text?: string
  props: Record<string, unknown>
  children: Node[]
  parent?: Node
  // runtime-dom 的 v-show 指令会读写 el.style.display，结构 renderer 需补一个空 style。
  style: Record<string, unknown>
}

function node(tag: string, text?: string): Node {
  return { tag, text, props: {}, children: [], style: {} }
}

function allNodes(root: Node): Node[] {
  const out: Node[] = []
  const walk = (n: Node) => {
    out.push(n)
    n.children.forEach(walk)
  }
  walk(root)
  return out
}

function classesOf(n: Node): string[] {
  const value = n.props.class
  if (typeof value === 'string') return value.split(/\s+/).filter(Boolean)
  if (Array.isArray(value)) return value.flatMap((item) => classesOf({ ...n, props: { class: item } }))
  if (value && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).filter(([, on]) => on).map(([name]) => name)
  }
  return []
}

const byClass = (root: Node, name: string) => allNodes(root).filter((n) => classesOf(n).includes(name))
const textOf = (n: Node) => allNodes(n).map((x) => x.text ?? '').join('')
const settle = async (times = 4) => {
  for (let i = 0; i < times; i += 1) await flush()
}

function structRenderer(root: Node) {
  return vue.createRenderer<Node, Node>({
    createElement: (tag: string) => node(tag),
    createText: (text: string) => node('#text', text),
    createComment: () => node('#comment'),
    setText: (n: Node, text: string) => { n.text = text },
    setElementText: (el: Node, text: string) => { el.children = [node('#text', text)] },
    parentNode: (n: Node) => n.parent ?? null,
    nextSibling: (n: Node) => {
      const parent = n.parent
      if (!parent) return null
      const index = parent.children.indexOf(n)
      return index >= 0 ? (parent.children[index + 1] ?? null) : null
    },
    insert: (child: Node, parent: Node | null) => {
      const container = parent ?? root
      if (child.parent) {
        const old = child.parent.children.indexOf(child)
        if (old >= 0) child.parent.children.splice(old, 1)
      }
      child.parent = container
      container.children.push(child)
    },
    remove: (child: Node) => {
      if (!child.parent) return
      const idx = child.parent.children.indexOf(child)
      if (idx >= 0) child.parent.children.splice(idx, 1)
      child.parent = undefined
    },
    patchProp: (el: Node, key: string, _prev: unknown, next: unknown) => { el.props[key] = next },
  } as never)
}

/** 编译 SFC 并挂载；依赖表给出 @/ 前缀模块，相对模块走 require 真实加载。 */
function mountSfc(
  t: TestContext,
  component: URL,
  dependencies: Record<string, unknown>,
  props: Record<string, unknown>,
  liveProps?: vue.Ref<Record<string, unknown>>,
) {
  const f = fixture(t)
  const { descriptor } = parse(readFileSync(component, 'utf8'), { filename: component.pathname })
  const script = compileScript(descriptor, { id: 't06-mount' })
  const template = descriptor.template
  assert.ok(template, `${component.pathname} 应包含模板`)
  const compiled = compileTemplate({
    source: template.content,
    filename: component.pathname,
    id: 't06-mount',
    compilerOptions: { bindingMetadata: script.bindings },
  })
  const require = createRequire(component)
  const scriptExports = f.evaluate(
    script.content.replace('__expose();', '').replace('return __returned__', '__expose(__returned__); return __returned__'),
    { vue, ...dependencies },
    (name) => {
      if (name.endsWith('.vue')) return { default: { name: name.split('/').pop(), render: () => null } }
      if (name.startsWith('.')) return require(name)
      assert.fail(`Unexpected component dependency: ${name}`)
    },
  )
  const renderExports = f.evaluate(compiled.code, { vue })
  const comp = scriptExports.default as Record<string, unknown>
  comp.render = renderExports.render
  const root = node('root')
  // liveProps 场景用宿主组件包一层：根组件 props 的外部变更不会刷新内部
  // props，包一层后每次父渲染都传入新 props，watch(props.knowledgeId) 才会触发。
  let childVm: any
  const app = liveProps
    ? structRenderer(root).createApp(vue.defineComponent({
        setup: () => () => vue.h(comp as vue.Component, {
          ...liveProps.value,
          ref: (value: any) => { childVm = value },
        }),
      }))
    : structRenderer(root).createApp(comp as vue.Component, props)
  app.config.warnHandler = () => {}
  // 模板里 KnowledgeBase.vue 用 $t（vue-i18n 全局注入），测试等价注入。
  app.config.globalProperties.$t = zhText as unknown as typeof app.config.globalProperties.$t
  // t-alert 的正文在 #message 具名插槽里；宿主元素不会执行插槽函数，
  // 用结构等价桩把插槽内容真正渲染出来（文案断言需要它）。
  app.component('t-alert', {
    name: 't-alert',
    props: { theme: { type: String, default: '' }, close: { type: Boolean, default: false } },
    emits: ['close'],
    setup(_props: unknown, { slots }: any) {
      return () => vue.h('div', { class: 't-alert-stub' }, slots.message ? [slots.message()] : (slots.default ? [slots.default()] : []))
    },
  })
  const mountedRoot = app.mount(root) as any
  const vm = childVm ?? mountedRoot
  t.after(async () => {
    try {
      await flush()
      app.unmount()
      await flush()
    } catch {
      // 结构 renderer 无完整 DOM 语义；卸载报错不掩盖断言结果。
    }
  })
  return { root, vm }
}

// ---------- 429 信封（request 层摊平后的形状，见 T06 notes §2） ----------

function quotaError(recoverAt: string) {
  return {
    status: 429,
    message: '今日重试次数已用完',
    success: false,
    error: {
      code: 1006,
      message: '今日重试次数已用完',
      details: { error_code: RETRY_LIMIT_EXHAUSTED, recover_at: recoverAt },
    },
  }
}

const recoverLabel = (iso: string) => new Date(iso).toLocaleString('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })

// ---------- 时间线挂载 ----------

function failedSpans() {
  return {
    success: true,
    data: {
      knowledge_id: 'doc-1',
      attempt: 1,
      latest_attempt: 1,
      parse_status: 'failed',
      trace: { span_id: 'root', name: 'root', status: 'failed', children: [] },
      last_error: {
        name: 'docreader',
        error_code: 'DOCREADER_PARSE_FAILED',
        error_message: 'Unsupported file type: htm',
      },
    },
  }
}

function mountTimeline(
  t: TestContext,
  reparse: (...args: any[]) => Promise<unknown>,
  props: Record<string, unknown> = {},
  live = false,
) {
  const calls: any[][] = []
  const liveProps = live
    ? vue.ref<Record<string, unknown>>({ knowledgeId: 'doc-1', docTitle: '季度报告.pdf', autoPoll: false, ...props })
    : undefined
  const mounted = mountSfc(t, TIMELINE, {
    'vue-i18n': realI18n,
    'tdesign-vue-next': { MessagePlugin: messageStub() },
    '@/utils/clipboard': { copyWithToast() {} },
    '@/utils/knowledgeTrace': knowledgeTrace,
    '@/utils/knowledgeProcessingStatus': processingStatus,
    '@/utils/knowledgeProcessingStall': processingStall,
    '@/utils/traceAxis': traceAxis,
    '@/api/knowledge-base/index': {
      getKnowledgeSpans: async () => failedSpans(),
      reparseKnowledge: async (...args: any[]) => {
        calls.push(args)
        return reparse(...args)
      },
      cancelKnowledgeParse: async () => ({ success: true }),
      getKnowledgeDetails: async () => ({ success: true, data: { metadata: {} } }),
    },
  }, live ? {} : { knowledgeId: 'doc-1', docTitle: '季度报告.pdf', autoPoll: false, ...props }, liveProps)
  return {
    ...mounted,
    calls,
    setKnowledgeId: (id: string) => {
      if (liveProps) liveProps.value = { ...liveProps.value, knowledgeId: id }
    },
  }
}

test('T06-A 时间线失败卡 429 → .kp-quota-card 文案逐字、隐藏重试按钮、无复制/诊断入口（条件 1/7）', async (t) => {
  const recoverAt = new Date(Date.now() + 3_600_000).toISOString()
  const mounted = mountTimeline(t, async () => { throw quotaError(recoverAt) })
  await settle()

  // 前置：失败卡已渲染且原来的重试按钮在（证明后续不是空挂载）。
  const before = byClass(mounted.root, 'kp-err-btn')
  assert.equal(before.length, 1, '429 之前失败卡应有重试按钮')
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 0, '未重试时不应有额度卡片')

  await mounted.vm.onRetry()
  await settle()

  assert.equal(mounted.calls.length, 1, '重试应提交一次 reparse')
  const cards = byClass(mounted.root, 'kp-quota-card')
  assert.equal(cards.length, 1, '429 后应渲染 .kp-quota-card')
  const text = textOf(cards[0]!)

  assert.ok(text.includes(zhText('reparseQuota.title', { title: '季度报告.pdf' })), `卡片标题逐字：${text}`)
  assert.ok(text.includes(zhText('knowledgeStages.errorCode.RETRY_LIMIT_EXHAUSTED')), `原因逐字：${text}`)
  assert.ok(text.includes(zhText('knowledgeStages.errorCode.RETRY_LIMIT_EXHAUSTED_SUGGESTION')), `指引用词逐字：${text}`)
  assert.ok(
    text.includes(zhText('reparseQuota.recoverAt', { time: recoverLabel(recoverAt) })),
    `恢复时间行逐字：${text}`,
  )
  assert.ok(text.includes('「季度报告.pdf」'), '卡片含文档名（票条件 7）')

  // 卡片是提示而非按钮；重试位置不再出现重试按钮，也无复制/诊断入口。
  assert.equal(byClass(mounted.root, 'kp-err-btn').length, 0, '卡片出现后失败卡重试按钮应隐藏')
  assert.equal(allNodes(mounted.root).filter((n) => n.tag === 't-button').length, 0, '工具栏重试按钮应隐藏')
  const cardNodes = allNodes(cards[0]!)
  assert.equal(cardNodes.filter((n) => n.tag === 'button').length, 0, '卡片内不得有按钮（无复制按钮）')
  assert.equal(cardNodes.filter((n) => n.tag === 't-icon').length, 0, '卡片内不得有诊断/图标入口')
  assert.equal(cardNodes.some((n) => classesOf(n).includes('kp-err-copy')), false, '卡片内不得出现复制错误入口')
})

test('T06-B 对照组：非额度耗尽错误不出现卡片，重试按钮保持（条件 5）', async (t) => {
  const mounted = mountTimeline(t, async () => { throw new Error('temporary network failure') })
  await settle()
  await mounted.vm.onRetry()
  await settle()

  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 0, '普通失败不得渲染额度卡片')
  assert.equal(mounted.vm.quotaNotice, null, '普通失败不写 quotaNotice')
  assert.equal(byClass(mounted.root, 'kp-err-btn').length, 1, '普通失败仍保留原重试按钮')
})

test('T06-C recover_at 到点自动撤卡、重试按钮回来（条件 4，假时钟）', async (t) => {
  const recoverAt = new Date(Date.now() + 60_000).toISOString()
  const mounted = mountTimeline(t, async () => { throw quotaError(recoverAt) })
  await settle()
  await mounted.vm.onRetry()
  await settle()
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 1, '到点前卡片在')

  t.mock.timers.tick(60_000)
  await settle()

  assert.equal(mounted.vm.quotaNotice, null, '恢复时间到达后清空提示（不持久化）')
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 0, '到点后卡片消失')
  assert.equal(byClass(mounted.root, 'kp-err-btn').length, 1, '到点后重试按钮回到可点状态')
})

test('CP3-F5 切换文档清空上一文档的额度耗尽卡（watch knowledgeId）', async (t) => {
  const recoverAt = new Date(Date.now() + 3_600_000).toISOString()
  const mounted = mountTimeline(t, async () => { throw quotaError(recoverAt) }, {}, true)
  await settle()
  await mounted.vm.onRetry()
  await settle()
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 1, '前置：doc-1 有额度卡')

  mounted.setKnowledgeId('doc-2')
  await settle()

  assert.equal(mounted.vm.quotaNotice, null, '换文档后不得保留上一文档的额度提示')
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 0, '换文档后不得渲染上一文档的卡片')
})

test('CP3-FS5 额度卡仅在 parse_status=failed 时显示，重建成功后自动消失', async (t) => {
  const recoverAt = new Date(Date.now() + 3_600_000).toISOString()
  const mounted = mountTimeline(t, async () => { throw quotaError(recoverAt) })
  await settle()
  await mounted.vm.onRetry()
  await settle()
  assert.equal(mounted.vm.quotaNotice !== null, true, '前置：429 后已记录额度提示')
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 1, '前置：failed 时卡片渲染')

  // 文档重建成功：parse_status 离开 failed，卡片随条件消失（不清 quotaNotice 也能走隐藏）。
  mounted.vm.data.parse_status = 'completed'
  await settle()
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 0, 'parse_status 离开 failed 后卡片必须消失')
})

test('T06-D 无持久状态：T06 相关代码零 localStorage/sessionStorage、新挂载无卡片（条件 4）', async (t) => {
  // 时间线与解析工具是本票新增/承载耗尽态的全部代码，直接整文件守护。
  for (const storage of ['localStorage', 'sessionStorage', 'indexedDB']) {
    assert.equal(TIMELINE_SOURCE.includes(storage), false, `knowledge-processing-timeline.vue 不得写 ${storage}（前端不缓存耗尽标记）`)
    assert.equal(QUOTA_UTIL_SOURCE.includes(storage), false, `reparseQuota.ts 不得写 ${storage}（前端不缓存耗尽标记）`)
  }
  // KnowledgeBase.vue 是存量大文件，只看本票相关行（reparseQuota* / RETRY_LIMIT_EXHAUSTED /
  // data.skipped 解析），避免把上游其它 localStorage 用法误算到本票。
  const quotaLines = KB_SOURCE.split('\n').filter((line) => /reparseQuota|ReparseQuota|RETRY_LIMIT_EXHAUSTED|readReparseQuotaSkips/.test(line))
  assert.ok(quotaLines.length >= 8, `应能圈出 T06 相关行（实际 ${quotaLines.length}）`)
  for (const line of quotaLines) {
    for (const storage of ['localStorage', 'sessionStorage', 'indexedDB']) {
      assert.equal(line.includes(storage), false, `KnowledgeBase.vue 的 T06 分支不得写 ${storage}：${line.trim()}`)
    }
  }

  const mounted = mountTimeline(t, async () => ({ success: true }))
  await settle()
  assert.equal(mounted.vm.quotaNotice, null, '新挂载（等价刷新）不残留耗尽态')
  assert.equal(byClass(mounted.root, 'kp-quota-card').length, 0, '新挂载不渲染额度卡片')
})

// ---------- 文档行「重建知识」/ 批量重解析挂载 ----------

type Row = { id: string, file_name: string, title?: string, parse_status: string }

// 每用例独立构造依赖（挂载会真实执行 script，避免跨用例共享可变 ref）。
function mountKnowledgeBase(t: TestContext, options: {
  rows: Row[]
  reparse?: (id: string) => Promise<unknown>
  batch?: (ids: string[]) => Promise<unknown>
}) {
  const route = vue.reactive({ params: { kbId: 'kb' }, query: {} })
  const cards = vue.ref<Row[]>(options.rows.map((item) => ({ ...item })))
  const total = vue.ref(cards.value.length)
  const messages = { success: t.mock.fn(), error: t.mock.fn(), info: t.mock.fn(), warning: t.mock.fn() }
  const reparseCalls: string[] = []
  const batchCalls: string[][] = []
  const hook = {
    cardList: cards,
    total,
    moreIndex: vue.ref(-1),
    details: vue.reactive({ id: '' }),
    getKnowled: async () => {},
    getCardDetails() {},
    getfDetails() {},
    onVisibleChange() {},
  }
  const mounted = mountSfc(t, KNOWLEDGE_BASE, {
    'vue-i18n': realI18n,
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace() {} }) },
    'tdesign-vue-next': { MessagePlugin: messages },
    '@/utils/assistantDataChange': { useAssistantDataChange() {}, emitAssistantDataChange() {} },
    '@/hooks/useKnowledgeBase': () => hook,
    '@/stores/ui': { useUIStore: () => ({ clearSelectedTagIds() {} }) },
    '@/stores/organization': { useOrganizationStore: () => ({ sharedKnowledgeBases: [], getKBPermission: () => 'owner' }) },
    '@/stores/auth': { useAuthStore: () => ({ hasRole: () => true }) },
    '@/stores/chatResources': {
      useChatResourcesStore: () => ({
        fetchKnowledgeBaseById: async (id: string) => ({ id, type: 'document' }),
        rawKnowledgeBases: [],
        ensureKnowledgeBases: async () => {},
      }),
    },
    '@/stores/editorResources': { useEditorResourcesStore: () => ({ parserEngines: [], ensureParserEngines: async () => {} }) },
    '@/stores/uploadConfirm': { useUploadConfirmStore: () => ({}) },
    '@/stores/uploadTasks': { useUploadTasksStore: () => ({}) },
    '@/hooks/useMarqueeSelect': { useMarqueeSelect: () => ({}) },
    '@/composables/useAssistantPageContext': { useAssistantPageContext() {} },
    '@/utils/kbPermission': { permissionCanEditKB: () => true, permissionCanManageKB: () => true },
    '@/utils/knowledgeDeletion': { waitForKnowledgeDeletion: async () => 'completed' },
    '@/utils/knowledgeTrace': knowledgeTrace,
    '@/utils/knowledgeProcessingStall': processingStall,
    '@/api/wiki': { getWikiStats: async () => ({ data: {} }) },
    '@/api/knowledge-base': { listMoveTargets: async () => [], moveKnowledge: async () => ({}), getKnowledgeMoveProgress: async () => ({}) },
    '@/api/knowledge-base/index': {
      batchQueryKnowledge: async (query: string) => {
        const ids = new URLSearchParams(query).getAll('ids')
        return { success: true, data: cards.value.filter((c) => ids.includes(c.id)).map((c) => ({ ...c })) }
      },
      reparseKnowledge: async (id: string) => {
        reparseCalls.push(id)
        return options.reparse ? options.reparse(id) : { success: true }
      },
      batchReparseKnowledge: async (_kb: string, ids: string[]) => {
        batchCalls.push([...ids])
        return options.batch ? options.batch(ids) : { success: true }
      },
      cancelKnowledgeParse: async () => ({ success: true }),
      batchDeleteKnowledge: async () => ({ success: true }),
      batchDownloadKnowledge: async () => ({ success: true }),
      delKnowledgeDetails: async () => ({ success: true }),
      getKnowledgeSpans: async () => ({ success: true, data: { trace: { children: [] } } }),
      getKnowledgeDetails: async () => ({ success: true, data: {} }),
      listKnowledgeFolders: async () => ({ data: { folders: [] } }),
      listKnowledgeTags: async () => ({ data: { data: [], total: 0 } }),
      updateKnowledgeTagBatch: async () => ({ success: true }),
      createKnowledgeFromURL: async () => ({ success: true }),
      moveKnowledgeToFolder: async () => ({ success: true }),
      renameKnowledgeFolder: async () => ({ success: true }),
      downKnowledgeDetails: async () => ({}),
    },
  }, {})
  // 挂载期间 onMounted 会走一遍列表加载（getKnowled 是空桩），把用例数据补回来，
  // 保证 knowledgeDisplayName / 乐观状态断言作用在真实行上。
  const resetRows = () => { cards.value = options.rows.map((item) => ({ ...item })) }
  return { ...mounted, cards, messages, reparseCalls, batchCalls, hook, resetRows }
}

test('T06-E 重建知识 429 → 页内 .doc-reparse-quota 警示区文案逐字、不裸抛错误（条件 2/7）', async (t) => {
  const recoverAt = new Date(Date.now() + 3_600_000).toISOString()
  const mounted = mountKnowledgeBase(t, {
    rows: [{ id: 'doc-1', file_name: '文档A.pdf', parse_status: 'failed' }],
    reparse: async () => { throw quotaError(recoverAt) },
  })
  await settle()
  mounted.resetRows()

  await mounted.vm.submitReparse('doc-1')
  await settle()

  assert.deepEqual(mounted.reparseCalls, ['doc-1'], '提交一次重建')
  const alerts = byClass(mounted.root, 'doc-reparse-quota')
  assert.equal(alerts.length, 1, '429 后应渲染页内 .doc-reparse-quota 警示区')
  const text = textOf(alerts[0]!)
  assert.ok(text.includes(zhText('reparseQuota.title', { title: '文档A.pdf' })), `单文档标题逐字：${text}`)
  assert.ok(text.includes(zhText('knowledgeStages.errorCode.RETRY_LIMIT_EXHAUSTED')), `原因逐字：${text}`)
  assert.ok(text.includes(zhText('knowledgeStages.errorCode.RETRY_LIMIT_EXHAUSTED_SUGGESTION')), `指引用词逐字：${text}`)
  assert.ok(
    text.includes(zhText('reparseQuota.recoverAt', { time: recoverLabel(recoverAt) })),
    `恢复时间逐字：${text}`,
  )
  assert.equal(mounted.messages.error.mock.callCount(), 0, '额度耗尽不再走裸 MessagePlugin.error')
  assert.equal(mounted.messages.success.mock.callCount(), 0, '失败不提示重建成功')
  const alertNodes = allNodes(alerts[0]!)
  assert.equal(alertNodes.some((n) => classesOf(n).includes('doc-reparse-quota__docs')), false, '单文档不显示批量文档行')
})

test('T06-E2 对照组：重建知识普通失败仍走 MessagePlugin.error（条件 5）', async (t) => {
  const mounted = mountKnowledgeBase(t, {
    rows: [{ id: 'doc-1', file_name: '文档A.pdf', parse_status: 'failed' }],
    reparse: async () => { throw new Error('boom') },
  })
  await settle()
  mounted.resetRows()
  await mounted.vm.submitReparse('doc-1')
  await settle()

  assert.equal(byClass(mounted.root, 'doc-reparse-quota').length, 0, '普通失败不出现额度警示区')
  assert.equal(mounted.messages.error.mock.callCount(), 1, '普通失败保留原错误提示')
})

test('T06-F 批量 200+skipped：被拒文档点名、成功数=reparse_count、乐观状态排除被拒（条件 3）', async (t) => {
  const mounted = mountKnowledgeBase(t, {
    rows: [
      { id: 'doc-1', file_name: '文档A.pdf', parse_status: 'completed' },
      { id: 'doc-2', file_name: '文档B.pdf', parse_status: 'completed' },
    ],
    batch: async () => ({
      success: true,
      data: {
        reparse_count: 1,
        skipped: [{ knowledge_id: 'doc-2', reason: RETRY_LIMIT_EXHAUSTED, recover_at: new Date().toISOString() }],
      },
    }),
  })
  await settle()
  mounted.resetRows()

  mounted.vm.selectedIds = new Set(['doc-1', 'doc-2'])
  mounted.vm.batchMode = true
  await mounted.vm.confirmBatchReparse()
  await settle()

  assert.deepEqual(mounted.batchCalls, [['doc-1', 'doc-2']], '两篇都提交给后端预检')
  const alerts = byClass(mounted.root, 'doc-reparse-quota')
  assert.equal(alerts.length, 1, '批量被拒应渲染警示区')
  const text = textOf(alerts[0]!)
  assert.ok(text.includes(zhText('reparseQuota.batchTitle')), `批量标题逐字：${text}`)
  assert.ok(text.includes(zhText('reparseQuota.docs', { docs: '文档B.pdf' })), `被拒文档点名（文档名）：${text}`)
  assert.ok(text.includes(zhText('knowledgeStages.errorCode.RETRY_LIMIT_EXHAUSTED')), `原因逐字：${text}`)
  assert.ok(text.includes(zhText('knowledgeStages.errorCode.RETRY_LIMIT_EXHAUSTED_SUGGESTION')), `指引用词逐字：${text}`)

  // 成功数以后端 reparse_count 为准（不是提交数 2）。
  assert.equal(mounted.messages.success.mock.callCount(), 1, '有成功入队时提示一次成功')
  assert.equal(
    mounted.messages.success.mock.calls[0]!.arguments[0],
    zhText('knowledgeBase.batchReparseSuccess', { count: 1 }),
    '成功提示数量=reparse_count',
  )
  // 乐观状态只作用于实际入队文档。
  const row = (id: string) => mounted.cards.value.find((item) => item.id === id)
  assert.equal(row('doc-1')!.parse_status, 'pending', '入队文档进入临时待处理')
  assert.equal(row('doc-2')!.parse_status, 'completed', '被拒文档不得被改成待处理')
})

test('CP3-FS8 批量被拒卡片显示最早恢复时间；无 recover_at 不显示时间行', async (t) => {
  const early = new Date(Date.now() + 3_600_000).toISOString()
  const late = new Date(Date.now() + 7_200_000).toISOString()
  const mounted = mountKnowledgeBase(t, {
    rows: [
      { id: 'doc-1', file_name: '文档A.pdf', parse_status: 'completed' },
      { id: 'doc-2', file_name: '文档B.pdf', parse_status: 'completed' },
    ],
    batch: async () => ({
      success: true,
      data: {
        reparse_count: 0,
        skipped: [
          { knowledge_id: 'doc-1', reason: RETRY_LIMIT_EXHAUSTED, recover_at: late },
          { knowledge_id: 'doc-2', reason: RETRY_LIMIT_EXHAUSTED, recover_at: early },
        ],
      },
    }),
  })
  await settle()
  mounted.resetRows()

  mounted.vm.selectedIds = new Set(['doc-1', 'doc-2'])
  mounted.vm.batchMode = true
  await mounted.vm.confirmBatchReparse()
  await settle()

  const text = textOf(byClass(mounted.root, 'doc-reparse-quota')[0]!)
  assert.ok(
    text.includes(zhText('reparseQuota.recoverAt', { time: recoverLabel(early) })),
    `批量卡显示最早恢复时间（早于另一条）：${text}`,
  )
  assert.equal(text.includes(recoverLabel(late)), false, '较晚的恢复时间不得出现在卡片上')
})

test('CP3-FS8 批量 skipped 缺 recover_at 时不渲染恢复时间行', async (t) => {
  // 无 recover_at 时沿用既有口径：不渲染恢复时间行（键不出现）。
  const noTime = mountKnowledgeBase(t, {
    rows: [{ id: 'doc-3', file_name: '文档C.pdf', parse_status: 'completed' }],
    batch: async () => ({
      success: true,
      data: {
        reparse_count: 0,
        skipped: [{ knowledge_id: 'doc-3', reason: RETRY_LIMIT_EXHAUSTED }],
      },
    }),
  })
  await settle()
  noTime.resetRows()
  noTime.vm.selectedIds = new Set(['doc-3'])
  noTime.vm.batchMode = true
  await noTime.vm.confirmBatchReparse()
  await settle()
  const noTimeText = textOf(byClass(noTime.root, 'doc-reparse-quota')[0]!)
  assert.equal(noTimeText.includes('reparseQuota.recoverAt'), false, '缺 recover_at 不显示恢复时间行')
})

test('T06-G 批量全被拒：不提示成功、保留选择与批量模式（条件 3）', async (t) => {
  const mounted = mountKnowledgeBase(t, {
    rows: [
      { id: 'doc-1', file_name: '文档A.pdf', parse_status: 'completed' },
      { id: 'doc-2', file_name: '文档B.pdf', parse_status: 'completed' },
    ],
    batch: async () => ({
      success: true,
      data: {
        reparse_count: 0,
        skipped: [
          { knowledge_id: 'doc-1', reason: RETRY_LIMIT_EXHAUSTED, recover_at: new Date().toISOString() },
          { knowledge_id: 'doc-2', reason: RETRY_LIMIT_EXHAUSTED, recover_at: new Date().toISOString() },
        ],
      },
    }),
  })
  await settle()
  mounted.resetRows()

  mounted.vm.selectedIds = new Set(['doc-1', 'doc-2'])
  mounted.vm.batchMode = true
  await mounted.vm.confirmBatchReparse()
  await settle()

  assert.equal(mounted.messages.success.mock.callCount(), 0, '全被拒不得提示成功')
  assert.equal(mounted.vm.batchMode, true, '全被拒保留批量模式便于核对')
  assert.equal(mounted.vm.selectedIds.size, 2, '全被拒不清空选择')
  const alertText = textOf(byClass(mounted.root, 'doc-reparse-quota')[0]!)
  assert.ok(alertText.includes('文档A.pdf') && alertText.includes('文档B.pdf'), `两篇都被点名：${alertText}`)
  for (const card of mounted.cards.value) {
    assert.equal(card.parse_status, 'completed', `${card.id} 未被提交，状态不得变化`)
  }
})

test('T06-H 批量无 skipped：行为与改动前一致（条件 5）', async (t) => {
  const mounted = mountKnowledgeBase(t, {
    rows: [
      { id: 'doc-1', file_name: '文档A.pdf', parse_status: 'completed' },
      { id: 'doc-2', file_name: '文档B.pdf', parse_status: 'completed' },
    ],
    batch: async () => ({ success: true, data: { reparse_count: 2 } }),
  })
  await settle()
  mounted.resetRows()

  mounted.vm.selectedIds = new Set(['doc-1', 'doc-2'])
  mounted.vm.batchMode = true
  await mounted.vm.confirmBatchReparse()
  await settle()

  assert.equal(byClass(mounted.root, 'doc-reparse-quota').length, 0, '无 skipped 不出现额度警示')
  assert.equal(
    mounted.messages.success.mock.calls[0]!.arguments[0],
    zhText('knowledgeBase.batchReparseSuccess', { count: 2 }),
    '正常批量成功提示数量不变',
  )
  for (const card of mounted.cards.value) {
    assert.equal(card.parse_status, 'pending', `${card.id} 正常入队后进入临时待处理`)
  }
})

// ---------- 解析工具（错误链与 skipped 结构） ----------

test('T06-U 解析工具：摊平对象/后端信封两种入参、skipped 只认额度耗尽原因（条件 1/3）', () => {
  const flattened = quotaError('2026-11-20T00:00:00Z')
  assert.deepEqual(detectReparseQuotaExhausted(flattened), { recoverAt: '2026-11-20T00:00:00Z' })

  const envelope = { success: false, error: flattened.error }
  assert.deepEqual(detectReparseQuotaExhausted(envelope), { recoverAt: '2026-11-20T00:00:00Z' })

  assert.equal(detectReparseQuotaExhausted(new Error('boom')), null, '普通错误不误判')
  assert.equal(detectReparseQuotaExhausted({ error: { details: { error_code: 'OTHER' } } }), null, '其他错误码不误判')

  const skips = readReparseQuotaSkips({
    success: true,
    data: {
      skipped: [
        { knowledge_id: 'a', reason: RETRY_LIMIT_EXHAUSTED, recover_at: '2026-11-20T00:00:00Z' },
        { knowledge_id: 'b', reason: 'OTHER_REASON' },
        { reason: RETRY_LIMIT_EXHAUSTED },
      ],
    },
  })
  assert.deepEqual(skips, [{ knowledgeId: 'a', reason: RETRY_LIMIT_EXHAUSTED, recoverAt: '2026-11-20T00:00:00Z' }])
  assert.deepEqual(readReparseQuotaSkips({ success: true, data: {} }), [])
})
