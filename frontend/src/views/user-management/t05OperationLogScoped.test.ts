/**
 * T05 票级验证：操作记录 Tab 纳入本空间文档类行为（AC-10 前端半）。
 *
 * 覆盖五行：
 * A. OperationLog 调 listAuditLog 必须带 `include_scoped: true`；
 * B. 8 个 KB 动作行 + 成员行全部渲染，噪音与「有意不加」动作不渲染；
 * C. TenantMembers.vue 同接口不传 include_scoped（与 A 形成两处对比）；
 * D. 8 个新动作在真实 zh-CN bundle 下逐条渲染中文显示名，不露英文 action 串；
 * E. api 层 audit-log.ts：true 拼参、缺省/false 不拼。
 *
 * 挂载方式沿用 t46SfcMount（完整编译模板并渲染真实 OperationLog.vue），
 * api 层沿用 t36PlatformActions 的 fixture.evaluate 源码求值先例。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { reactive, ref } from 'vue'
import { auditActionLabel } from '../../i18n/auditActionLabel.ts'
import { AUDIT_ACTION_I18N_ROOTS } from '../../i18n/auditActionRegistry.ts'
import zhCN from '../../i18n/locales/zh-CN.ts'
import { fixture, flush } from '../../test-utils/vueFixture.ts'
import { findByTag, mountRendered, settle } from './t46SfcMount.ts'

const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)
const TENANT_MEMBERS = new URL('../settings/TenantMembers.vue', import.meta.url)
const AUDIT_LOG_API = new URL('../../api/tenant/audit-log.ts', import.meta.url)

/** 本票新增的 8 个 KB 动作（顺序同 T05 notes §2）。 */
const SCOPED_ACTIONS = [
  'knowledge.created',
  'knowledge.updated',
  'knowledge.deleted',
  'knowledge.batch_deleted',
  'knowledge.reparse_started',
  'kb.created',
  'kb.updated',
  'kb.deleted',
] as const

/** 票面「有意不加」的噪音动作：白名单模式下不得渲染。 */
const NOISE_ACTIONS = [
  'rbac.access_denied',
  'vector_store.index_created',
  'tag.created',
  'knowledge.parse_canceled',
  'datasource.updated',
] as const

/** 逐段解析 zh-CN bundle 的路径，保证组件传的 root 是真实存在的 i18n 根。 */
function resolveLocalePath(source: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[part]
    return undefined
  }, source)
}

/** 真实 zh-CN 文案的 i18n 打桩：t() 返回键名（本用例不依赖），tm() 走真实 bundle。 */
const realI18n = {
  useI18n: () => ({
    t: (key: string) => key,
    tm: (root: string) => resolveLocalePath(zhCN, root) ?? {},
    locale: { value: 'zh-CN' },
  }),
}

/** 真实 auditActionLabel / 真实 i18n 根常量，避免用打桩把显示名逻辑绕掉。 */
const realAuditDeps = {
  '@/i18n/auditActionLabel': { auditActionLabel },
  '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS },
}

const row = (id: number, action: string, actor = 'u-owner') => ({
  id,
  action,
  actor_user_id: actor,
  target_user_id: '',
  outcome: 'success',
  created_at: '2026-11-19T00:00:00Z',
})

const PAGE_SIZE = 50

/** 挂载完整 OperationLog：pages 按请求序返回（本票只需首屏）。 */
function mountLog(t: TestContext, rows: Array<Record<string, unknown>>) {
  const calls: any[][] = []
  const mounted = mountRendered(t, {
    component: OPERATIONS,
    dependencies: {
      'vue-i18n': realI18n,
      ...realAuditDeps,
      '@/api/tenant/audit-log': {
        listAuditLog: async (...args: any[]) => {
          calls.push(args)
          return { success: true, data: rows, next_cursor: 0 }
        },
      },
      '@/api/user-management': {
        listUsers: async () => ({
          data: { items: [{ user_id: 'u-owner', email: 'owner@example.com', username: 'owner' }] },
        }),
        resolveUserManagementError: (error: unknown) => `resolved:${String(error)}`,
      },
    },
    props: { tenantId: 7 },
  })
  return { ...mounted, calls }
}

test('T05-A 操作记录 Tab 调 listAuditLog 必须带 include_scoped=true（AC-10）', async (t) => {
  const { calls } = mountLog(t, [row(1, 'rbac.member_banned')])
  await settle()

  assert.equal(calls.length, 1, '首屏一次审计请求')
  assert.equal(calls[0]![0], 7, '按本空间 tenantId 拉取')
  assert.equal(calls[0]![1].include_scoped, true, '本 Tab 必须纳入知识库 scope 行')
  assert.equal(calls[0]![1].limit, PAGE_SIZE, '分页大小不变（条件 6）')
  assert.equal(calls[0]![1].after_id, undefined, '首屏不带游标')
})

