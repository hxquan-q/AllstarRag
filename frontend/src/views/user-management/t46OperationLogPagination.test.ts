/**
 * T46 红阶段（一）：OperationLog 分页假终态（死路）。
 *
 * 现状缺陷：visibleRows 前端过滤隐藏类型后，若整页都是隐藏类型
 * （rbac.access_denied 等），模板走 v-else-if="visibleRows.length===0"
 * 的「暂无记录」空态；「加载更多」在 v-else 分支内不渲染——即使
 * hasMore=true，历史记录也永久不可达（分页死路）。
 *
 * 目标行为（03-tickets/T46.md 契约）：
 * - 空态条件改为 visibleRows.length === 0 && !hasMore；
 * - 「加载更多」在 hasMore=true 时始终可见，空页也能继续加载；
 * - 加载中/错误态语义不变。
 *
 * 基建：t46SfcMount 完整渲染真实 OperationLog.vue（模板分支真实执行），
 * listAuditLog / listUsers 打桩，host 树断言渲染了哪个分支。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { findByTag, mountRendered, settle, textOf } from './t46SfcMount.ts'

const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)

const i18n = {
  useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: { value: 'zh-CN' } }),
}

/** 成员操作行（可见类型）。 */
const memberRow = (id: number) => ({
  id,
  action: 'rbac.member_banned',
  created_at: '2026-11-10T00:00:00Z',
})

/** 隐藏类型行（access_denied 等噪音，被前端过滤）。 */
const hiddenRow = (id: number) => ({
  id,
  action: 'rbac.access_denied',
  created_at: '2026-11-10T00:00:00Z',
})

interface AuditPage {
  data?: Array<Record<string, unknown>>
  next_cursor?: number
  success?: boolean
  reject?: Error
}

/** 挂载完整 OperationLog；pages 按请求序返回响应（reject 表示失败页）。 */
function mountLog(t: TestContext, pages: AuditPage[]) {
  const calls: any[][] = []
  const mounted = mountRendered(t, {
    component: OPERATIONS,
    dependencies: {
      'vue-i18n': i18n,
      '@/api/tenant/audit-log': {
        listAuditLog: async (...args: any[]) => {
          calls.push(args)
          const page = pages[Math.min(calls.length - 1, pages.length - 1)]!
          if (page.reject) throw page.reject
          return {
            success: page.success ?? true,
            data: page.data ?? [],
            next_cursor: page.next_cursor ?? 0,
          }
        },
      },
      '@/api/user-management': {
        listUsers: async () => ({ data: { items: [] } }),
        resolveUserManagementError: (error: unknown) => `resolved:${String(error)}`,
      },
      '@/i18n/auditActionLabel': { auditActionLabel: (_t: unknown, action: string) => action },
      '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' } },
    },
    props: { tenantId: 7 },
  })
  return { ...mounted, calls }
}

/** 找「加载更多」按钮（工具栏「刷新」也是 t-button，用文案区分；i18n 打桩返回 key）。 */
function findLoadMore(root: ReturnType<typeof mountRendered>['root']) {
  return findByTag(root, 't-button').filter((el) =>
    textOf(el).includes('userManagement.operations.loadMore'),
  )
}

test('T46-P1 整页隐藏类型且 hasMore=true：不得进空态死路，加载更多可达第二页', async (t) => {
  const { root, vm, calls } = mountLog(t, [
    { data: [hiddenRow(1), hiddenRow(2)], next_cursor: 55 },
    { data: [memberRow(3)], next_cursor: 0 },
  ])
  await settle()

  assert.equal(calls.length, 1, '首屏一次请求')
  assert.equal(vm.hasMore, true, '服务端还有下一页（next_cursor=55）')
  assert.equal(vm.visibleRows.length, 0, '整页都是隐藏类型，前端无可显示行')

  // 空态不得出现：hasMore=true 时「暂无记录」是假终态，把加载更多一起吞掉。
  assert.equal(findByTag(root, 't-empty').length, 0, 'hasMore=true 不得渲染「暂无记录」空态')

  // 「加载更多」必须可见（当前页无可显示行也要能继续翻页）。
  const more = findLoadMore(root)
  assert.equal(more.length, 1, 'hasMore=true 时「加载更多」按钮必须渲染')

  // 点击加载更多：第二页含封禁记录，应正常出现在表格。
  ;(more[0]!.props.onClick as () => void)()
  await settle()
  assert.equal(calls.length, 2, '点击后发出第二页请求')
  assert.equal(calls[1]![1].after_id, 55, '第二页携带首屏游标')
  assert.ok(
    (vm.visibleRows as any[]).some((row) => row.id === 3),
    '封禁记录进入可见行',
  )
  const table = findByTag(root, 't-table')[0]!
  assert.ok(
    ((table.props.data as any[]) ?? []).some((row) => row.id === 3),
    '表格渲染第二页的封禁记录',
  )
  assert.equal(vm.hasMore, false, '第二页后无更多')
  assert.equal(findByTag(root, 't-empty').length, 0, '有可见行时无空态')
})

test('T46-P2 真终态：无可见行且 hasMore=false 时仍显示「暂无记录」', async (t) => {
  const { root, vm } = mountLog(t, [{ data: [hiddenRow(1)], next_cursor: 0 }])
  await settle()

  assert.equal(vm.hasMore, false)
  assert.equal(vm.visibleRows.length, 0)

  const empty = findByTag(root, 't-empty')
  assert.equal(empty.length, 1, '真终态必须渲染空态')
  assert.equal(empty[0]!.props.description, 'userManagement.operations.empty')
  assert.equal(findLoadMore(root).length, 0, 'hasMore=false 不渲染加载更多')
})

test('T46-P3 保护网：请求失败时错误态优先渲染，不被空态/表格吞掉', async (t) => {
  const { root, vm } = mountLog(t, [{ reject: new Error('boom') }])
  await settle()

  assert.equal(vm.error, 'resolved:Error: boom')
  assert.equal(findByTag(root, 't-alert').length, 1, '错误态渲染 t-alert')
  assert.equal(findByTag(root, 't-empty').length, 0, '错误时不渲染空态')
  assert.equal(findByTag(root, 't-table').length, 0, '错误时不渲染表格')
  assert.equal(findLoadMore(root).length, 0, '错误时不渲染加载更多')
})
