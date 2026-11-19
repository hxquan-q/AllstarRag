/**
 * T03 测试先行红阶段：设置分区可见性矩阵 + deep-link role-denied 兜底
 * （AC-3 / AC-4 / AC-5；验收条件 1/2/3/4）。
 *
 * 目标态（实现未动，本轮跑必红）：
 * - integration-claw：所有身份（owner/admin/超管/客户）canSeeSection=false
 *   → deep-link ?section=integration-claw 全员落 role-denied；
 * - integration-chrome / integration-cli / ollama / weknoracloud：
 *   非超管（owner/admin/客户）false、系统管理员 true（超管保留=用户有意，勿统一）；
 * - owner 端「发布集成」仅剩 IM/网页嵌入/API/MCP 四 tab；
 *   「模型与运行时」仅剩「模型」（models 对 owner/admin 仍可见）；
 * - 客户白名单四分区无回归（现状已隐藏，绿组守卫）。
 *
 * 手法沿 customerSettingsAccess 先例：从 Settings.vue 源码 AST 抽取真实
 * canSeeSection，在 VM 里以按身份构造的 authStore / capabilitiesStore 执行。
 * 这只是界面不渲染与兜底（UI gating only），真权限以后端 RBAC 为准（C4）；
 * 后端拒绝面（O6/O7 六+二条路由）在 T01 已落地并由其测试覆盖。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { isCustomerViewer, isCustomerSettingsSection } from '../../config/customerAccess.ts'
import { INTEGRATION_PREVIEW_ITEMS, INTEGRATION_TAB_MIN_ROLE } from '../../config/integrations.ts'
import { isIntegrationSection, integrationTabFromSection, integrationSectionKey, normalizeSettingsSection } from '../../config/settingsRoute.ts'
import { SETTINGS_SECTION_MIN_ROLE, SYSTEM_ADMIN_SETTINGS_SECTIONS } from '../../config/settingsAccess.ts'

const settingsSource = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')
const script = settingsSource.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)![1]!
const ast = ts.createSourceFile('Settings.ts', script, ts.ScriptTarget.Latest, true)

const canSeeStatement = ast.statements.find((statement) =>
  ts.isVariableStatement(statement)
  && statement.declarationList.declarations.some(
    (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'canSeeSection',
  ),
)
assert.ok(canSeeStatement, 'Settings.vue must define canSeeSection')
const canSeeSectionText = ts.transpileModule(
  canSeeStatement.getText(ast).replace(/^const canSeeSection = /, ''),
  { compilerOptions: { target: ts.ScriptTarget.ES2022 } },
).outputText

// 若②按调研建议新增 config/ownerSurface.ts 纯函数模块并在 canSeeSection 引用，
// 这里自动带上其导出（红阶段模块尚不存在，容错为空）。
let surfaceModuleExtras: Record<string, unknown> = {}
try {
  surfaceModuleExtras = await import('../../config/ownerSurface.ts') as Record<string, unknown>
} catch { /* 红阶段预期：模块未创建 */ }

// ---------- 身份构造（research「三种身份的可复用判定」：三字段纯传参） ----------

interface SurfaceIdentity {
  label: string
  role: 'owner' | 'admin' | 'viewer'
  isSystemAdmin?: boolean
  canAccessAllTenants?: boolean
}

const IDENTITIES: Record<string, SurfaceIdentity> = {
  owner: { label: 'owner', role: 'owner' },
  admin: { label: '空间 admin', role: 'admin' },
  sysadmin: { label: '系统管理员', role: 'admin', isSystemAdmin: true, canAccessAllTenants: true },
  customer: { label: '客户（viewer）', role: 'viewer' },
}

const ROLE_LEVELS: Record<string, number> = { viewer: 10, contributor: 20, admin: 30, owner: 40 }

function makeAuthStore(identity: SurfaceIdentity) {
  return {
    currentTenantRole: identity.role,
    isSystemAdmin: identity.isSystemAdmin ?? false,
    canAccessAllTenants: identity.canAccessAllTenants ?? false,
    hasRole: (min: string) => (ROLE_LEVELS[identity.role] ?? 0) >= (ROLE_LEVELS[min] ?? 0),
  }
}

/** 以指定身份执行真实 canSeeSection（每身份独立求值，避免闭包串染）。 */
function canSeeAs(identity: SurfaceIdentity, key: string): boolean {
  const authStore = makeAuthStore(identity)
  const fn = runInNewContext(canSeeSectionText, {
    isCustomerViewer,
    isCustomerSettingsSection,
    isIntegrationSection,
    integrationTabFromSection,
    INTEGRATION_TAB_MIN_ROLE,
    SYSTEM_ADMIN_SECTIONS: SYSTEM_ADMIN_SETTINGS_SECTIONS,
    SETTINGS_SECTION_MIN_ROLE,
    authStore,
    capabilitiesStore: { can: () => false },
    ...surfaceModuleExtras,
  }) as (key: string) => boolean
  return fn(key)
}

/** 身份可见的集成分区（按真实 INTEGRATION_PREVIEW_ITEMS 顺序，即导航组形态）。 */
const visibleIntegrationKeys = (identity: SurfaceIdentity) =>
  INTEGRATION_PREVIEW_ITEMS
    .map((item) => integrationSectionKey(item.key))
    .filter((key) => canSeeAs(identity, key))

const DENIED_FOR_NON_SYSADMIN = ['integration-chrome', 'integration-cli', 'ollama', 'weknoracloud']
const ALL_IDENTITIES = Object.values(IDENTITIES)

// ---------- AC-3：claw 全员不渲染（含超管） ----------

