import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

/**
 * 客户外壳与对话链路（AC-5 / AC-17 / AC-20 / AC-21 / AC-22 / AC-29 / C6）。
 * 组件逻辑依赖运行时 i18n / Pinia / 真实发送链路，这里做源码级接线断言；
 * 真机效果由 05-tests.md 的浏览器证据覆盖。
 */

const welcome = readFileSync(new URL('./CustomerWelcome.vue', import.meta.url), 'utf8')
const topBar = readFileSync(new URL('./CustomerTopBar.vue', import.meta.url), 'utf8')
const lineSelector = readFileSync(new URL('./LineSelector.vue', import.meta.url), 'utf8')
const creatChat = readFileSync(new URL('../../views/creatChat/creatChat.vue', import.meta.url), 'utf8')
const inputField = readFileSync(new URL('../Input-field.vue', import.meta.url), 'utf8')
const chatView = readFileSync(new URL('../../views/chat/index.vue', import.meta.url), 'utf8')
const menuVue = readFileSync(new URL('../menu.vue', import.meta.url), 'utf8')
const loginVue = readFileSync(new URL('../../views/auth/Login.vue', import.meta.url), 'utf8')

test('customer welcome renders agent title, visible line cards, samples and the empty state', () => {
  // AC-21：标题改为「选择你的 agent」字标（中文环境用图片，其他语言保留本地化文字）。
  assert.match(welcome, /brand\/choose-agent-wordmark\.png/)
  assert.match(welcome, /t\('brand\.welcome\.chooseAgent'\)/)
  assert.doesNotMatch(welcome, /brandConfig\.productName/)
  // AC-12：卡片渲染折叠后的 visibleLines，而不是全量 lines。
  assert.match(welcome, /v-for="line in visibleLines"/)
  assert.match(welcome, /v-for="question in samples"/)
  // 免责声明已移到 creatChat 输入框下方（用户反馈「放到最底部」），欢迎区不再渲染。
  assert.doesNotMatch(welcome, /brandConfig\.chatDisclaimer/)
  assert.match(creatChat, /customer-disclaimer/)
  // AC-10 / W5：空态只在已就绪且 0 条时出现；旧 productLinesFallback 提示已删除。
  assert.match(welcome, /const showEmptyState = computed\(\(\) => productLineStore\.ready && lines\.value\.length === 0\)/)
  // W9：欢迎区不再自己渲染空态副标题/去绑定按钮，未绑定提示交由 CustomerNoAgentDialog 承接。
  assert.doesNotMatch(welcome, /v-if="showEmptyState"/)
  assert.doesNotMatch(welcome, /customer-welcome__subtitle/)
  assert.doesNotMatch(welcome, /customer-welcome__bind-btn/)
  assert.doesNotMatch(welcome, /productLinesFallback/)
  assert.match(welcome, /samples-swap/)
  assert.doesNotMatch(welcome, /WeKnora/)
  // 示例问题只抛事件，不自己发请求：与手动输入共用发送链路（AC-20）。
  assert.match(welcome, /emit\('send', question\)/)
  assert.doesNotMatch(welcome, /api\/|fetch\(|axios|knowledge-chat/)
})

test('customer welcome collapses cards above four and can expand/collapse in place', () => {
  // AC-12：阈值 4（E2E 反馈修订，>4 才折叠）、折叠切片、就地展开收起。
  assert.match(welcome, /const COLLAPSED_LINE_COUNT = 4/)
  assert.match(welcome, /const hasOverflow = computed\(\(\) => lines\.value\.length > COLLAPSED_LINE_COUNT\)/)
  assert.match(
    welcome,
    /visibleLines = computed\(\(\) =>\s*hasOverflow\.value && !expanded\.value \? lines\.value\.slice\(0, COLLAPSED_LINE_COUNT\) : lines\.value,?\s*\)/,
  )
  assert.match(welcome, /function toggleExpanded\(\): void \{\s*expanded\.value = !expanded\.value\s*\}/)
  assert.match(welcome, /v-if="hasOverflow"/)
  assert.match(welcome, /:aria-expanded="expanded"/)
  assert.match(welcome, /t\('brand\.welcome\.(showMoreLines|collapseLines)'\)/)
})

test('customer welcome no longer renders subtitle / bind link; no-agent dialog takes over', () => {
  // W9 / 2026-10-30 用户修正：未绑定时欢迎区不再显示「输入 agentid…」副标题与
  // 「去绑定」按钮，改由可关闭的 CustomerNoAgentDialog 承接（T02）。
  assert.doesNotMatch(welcome, /customer-welcome__subtitle/)
  assert.doesNotMatch(welcome, /customer-welcome__bind-btn/)
  assert.doesNotMatch(welcome, /class="sample-chip customer-welcome__bind-btn"/)
  assert.doesNotMatch(welcome, /router\.push\(/)
  assert.doesNotMatch(welcome, /brand\.welcome\.emptyAction/)
  assert.doesNotMatch(welcome, /t\('brand\.welcome\.emptyNotice'\)/, '空态不再显示提示句')
  // W5：就绪、未失败且 0 条时由欢迎页触发弹窗（关闭由弹窗自己处理）。
  assert.match(welcome, /if \(ready && !failed && len === 0\) uiStore\.openCustomerNoAgentDialog\(\)/)
  assert.match(welcome, /const uiStore = useUIStore\(\)/)
})

test('sample questions and manual input both go through Input-field.triggerSend', () => {
  assert.match(creatChat, /CustomerWelcome v-if="isCustomer" @send="handleSampleQuestion"/)
  assert.match(creatChat, /const handleSampleQuestion = \(question: string\) => \{[\s\S]*?inputFieldRef\.value\?\.triggerSend\(question\)/)
  assert.match(creatChat, /<InputField ref="inputFieldRef" :customer-mode="isCustomer"/)
  // 推荐问题块只在非客户分支渲染，客户首页不显示空消息列表/旧推荐块。
  assert.match(creatChat, /<CustomerWelcome v-if="isCustomer"/)
  assert.match(creatChat, /<div v-else ref="sqContainerRef" class="suggested-questions-container">/)
})

test('customer composer keeps only the line selector and send button', () => {
  // R3-2：客户分支必须整体包裹，0/1 条产线（或 agents 未加载）时也不能掉进
  // 下方管理员 v-else 工具栏；产线数条件只控制客户分支内部的 LineSelector。
  assert.match(inputField, /<template v-if="customerMode">/)
  assert.match(inputField, /<LineSelector\s+v-if="\(productLineStore\?\.lines\.length \?\? 0\) > 1"/)
  // AC-13：下拉拿全量列表，不做 3 条切片。
  assert.match(inputField, /:lines="productLineStore\?\.lines \?\? \[\]"/)
  assert.match(inputField, /<template v-else>\s*<!-- Agent 模式切换按钮 -->/)
  assert.match(inputField, /<AttachmentUpload v-if="!customerMode"/)
  assert.match(inputField, /<MorphIcon v-if="customerMode" class="send-btn__morph"/)
  // 客户模式不创建/读取管理员侧的智能体与知识库控件。
  assert.doesNotMatch(inputField, /customerMode[\s\S]{0,200}AgentSelector\s+v-if/)
})

test('customer line selector shows only passed-in lines, with no admin entry points', () => {
  assert.match(lineSelector, /v-for="\(line, index\) in lines"/)
  // 产线数据来自 productLine store 的 props，不再读品牌配置（C5）。
  assert.doesNotMatch(lineSelector, /brandConfig\.productLines/)
  assert.match(lineSelector, /lines: \(\) => \[\]/)
  for (const forbidden of ['管理智能体', '内置智能体', '共享给我', '内置', '自定义']) {
    assert.equal(lineSelector.includes(forbidden), false, `line selector must not contain ${forbidden}`)
  }
  // AC-13：输入区下拉不做折叠切片。
  assert.doesNotMatch(lineSelector, /lines\.slice\(/)
  assert.match(lineSelector, /role="listbox"/)
  assert.match(lineSelector, /aria-expanded/)
  assert.match(lineSelector, /ArrowDown/)
  assert.match(lineSelector, /Escape/)
})

test('customer shell swaps in CustomerTopBar on both new-chat and history chat pages', () => {
  assert.match(creatChat, /<CustomerTopBar v-if="isCustomer" \/>/)
  assert.match(chatView, /<CustomerTopBar v-if="!embeddedMode && isCustomer"/)
  // 客户页不再渲染 ChatHeader，避免双顶栏。
  assert.match(chatView, /<div v-else-if="!embeddedMode" class="chat-topbar">\s*<ChatHeader :session="currentSession" \/>/)
  // 产品名改用字标图片，左侧小图标保留（用户反馈：不要去掉小图标）。
  assert.match(topBar, /customer-topbar__product-icon/)
  assert.match(topBar, /brand\/xiaoi-wordmark\.png/)
  assert.doesNotMatch(topBar, /brandConfig\.productName/)
  for (const forbidden of ['WeKnora', 'weknora']) {
    assert.equal(topBar.includes(forbidden), false)
  }
})

test('logo sizing: sidebar fixed 108px for both roles, login uses runtime logoWidth, no dark invert filter', () => {
  assert.match(menuVue, /sidebarLogoSrc = computed\(\(\) => \(isCustomer\.value \? brandConfig\.logoUrl : allstarLogoUrl\)\)/)
  // U2：侧栏 logo 客户与管理员共用同一条 108px CSS，menu.vue 不再读 logoWidth
  // （代码里不允许出现 brandConfig.logoWidth，注释提法不算）。
  assert.doesNotMatch(menuVue, /brandConfig\.logoWidth/)
  assert.match(menuVue, /\.logo \{\s*width: 108px;/)
  // logoWidth 语义收窄为「登录页 logo 宽度」：Login.vue 注入 --brand-logo-width。
  assert.match(loginVue, /'--brand-logo-width': `\$\{brandConfig\.logoWidth\}px`/)
  // 深色模式只允许对菜单图标做 invert；logo 不再走反色滤镜（AC-5 深色可辨）。
  assert.doesNotMatch(menuVue, /html\[theme-mode="dark"\][\s\S]{0,200}\.logo[\s\S]{0,200}(?:invert|hue-rotate)/)
  assert.doesNotMatch(menuVue, /\.logo\s*\{[^}]*filter:/)
})
