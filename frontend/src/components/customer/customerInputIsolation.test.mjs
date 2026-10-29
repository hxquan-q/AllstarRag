import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

/**
 * 客户输入区与助手隔离（H2 / M2 / M7 / M8 / M9）。
 *
 * 这些都是组件内部的条件分支，node 环境不能挂载整个 Vue 页面，
 * 因此沿用仓库已有的「读源码 + 正则断言真实分支」做法；
 * 行为层由真实容器走查补证（05-tests.md R1 复验章节）。
 */

const inputField = readFileSync(new URL('../Input-field.vue', import.meta.url), 'utf8')
const menuVue = readFileSync(new URL('../menu.vue', import.meta.url), 'utf8')
const userMenu = readFileSync(new URL('../UserMenu.vue', import.meta.url), 'utf8')
const chatView = readFileSync(new URL('../../views/chat/index.vue', import.meta.url), 'utf8')
const platformView = readFileSync(new URL('../../views/platform/index.vue', import.meta.url), 'utf8')
const settingsView = readFileSync(new URL('../../views/settings/Settings.vue', import.meta.url), 'utf8')
const customerWelcome = readFileSync(new URL('./CustomerWelcome.vue', import.meta.url), 'utf8')
const productLineStore = readFileSync(new URL('../../stores/productLine.ts', import.meta.url), 'utf8')
const locales = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'ko-KR', 'ru-RU'].map((lang) => ({
  lang,
  source: readFileSync(new URL(`../../i18n/locales/${lang}.ts`, import.meta.url), 'utf8'),
}))

