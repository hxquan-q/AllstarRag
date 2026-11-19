/**
 * CP3 修复轮 1 复验（修-2 前端半）：OQ3 报障文案外链清理 + FS6 claw 死代码/死样式零残留。
 *
 * OQ3：SystemInfo 的 DB 迁移排查不再外链 weixin.qq.com，改用
 * `system.dbMigrationGuide`；6 locale 该键存在且非空、不含该外链；
 * docsUrl 的 troubleshootingMigrations 常量已无消费方并删除。
 * FS6：claw 变体/emoji 映射/死样式全部清空；config/integrations 的
 * deep-link 归一化保留（最新裁定，不属于死代码）。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import enUS from '../../i18n/locales/en-US.ts'
import jaJP from '../../i18n/locales/ja-JP.ts'
import koKR from '../../i18n/locales/ko-KR.ts'
import ruRU from '../../i18n/locales/ru-RU.ts'
import zhCN from '../../i18n/locales/zh-CN.ts'
import zhTW from '../../i18n/locales/zh-TW.ts'

const SYSTEM_INFO = new URL('./SystemInfo.vue', import.meta.url)
const DOCS_URL = new URL('../../utils/docsUrl.ts', import.meta.url)
const EXTERNAL_CTA = new URL('../integrations/IntegrationExternalCta.vue', import.meta.url)
const LANDING_LAYOUT = new URL('../integrations/IntegrationLandingLayout.vue', import.meta.url)
const LANDING_LESS = new URL('../integrations/integration-landing.less', import.meta.url)
const SETTINGS = new URL('./Settings.vue', import.meta.url)
const SHELL = new URL('../../components/SettingsModalShell.vue', import.meta.url)
const INTEGRATIONS = new URL('../../config/integrations.ts', import.meta.url)
const PARSER_ENGINE = new URL('./ParserEngineSettings.vue', import.meta.url)

const read = (url: URL) => readFileSync(url, 'utf8')

const locales: Array<[string, Record<string, unknown>]> = [
  ['zh-CN', zhCN as unknown as Record<string, unknown>],
  ['zh-TW', zhTW as unknown as Record<string, unknown>],
  ['en-US', enUS as unknown as Record<string, unknown>],
  ['ja-JP', jaJP as unknown as Record<string, unknown>],
  ['ko-KR', koKR as unknown as Record<string, unknown>],
  ['ru-RU', ruRU as unknown as Record<string, unknown>],
]

function lookup(bundle: Record<string, unknown>, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (
    node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined
  ), bundle)
}

test('CP3-OQ3 6 locale 的 dbMigrationGuide 为存在的人话文案，无 weixin 外链', () => {
  for (const [name, bundle] of locales) {
    const value = lookup(bundle, 'system.dbMigrationGuide')
    assert.equal(typeof value, 'string', `${name} 必须有 system.dbMigrationGuide`)
    assert.ok((value as string).trim().length > 0, `${name} 文案非空`)
    assert.equal((value as string).includes('weixin.qq.com'), false, `${name} 不得含旧外链`)
    assert.equal((value as string).includes('http://'), false, `${name} 不得新增外链`)
    assert.equal((value as string).includes('https://'), false, `${name} 不得新增外链`)
  }
})

test('CP3-OQ3 SystemInfo 改用新键、无 weixin 外链；docsUrl 旧常量已删', () => {
  const systemInfo = read(SYSTEM_INFO)
  // 注释里会保留「原外链已移除」的说明，按代码行核对，避免把注释误判成外链。
  const codeLines = systemInfo
    .split('\n')
    .filter((line) => !line.trim().startsWith('//') && !line.trim().startsWith('*') && !line.trim().startsWith('<!--'))
    .join('\n')
  assert.ok(codeLines.includes('system.dbMigrationGuide'), '模板引用新键')
  assert.equal(codeLines.includes('weixin.qq.com'), false, 'SystemInfo 代码不得残留 weixin 外链')
  assert.equal(codeLines.includes('troubleshootingMigrations'), false, '旧常量引用已清')

  const docsUrl = read(DOCS_URL)
  assert.equal(docsUrl.includes('troubleshootingMigrations'), false, 'docsUrl 旧常量已删除')
})

test('CP3-F4 ParserEngineSettings：非超管在请求凭证状态前早退（auth 闸）', () => {
  const source = read(PARSER_ENGINE)
  const start = source.indexOf('async function checkWkcStatus')
  assert.ok(start >= 0, '能定位 checkWkcStatus')
  const block = source.slice(start, source.indexOf('\n}', start))
  const gate = block.indexOf('if (!authStore.isSystemAdmin)')
  const request = block.indexOf('getWeKnoraCloudStatus(')
  assert.ok(gate >= 0, 'checkWkcStatus 必须有 authStore.isSystemAdmin 闸')
  assert.ok(request > gate, '闸必须在调用状态接口之前（非超管零请求）')
  assert.ok(block.includes("wkcState.value = 'unconfigured'"), '非超管按未配置态渲染能力说明')
})

test('CP3-FS6 claw 死代码/死样式零残留（4 文件 + 壳层）', () => {
  for (const [name, url] of [
    ['IntegrationExternalCta.vue', EXTERNAL_CTA],
    ['IntegrationLandingLayout.vue', LANDING_LAYOUT],
  ] as const) {
    const source = read(url)
    assert.equal(/['"]claw['"]/.test(source), false, `${name} 不得再有 'claw' 变体`)
  }

  const less = read(LANDING_LESS)
  assert.equal(less.includes('--claw'), false, '不得残留 &--claw 变体')
  assert.equal(less.includes('@claw-accent'), false, '不得残留 @claw-accent 变量')
  assert.equal(less.includes('.ext-cta-emoji'), false, '不得残留无消费方的 .ext-cta-emoji')

  const settings = read(SETTINGS)
  assert.equal(/NavItem\.emoji|emoji\s*:/.test(settings), false, 'Settings 不得残留 emoji 映射')
  assert.equal(read(SHELL).includes('nav-icon-emoji'), false, '壳层不得残留 .nav-icon-emoji 死样式')

  const integrations = read(INTEGRATIONS)
  assert.equal(integrations.includes("type: 'emoji'"), false, 'emoji 联合成员已删除')
  // deep-link 归一化仍需 claw：保留是有意行为，不是死代码。
  assert.ok(integrations.includes("'claw'"), 'config/integrations 的 claw 归一化必须保留')
})