test('T05-B 8 个 KB 动作行与成员行全部渲染，噪音动作被白名单滤掉（条件 1/2/3）', async (t) => {
  const rows = [
    ...SCOPED_ACTIONS.map((action, i) => row(100 + i, action)),
    row(201, 'rbac.member_banned'),
    row(202, 'rbac.owner_transferred'),
    ...NOISE_ACTIONS.map((action, i) => row(300 + i, action)),
  ]
  const { root, vm } = mountLog(t, rows)
  await settle()

  const visible = (vm.visibleRows as any[]).map((r) => r.action)
  for (const action of SCOPED_ACTIONS) {
    assert.ok(visible.includes(action), `${action} 必须进入可见行（AC-10）`)
  }
  assert.ok(visible.includes('rbac.member_banned'), '成员治理动作保持可见（白名单模式不变）')
  assert.ok(visible.includes('rbac.owner_transferred'), '成员治理动作保持可见')
  for (const action of NOISE_ACTIONS) {
    assert.equal(visible.includes(action), false, `${action} 属噪音/有意不加，不得显示`)
  }
  assert.equal(visible.length, SCOPED_ACTIONS.length + 2, '可见行 = 8 KB + 2 成员，噪音零残留')

  // 表格真实渲染的 data 与可见行一致（模板分支真实执行，不只是计算属性）。
  const table = findByTag(root, 't-table')[0]
  assert.ok(table, '有可见行时渲染表格')
  const tableActions = ((table!.props.data as any[]) ?? []).map((r) => r.action)
  assert.deepEqual(tableActions, visible, '表格数据 = 白名单过滤后的行')

  // 条件 3：KB 行的操作人沿用现有用户名解析（不再显示「未知用户」）。
  assert.equal(vm.displayUser('u-owner'), 'owner@example.com', '操作人回填邮箱不变')
  assert.equal(vm.displayUser(''), '—', '空操作人占位不变')
})

test('T05-B2 动作筛选下拉 = 19 个白名单动作且 8 个新动作已是中文显示名', async (t) => {
  const { root } = mountLog(t, [row(1, 'kb.created')])
  await settle()

  const select = findByTag(root, 't-select')[0]
  assert.ok(select, '工具栏渲染动作筛选下拉')
  const options = (select!.props.options as Array<{ value: string; label: string }>) ?? []
  assert.equal(options.length, 19, '白名单 11 成员 + 8 KB = 19 个筛选项')

  const bag = resolveLocalePath(zhCN, AUDIT_ACTION_I18N_ROOTS.tenantMember) as Record<string, string>
  for (const action of SCOPED_ACTIONS) {
    const option = options.find((o) => o.value === action)
    assert.ok(option, `筛选下拉含 ${action}`)
    assert.equal(option!.label, bag[action], `${action} 筛选项为 zh-CN 显示名`)
    assert.notEqual(option!.label, action, `${action} 不得露出英文 action 串`)
  }
})

test('T05-C TenantMembers.vue 同接口不传 include_scoped（条件 4，与用例 A 两处对比）', async (t) => {
  const f = fixture(t)
  const auditCalls: any[][] = []
  const auth = reactive({
    currentTenantId: 7,
    currentTenantRole: 'owner',
    canAccessAllTenants: false,
    user: { id: 'u-owner' },
  })
  const { vm } = f.component(TENANT_MEMBERS, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: ref('zh-CN') }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {}, warning() {}, info() {} } },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/clipboard': { copyWithToast: async () => {} },
    '@/utils/docsUrl': { docsUrl: () => '' },
    '@/utils/memberRoles': { allowedCreateRoles: () => [], canCreateAccount: () => false, canResetTarget: () => false },
    ...realAuditDeps,
    '@/api/tenant/members': {
      listMembers: async () => ({ success: true, data: { members: [], total: 0, page: 1, page_size: 20 } }),
    },
    '@/api/tenant/invitations': {
      listTenantInvitations: async () => ({ success: true, data: { invitations: [], total: 0 } }),
    },
    '@/api/tenant/audit-log': {
      listAuditLog: async (...args: any[]) => {
        auditCalls.push(args)
        return { success: true, data: [], next_cursor: 0 }
      },
    },
  })
  await flush()

  vm.openAuditDrawer()
  await flush()

  assert.equal(auditCalls.length, 1, '打开审计抽屉发起一次请求')
  assert.equal(auditCalls[0]![0], 7)
  const params = auditCalls[0]![1] ?? {}
  assert.equal('include_scoped' in params, false, 'TenantMembers 不传 include_scoped（旧请求形态逐字节不变）')
  assert.equal(JSON.stringify(params).includes('include_scoped'), false, '序列化后也不含该参数')
  assert.deepEqual(Object.keys(params).sort(), ['after_id', 'limit'], '参数集合 = 旧形态 after_id+limit')
})

