/**
 * T09：云之家登录失败页。
 *
 * 错误页拿到的 error 形状是 request 层 reject 的 `{ status, message }`。
 * 钉死三件事：
 *   1. 400/401/503/429 与后端固定文案映射到本地化 key；
 *   2. 未知文案/状态回落到通用提示，不把服务端原文抛给用户；
 *   3. 错误页绝不回显 ticket（测试用假串，避免真实凭证）。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { findByTag, mountRendered, textOf } from '../user-management/t46SfcMount.ts'

const COMPONENT = new URL('./YunzhijiaLoginError.vue', import.meta.url)

// 一个 test() 内多次挂载时，fixture 会重复 enable mock timers；两次之间 reset。
const usedContexts = new WeakSet<object>()

function renderError(t: Parameters<typeof mountRendered>[0], error: unknown) {
  if (usedContexts.has(t)) t.mock.timers.reset()
  usedContexts.add(t)
  return mountRendered(t, {
    component: COMPONENT,
    props: { error },
    dependencies: {
      'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
      '@/brand': { brandConfig: { productName: 'Allstar' } },
    },
  })
}

function renderedText(t: Parameters<typeof mountRendered>[0], error: unknown): string {
  const { root } = renderError(t, error)
  return textOf(root)
}

test('400 maps to the invalid-ticket message', (t) => {
  const text = renderedText(t, { status: 400, message: 'parameter error' })
  assert.match(text, /auth\.yunzhijia\.errorInvalidTicket/)
  assert.match(text, /auth\.yunzhijia\.errorTitle/)
  assert.match(text, /auth\.yunzhijia\.backToLogin/)
})

test('503 maps to unavailable and 429 maps to rate-limited', (t) => {
  assert.match(renderedText(t, { status: 503 }), /auth\.yunzhijia\.errorUnavailable/)
  assert.match(renderedText(t, { status: 429 }), /auth\.yunzhijia\.errorRateLimited/)
})

test('401 server messages map to their localized reasons', (t) => {
  const cases: Array<[string, string]> = [
    ['登录凭证已失效，请从云之家重新打开', 'errorInvalidTicket'],
    ['账号开通中，请明天再试', 'errorProvisioning'],
    ['在职员工存在同名，请联系管理员', 'errorDuplicateName'],
    ['账号不可用，请联系管理员', 'errorAccountBlocked'],
    ['系统暂不可用，请联系管理员', 'errorUnavailable'],
    ['云之家服务暂时不可用，请稍后再试', 'errorUpstream'],
  ]

  for (const [message, key] of cases) {
    const text = renderedText(t, { status: 401, message })
    assert.match(text, new RegExp(`auth\\.yunzhijia\\.${key}`), `401 "${message}" should map to ${key}`)
  }
})

test('unknown server message falls back to the generic message', (t) => {
  const text = renderedText(t, { status: 401, message: 'some unexpected backend text' })
  assert.match(text, /auth\.yunzhijia\.errorGeneric/)
  assert.doesNotMatch(text, /some unexpected backend text/)
})

test('unknown status without a message falls back to generic', (t) => {
  assert.match(renderedText(t, { status: 500 }), /auth\.yunzhijia\.errorGeneric/)
  assert.match(renderedText(t, undefined), /auth\.yunzhijia\.errorGeneric/)
})

test('the rendered page never echoes a ticket or raw error text', (t) => {
  const fakeTicket = 'T09-SECRET-TICKET-123'
  const text = renderedText(t, {
    status: 401,
    message: `ticket=${fakeTicket} 无法识别`,
    ticket: fakeTicket,
  })

  assert.match(text, /auth\.yunzhijia\.errorGeneric/)
  assert.doesNotMatch(text, new RegExp(fakeTicket))
  assert.doesNotMatch(text, /ticket=/i)
})

test('the back-to-login action points at /login', (t) => {
  const { root } = renderError(t, { status: 400 })
  const anchors = findByTag(root, 'a')
  assert.equal(anchors.length, 1)
  assert.equal(anchors[0].props.href, '/login')
})
