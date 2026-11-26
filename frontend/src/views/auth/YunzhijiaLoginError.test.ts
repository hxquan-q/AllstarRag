/**
 * T09：云之家登录失败页。
 *
 * 错误页拿到的 error 形状是 request 层 reject 的 `{ status?, message? }`。
 * 2026-11-25 审查清理后（PITFALLS G-20）不再用后端中文文案当逻辑判别键：
 *   1. 只要带了 server message 就原样展示（含 401 业务拒绝、网络/超时本地化文案）；
 *   2. 没有 message 才按状态码兜底：400→invalidTicket、429→rateLimited、
 *      503→unavailable、其它/未知→generic；
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

test('a server message is shown verbatim and wins over the status fallback', (t) => {
  const cases: Array<{ status: number; message: string }> = [
    { status: 401, message: '登录凭证已失效，请从云之家重新打开' },
    { status: 401, message: '账号开通中，请明天再试' },
    { status: 503, message: '云之家服务暂时不可用，请稍后再试' },
    { status: 400, message: 'parameter error' },
  ]

  for (const error of cases) {
    const text = renderedText(t, error)
    assert.ok(text.includes(error.message), `status ${error.status} should show "${error.message}"`)
    assert.match(text, /auth\.yunzhijia\.errorTitle/)
    assert.match(text, /auth\.yunzhijia\.backToLogin/)
    assert.doesNotMatch(
      text,
      /auth\.yunzhijia\.error(?:InvalidTicket|RateLimited|Unavailable|Generic)/,
      'server message must take precedence over localized fallbacks',
    )
  }
})

test('a 429 rate-limit response shows the localized hint, not the English server message', (t) => {
  // L2：后端限速中间件返回英文文案；中文界面必须优先本地化提示。
  const text = renderedText(t, {
    status: 429,
    message: 'too many requests, please try again later',
  })

  assert.match(text, /auth\.yunzhijia\.errorRateLimited/)
  assert.doesNotMatch(text, /too many requests/i,
    '英文限速文案不得覆盖本地化提示')
})

test('without a server message, the status code selects the localized fallback', (t) => {
  const cases: Array<[unknown, string]> = [
    [{ status: 400 }, 'errorInvalidTicket'],
    [{ status: 429 }, 'errorRateLimited'],
    [{ status: 503 }, 'errorUnavailable'],
  ]

  for (const [error, key] of cases) {
    const text = renderedText(t, error)
    assert.match(text, new RegExp(`auth\\.yunzhijia\\.${key}`), `${JSON.stringify(error)} should map to ${key}`)
    assert.doesNotMatch(text, /auth\.yunzhijia\.errorGeneric/)
  }
})

test('an empty message falls back to the status mapping', (t) => {
  assert.match(renderedText(t, { status: 400, message: '' }), /auth\.yunzhijia\.errorInvalidTicket/)
})

test('other, missing or non-numeric status without a message falls back to generic', (t) => {
  const cases: unknown[] = [
    { status: 500 },
    { status: 0 },
    { status: 401 },
    { status: '503' },
    {},
    undefined,
  ]

  for (const error of cases) {
    assert.match(
      renderedText(t, error),
      /auth\.yunzhijia\.errorGeneric/,
      `${JSON.stringify(error)} should fall back to generic`,
    )
  }
})

test('localized network and timeout messages from the request layer are displayed', (t) => {
  const messages = [
    '网络错误，请检查您的网络连接',
    '请求超时，文件较大或网络较慢时请稍后重试',
  ]

  for (const message of messages) {
    // request.ts 对无响应的网络错误/超时 reject `{ message }`，不带 status。
    const text = renderedText(t, { message })
    assert.ok(text.includes(message), `network-layer message "${message}" should be displayed`)
    assert.doesNotMatch(text, /auth\.yunzhijia\.errorGeneric/)
  }
})

test('an unrecognized server message is displayed as-is instead of the generic fallback', (t) => {
  const text = renderedText(t, { status: 401, message: 'some unexpected backend text' })
  assert.ok(text.includes('some unexpected backend text'))
  assert.doesNotMatch(text, /auth\.yunzhijia\.errorGeneric/)
})

test('the rendered page never echoes a ticket even though the server message is shown', (t) => {
  const fakeTicket = 'T09-SECRET-TICKET-123'
  const serverMessage = '账号开通中，请明天再试'
  const { root } = renderError(t, { status: 401, message: serverMessage, ticket: fakeTicket })
  const text = textOf(root)

  assert.ok(text.includes(serverMessage), 'server message must be displayed')
  assert.doesNotMatch(text, new RegExp(fakeTicket))
  assert.doesNotMatch(text, /ticket=/i)
  for (const anchor of findByTag(root, 'a')) {
    assert.ok(!String(anchor.props.href).includes(fakeTicket), 'links must not carry the ticket')
  }
})

test('the back-to-login action points at /login', (t) => {
  const { root } = renderError(t, { status: 400 })
  const anchors = findByTag(root, 'a')
  assert.equal(anchors.length, 1)
  assert.equal(anchors[0].props.href, '/login')
})