test('T05-C2 源码守护：OperationLog 传 true、TenantMembers 调用块不出现 include_scoped', () => {
  const operationsSource = readFileSync(OPERATIONS, 'utf8')
  const membersSource = readFileSync(TENANT_MEMBERS, 'utf8')

  assert.match(operationsSource, /include_scoped:\s*true/, 'OperationLog 源码必须显式传 true')

  const start = membersSource.indexOf('const resp = await listAuditLog(')
  const end = membersSource.indexOf('})', start)
  assert.ok(start >= 0 && end > start, '能定位 TenantMembers 的 listAuditLog 调用块')
  assert.equal(
    membersSource.slice(start, end).includes('include_scoped'),
    false,
    'TenantMembers 调用块不得出现 include_scoped',
  )
})

test('T05-D 8 个新动作 zh-CN 显示名逐条渲染，且不露英文 action 串（条件 1/5）', async (t) => {
  const rows = SCOPED_ACTIONS.map((action, i) => row(100 + i, action))
  const { vm } = mountLog(t, rows)
  await settle()

  const bag = resolveLocalePath(zhCN, AUDIT_ACTION_I18N_ROOTS.tenantMember) as Record<string, string>

  // 表格单元格走 <t-tag>{{ actionLabel(row.action) }}</t-tag>；结构 renderer 下
  // t-table 是未注册的 host 标签、作用域插槽不会执行，故直接执行同一行内绑定的
  // actionLabel，并用源码断言锁定「动作单元格确实调用它」。
  const operationsSource = readFileSync(OPERATIONS, 'utf8')
  assert.match(operationsSource, /\{\{\s*actionLabel\(row\.action\)\s*\}\}/, '动作单元格绑定 actionLabel')

  for (const action of SCOPED_ACTIONS) {
    const expected = bag[action]
    assert.ok(typeof expected === 'string' && expected.length > 0, `zh-CN bundle 必须有 ${action} 显示名`)
    assert.equal(vm.actionLabel(action), expected, `${action} 渲染为「${expected}」`)
    assert.notEqual(vm.actionLabel(action), action, `${action} 英文串不得出现在单元格`)
  }

  // 与 T05 notes §2 的 zh-CN 口径逐字核对（防止 bundle 被静默改写）。
  assert.deepEqual(
    SCOPED_ACTIONS.map((action) => bag[action]),
    ['上传文档', '更新文档', '删除文档', '批量删除文档', '重新解析', '创建知识库', '更新知识库', '删除知识库'],
  )
})

test('T05-E api 层 include_scoped：true 拼参、缺省/false 不拼（条件 1）', (t) => {
  const f = fixture(t)
  const urls: string[] = []
  // fixture.evaluate 的 VM 上下文没有 URLSearchParams，先注入（沿 useKnowledgeBase.test 先例）。
  const api = f.evaluate(`import { URLSearchParams } from 'node:url'\n${readFileSync(AUDIT_LOG_API, 'utf8')}`, {
    'node:url': { URLSearchParams },
    '@/utils/request': {
      get: async (url: string) => {
        urls.push(url)
        return {}
      },
    },
  }) as { listAuditLog: (tenantId: number, params?: Record<string, unknown>) => Promise<unknown> }

  return (async () => {
    await api.listAuditLog(7, { include_scoped: true })
    assert.equal(urls[0], '/api/v1/tenants/7/audit-log?include_scoped=true', 'true 拼出 include_scoped=true')

    await api.listAuditLog(7, {})
    assert.equal(urls[1], '/api/v1/tenants/7/audit-log', '缺省不拼参数（旧请求形态）')

    await api.listAuditLog(7, { include_scoped: false })
    assert.equal(urls[2], '/api/v1/tenants/7/audit-log', 'false 同样不拼参数')

    // TenantMembers 的真实入参形态：after_id 缺省 + limit，URL 不得含 include_scoped。
    await api.listAuditLog(7, { after_id: undefined, limit: 50 })
    assert.equal(urls[3], '/api/v1/tenants/7/audit-log?limit=50', 'TenantMembers 形态参数不含 include_scoped')

    // 与其它筛选组合时参数顺序保持既有实现顺序，include_scoped 追加在末尾。
    await api.listAuditLog(7, { limit: 50, action: 'kb.created', q: '文档', include_scoped: true })
    assert.equal(
      urls[4],
      '/api/v1/tenants/7/audit-log?limit=50&action=kb.created&q=%E6%96%87%E6%A1%A3&include_scoped=true',
      '组合参数时 include_scoped 追加在末尾',
    )
  })()
})
