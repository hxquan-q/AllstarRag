import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import type { TestContext } from 'node:test'
import { fixture } from '../../test-utils/vueFixture.ts'

/**
 * api/auth 偏好写入的错误通道（F15 / F5）。
 *
 * 非 2xx 时 request 层 reject 的就是响应体 {success:false,error:{code,message,details}}，
 * updateMyPreferences 必须把 error.details 透出为 errorCode（后端校验错误的稳定 code），
 * 组件层据此做 code 优先映射；这里直接钉住提取链，不依赖任何后端。
 */

test('updateMyPreferences surfaces error.details as errorCode when the request rejects (F15 / F5)', async t => {
  const f = fixture(t)
  const rejected = Object.assign(new Error('Request failed with status code 400'), {
    status: 400,
    error: { code: 400, message: 'bad request', details: 'bound_agent_limit' },
  })
  const api = f.evaluate(
    readFileSync(new URL('./index.ts', import.meta.url), 'utf8'),
    {
      '@/utils/request': {
        post: () => Promise.reject(new Error('unused in this test')),
        get: () => Promise.reject(new Error('unused in this test')),
        put: () => Promise.reject(rejected),
      },
      '@/i18n': { default: { global: { t: (key: string) => key } } },
    },
  )

  const result = await api.updateMyPreferences({ bound_agent_ids: ['agent-a'] })

  assert.equal(result.success, false)
  assert.equal(result.errorCode, 'bound_agent_limit')
  assert.equal(result.message, 'Request failed with status code 400')
})

test('updateMyPreferences leaves errorCode undefined when the rejection has no string details', async t => {
  const f = fixture(t)
  const rejected = Object.assign(new Error('network down'), { status: -1 })
  const api = f.evaluate(
    readFileSync(new URL('./index.ts', import.meta.url), 'utf8'),
    {
      '@/utils/request': {
        post: () => Promise.reject(new Error('unused in this test')),
        get: () => Promise.reject(new Error('unused in this test')),
        put: () => Promise.reject(rejected),
      },
      '@/i18n': { default: { global: { t: (key: string) => key } } },
    },
  )

  const result = await api.updateMyPreferences({ bound_agent_ids: ['agent-a'] })

  assert.equal(result.success, false)
  assert.equal(result.errorCode, undefined, '无 details 不得伪造 code，组件层退回 message 兜底')
})
