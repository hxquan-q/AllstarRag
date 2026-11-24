/**
 * T09：云之家 ticket 启动登录 helper。
 *
 * 这里只测 `utils/yunzhijiaLogin.ts` 的纯逻辑（readUrl / replaceUrl 全部注入），
 * 钉死顺序与失败语义：
 *   读 ticket → 立即清地址栏 → 清旧会话 → POST 换票 → 成功 persist + 切语言
 * 失败时旧会话已被清掉，调用方不能落回旧账号。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import {
  YUNZHIJIA_PARAM_KEYS,
  bootstrapYunzhijiaLogin,
  cleanYunzhijiaUrl,
  readYunzhijiaEntry,
  resolveYunzhijiaLocale,
  type LocationSnapshot,
  type YunzhijiaBootOptions,
  type YunzhijiaLocale,
} from './yunzhijiaLogin.ts'

const SUCCESS_RESPONSE = {
  success: true,
  token: 'token-1',
  refresh_token: 'refresh-1',
  user: { id: 'u1', username: 'employee', email: 'employee@example.com', tenant_id: 1 },
} as never

function snapshot(search: string, overrides: Partial<LocationSnapshot> = {}): LocationSnapshot {
  return {
    pathname: '/chat',
    search,
    hash: '',
    state: null,
    ...overrides,
  }
}

interface Harness {
  options: YunzhijiaBootOptions
  events: string[]
  replaced: Array<{ url: string; state: unknown }>
  loggedInWith: string[]
  persisted: unknown[]
  locales: YunzhijiaLocale[]
  cleared: number
}

function harness(overrides: Partial<YunzhijiaBootOptions> = {}): Harness {
  const events: string[] = []
  const replaced: Array<{ url: string; state: unknown }> = []
  const loggedInWith: string[] = []
  const persisted: unknown[] = []
  const locales: YunzhijiaLocale[] = []
  let cleared = 0

  const options: YunzhijiaBootOptions = {
    readUrl: () => snapshot(''),
    replaceUrl: (url, state) => {
      events.push('replace')
      replaced.push({ url, state })
    },
    clearSession: () => {
      events.push('clear')
      cleared += 1
    },
    login: async (ticket: string) => {
      events.push('login')
      loggedInWith.push(ticket)
      return SUCCESS_RESPONSE
    },
    persist: async (response) => {
      events.push('persist')
      persisted.push(response)
    },
    applyLocale: (locale) => {
      events.push('locale')
      locales.push(locale)
    },
    ...overrides,
  }

  return {
    options,
    events,
    replaced,
    loggedInWith,
    persisted,
    locales,
    get cleared() {
      return cleared
    },
  }
}

test('readYunzhijiaEntry trims ticket and yzjLang and ignores unknown params', () => {
  assert.deepEqual(readYunzhijiaEntry('?ticket=%20T-1%20&yzjLang=en-US&x=1'), {
    ticket: 'T-1',
    yzjLang: 'en-US',
  })
  assert.deepEqual(readYunzhijiaEntry('?appid=abc'), { ticket: '', yzjLang: '' })
})

test('cleanYunzhijiaUrl removes every Yunzhijia param but keeps other params and hash', () => {
  const cleaned = cleanYunzhijiaUrl(
    '/chat',
    '?ticket=T-1&appid=app&expire_time=99&yzjLang=ja-JP&client_id=c&msgShowStyle=1&invite=keep',
    '#/home',
  )
  assert.equal(cleaned, '/chat?invite=keep#/home')

  for (const key of YUNZHIJIA_PARAM_KEYS) {
    assert.doesNotMatch(cleaned, new RegExp(`[?&]${key}=`), `${key} must be removed`)
  }

  // 只带云之家参数时不能留下孤零零的 ?
  assert.equal(cleanYunzhijiaUrl('/chat', '?ticket=T-1', ''), '/chat')
})

test('resolveYunzhijiaLocale accepts supported locales and falls back to zh-CN', () => {
  for (const locale of ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'ko-KR', 'ru-RU'] as const) {
    assert.equal(resolveYunzhijiaLocale(locale), locale)
  }
  assert.equal(resolveYunzhijiaLocale('fr-FR'), 'zh-CN')
  assert.equal(resolveYunzhijiaLocale('en'), 'zh-CN')
  assert.equal(resolveYunzhijiaLocale(''), 'zh-CN')
})

test('ticket entry: replaceState happens before clear/POST, keeps invite param and history state', async () => {
  const h = harness({
    readUrl: () =>
      snapshot('?ticket=T-9&yzjLang=en-US&appid=app&invite=inv-1', {
        hash: '#/welcome',
        state: { from: 'yunzhijia' },
      }),
    login: async (ticket) => {
      h.events.push('login')
      h.loggedInWith.push(ticket)
      return SUCCESS_RESPONSE
    },
  })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'success' })
  assert.deepEqual(h.events, ['replace', 'clear', 'login', 'persist', 'locale'])
  assert.equal(h.replaced.length, 1)
  assert.equal(h.replaced[0].url, '/chat?invite=inv-1#/welcome')
  assert.deepEqual(h.replaced[0].state, { from: 'yunzhijia' })
  assert.deepEqual(h.loggedInWith, ['T-9'])
  assert.deepEqual(h.persisted, [SUCCESS_RESPONSE])
  assert.deepEqual(h.locales, ['en-US'])
})

test('success writes the ticket response and applies the normalized locale', async () => {
  const h = harness({ readUrl: () => snapshot('?ticket=T-2&yzjLang=fr-FR') })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'success' })
  assert.deepEqual(h.persisted, [SUCCESS_RESPONSE])
  assert.deepEqual(h.locales, ['zh-CN'], 'unsupported yzjLang falls back to zh-CN')
})

test('no ticket: never touches the address bar, session or login', async () => {
  const h = harness({ readUrl: () => snapshot('?yzjLang=en-US&invite=inv-1', { hash: '#/x' }) })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'none' })
  assert.deepEqual(h.events, [])
  assert.equal(h.replaced.length, 0)
  assert.equal(h.cleared, 0)
})

test('whitespace-only ticket is treated as no ticket', async () => {
  const h = harness({ readUrl: () => snapshot('?ticket=%20%20') })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'none' })
  assert.deepEqual(h.events, [])
})

test('login rejection returns error and never persists the old or new account', async () => {
  const failure = Object.assign(new Error('登录凭证已失效，请从云之家重新打开'), {
    status: 401,
    message: '登录凭证已失效，请从云之家重新打开',
  })
  const h = harness({
    readUrl: () => snapshot('?ticket=bad'),
    login: async () => {
      h.events.push('login')
      throw failure
    },
  })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'error', error: failure })
  assert.deepEqual(h.events, ['replace', 'clear', 'login'])
  assert.equal(h.cleared, 1, 'old session must be cleared before the failed login')
  assert.deepEqual(h.persisted, [])
  assert.deepEqual(h.locales, [])
})

test('success:false response is an error and does not persist', async () => {
  const response = { success: false, message: '系统暂不可用，请联系管理员' } as never
  const h = harness({
    readUrl: () => snapshot('?ticket=T-3'),
    login: async () => {
      h.events.push('login')
      return response
    },
  })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'error', error: response })
  assert.deepEqual(h.persisted, [])
  assert.deepEqual(h.locales, [])
})

test('a response without user/token is rejected before persist', async () => {
  const response = { success: true, token: 'only-token' } as never
  const h = harness({
    readUrl: () => snapshot('?ticket=T-4'),
    login: async () => {
      h.events.push('login')
      return response
    },
  })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'error', error: response })
  assert.deepEqual(h.persisted, [])
})

test('clearSession throwing aborts before login and still surfaces as an error', async () => {
  const failure = new Error('logout failed')
  const h = harness({
    readUrl: () => snapshot('?ticket=T-5'),
    clearSession: () => {
      h.events.push('clear')
      throw failure
    },
  })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'error', error: failure })
  assert.deepEqual(h.events, ['replace', 'clear'])
  assert.deepEqual(h.loggedInWith, [])
})

test('persist throwing turns a successful login into an error result', async () => {
  const failure = new Error('storage full')
  const h = harness({
    readUrl: () => snapshot('?ticket=T-6'),
    persist: async () => {
      h.events.push('persist')
      throw failure
    },
  })

  const result = await bootstrapYunzhijiaLogin(h.options)

  assert.deepEqual(result, { kind: 'error', error: failure })
})

test('the ticket is never present in the replacement URL', async () => {
  const h = harness({ readUrl: () => snapshot('?ticket=T-7') })

  await bootstrapYunzhijiaLogin(h.options)

  assert.equal(h.replaced.length, 1)
  assert.doesNotMatch(h.replaced[0].url, /T-7/)
  assert.doesNotMatch(h.replaced[0].url, /ticket=/)
})
