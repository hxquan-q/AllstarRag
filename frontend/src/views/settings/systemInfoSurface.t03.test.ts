/**
 * T03 测试先行红阶段：版本页字样与报障外链（AC-8 / AC-9；验收条件 5）。
 *
 * 目标态（实现未动，本轮跑必红）：
 * - 6 个 locale 的 system.versionDescription 去掉「（weknora-app）」；
 * - SystemInfo.vue 的 DB 迁移报障引导不再外链 github.com/Tencent/WeKnora/issues，
 *   文案改为人话指引（「如遇迁移问题请联系系统管理员」类）；
 * - 前端版本行（含 weknora-ui 描述）按正本不动——守卫用例（绿）。
 *
 * 字样属用户可见文案（user_visible_copy），「应用服务版本」行本身保留；
 * 版本号值链当前无 weknora 字样（VERSION=0.9.0），无断言点。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { LOCALE_BUNDLES, getLocaleValueAtPath } from '../../i18n/localeKeyAudit.ts'

const SYSTEM_INFO = new URL('./SystemInfo.vue', import.meta.url)
const SYSTEM_INFO_SOURCE = readFileSync(SYSTEM_INFO, 'utf8')
const LOCALE_NAMES = Object.keys(LOCALE_BUNDLES) as Array<keyof typeof LOCALE_BUNDLES>

assert.equal(LOCALE_NAMES.length, 6, 'locale 基数应为 6')

// ---------- AC-8：6 locale versionDescription 无 weknora-app ----------

test('T03 6 个 locale 的 system.versionDescription 不含 weknora-app（AC-8，现状全含=预期红）', () => {
  for (const name of LOCALE_NAMES) {
    const value = getLocaleValueAtPath(LOCALE_BUNDLES[name], 'system.versionDescription')
    assert.equal(typeof value, 'string', `${name} system.versionDescription 应为字符串`)
    assert.equal(
      (value as string).includes('weknora-app'), false,
      `${name} 的应用版本描述仍含 weknora-app 字样`,
    )
  }
})

// ---------- AC-8 守卫：前端版本行不动（weknora-ui 描述保留） ----------

test('T03 守卫：前端版本行不动——frontendVersionDescription 保留 weknora-ui（AC-8，绿组）', () => {
  for (const name of LOCALE_NAMES) {
    const value = getLocaleValueAtPath(LOCALE_BUNDLES[name], 'system.frontendVersionDescription')
    assert.equal(typeof value, 'string', `${name} system.frontendVersionDescription 应为字符串`)
    assert.equal(
      (value as string).includes('weknora-ui'), true,
      `${name} 的前端版本描述应保持不动（仍含 weknora-ui）`,
    )
  }
  // 模板两行都保留（应用服务版本行只清字样、前端版本行零改动）。
  assert.ok(SYSTEM_INFO_SOURCE.includes('system.versionLabel'), '应用版本 label 行保留')
  assert.ok(SYSTEM_INFO_SOURCE.includes('system.frontendVersionLabel'), '前端版本 label 行保留')
  assert.ok(SYSTEM_INFO_SOURCE.includes('system.versionDescription'), '应用版本描述行保留（只清字样不删行）')
})

// ---------- AC-9：报障外链清理 ----------

test('T03 SystemInfo.vue 无 github.com 外链（AC-9，现状 :257 issues 链接=预期红）', () => {
  assert.equal(
    SYSTEM_INFO_SOURCE.includes('github.com'), false,
    'SystemInfo.vue 不得再外链 github.com（DB 迁移报障引导改人话指引）',
  )
  // 报障引导的描述文案保留（改写为联系管理员类指引，键不变或换键由②定，
  // 复跑轮再补精确文案断言——沿 T01 Q1 先例）。
  assert.ok(SYSTEM_INFO_SOURCE.includes('dbMigrationFailedDesc'), '迁移失败描述文案仍在')
})

test('T03 守卫：6 个 locale 的 system 段文案无 github.com 外链（AC-9，绿组）', () => {
  const walk = (node: unknown, path: string, hits: string[]) => {
    if (typeof node === 'string') {
      if (node.includes('github.com')) hits.push(`${path}: ${node}`)
      return
    }
    if (node && typeof node === 'object') {
      for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
        walk(value, `${path}.${key}`, hits)
      }
    }
  }
  for (const name of LOCALE_NAMES) {
    const systemTree = getLocaleValueAtPath(LOCALE_BUNDLES[name], 'system')
    const hits: string[] = []
    walk(systemTree, `${name}.system`, hits)
    assert.deepEqual(hits, [], `${name} system 段不得含 github.com 外链`)
  }
})
