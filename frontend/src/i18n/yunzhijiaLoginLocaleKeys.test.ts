/**
 * T09 / G-25：云之家登录 i18n scoped 用例。
 *
 * 仓库级 localeKeyAudit 只能发现「某个 locale 少了一个键」，发现不了
 * 「代码引用了某个键、但所有 locale 都没有」。这里把范围钉在 T09 的三个
 * 源码文件上：代码里引用的 `auth.yunzhijia.*` 必须全部存在于 6 个 locale。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { LOCALE_BUNDLES, collectLocaleKeys, type LocaleName } from './localeKeyAudit.ts'

const GUARDED_PREFIX = 'auth.yunzhijia.'
const STATIC_KEY_PATTERN = /\$?\bt\(\s*['"]([a-zA-Z][\w]*(?:\.[\w]+)+)['"]/g

const SOURCE_FILES = [
  new URL('../utils/yunzhijiaLogin.ts', import.meta.url),
  new URL('../views/auth/YunzhijiaLoginError.vue', import.meta.url),
  new URL('../views/auth/Login.vue', import.meta.url),
]

// 2026-11-25 审查清理：错误页不再用后端文案做逻辑键，删除
// errorProvisioning/errorDuplicateName/errorAccountBlocked/errorUpstream。
const EXPECTED_KEYS = [
  'auth.yunzhijia.backToLogin',
  'auth.yunzhijia.employeeHint',
  'auth.yunzhijia.errorGeneric',
  'auth.yunzhijia.errorInvalidTicket',
  'auth.yunzhijia.errorRateLimited',
  'auth.yunzhijia.errorTitle',
  'auth.yunzhijia.errorUnavailable',
]

function collectReferencedYunzhijiaKeys(): string[] {
  const keys = new Set<string>()
  for (const file of SOURCE_FILES) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(STATIC_KEY_PATTERN)) {
      if (match[1].startsWith(GUARDED_PREFIX)) keys.add(match[1])
    }
  }
  return [...keys].sort()
}

function valueAtPath(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[part]
    return undefined
  }, root)
}

test('every auth.yunzhijia.* key referenced in T09 code is defined in all six locales', () => {
  const referenced = collectReferencedYunzhijiaKeys()
  assert.ok(referenced.length > 0, 'T09 code must reference auth.yunzhijia.* keys')

  for (const [localeName, bundle] of Object.entries(LOCALE_BUNDLES) as Array<[LocaleName, unknown]>) {
    const localeKeys = collectLocaleKeys(bundle)
    const missing = referenced.filter((key) => !localeKeys.has(key))
    assert.deepEqual(missing, [], `${localeName} is missing: ${missing.join(', ')}`)
  }
})

test('all expected Yunzhijia login keys exist in all six locales with non-empty text', () => {
  for (const [localeName, bundle] of Object.entries(LOCALE_BUNDLES) as Array<[LocaleName, unknown]>) {
    const localeKeys = collectLocaleKeys(bundle)
    for (const key of EXPECTED_KEYS) {
      assert.ok(localeKeys.has(key), `${localeName} is missing ${key}`)
      const value = valueAtPath(bundle, key)
      assert.equal(typeof value, 'string', `${localeName}:${key} must be a string`)
      assert.ok((value as string).trim().length > 0, `${localeName}:${key} must not be empty`)
    }
  }
})

test('all six locales expose the same auth.yunzhijia.* key set', () => {
  const yunzhijiaKeys = (bundle: unknown) =>
    [...collectLocaleKeys(bundle)].filter((key) => key.startsWith(GUARDED_PREFIX)).sort()
  const reference = yunzhijiaKeys(LOCALE_BUNDLES['zh-CN'])
  assert.deepEqual(reference, [...EXPECTED_KEYS].sort())

  for (const [localeName, bundle] of Object.entries(LOCALE_BUNDLES) as Array<[LocaleName, unknown]>) {
    assert.deepEqual(yunzhijiaKeys(bundle), reference, `${localeName} key set differs from zh-CN`)
  }
})

test('zh-CN carries the exact requested employee hint', () => {
  assert.equal(valueAtPath(LOCALE_BUNDLES['zh-CN'], 'auth.yunzhijia.employeeHint'), '员工请从云之家进入')
})
