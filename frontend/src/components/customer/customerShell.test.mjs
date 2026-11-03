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

test('customer welcome renders agent title, visible line cards and samples', () => {
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
  // 旧 productLinesFallback 提示已删除（AC-10）；showEmptyState 空态 computed
  // 已随 T04 删除（模板本无消费）——空态提示统一由下方 W9 弹窗承接。
  // W9：欢迎区不再自己渲染空态副标题/去绑定按钮，未绑定提示交由 CustomerNoAgentDialog 承接。
  assert.doesNotMatch(welcome, /customer-welcome__subtitle/)
  assert.doesNotMatch(welcome, /customer-welcome__bind-btn/)
  assert.doesNotMatch(welcome, /productLinesFallback/)
  assert.match(welcome, /samples-swap/)
  assert.doesNotMatch(welcome, /WeKnora/)
  // 示例问题只抛事件，不自己发请求：与手动输入共用发送链路（AC-20）。
  assert.match(welcome, /emit\('send', question\)/)
  assert.doesNotMatch(welcome, /api\/|fetch\(|axios|knowledge-chat/)
})

test('customer welcome opens the agent picker dialog from the desktop toggle above four (T01)', () => {
  // 桌面：阈值 4（AC-12，E2E 反馈修订，>4 才折叠）保留；PC「查看更多」改为打开选择弹窗（T01/AC-1）。
  assert.match(welcome, /const COLLAPSED_LINE_COUNT = 4/)
  assert.match(welcome, /const hasOverflow = computed\(\(\) => lines\.value\.length > COLLAPSED_LINE_COUNT\)/
  )
  // T10/T01：两个 toggle 分支并存——移动「查看更多」按钮在前（弹窗入口），桌面按钮改挂 v-else-if。
  assert.match(welcome, /<button v-if="hasMobileOverflow" type="button" class="sample-chip customer-welcome__toggle"/)
  assert.match(welcome, /aria-haspopup="dialog" @click="agentPickerVisible = true"/)
  assert.match(welcome, /<button v-else-if="hasOverflow" type="button" class="sample-chip customer-welcome__toggle"/)
  // AC-1：桌面按钮语义改为 aria-haspopup="dialog"，点击打开同一个 T10 选择弹窗，不再内联展开。
  assert.match(
    welcome,
    /<button v-else-if="hasOverflow"[^>]*aria-haspopup="dialog" @click="agentPickerVisible = true"/,
  )
  // AC-1：桌面按钮无 aria-expanded 切换（折叠/展开语义整体移除）。
  assert.doesNotMatch(welcome, /aria-expanded/)
  // AC-1：按钮文案固定 showMoreLines，不再有「收起」态（旧的 expanded?collapseLines 三态消失）。
  assert.match(welcome, /t\('brand\.welcome\.showMoreLines'\)/)
  assert.doesNotMatch(welcome, /t\('brand\.welcome\.collapseLines'\)/)
})

test('mobile welcome shows two cards with the current line first and opens a picker dialog (T10)', () => {
  // 移动端阈值 2：>2 张才出「查看更多」，且必须同时要求 isMobileLayout（桌面恒 false）。
  assert.match(welcome, /const MOBILE_MAX_LINE_COUNT = 2/)
  assert.match(
    welcome,
    /const hasMobileOverflow = computed\(\s*\(\) => isMobileLayout\.value && lines\.value\.length > MOBILE_MAX_LINE_COUNT,?\s*\)/,
  )
  // 弹窗全量列表：当前选中永远排第一，其余保持绑定顺序。
  assert.match(
    welcome,
    /const pickerLines = computed\(\(\) => \{\s*const current = currentLine\.value\s*if \(!current\) return lines\.value\s*return \[current, \.\.\.lines\.value\.filter\(\(line\) => line\.id !== current\.id\)\]\s*\}\)/,
  )
  // T02 排序修复：visibleLines 统一交给纯函数 computeVisibleLines——未超限保留绑定顺序、
  // 超限当前选中置顶并按顺序补足。真实排序行为由 visibleLines.test.mjs 单测覆盖，这里钉接线。
  const visibleStart = welcome.indexOf('const visibleLines = computed')
  assert.ok(visibleStart >= 0, 'visibleLines not found')
  const visibleBody = welcome.slice(visibleStart, visibleStart + 400)
  assert.match(
    welcome,
    /import \{ computeVisibleLines \} from '\.\/visibleLines'/,
    'visibleLines 未接入可见列表纯函数 computeVisibleLines',
  )
  assert.match(
    visibleBody,
    /const maxVisible = isMobileLayout\.value \? MOBILE_MAX_LINE_COUNT : COLLAPSED_LINE_COUNT/,
    '布局上限映射（移动 2 / 桌面 4）缺失',
  )
  assert.match(
    visibleBody,
    /return computeVisibleLines\(lines\.value, currentLine\.value\?\.id, maxVisible\)/,
    'visibleLines 未调用 computeVisibleLines',
  )
  // AC-3：桌面不再有 expanded 全量分支；expanded / toggleExpanded 整体移除。
  assert.doesNotMatch(visibleBody, /expanded\.value/)
  assert.doesNotMatch(welcome, /\bexpanded\b/)
  assert.doesNotMatch(welcome, /toggleExpanded/)
  // 查看更多只打开选择弹窗（t-dialog + 全量 pickerLines），点选切线并关闭。
  assert.match(welcome, /<t-dialog\s+v-model:visible="agentPickerVisible"/)
  assert.match(welcome, /:close-btn="true"/)
  assert.match(welcome, /:close-on-overlay-click="true"/)
  assert.match(welcome, /:close-on-esc-keydown="true"/)
  assert.match(welcome, /v-for="line in pickerLines"/)
  assert.match(welcome, /@click="chooseLineInPicker\(line\)"/)
  assert.match(welcome, /agentPickerVisible\.value = false/)
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

test('customer shell dark theme swaps to page background with white grid, light grid intact (T03)', () => {
  // T03 / AC-9：两个客户壳在 theme-mode="dark" 下换既有主题页面底变量 + 低透明度白线网格，
  // 不新造色值；AC-10：dark 块不强制文字色（正文走主题变量）。管理员页不带 customer-shell，
  // 规则零命中（C6）。浅色（默认）网格定义必须原样保留。真机深色观感由用户在 8084 复验。
  const shells = [
    ['chat/index.vue', chatView, '.chat.customer-shell'],
    ['creatChat.vue', creatChat, '.dialogue-wrap.customer-shell'],
  ]
  for (const [name, source, selector] of shells) {
    const escaped = selector.replace(/\./g, '\\.')

    // AC-9：dark 覆盖块存在，背景用 --td-bg-color-page，网格线横竖两向都是 0.04 白线。
    const darkBlock = new RegExp(`html\\[theme-mode="dark"\\] ${escaped} \\{([^}]*)\\}`).exec(source)
    assert.ok(darkBlock, `${name} 缺少 html[theme-mode="dark"] + ${selector} 深色覆盖块`)
    assert.match(
      darkBlock[1],
      /background-color:\s*var\(--td-bg-color-page\);/,
      `${name} dark 块背景未用 --td-bg-color-page`,
    )
    for (const deg of ['0deg', '90deg']) {
      assert.match(
        darkBlock[1],
        new RegExp(`repeating-linear-gradient\\(${deg},\\s*rgba\\(255,\\s*255,\\s*255,\\s*0\\.04\\)`),
        `${name} dark 块缺 ${deg} 白线网格`,
      )
    }

    // AC-10：dark 块不得出现 color 声明（background-color 不算），不全局强制文字色。
    assert.doesNotMatch(darkBlock[1], /(^|[^-\w])color\s*:/, `${name} dark 块出现 color 声明`)

    // AC-9：浅色（默认）网格定义原样保留——#F6F8FB 底 + 深色细线网格。
    const lightBlock = new RegExp(`${escaped} \\{([^}]*)\\}`).exec(source)
    assert.ok(lightBlock, `${name} 浅色 ${selector} 背景定义缺失`)
    assert.match(lightBlock[1], /background-color:\s*#F6F8FB;/, `${name} 浅色 #F6F8FB 底色缺失`)
    assert.match(lightBlock[1], /rgba\(15,\s*23,\s*42,\s*0\.035\)/, `${name} 浅色网格线定义缺失`)

    // F21/F22 守卫口径：这两份壳文件不出现 :global(（要跨作用域的规则进全局 CSS）。
    assert.doesNotMatch(source, /:global\(/, `${name} 不得使用 :global(`)
  }
})