test('T03 integration-claw 对所有身份不可见（AC-3，前三身份现状放行=预期红）', () => {
  for (const identity of ALL_IDENTITIES) {
    assert.equal(
      canSeeAs(identity, 'integration-claw'), false,
      `${identity.label} 不得看到 claw 集成分区（全员删除）`,
    )
  }
})

// ---------- AC-3/4/5：chrome/cli/ollama/weknoracloud 非超管不可见、超管保留 ----------

test('T03 chrome/cli/ollama/weknoracloud：非超管不可见（AC-3/4/5，owner/admin 现状可见=预期红）', () => {
  for (const key of DENIED_FOR_NON_SYSADMIN) {
    for (const name of ['owner', 'admin', 'customer']) {
      assert.equal(
        canSeeAs(IDENTITIES[name]!, key), false,
        `${IDENTITIES[name]!.label} 不得看到 ${key}（非超管删除；后端拒绝见 T01）`,
      )
    }
  }
})

test('T03 守卫：系统管理员保留 chrome/cli/ollama/weknoracloud 与 5 个系统管理分区（用户有意，勿统一）', () => {
  const sysadmin = IDENTITIES.sysadmin!
  for (const key of [...DENIED_FOR_NON_SYSADMIN, 'integration-im', 'integration-embed', 'integration-mcpserver']) {
    assert.equal(canSeeAs(sysadmin, key), true, `系统管理员应保留 ${key}`)
  }
  for (const key of SYSTEM_ADMIN_SETTINGS_SECTIONS) {
    assert.equal(canSeeAs(sysadmin, key), true, `系统管理员应保留系统管理分区 ${key}`)
  }
})

// ---------- AC-3/4：owner 端「发布集成」四 tab 形态 ----------

test('T03 owner 端发布集成仅剩 IM/网页嵌入/API/MCP 四 tab；admin 无 API（AC-3，现状含 claw/chrome/cli=预期红）', () => {
  assert.deepEqual(
    visibleIntegrationKeys(IDENTITIES.owner!),
    ['integration-im', 'integration-embed', 'integration-api', 'integration-mcpserver'],
    'owner 集成分区应只剩四 tab',
  )
  assert.deepEqual(
    visibleIntegrationKeys(IDENTITIES.admin!),
    ['integration-im', 'integration-embed', 'integration-mcpserver'],
    'admin 同 owner 裁剪面，且 API（owner 门槛）本就不可见',
  )
  assert.deepEqual(
    visibleIntegrationKeys(IDENTITIES.sysadmin!),
    ['integration-im', 'integration-embed', 'integration-api', 'integration-mcpserver', 'integration-cli', 'integration-chrome'],
    '系统管理员保留 chrome/cli、无 claw',
  )
})

// ---------- AC-4/5：deep-link 兜底（沿客户先例 Settings.vue:74 role-denied） ----------

test('T03 deep-link 兜底：claw 全员、chrome/cli/ollama/weknoracloud 非超管落 role-denied（AC-4/5）', () => {
  // deep-link 链路：?section= 经 normalizeSettingsSection 归一化后由 canSeeSection 兜底。
  assert.equal(normalizeSettingsSection('claw'), 'integration-claw', 'claw 别名映射保持（否则兜底链断）')
  assert.equal(normalizeSettingsSection('integrations', 'chrome'), 'integration-chrome')
  for (const identity of ALL_IDENTITIES) {
    assert.equal(
      canSeeAs(identity, normalizeSettingsSection('claw')), false,
      `?section=integration-claw 对 ${identity.label} 应落 role-denied`,
    )
  }
  for (const key of DENIED_FOR_NON_SYSADMIN) {
    for (const name of ['owner', 'admin', 'customer']) {
      assert.equal(
        canSeeAs(IDENTITIES[name]!, normalizeSettingsSection(key)), false,
        `?section=${key} 对 ${IDENTITIES[name]!.label} 应落 role-denied`,
      )
    }
    assert.equal(
      canSeeAs(IDENTITIES.sysadmin!, normalizeSettingsSection(key)), true,
      `?section=${key} 系统管理员放行`,
    )
  }
  // 兜底渲染守卫：不可见分区必须命中 role-denied 块（客户先例模板保留）。
  assert.match(settingsSource, /v-if="!canSeeSection\(currentSection\)"/)
  assert.match(settingsSource, /role-denied/)
})

// ---------- AC-4：owner 端「模型与运行时」仅剩「模型」 ----------

test('T03 owner/admin「模型与运行时」仅剩「模型」：models 保留、ollama/weknoracloud 不见（AC-4/5）', () => {
  for (const name of ['owner', 'admin']) {
    assert.equal(canSeeAs(IDENTITIES[name]!, 'models'), true, `${name} 保留「模型」分区`)
    assert.equal(canSeeAs(IDENTITIES[name]!, 'ollama'), false)
    assert.equal(canSeeAs(IDENTITIES[name]!, 'weknoracloud'), false)
  }
})

// ---------- AC-3 验收条件 3：客户无回归（现状守卫，绿组） ----------

test('T03 客户无回归：白名单四分区仍可见、五项裁剪分区仍不可见（验收 3，绿组守卫）', () => {
  const customer = IDENTITIES.customer!
  for (const key of ['general', 'userprofile', 'mymemory', 'agentlines']) {
    assert.equal(canSeeAs(customer, key), true, `客户保留 ${key}`)
  }
  for (const key of ['integration-claw', ...DENIED_FOR_NON_SYSADMIN, 'models', 'system']) {
    assert.equal(canSeeAs(customer, key), false, `客户不见 ${key}（现状已隐藏，无回归）`)
  }
})
