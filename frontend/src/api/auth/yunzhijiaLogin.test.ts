/**
 * T09：云之家登录 API 契约。
 *
 * - body 字段必须是 `ticket`（后端 logger 的 sanitizeBody 依赖这个字段名）；
 * - API 层不得吞掉 401/503/400 的 status 与 server message，错误页要能拿到；
 * - 只替换 request 层，不触网。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { fixture } from '../../test-utils/vueFixture.ts'

const AUTH_API = new URL('./index.ts', import.meta.url)

type PostCall = { url: string; body: unknown }

function loadApi(t: Parameters<typeof fixture>[0], post: (url: string, body: unknown) => Promise<unknown>) {
  const f = fixture(t)
  const calls: PostCall[] = []
  const api = f.evaluate(readFileSync(AUTH_API, 'utf8'), {
    '@/utils/request': {
      post: (url: string, body: unknown) => {
        calls.push({ url, body })
        return post(url, body)
      },
      get: () => Promise.reject(new Error('unused')),
      put: () => Promise.reject(new Error('unused')),
    },
    '@/i18n': { __esModule: true, default: { global: { t: (key: string) => key } } },
  })
  return { api: api as { loginWithYunzhijia: (ticket: string) => Promise<unknown> }, calls }
}

test('loginWithYunzhijia posts { ticket } to the Yunzhijia login endpoint and returns the response', async (t) => {
  const response = { success: true, token: 'tok', user: { id: 'u1' } }
  const { api, calls } = loadApi(t, async () => response)

  const result = await api.loginWithYunzhijia('T-abc')

  assert.deepEqual(result, response)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].url, '/api/v1/auth/yunzhijia/login')
  assert.deepEqual(JSON.parse(JSON.stringify(calls[0].body)), { ticket: 'T-abc' })
  assert.deepEqual(Object.keys(calls[0].body as object), ['ticket'])
})

test('loginWithYunzhijia propagates a 401 business rejection with its server message', async (t) => {
  const rejection = Object.assign(new Error('登录凭证已失效，请从云之家重新打开'), {
    status: 401,
    message: '登录凭证已失效，请从云之家重新打开',
  })
  const { api } = loadApi(t, async () => Promise.reject(rejection))

  await assert.rejects(
    () => api.loginWithYunzhijia('bad-ticket'),
    (error: unknown) => {
      const err = error as { status?: number; message?: string }
      assert.equal(err.status, 401)
      assert.equal(err.message, '登录凭证已失效，请从云之家重新打开')
      return true
    },
  )
})

test('loginWithYunzhijia propagates a 503 server message unchanged', async (t) => {
  const rejection = Object.assign(new Error('系统暂不可用，请联系管理员'), {
    status: 503,
    message: '系统暂不可用，请联系管理员',
  })
  const { api } = loadApi(t, async () => Promise.reject(rejection))

  await assert.rejects(
    () => api.loginWithYunzhijia('T'),
    (error: unknown) => {
      const err = error as { status?: number; message?: string }
      assert.equal(err.status, 503)
      assert.equal(err.message, '系统暂不可用，请联系管理员')
      return true
    },
  )
})

test('loginWithYunzhijia propagates a 400 server message unchanged', async (t) => {
  const rejection = Object.assign(new Error('请求参数错误'), {
    status: 400,
    message: '请求参数错误',
  })
  const { api } = loadApi(t, async () => Promise.reject(rejection))

  await assert.rejects(
    () => api.loginWithYunzhijia('T'),
    (error: unknown) => {
      const err = error as { status?: number; message?: string }
      assert.equal(err.status, 400)
      assert.equal(err.message, '请求参数错误')
      return true
    },
  )
})
