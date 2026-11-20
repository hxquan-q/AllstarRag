/**
 * T03 版本页字样与报障外链（AC-8 / AC-9；验收条件 5）。
 *
 * 红阶段目标态（②实现 76a932d32 后已全部转绿）：
 * - 6 个 locale 的 system.versionDescription 去掉「（weknora-app）」；
 * - SystemInfo.vue 的 DB 迁移报障引导不再外链 github.com/Tencent/WeKnora/issues，
 *   文案改为人话指引（「如遇迁移问题请联系系统管理员」类）；
 * - 前端版本行描述同样去 weknora-ui（2026-11-20 用户 E2E 裁定，原「不动」口径作废）——守卫用例（绿）。
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

// ---------- AC-8 守卫：前端版本行描述同样去 weknora-ui（2026-11-20 用户 E2E 新口径） ----------

test('T03 守卫：前端版本描述不含 weknora-ui（用户 E2E 口径，绿组）', () => {
  for (const name of LOCALE_NAMES) {
    const value = getLocaleValueAtPath(LOCALE_BUNDLES[name], 'system.frontendVersionDescription')
    assert.equal(typeof value, 'string', `${name} system.frontendVersionDescription 应为字符串`)
    assert.equal(
      (value as string).includes('weknora-ui'), false,
      `${name} 的前端版本描述仍含 weknora-ui 字样（2026-11-20 用户裁定去除）`,
    )
  }
  // 模板两行都保留（应用服务版本行只清字样、前端版本行仅清描述字样）。
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
  // 报障引导的描述文案保留（改写为联系管理员类指引，键不变），
  // 精确文案断言见下一条（票级复跑补，沿 T01 Q1 先例）。
  assert.ok(SYSTEM_INFO_SOURCE.includes('dbMigrationFailedDesc'), '迁移失败描述文案仍在')
})

// ---------- AC-9：报障引导新文案六语言精确断言（票级复跑补，基准=T03-notes OQ3 定稿） ----------

test('T03 dbMigrationFailedDesc 六语言逐字等于 OQ3 定稿文案（AC-9，票级复跑补断言）', () => {
  // 基准=T03-notes OQ3：键名不变，六语言人话指引（「如遇迁移问题，请联系系统管理员处理」类）。
  // 逐字断言锁用户可见文案（user_visible_copy），后续改动措辞须先改这里的基准。
  const expected: Record<string, string> = {
    'zh-CN': '启动时数据库迁移未成功完成，部分表或索引可能未创建，会导致 Wiki、知识图谱等功能异常。如遇迁移问题，请联系系统管理员处理。',
    'zh-TW': '啟動時資料庫遷移未成功完成，部分表或索引可能未建立，會導致 Wiki、知識圖譜等功能例外。如遇遷移問題，請聯絡系統管理員處理。',
    'en-US': 'The startup database migration did not complete successfully. Some tables or indexes may be missing, which can break Wiki ingest, the knowledge graph, and other features. If the migration keeps failing, contact your system administrator.',
    'ja-JP': '起動時のデータベースマイグレーションが正常に完了しませんでした。一部のテーブルやインデックスが欠落し、Wikiの取り込みやナレッジグラフなどの機能が動作しない可能性があります。マイグレーション問題が解決しない場合は、システム管理者に連絡してください。',
    'ko-KR': '시작 시 데이터베이스 마이그레이션이 정상적으로 완료되지 않았습니다. 일부 테이블이나 인덱스가 생성되지 않았을 수 있으며, Wiki와 지식 그래프 기능 등이 동작하지 않을 수 있습니다. 마이그레이션 문제가 해결되지 않으면 시스템 관리자에게 문의해 주세요.',
    'ru-RU': 'Миграция базы данных при запуске не завершилась успешно. Часть таблиц или индексов могла не создаться, из-за чего Wiki, граф знаний и другие функции могут не работать. Если проблема с миграцией не решается, обратитесь к системному администратору.',
  }
  for (const name of LOCALE_NAMES) {
    const value = getLocaleValueAtPath(LOCALE_BUNDLES[name], 'system.dbMigrationFailedDesc')
    assert.equal(typeof value, 'string', `${name} system.dbMigrationFailedDesc 应为字符串`)
    assert.equal(
      value, expected[name],
      `${name} system.dbMigrationFailedDesc 应逐字等于 OQ3 定稿文案`,
    )
  }
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
