import assert from 'node:assert/strict'
import { test } from 'node:test'

import { LOCALE_BUNDLES, type LocaleName } from './localeKeyAudit.ts'

// 01-spec §3.3 白名单：这些技术标识允许出现在文案值里。
const ALLOWED_IN_VALUE = [
  /X-WeKnora-(?:Signature|Desktop-Token)/g,
  /WeKnora_(?:theme|settings)/g,
  /WeKnora\.init/g,
  /weknora-widget\.js/g,
  /https?:\/\/[^\s'"`]*WeKnora[^\s'"`]*/g,
]

type StringEntry = { path: string; value: string }

// 只遍历对象的值；key 名不当作文案扫描。
function collectStringValues(node: unknown, path: string, out: StringEntry[]): void {
  if (typeof node === 'string') {
    out.push({ path, value: node })
    return
  }
  if (Array.isArray(node)) {
    node.forEach((item, index) => collectStringValues(item, `${path}[${index}]`, out))
    return
  }
  if (node && typeof node === 'object') {
    for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
      collectStringValues(value, path ? `${path}.${key}` : key, out)
    }
  }
}

test('locale 文案值中不出现品牌词 WeKnora（白名单技术标识除外）', () => {
  const violations: string[] = []

  for (const [locale, bundle] of Object.entries(LOCALE_BUNDLES) as Array<[LocaleName, unknown]>) {
    const entries: StringEntry[] = []
    collectStringValues(bundle, locale, entries)
    // 防止遍历逻辑失效导致空扫描“假通过”。
    if (entries.length === 0) violations.push(`${locale}: 未遍历到任何文案值`)
    for (const { path, value } of entries) {
      let rest = value
      for (const pattern of ALLOWED_IN_VALUE) rest = rest.replace(pattern, '')
      if (rest.includes('WeKnora')) violations.push(`${path}: ${value}`)
    }
  }

  assert.deepEqual(violations, [], violations.slice(0, 20).join('\n'))
})
