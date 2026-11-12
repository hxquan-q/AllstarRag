/**
 * T43 / GPT #2：OperationLog 关键词防抖窗口竞态。
 *
 * 关键词 watcher 一触发就必须立即失效旧请求（递增请求序号）；旧请求在
 * 300ms 防抖触发前 resolve/reject，不得写回 rows/cursor/hasMore/error/
 * loading。防抖窗口内「加载更多」也不得用旧关键词的游标发请求；防抖
 * 触发后新关键词请求应正常写回。
 *
 * 沿用 vueFixture 打桩模式，执行真实 OperationLog.vue 脚本。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)

const i18n = {
  useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: { value: 'zh-CN' } }),
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (error: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

interface AuditCall {
  args: any[]
  deferred: Deferred<any>
}

const row = (id: number) => ({ id, action: 'rbac.member_banned', created_at: '2026-11-09T00:00:00Z' })

function mountLog(t: TestContext) {
  const calls: AuditCall[] = []
  const f = fixture(t)
  const { vm } = f.component(OPERATIONS, {
    'vue-i18n': i18n,
    '@/api/tenant/audit-log': {
      listAuditLog: (...args: any[]) => {
        const d = deferred<any>()
        calls.push({ args, deferred: d })
        return d.promise
      },
    },
    '@/api/user-management': {
      listUsers: async () => ({ data: { items: [] } }),
      resolveUserManagementError: (error: unknown) => `resolved:${String(error)}`,
    },
    '@/i18n/auditActionLabel': { auditActionLabel: (_t: unknown, action: string) => action },
    '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' } },
  }, { tenantId: 7 })
  return { vm: vm as any, calls }
}

/** 只改关键词并让 watcher 执行，不推进 300ms 防抖定时器。 */
async function changeKeywordInDebounceWindow(vm: any, keyword: string) {
  vm.keyword = keyword
  await flush()
}

function snapshot(vm: any) {
  return {
    entries: vm.entries.map((item: any) => item.id),
    cursor: vm.cursor,
    hasMore: vm.hasMore,
    error: vm.error,
    loading: vm.loading,
  }
}

test('T43 #2 防抖窗口内旧请求 resolve 不得写回 rows/cursor/loading', async (t) => {
  const { vm, calls } = mountLog(t)
  await flush()
  assert.equal(calls.length, 1, '首屏应发起一次请求')
  assert.equal(vm.loading, true, '旧请求仍在飞行中')

  await changeKeywordInDebounceWindow(vm, 'alpha')
  assert.equal(calls.length, 1, '防抖未触发前不得发新请求')
  const before = snapshot(vm)

  // 旧请求在防抖触发前返回：必须整体丢弃。
  calls[0]!.deferred.resolve({ success: true, data: [row(1)], next_cursor: 88 })
  await flush()

  const after = snapshot(vm)
  assert.deepEqual(after.entries, before.entries, '旧响应不得写入 rows')
  assert.equal(after.cursor, before.cursor, '旧响应不得写入游标')
  assert.equal(after.hasMore, before.hasMore, '旧响应不得写入 hasMore')
  assert.equal(after.error, before.error, '旧响应不得写入 error')
  assert.equal(after.loading, before.loading, '旧 finally 不得改写 loading')

  // 防抖触发后新关键词请求正常。
  t.mock.timers.tick(300)
  await flush()
  assert.equal(calls.length, 2, '防抖触发后应发起新关键词请求')
  assert.equal(calls[1]!.args[1].q, 'alpha')
  assert.equal(calls[1]!.args[1].after_id, undefined, '新关键词从第一页开始')

  calls[1]!.deferred.resolve({ success: true, data: [row(2)], next_cursor: 0 })
  await flush()
  assert.deepEqual(vm.entries.map((item: any) => item.id), [2], '新请求结果正常写回')
  assert.equal(vm.cursor, 0)
  assert.equal(vm.loading, false)
})

test('T43 #2 防抖窗口内旧请求 reject 不得写回 error/loading', async (t) => {
  const { vm, calls } = mountLog(t)
  await flush()
  assert.equal(calls.length, 1)

  await changeKeywordInDebounceWindow(vm, 'beta')
  assert.equal(calls.length, 1, '防抖未触发前不得发新请求')
  const before = snapshot(vm)

  // 旧请求在防抖触发前失败：错误与 finally 都必须丢弃。
  calls[0]!.deferred.reject(Object.assign(new Error('boom'), { status: 500 }))
  await flush()

  const after = snapshot(vm)
  assert.equal(after.error, before.error, '旧错误不得写入新关键词的状态')
  assert.equal(after.loading, before.loading, '旧 finally 不得改写 loading')
  assert.deepEqual(after.entries, before.entries, '旧错误不得留下脏结果')

  // 防抖触发后新请求仍能正常返回。
  t.mock.timers.tick(300)
  await flush()
  assert.equal(calls.length, 2)
  assert.equal(calls[1]!.args[1].q, 'beta')

  calls[1]!.deferred.resolve({ success: true, data: [row(3)], next_cursor: 0 })
  await flush()
  assert.equal(vm.error, '')
  assert.deepEqual(vm.entries.map((item: any) => item.id), [3])
  assert.equal(vm.loading, false)
})

test('T43 #2 防抖窗口内 load-more 不得使用旧游标', async (t) => {
  const { vm, calls } = mountLog(t)
  await flush()

  // 首屏返回带游标，证明「加载更多」本应使用游标。
  calls[0]!.deferred.resolve({ success: true, data: [row(1)], next_cursor: 456 })
  await flush()
  assert.equal(vm.cursor, 456)
  assert.equal(vm.hasMore, true)
  assert.equal(vm.loading, false)

  await changeKeywordInDebounceWindow(vm, 'gamma')
  assert.equal(calls.length, 1, '防抖未触发前不得发新请求')

  // 防抖等待期间点「加载更多」：不得用旧游标 + 新关键词拼出请求。
  // 不 await：旧实现会挂起等待一个本不该发出的请求，测试只关心是否发请求。
  void vm.load(false)
  await flush()
  assert.equal(calls.length, 1, '防抖窗口内 load(false) 不得发起请求')

  // 防抖触发后新关键词请求从第一页开始。
  t.mock.timers.tick(300)
  await flush()
  assert.equal(calls.length, 2)
  assert.equal(calls[1]!.args[1].q, 'gamma')
  assert.equal(calls[1]!.args[1].after_id, undefined, '关键词变化必须从第一页开始')

  calls[1]!.deferred.resolve({ success: true, data: [row(2)], next_cursor: 0 })
  await flush()
  assert.deepEqual(vm.entries.map((item: any) => item.id), [2], '新结果替换旧列表')
  assert.equal(vm.cursor, 0)
})
