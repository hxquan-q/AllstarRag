/**
 * T10 / G-25：云之家同步面板 i18n scoped 用例。
 *
 * 仓库级 check-i18n 只能发现「某个 locale 少了一个键」，发现不了
 * 「代码引用了某个键、但 6 个 locale 全都没有」（PITFALLS G-25）。
 * 这里把范围钉在 YunzhijiaSyncPanel.vue：
 * 1. 源码里以字面量出现的 `system.globalSettings.yunzhijiaSync.*` 必须全部存在于 6 个 locale；
 * 2. enumLabel 动态拼出的 triggers/statuses/anomalyKinds 键逐个枚举并断言存在；
 * 3. 6 个 locale 的 yunzhijiaSync 键集完全一致；
 * 4. `updatedCount` 中文必须是「已核对」，不得写「更新」。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { LOCALE_BUNDLES, collectLocaleKeys, type LocaleName } from './localeKeyAudit.ts'

const PANEL = new URL('../views/system/YunzhijiaSyncPanel.vue', import.meta.url)
const PREFIX = 'system.globalSettings.yunzhijiaSync.'
const LITERAL_KEY_PATTERN = /['"](system\.globalSettings\.yunzhijiaSync\.[A-Za-z0-9_.]+)['"]/g

/** 面板用到的非 yunzhijiaSync 命名空间键。 */
const EXTERNAL_KEYS = ['common.cancel']

const FIELDS = [
  'startedAt', 'trigger', 'status', 'duration',
  'activeCount', 'inactiveCount', 'createdCount', 'updatedCount',
  'disabledCount', 'migratedCount', 'anomalyCount',
] as const

const TRIGGERS = ['startup', 'cron', 'manual', 'preview'] as const
const STATUSES = ['running', 'success', 'failed'] as const
const ANOMALY_KINDS = [
  'duplicate_name',
  'duplicate_open_id',
  'unknown_department',
  'provision_failed',
  'blocked_skipped',
  'banned_skipped',
  'local_user_missing',
  'local_active_missing',
] as const

const EXPECTED_KEYS = [
  'title',
  'description',
  'refresh',
  'loading',
  'loadFailed',
  'enabled',
  'disabled',
  'disabledHint',
  'latestTitle',
  'noRuns',
  'durationValue',
  'errorSummary',
  'anomaliesTitle',
  'anomaliesEmpty',
  'blockedTitle',
  'blockedEmpty',
  'unblock',
  'unblockConfirm',
  'preview',
  'previewTitle',
  'previewActive',
  'previewInactive',
  'previewTenantDefault',
  'previewTenantHR',
  'previewAnomalies',
  'syncNow',
  ...FIELDS.map((field) => `fields.${field}`),
  ...[
    'anomalyColumns.kind', 'anomalyColumns.name', 'anomalyColumns.detail',
    'blockedColumns.openId', 'blockedColumns.name', 'blockedColumns.department',
    'blockedColumns.blockedAt', 'blockedColumns.blockedBy', 'blockedColumns.actions',
  ],
  ...TRIGGERS.map((value) => `triggers.${value}`),
  ...STATUSES.map((value) => `statuses.${value}`),
  ...ANOMALY_KINDS.map((value) => `anomalyKinds.${value}`),
  ...['syncSuccess', 'syncFailed', 'previewFailed', 'unblockSuccess', 'unblockFailed', 'running', 'forbidden']
    .map((key) => `messages.${key}`),
].sort()

function valueAtPath(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[part]
    return undefined
  }, root)
}

function referencedLiteralKeys(): string[] {
  const source = readFileSync(PANEL, 'utf8')
  const keys = new Set<string>()
  for (const match of source.matchAll(LITERAL_KEY_PATTERN)) keys.add(match[1]!)
  return [...keys].sort()
}

/** 动态（enumLabel）拼出的完整键，须逐条枚举，静态扫描抓不到。 */
function dynamicKeys(): string[] {
  return [
    ...TRIGGERS.map((value) => `${PREFIX}triggers.${value}`),
    ...STATUSES.map((value) => `${PREFIX}statuses.${value}`),
    ...ANOMALY_KINDS.map((value) => `${PREFIX}anomalyKinds.${value}`),
  ]
}

const localeEntries = Object.entries(LOCALE_BUNDLES) as Array<[LocaleName, unknown]>

test('every key literally referenced by the panel is defined in all six locales (G-25)', () => {
  const referenced = referencedLiteralKeys()
  assert.ok(referenced.length >= 40, `expected many referenced keys, got ${referenced.length}`)

  for (const [localeName, bundle] of localeEntries) {
    const localeKeys = collectLocaleKeys(bundle)
    const missing = referenced.filter((key) => !localeKeys.has(key))
    assert.deepEqual(missing, [], `${localeName} is missing: ${missing.join(', ')}`)
  }
})

test('dynamic enum keys (triggers/statuses/anomalyKinds) exist in all six locales', () => {
  const dynamic = dynamicKeys()
  for (const [localeName, bundle] of localeEntries) {
    const localeKeys = collectLocaleKeys(bundle)
    const missing = dynamic.filter((key) => !localeKeys.has(key))
    assert.deepEqual(missing, [], `${localeName} is missing: ${missing.join(', ')}`)
  }
})

test('all expected Yunzhijia sync keys exist in all six locales with non-empty text', () => {
  for (const [localeName, bundle] of localeEntries) {
    for (const key of EXPECTED_KEYS) {
      const value = valueAtPath(bundle, `${PREFIX}${key}`)
      assert.equal(typeof value, 'string', `${localeName}:${PREFIX}${key} must be a string`)
      assert.ok((value as string).trim().length > 0, `${localeName}:${PREFIX}${key} must not be empty`)
    }
    for (const key of EXTERNAL_KEYS) {
      assert.equal(typeof valueAtPath(bundle, key), 'string', `${localeName}:${key} must be a string`)
    }
  }
})

test('all six locales expose the same yunzhijiaSync key set', () => {
  const keysOf = (bundle: unknown) =>
    [...collectLocaleKeys(bundle)].filter((key) => key.startsWith(PREFIX)).sort()
  const reference = keysOf(LOCALE_BUNDLES['zh-CN'])

  for (const [localeName, bundle] of localeEntries) {
    assert.deepEqual(keysOf(bundle), reference, `${localeName} key set differs from zh-CN`)
  }
  for (const key of EXPECTED_KEYS) assert.ok(reference.includes(`${PREFIX}${key}`), `missing ${key}`)
})

test('the updated count is labelled 已核对 in Chinese and never 更新', () => {
  assert.equal(valueAtPath(LOCALE_BUNDLES['zh-CN'], `${PREFIX}fields.updatedCount`), '已核对')
  for (const [localeName, bundle] of localeEntries) {
    const value = String(valueAtPath(bundle, `${PREFIX}fields.updatedCount`))
    assert.ok(!value.includes('更新'), `${localeName} must not label updatedCount as 更新`)
  }
})
