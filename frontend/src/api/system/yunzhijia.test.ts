/**
 * T10：云之家同步管理端 API 契约。
 *
 * 钉住四件事：
 * 1. 四个管理接口的路径与 HTTP 方法（后端 T08 已实现，前端不能拼错）；
 * 2. 预检 / 手动同步必须带 120s 超时，避免浏览器先断而后台还在跑；
 * 3. 状态 / 解除 blocked 不覆盖超时，沿用 request 层默认 30s；
 * 4. 响应原样透传，API 层不吞字段。
 *
 * 只替换 request 层，不触网。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { fixture } from '../../test-utils/vueFixture.ts'

const API = new URL('./yunzhijia.ts', import.meta.url)
const BASE = '/api/v1/system/admin/yunzhijia'

type Call = { method: 'get' | 'post'; url: string; data?: unknown; config?: any }

function loadApi(
  t: Parameters<typeof fixture>[0],
  response: () => Promise<unknown> = async () => ({}),
) {
  const f = fixture(t)
  const calls: Call[] = []
  const api = f.evaluate(readFileSync(API, 'utf8'), {
    '@/utils/request': {
      get: (url: string, config?: unknown) => {
        calls.push({ method: 'get', url, config })
        return response()
      },
      post: (url: string, data?: unknown, config?: unknown) => {
        calls.push({ method: 'post', url, data, config })
        return response()
      },
    },
  })
  return { api, calls }
}

test('YUNZHIJIA_SYNC_TIMEOUT_MS is 120 seconds', (t) => {
  const { api } = loadApi(t)
  assert.equal(api.YUNZHIJIA_SYNC_TIMEOUT_MS, 120_000)
})

test('getYunzhijiaSyncStatus GETs the sync status endpoint without overriding the default timeout', async (t) => {
  const payload = { enabled: true, latest: { id: 1 }, blocked: [] }
  const { api, calls } = loadApi(t, async () => payload)

  const result = await api.getYunzhijiaSyncStatus()

  assert.equal(calls.length, 1)
  assert.equal(calls[0]!.method, 'get')
  assert.equal(calls[0]!.url, `${BASE}/sync`)
  assert.equal(calls[0]!.config, undefined, '状态接口沿用 request 默认超时')
  assert.deepEqual(JSON.parse(JSON.stringify(result)), payload)
})

test('previewYunzhijiaSync POSTs to /sync/preview with the 120s timeout', async (t) => {
  const payload = { active_count: 10, inactive_count: 2, tenant_default_count: 8, tenant_hr_count: 2, anomalies: [] }
  const { api, calls } = loadApi(t, async () => payload)

  const result = await api.previewYunzhijiaSync()

  assert.equal(calls.length, 1)
  assert.equal(calls[0]!.method, 'post')
  assert.equal(calls[0]!.url, `${BASE}/sync/preview`)
  assert.deepEqual(JSON.parse(JSON.stringify(calls[0]!.data)), {})
  assert.equal(calls[0]!.config.timeout, 120_000)
  assert.deepEqual(JSON.parse(JSON.stringify(result)), payload)
})

test('runYunzhijiaSync POSTs to /sync with the 120s timeout', async (t) => {
  const payload = { id: 9, trigger: 'manual', status: 'success' }
  const { api, calls } = loadApi(t, async () => payload)

  const result = await api.runYunzhijiaSync()

  assert.equal(calls.length, 1)
  assert.equal(calls[0]!.method, 'post')
  assert.equal(calls[0]!.url, `${BASE}/sync`)
  assert.deepEqual(JSON.parse(JSON.stringify(calls[0]!.data)), {})
  assert.equal(calls[0]!.config.timeout, 120_000)
  assert.deepEqual(JSON.parse(JSON.stringify(result)), payload)
})

test('unblockYunzhijiaEmployee POSTs the encoded open_id and keeps the default timeout', async (t) => {
  const payload = { success: true }
  const { api, calls } = loadApi(t, async () => payload)
  const openId = 'oid 中文/1?x=2'

  const result = await api.unblockYunzhijiaEmployee(openId)

  assert.equal(calls.length, 1)
  assert.equal(calls[0]!.method, 'post')
  assert.equal(
    calls[0]!.url,
    `${BASE}/employees/${encodeURIComponent(openId)}/unblock`,
  )
  assert.ok(!calls[0]!.url!.includes(' '), 'open_id 必须 URL 编码')
  assert.deepEqual(JSON.parse(JSON.stringify(calls[0]!.data)), {})
  assert.equal(calls[0]!.config, undefined)
  assert.deepEqual(JSON.parse(JSON.stringify(result)), payload)
})

test('API failures propagate unchanged so the panel can map status codes', async (t) => {
  const rejection = Object.assign(new Error('running'), { status: 409 })
  const { api } = loadApi(t, async () => Promise.reject(rejection))

  await assert.rejects(
    () => api.runYunzhijiaSync(),
    (error: unknown) => {
      assert.equal((error as { status?: number }).status, 409)
      return true
    },
  )
})
