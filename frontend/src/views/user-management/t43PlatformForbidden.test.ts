/**
 * T43 / GPT #5：平台 403 文案不再误导。
 *
 * 平台接口的 403 既可能是「调用者不是系统管理员」，也可能是「目标仍持有
 * 跨空间权限/空间而受保护」。旧文案「仅系统管理员可执行该操作」会把后一种
 * 情况误导成权限不足。本测试用 6 个真实 locale bundle 解析
 * resolvePlatformUserManagementError({status:403})，断言文案为通用 forbidden
 * 表述，且不含旧限定语。
 *
 * 不锁具体 key 名：② 可改 platformForbidden 的文案，也可映射到等价新 key。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { fixture } from '../../test-utils/vueFixture.ts'
import enUS from '../../i18n/locales/en-US.ts'
import jaJP from '../../i18n/locales/ja-JP.ts'
import koKR from '../../i18n/locales/ko-KR.ts'
import ruRU from '../../i18n/locales/ru-RU.ts'
import zhCN from '../../i18n/locales/zh-CN.ts'
import zhTW from '../../i18n/locales/zh-TW.ts'

const API_SOURCE = readFileSync(new URL('../../api/user-management.ts', import.meta.url), 'utf8')

const BUNDLES: Record<string, any> = {
  'zh-CN': zhCN,
  'zh-TW': zhTW,
  'en-US': enUS,
  'ja-JP': jaJP,
  'ko-KR': koKR,
  'ru-RU': ruRU,
}

/** 旧的误导文案：把平台 403 说成「只有系统管理员能做」。 */
const OLD_MISLEADING_COPY: Record<string, string> = {
  'zh-CN': '仅系统管理员可执行该操作。',
  'zh-TW': '僅系統管理員可執行此操作。',
  'en-US': 'Only system administrators can perform this action.',
  'ja-JP': 'この操作はシステム管理者のみ実行できます。',
  'ko-KR': '이 작업은 시스템 관리자만 수행할 수 있습니다.',
  'ru-RU': 'Это действие могут выполнять только системные администраторы.',
}

/** 旧文案的排他限定语；新文案不得再出现。 */
const OLD_EXCLUSIVE_PHRASE: Record<string, RegExp> = {
  'zh-CN': /仅系统管理员/,
  'zh-TW': /僅系統管理員/,
  'en-US': /only system administrators/i,
  'ja-JP': /システム管理者のみ/,
  'ko-KR': /시스템 관리자만/,
  'ru-RU': /только системные администраторы/i,
}

/** 通用 forbidden 语义的最低语言锚点。 */
const GENERAL_FORBIDDEN_PHRASE: Record<string, RegExp> = {
  'zh-CN': /无权限|没有权限|无权/,
  'zh-TW': /無權限|沒有權限|無權/,
  'en-US': /permission|not allowed|forbidden/i,
  'ja-JP': /権限/,
  'ko-KR': /권한/,
  'ru-RU': /прав|доступ|запрещ/i,
}

function lookup(bundle: any, key: string): unknown {
  return key.split('.').reduce<any>(
    (node, part) => (node == null ? undefined : node[part]),
    bundle,
  )
}

for (const [locale, bundle] of Object.entries(BUNDLES)) {
  test(`T43 #5 平台 403 使用通用 forbidden 文案（${locale}）`, (t) => {
    const f = fixture(t)
    const api = f.evaluate(API_SOURCE, {
      '@/utils/request': { get: async () => ({}), post: async () => ({}) },
      '@/i18n': {
        __esModule: true,
        default: { global: { t: (key: string) => lookup(bundle, key) } },
      },
    }) as any

    const message = api.resolvePlatformUserManagementError({ status: 403 })
    assert.equal(typeof message, 'string', `平台 403 必须有可展示文案（${locale}）`)
    const copy = String(message).trim()
    assert.ok(copy.length > 0, `平台 403 文案不得为空（${locale}）`)

    assert.notEqual(copy, OLD_MISLEADING_COPY[locale], `平台 403 不得继续使用旧误导文案（${locale}）`)
    assert.ok(
      !OLD_EXCLUSIVE_PHRASE[locale]!.test(copy),
      `平台 403 文案不得再暗示「只有系统管理员能做」（${locale}）：${copy}`,
    )
    assert.ok(
      GENERAL_FORBIDDEN_PHRASE[locale]!.test(copy),
      `平台 403 文案应为通用无权限表述（${locale}）：${copy}`,
    )
  })
}