test('M7 customer send drops hidden KB / file / MCP selections', () => {
  // 发送与 steer 两条路径都按客户模式清空提及与附件。
  assert.match(inputField, /const mentionSource = props\.customerMode \? \[\] : allSelectedItems\.value/)
  assert.match(inputField, /const imageFiles = props\.customerMode \? \[\] : uploadedImages\.value\.map/)
  assert.match(inputField, /const attachmentFiles = props\.customerMode \? \[\] : uploadedAttachments\.value/)
  assert.match(
    inputField,
    /const steerMentions: MentionRequestItem\[\] = \(props\.embeddedMode \|\| props\.customerMode \? \[\]/,
  )
})

test('M8 customer blocks paste, drag-drop, @ mentions and image upload / preview', () => {
  // 拖拽
  assert.match(inputField, /const handleDroppedFiles = \(files: File\[\]\) => \{[\s\S]*?if \(props\.customerMode\) return;/)
  // 粘贴图片
  assert.match(inputField, /const onPaste = \(e: ClipboardEvent\) => \{[\s\S]*?if \(props\.customerMode\) return;/)
  // @ 提及
  assert.match(inputField, /const isMentionDisabled = computed\(\(\) => \{[\s\S]*?if \(props\.customerMode\) return true;/)
  // 图片入队与预览条
  assert.match(inputField, /const addImageFiles = \(files: File\[\]\) => \{[\s\S]*?if \(props\.customerMode\) return;/)
  assert.match(inputField, /v-if="!customerMode && uploadedImages\.length > 0"/)
  assert.match(inputField, /<AttachmentUpload v-if="!customerMode"/)
})

test('M9 session switching locks send and stop so nothing lands in the old session', () => {
  assert.match(chatView, /const sessionSwitching = ref\(false\)/)
  assert.match(chatView, /const composerLocked = computed\(\(\) =>[\s\S]*?sessionSwitching\.value/)
  assert.match(chatView, /const handleStopGeneration = \(\) => \{[\s\S]*?if \(sessionSwitching\.value\) return;/)
  assert.match(chatView, /const sendMsg = async[\s\S]*?if \(sessionSwitching\.value\) return;/)
  // 只有最后一次切换负责解锁，被顶掉的旧切换不会提前放行。
  assert.match(chatView, /if \(switchSeq === sessionSwitchSeq\) sessionSwitching\.value = false/)
})

test('H2 customer hides every WeKnora assistant entry point', () => {
  // 回答工具栏「就这条回答问助手」
  assert.match(chatView, /:can-ask-assistant="!embeddedMode && !isCustomer"/)
  assert.match(chatView, /function handleAskAssistant\(messageId\) \{[\s\S]*?if \(isCustomer\.value\) return/)
  // 助手面板 / 悬浮入口 / 页面桥接整体不挂载
  assert.match(
    platformView,
    /<template v-if="!isCustomer">[\s\S]*?<AssistantPanel \/>[\s\S]*?<AssistantLauncher \/>[\s\S]*?<AssistantPageBridge \/>/,
  )
  assert.match(platformView, /if \(customer && assistantStore\.visible\) assistantStore\.close\(\)/)
  // 侧栏助手按钮（桌面 + 窄屏）
  assert.equal((menuVue.match(/assistantStore\.entry === 'sidebar' && !isCustomer/g) ?? []).length, 2)
  // 设置里的助手分区
  assert.match(settingsView, /currentSection === 'assistant' && !isCustomerViewerActive/)
})

test('M2 customer avatar menu hides "all settings" but keeps personal settings', () => {
  assert.match(userMenu, /<div v-if="!isCustomer" class="menu-item" @click="handleSettings">/)
  assert.match(userMenu, /\{\{ \$t\('general\.allSettings'\) \}\}/)
  // 个人设置入口对客户保留（没有 v-if 门槛）。
  assert.match(userMenu, /<div class="menu-item" @click="handleQuickNav\('general'\)">/)
  const personalBlock = userMenu.slice(
    userMenu.indexOf("@click=\"handleQuickNav('general')\"") - 200,
    userMenu.indexOf("@click=\"handleQuickNav('general')\"") + 50,
  )
  assert.doesNotMatch(personalBlock, /v-if="!isCustomer"/)
})

test('AC-11 customer menu exposes a direct agent line settings entry', () => {
  // 入口在客户分支内：同一 div 上同时挂 v-if="isCustomer" 与 agentlines 快捷导航。
  assert.match(
    userMenu,
    /<div v-if="isCustomer" class="menu-item" @click="handleQuickNav\('agentlines'\)">/,
  )
  // 菜单文案用独立 key（E2E 反馈：菜单显示「绑定 agent」，分区标题仍为「产线」）。
  const customerEntryBlock = userMenu.match(
    /<div v-if="isCustomer" class="menu-item" @click="handleQuickNav\('agentlines'\)">[\s\S]*?<\/div>/,
  )
  assert.ok(customerEntryBlock, 'customer agentlines menu item block not found')
  assert.match(customerEntryBlock[0], /\{\{ \$t\('agentLineSettings\.menuLabel'\) \}\}/)
  assert.doesNotMatch(customerEntryBlock[0], /v-if="!isCustomer"/)
  // 管理员侧看不到：全文只有这一处 agentlines 快捷导航，且它只对客户渲染
  // （没有任何「不带 v-if 的同类 menu-item」）。
  assert.equal((userMenu.match(/handleQuickNav\('agentlines'\)/g) ?? []).length, 1)
  assert.doesNotMatch(userMenu, /<div class="menu-item" @click="handleQuickNav\('agentlines'\)">/)
  // handleQuickNav 把 section 拼进 query，落到产线分区（实现处）。
  assert.match(
    userMenu,
    /const handleQuickNav = \(section: string\) => \{[\s\S]*?router\.push\(\{ path: '\/platform\/settings', query: \{ section \} \}\)/,
  )
})

test('F1 customer sidebar gates line grouping on agents readiness (AC-8.2)', () => {
  // 未就绪时 customerLineGroups 必须返回空：分组助手会把产线清单之外的会话
  // 全部塌进「历史」组，所以 ready 闸要放在调用 groupSessionsByProductLine 之前。
  const groupsStart = menuVue.indexOf('const customerLineGroups = computed')
  const groupsCall = menuVue.indexOf('groupSessionsByProductLine(rows', groupsStart)
  assert.ok(groupsStart >= 0, 'customerLineGroups computed must exist')
  assert.ok(groupsCall > groupsStart, 'customerLineGroups must call the grouping helper')
  const groupsBlock = menuVue.slice(groupsStart, groupsCall)
  assert.match(
    groupsBlock,
    /if \(!productLineStore\.ready\) return \[\]/,
    'not-ready must yield no groups (no collapse into the history group)',
  )
  // 骨架闸：未就绪时会话区挂加载骨架，不落「暂无会话」空态；短路保证管理员不实例化 store（C6）。
  assert.match(
    menuVue,
    /const customerLinesNotReady = computed\(\(\) => isCustomer\.value && !useProductLineStore\(\)\.ready\)/,
  )
  assert.match(menuVue, /\|\| customerLinesNotReady\) && filteredGroupedSessions\.length === 0/)
})

test('F6 agents fetch failure surfaces a retry in the welcome area (AC-10)', () => {
  // store：失败可观测（loadFailed 置位）且可重试（清 promise 再拉新请求）。
  assert.match(productLineStore, /const loadFailed = ref\(false\)/)
  assert.match(productLineStore, /\.catch\(\(error\) => \{[\s\S]*?loadFailed\.value = true/)
  assert.match(
    productLineStore,
    /function retryBootstrapAgents\(\): void \{[\s\S]*?bootstrapPromise = null[\s\S]*?bootstrapAgents\(\)/,
  )
  // 欢迎区：loadFailed 且没有任何产线时渲染「加载失败 + 重试」，重试走 store。
  // F13：必须同时要求 !ready，与空态（ready && 0 条）在 ready 维度互斥，
  // 否则「agents 已预取成功后二次 ensure 失败」会双空态块同现（role=status + role=alert）。
  assert.match(
    customerWelcome,
    /const showLoadFailed = computed\(\s*\(\) => productLineStore\.loadFailed && !productLineStore\.ready && lines\.value\.length === 0,?\s*\)/,
  )
  assert.match(customerWelcome, /v-if="showLoadFailed"[^>]*role="alert"/)
  assert.match(customerWelcome, /\{\{ t\('brand\.welcome\.loadFailed'\) \}\}/)
  assert.match(customerWelcome, /\{\{ t\('brand\.welcome\.retry'\) \}\}/)
  assert.match(
    customerWelcome,
    /function retryLoadAgents\(\): void \{[\s\S]*?productLineStore\.retryBootstrapAgents\(\)/,
  )
})

test('F6 welcome loadFailed / retry i18n keys exist in all six locales', () => {
  // 键位对齐由 npm run check-i18n 兜底；这里直接钉住六个语言文件里
  // brand.welcome 块（以 chooseAgent 为锚，避开其他同名 welcome 块）都补齐了两个新 key。
  // 块边界用索引定位而不是 [^}]*：文案值里就有字面量 }（如「欢迎使用{name}」）。
  for (const { lang, source } of locales) {
    const anchor = source.indexOf('chooseAgent')
    assert.ok(anchor >= 0, `${lang}: brand.welcome.chooseAgent not found`)
    const blockStart = source.lastIndexOf('welcome: {', anchor)
    // welcome 是扁平块：闭合处是行首 4 空格缩进的 },
    const blockEnd = source.indexOf('\n    },', anchor)
    assert.ok(blockStart >= 0 && blockEnd > anchor, `${lang}: brand.welcome block not found`)
    const block = source.slice(blockStart, blockEnd)
    assert.match(block, /loadFailed["']?\s*:/, `${lang} must define brand.welcome.loadFailed`)
    assert.match(block, /retry["']?\s*:/, `${lang} must define brand.welcome.retry`)
  }
})
