/**
 * T36 / AC-1：操作记录搜索竞态。
 *
 * 用可控 Promise 模拟两次 listAuditLog：先发关键词 A，在 A 未返回时改关键词 B，
 * 验证只有最新一轮请求能写回结果/错误/loading，且关键词变化会重置游标。
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

function changeKeyword(t: TestContext, vm: any, keyword: string) {
  vm.keyword = keyword
  return (async () => {
    await flush()
    t.mock.timers.tick(300)
    await flush()
  })()
}

test('T36 AC-1 旧响应不覆盖新关键词结果（先 A 后 B，A 迟到）', async (t) => {
  const { vm, calls } = mountLog(t)
  await flush()
  assert.equal(calls.length, 1)
  assert.equal(calls[0]!.args[1].q, undefined)

  await changeKeyword(t, vm, 'alpha')
  assert.equal(calls.length, 2, '防抖 300ms 后应发起新请求')
  assert.equal(calls[1]!.args[1].q, 'alpha')
  assert.equal(calls[1]!.args[1].after_id, undefined, '关键词重置游标')

  // 新请求先返回，写入 alpha 结果。
  calls[1]!.deferred.resolve({ success: true, data: [row(2)], next_cursor: 0 })
  await flush()
  assert.deepEqual(vm.entries.map((item: any) => item.id), [2])

  // 旧请求迟到：结果必须被丢弃，不能覆盖 alpha。
  calls[0]!.deferred.resolve({ success: true, data: [row(1)], next_cursor: 88 })
  await flush()
  assert.deepEqual(vm.entries.map((item: any) => item.id), [2])
  assert.equal(vm.cursor, 0, '旧响应的游标不得写回')
  assert.equal(vm.loading, false, '新请求已结束，loading 应为 false')
})

test('T36 AC-1 旧错误不覆盖新请求，旧 finally 不清新 loading', async (t) => {
  const { vm, calls } = mountLog(t)
  await flush()

  await changeKeyword(t, vm, 'beta')
  assert.equal(calls.length, 2)
  assert.equal(vm.loading, true, '新请求飞行中')

  // 旧请求失败迟到：不能把错误写进新关键词的状态。
  calls[0]!.deferred.reject(Object.assign(new Error('boom'), { status: 500 }))
  await flush()
  assert.equal(vm.error, '', '旧错误不得覆盖新请求')
  assert.equal(vm.loading, true, '旧 finally 不得清掉新请求的 loading')
  assert.equal(vm.entries.length, 0, '旧错误不能留下脏结果')

  calls[1]!.deferred.resolve({ success: true, data: [row(3)], next_cursor: 0 })
  await flush()
  assert.equal(vm.error, '')
  assert.deepEqual(vm.entries.map((item: any) => item.id), [3])
  assert.equal(vm.loading, false)
})

test('T36 AC-1 关键词变化重置 after_id；loading 中 load(false) 忽略，非 loading 用游标', async (t) => {
  const { vm, calls } = mountLog(t)
  await flush()

  // 首屏返回带游标，证明「加载更多」本应使用游标。
  calls[0]!.deferred.resolve({ success: true, data: [row(1)], next_cursor: 456 })
  await flush()
  assert.equal(vm.cursor, 456)
  assert.equal(vm.hasMore, true)

  await changeKeyword(t, vm, 'gamma')
  assert.equal(calls.length, 2)
  assert.equal(calls[1]!.args[1].q, 'gamma')
  assert.equal(calls[1]!.args[1].after_id, undefined, '关键词变化必须从第一页开始')

  // 新请求仍飞行中：加载更多应被忽略。
  assert.equal(vm.loading, true)
  await vm.load(false)
  assert.equal(calls.length, 2, 'loading 中 load(false) 不得发请求')

  calls[1]!.deferred.resolve({ success: true, data: [row(2)], next_cursor: 789 })
  await flush()
  assert.equal(vm.loading, false)

  // 非 loading 时「加载更多」使用服务端游标（该请求保持飞行，由测试收尾）。
  void vm.load(false)
  await flush()
  assert.equal(calls.length, 3)
  assert.equal(calls[2]!.args[1].after_id, 789)
  assert.equal(calls[2]!.args[1].q, 'gamma')
})
