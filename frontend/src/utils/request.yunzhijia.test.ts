/**
 * T09 / AC-17：云之家 ticket 登录是公开接口，401 是业务拒绝，不能触发
 * token refresh（否则会把用户重定向去登录页，错误页永远看不到 server message）。
 *
 * 直接求值 request.ts，捕获它注册的 response 错误拦截器；axios 用替身，
 * 不触网、不改实现。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { fixture } from '../test-utils/vueFixture.ts'

const REQUEST_TS = new URL('./request.ts', import.meta.url)
const YUNZHIJIA_URL = '/api/v1/auth/yunzhijia/login'

type ErrorHandler = (error: unknown) => Promise<unknown>

function loadRequest(t: Parameters<typeof fixture>[0], options: { timeout?: boolean } = {}) {
  const f = fixture(t)
  const retriedRequests: Array<{ url?: string; headers: Record<string, string> }> = []
  const refreshCalls: unknown[] = []
  let responseErrorHandler: ErrorHandler | undefined

  const fakeInstance = Object.assign(
    (config: { url?: string; headers: Record<string, string> }) => {
      retriedRequests.push(config)
      return Promise.resolve({})
    },
    {
      interceptors: {
        request: { use: () => {} },
        response: {
          use: (_onFulfilled: unknown, onRejected: ErrorHandler) => {
            responseErrorHandler = onRejected
          },
        },
      },
      defaults: { headers: {} },
    },
  )

  f.evaluate(readFileSync(REQUEST_TS, 'utf8'), {
    axios: { create: () => fakeInstance },
    './index': {
      generateRandomString: () => 'request-id',
      MAX_FILE_SIZE_MB: 100,
      MAX_SKILL_BUNDLE_SIZE_MB: 100,
    },
    '@/i18n': {
      __esModule: true,
      default: { global: { t: (key: string) => key, locale: { value: 'zh-CN' } } },
    },
    './api-base': { getApiBaseUrl: () => '' },
    './uploadLimit': { isSkillBundleUploadUrl: () => false },
    './requestTimeouts': { isTimeoutError: () => options.timeout === true, uploadTimeoutMs: 1000 },
    './authRefresh': {
      forceReloginRedirect: () => {},
      isEmbedPage: () => false,
      refreshAccessTokenShared: async (args: unknown) => {
        refreshCalls.push(args)
        return 'refreshed-token'
      },
    },
  })

  assert.equal(typeof responseErrorHandler, 'function', 'response interceptor must be registered')
  return {
    handle: (error: unknown) => responseErrorHandler!(error),
    retriedRequests,
    refreshCalls,
  }
}

function yunzhijia401(message: string) {
  return {
    config: { url: YUNZHIJIA_URL, headers: {} as Record<string, string> },
    response: { status: 401, data: { error: { message } } },
  }
}

test('a 401 from the Yunzhijia login endpoint is surfaced without token refresh', async (t) => {
  const { handle, refreshCalls, retriedRequests } = loadRequest(t)

  await assert.rejects(
    () => handle(yunzhijia401('登录凭证已失效，请从云之家重新打开')),
    (error: unknown) => {
      const err = error as { status?: number; message?: string }
      assert.equal(err.status, 401)
      assert.equal(err.message, '登录凭证已失效，请从云之家重新打开')
      return true
    },
  )

  assert.equal(refreshCalls.length, 0, 'public Yunzhijia login must not attempt refresh')
  assert.equal(retriedRequests.length, 0, 'request must not be retried')
})

test('a 401 from a protected endpoint still attempts the normal refresh', async (t) => {
  const { handle, refreshCalls, retriedRequests } = loadRequest(t)
  const originalRequest = {
    url: '/api/v1/knowledge-bases',
    headers: {} as Record<string, string>,
    _retry: false,
  }

  await handle({
    config: originalRequest,
    response: { status: 401, data: {} },
  })

  assert.equal(refreshCalls.length, 1)
  assert.equal(originalRequest._retry, true)
  assert.equal(retriedRequests.length, 1)
  assert.equal(originalRequest.headers.Authorization, 'Bearer refreshed-token')
})

test('a network failure without a response rejects with the localized message and no status', async (t) => {
  const { handle, refreshCalls, retriedRequests } = loadRequest(t)

  await assert.rejects(
    () => handle({ config: { url: YUNZHIJIA_URL, headers: {} } }),
    (error: unknown) => {
      // request.ts 在 fixture 的 VM 里求值，跨 realm 对象不能用 deepEqual；
      // i18n 在 fixture 里被替身为返回 key，故断言 i18n key 本身。
      const err = error as { status?: number; message?: string }
      assert.equal(err.message, 'error.networkError')
      assert.equal(err.status, undefined)
      return true
    },
  )

  assert.equal(refreshCalls.length, 0, 'network failure must not attempt refresh')
  assert.equal(retriedRequests.length, 0, 'network failure must not be retried')
})

test('a timeout without a response rejects with the localized timeout message', async (t) => {
  const { handle } = loadRequest(t, { timeout: true })

  await assert.rejects(
    () => handle({ config: { url: YUNZHIJIA_URL, headers: {} } }),
    (error: unknown) => {
      const err = error as { status?: number; message?: string }
      assert.equal(err.message, 'error.requestTimeout')
      assert.equal(err.status, undefined)
      return true
    },
  )
})

test('503 from the Yunzhijia endpoint keeps the server message for the error page', async (t) => {
  const { handle } = loadRequest(t)

  await assert.rejects(
    () =>
      handle({
        config: { url: YUNZHIJIA_URL, headers: {} },
        response: { status: 503, data: { error: { message: '系统暂不可用，请联系管理员' } } },
      }),
    (error: unknown) => {
      const err = error as { status?: number; message?: string }
      assert.equal(err.status, 503)
      assert.equal(err.message, '系统暂不可用，请联系管理员')
      return true
    },
  )
})
