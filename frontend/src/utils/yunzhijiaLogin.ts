import type { LoginResponse } from '@/api/auth'

// 云之家应用中心入口会把这些参数拼在根路径上。ticket 只能留在内存里：
// 读完立即从地址栏移除，POST 的 Referer 与浏览器历史都不会再带上它。
export const YUNZHIJIA_PARAM_KEYS = [
  'ticket',
  'appid',
  'expire_time',
  'yzjLang',
  'client_id',
  'msgShowStyle',
] as const

export const YUNZHIJIA_LOCALES = [
  'zh-CN',
  'zh-TW',
  'en-US',
  'ja-JP',
  'ko-KR',
  'ru-RU',
] as const

export type YunzhijiaLocale = (typeof YUNZHIJIA_LOCALES)[number]

export interface YunzhijiaEntry {
  ticket: string
  yzjLang: string
}

export interface LocationSnapshot {
  pathname: string
  search: string
  hash: string
  state: unknown
}

export interface YunzhijiaBootOptions {
  login: (ticket: string) => Promise<LoginResponse>
  persist: (response: LoginResponse) => void | Promise<void>
  clearSession: () => void
  applyLocale: (locale: YunzhijiaLocale) => void
  // 测试可注入这两个函数；生产默认直接读写浏览器地址栏。
  readUrl?: () => LocationSnapshot
  replaceUrl?: (url: string, state: unknown) => void
}

export type YunzhijiaBootResult =
  | { kind: 'none' }
  | { kind: 'success' }
  | { kind: 'error'; error: unknown }

export function readYunzhijiaEntry(search: string): YunzhijiaEntry {
  const params = new URLSearchParams(search)
  return {
    ticket: (params.get('ticket') || '').trim(),
    yzjLang: (params.get('yzjLang') || '').trim(),
  }
}

// 只移除云之家参数，保留入口上可能存在的其它参数（例如邀请 token）。
export function cleanYunzhijiaUrl(pathname: string, search: string, hash: string): string {
  const params = new URLSearchParams(search)
  for (const key of YUNZHIJIA_PARAM_KEYS) {
    params.delete(key)
  }
  const query = params.toString()
  return `${pathname}${query ? `?${query}` : ''}${hash}`
}

// yzjLang 必须是受支持的完整 locale；其它写法一律按需求回落到中文。
export function resolveYunzhijiaLocale(raw: string): YunzhijiaLocale {
  return (YUNZHIJIA_LOCALES as readonly string[]).includes(raw) ? (raw as YunzhijiaLocale) : 'zh-CN'
}

function readBrowserUrl(): LocationSnapshot {
  return {
    pathname: window.location.pathname,
    search: window.location.search,
    hash: window.location.hash,
    state: window.history.state,
  }
}

function replaceBrowserUrl(url: string, state: unknown): void {
  window.history.replaceState(state, '', url)
}

// 云之家 ticket 登录入口。顺序固定：读 ticket → 立即清地址栏 → 清旧会话 →
// POST 换票。失败时旧会话已被清掉，调用方只能展示错误页，不能落回旧账号。
export async function bootstrapYunzhijiaLogin(
  options: YunzhijiaBootOptions,
): Promise<YunzhijiaBootResult> {
  const readUrl = options.readUrl ?? readBrowserUrl
  const replaceUrl = options.replaceUrl ?? replaceBrowserUrl

  const location = readUrl()
  const { ticket, yzjLang } = readYunzhijiaEntry(location.search)
  if (!ticket) {
    return { kind: 'none' }
  }

  replaceUrl(cleanYunzhijiaUrl(location.pathname, location.search, location.hash), location.state)

  try {
    options.clearSession()
    const response = await options.login(ticket)
    if (!response?.success || !response.user || !response.token) {
      return { kind: 'error', error: response }
    }
    await options.persist(response)
    options.applyLocale(resolveYunzhijiaLocale(yzjLang))
    return { kind: 'success' }
  } catch (error) {
    return { kind: 'error', error }
  }
}
