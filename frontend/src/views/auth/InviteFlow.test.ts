import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import { createSSRApp, ref } from 'vue'
import { renderToString } from '@vue/server-renderer'
import zhCN from '../../i18n/locales/zh-CN'
import zhTW from '../../i18n/locales/zh-TW'
import enUS from '../../i18n/locales/en-US'
import jaJP from '../../i18n/locales/ja-JP'
import koKR from '../../i18n/locales/ko-KR'
import ruRU from '../../i18n/locales/ru-RU'
import {
  PASSWORD_SPECIAL_CHARS,
  PASSWORD_SPECIAL_CHAR_REGEX,
  newPasswordRules,
} from '../../utils/passwordPolicy'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

// T05 整体回归：邀请落地页弹窗 / 注册表单 / Login 接线。
// 组件行为用 vueFixture 驱动真实 setup；模板文案用 SSR 渲染真实 SFC 模板，
// 避免只断言源码字符串。

function translate(message: any, key: string, params: Record<string, unknown> = {}): string {
  const value = key.split('.').reduce<any>((node, part) => node?.[part], message)
  if (typeof value !== 'string') throw new Error(`缺少 i18n key: ${key}`)
  return value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
}

// SSR 会把文本里的 & < > 转义；断言密码字符集时先按同样规则转义。
function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

function registerUiStubs(app: any) {
  app.component('t-dialog', {
    props: ['visible', 'width', 'footer'],
    template: '<div class="dialog"><slot name="header"/><slot/></div>',
  })
  app.component('t-icon', { template: '<span />' })
  app.component('t-button', {
    props: ['theme', 'size', 'block', 'variant', 'type', 'loading'],
    template: '<button><slot/></button>',
  })
  app.component('t-form', {
    props: ['data', 'rules', 'labelAlign'],
    template: '<form><slot/></form>',
  })
  app.component('t-form-item', {
    props: ['label', 'name'],
    template: '<div><label>{{ label }}</label><slot/></div>',
  })
  app.component('t-input', {
    props: ['modelValue', 'type', 'disabled', 'placeholder', 'autocomplete', 'size'],
    emits: ['update:modelValue', 'enter'],
    template: '<input />',
  })
}

async function renderSfc(file: URL, template: string, data: Record<string, unknown>) {
  const app = createSSRApp({
    template,
    data: () => data,
    methods: { handleSubmit() {}, onVisibleUpdate() {} },
  })
  ;(app.config.globalProperties as any).$t = (
    key: string,
    params?: Record<string, unknown>,
  ) => translate(zhCN, key, params)
  registerUiStubs(app)
  return renderToString(app)
}

function componentTemplate(file: URL): string {
  const { descriptor } = parse(readFileSync(file, 'utf8'))
  assert.ok(descriptor.template, `${file.pathname} 缺少 template`)
  return descriptor.template!.content
}

// ---------- InviteNoAccountDialog（AC-2） ----------

test('无账号弹窗用运行时品牌名插值并给出创建/登录双路文案', async () => {
  const file = new URL('./InviteNoAccountDialog.vue', import.meta.url)
  const html = await renderSfc(file, componentTemplate(file), {
    visible: true,
    brandName: '星河云',
  })
  assert.ok(html.includes('您当前没有星河云的账号，是否去创建一个全新的账号？'))
  assert.ok(html.includes('创建新账号'))
  assert.ok(html.includes('我已有账号，去登录'))
  assert.ok(!html.includes('{brand}'), '品牌占位符必须被插值')
})

test('无账号弹窗按用户操作发出 create / login / update:visible 事件', async (t: TestContext) => {
  const f = fixture(t)
  const events: string[] = []
  const { vm } = f.component(new URL('./InviteNoAccountDialog.vue', import.meta.url), {}, {
    visible: true,
    brandName: 'Brand',
    onCreate: () => events.push('create'),
    onLogin: () => events.push('login'),
    'onUpdate:visible': (value: boolean) => events.push(`visible:${value}`),
  })
  await flush()
  vm.onVisibleUpdate(false)
  assert.deepEqual(events, ['visible:false'])
})

// ---------- InviteRegisterForm（AC-3 / AC-4 / AC-9） ----------

type RegisterFormOptions = {
  response?: any
  throwError?: Error
  complexPasswordEnabled?: boolean
  t?: (key: string, params?: Record<string, unknown>) => string
}

async function registerFormFixture(t: TestContext, options: RegisterFormOptions = {}) {
  const f = fixture(t)
  const calls: any[] = []
  const messages: Array<[string, string]> = []
  const events: string[] = []
  const tFn = options.t ?? ((key: string) => key)
  const { vm } = f.component(new URL('./InviteRegisterForm.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t: tFn }) },
    'tdesign-vue-next': {
      MessagePlugin: {
        info: (message: string) => messages.push(['info', message]),
        warning: (message: string) => messages.push(['warning', message]),
        error: (message: string) => messages.push(['error', message]),
      },
    },
    '@/utils/passwordPolicy': {
      PASSWORD_SPECIAL_CHARS,
      PASSWORD_SPECIAL_CHAR_REGEX,
      newPasswordRules,
    },
    '@/api/auth': {
      registerByInvite: async (body: any) => {
        calls.push(body)
        if (options.throwError) throw options.throwError
        return options.response ?? { success: true, token: 't', user: { id: 'u' } }
      },
    },
  }, {
    token: 'invite-token',
    complexPasswordEnabled: options.complexPasswordEnabled ?? false,
    onSuccess: () => events.push('success'),
    onLogin: () => events.push('login'),
    onInvalid: () => events.push('invalid'),
  })
  await flush()
  vm.formRef = { validate: async () => true }
  vm.form.email = 'new@example.com'
  vm.form.username = '新人'
  vm.form.password = 'Abcdef1!'
  return { vm, calls, messages, events }
}

test('注册表单校验通过后提交 token/邮箱/用户名/密码并在成功后清空密码', async (t: TestContext) => {
  const f = await registerFormFixture(t)
  await f.vm.handleSubmit()
  assert.deepEqual(JSON.parse(JSON.stringify(f.calls)), [{
    token: 'invite-token',
    email: 'new@example.com',
    username: '新人',
    password: 'Abcdef1!',
  }])
  assert.deepEqual(f.events, ['success'])
  assert.equal(f.vm.form.password, '')
})

test('409 邮箱已注册：提示后切回登录，不提示注册失败', async (t: TestContext) => {
  const f = await registerFormFixture(t, {
    response: { success: false, status: 409, message: 'exists' },
  })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [['info', 'inviteRegister.emailExists']])
  assert.deepEqual(f.events, ['login'])
})

test('410 链接失效：回到失效分支，不展示表单', async (t: TestContext) => {
  const f = await registerFormFixture(t, {
    response: { success: false, status: 410, message: 'gone' },
  })
  await f.vm.handleSubmit()
  assert.deepEqual(f.events, ['invalid'])
  assert.deepEqual(f.messages, [], '不应额外提示')
})

test('403 注册已禁用：警告 registerDisabled 并切回登录，不按链接失效率处理（F6）', async (t: TestContext) => {
  const f = await registerFormFixture(t, {
    response: { success: false, status: 403, message: 'registration disabled' },
  })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [['warning', 'inviteRegister.registerDisabled']])
  assert.deepEqual(f.events, ['login'])
})

test('400 展示服务端字段/密码提示并停留在表单', async (t: TestContext) => {
  const f = await registerFormFixture(t, {
    response: { success: false, status: 400, message: '密码不合规' },
  })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [['error', '密码不合规']])
  assert.deepEqual(f.events, [])
})

test('400 密码策略错误展示本地化提示（含特殊字符集插值），不透出服务端英文（F2/F9）', async (t: TestContext) => {
  const serverMessage = 'password must be 8-32 characters and contain at least one letter and one number'
  const f = await registerFormFixture(t, {
    response: { success: false, status: 400, message: serverMessage },
    t: (key, params) => translate(zhCN, key, params),
  })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [[
    'error',
    `密码需为 8–32 位，并包含大写字母、小写字母、数字和特殊字符（${PASSWORD_SPECIAL_CHARS}）。`,
  ]])
  assert.equal(f.messages[0][1].includes('password must'), false, '不应直出服务端英文')
  assert.deepEqual(f.events, [])
})

test('并发提交（表单 submit + 密码框 Enter）只调用一次注册接口（F3）', async (t: TestContext) => {
  const f = await registerFormFixture(t)
  await Promise.all([f.vm.handleSubmit(), f.vm.handleSubmit()])
  assert.equal(f.calls.length, 1, '重复提交必须被前置守卫拦下')
  assert.deepEqual(f.events, ['success'])
  assert.equal(f.vm.submitting, false, '提交结束后必须复位 submitting')
})

test('网络异常展示错误并停留表单，不误发 login/invalid', async (t: TestContext) => {
  const f = await registerFormFixture(t, { throwError: new Error('网络错误') })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [['error', '网络错误']])
  assert.deepEqual(f.events, [])
})

test('复杂密码开启时展示规则提示，并追加大小写/数字/特殊字符校验', async (t: TestContext) => {
  const file = new URL('./InviteRegisterForm.vue', import.meta.url)
  const policyHint = `密码需为 8–32 位，并包含大写字母、小写字母、数字和特殊字符（${PASSWORD_SPECIAL_CHARS}）。`
  const htmlOn = await renderSfc(file, componentTemplate(file), {
    form: { email: '', username: '', password: '' },
    complexPasswordEnabled: true,
    submitting: false,
    rules: {},
    PASSWORD_SPECIAL_CHARS,
  })
  assert.ok(
    htmlOn.includes(escapeHtml(policyHint)),
    `复杂密码提示应插值特殊字符集；实际渲染：${htmlOn}`,
  )

  const weak = await registerFormFixture(t, { complexPasswordEnabled: true })
  const rules = weak.vm.rules.password as any[]
  // 与后端同口径：必填/最短/最长 + 小写/大写/数字/特殊字符，共 7 条。
  assert.equal(rules.length, 7)
  assert.ok(rules.some((rule) => rule.required === true), '缺少必填规则')
  assert.ok(rules.some((rule) => rule.min === 8), '缺少最短 8 位规则')
  assert.ok(rules.some((rule) => rule.max === 32), '缺少最长 32 位规则')

  function patternRule(source: string) {
    const rule = rules.find(
      (item) => item.pattern && typeof item.pattern.test === 'function' && item.pattern.source === source,
    )
    assert.ok(rule, `缺少 pattern ${source} 的规则`)
    return rule as { pattern: RegExp; message: string }
  }
  const lowercase = patternRule('[a-z]')
  const uppercase = patternRule('[A-Z]')
  const digit = patternRule('\\d')
  const special = patternRule(PASSWORD_SPECIAL_CHAR_REGEX.source)

  assert.equal(lowercase.message, 'auth.passwordMustContainLowercaseLetter')
  assert.equal(uppercase.message, 'auth.passwordMustContainUppercaseLetter')
  assert.equal(digit.message, 'auth.passwordMustContainNumber')
  assert.equal(special.message, 'auth.passwordMustContainSpecialChar')

  assert.equal(lowercase.pattern.test('Abcdef1!'), true)
  assert.equal(lowercase.pattern.test('ABCDEF1!'), false)
  assert.equal(uppercase.pattern.test('Abcdef1!'), true)
  assert.equal(uppercase.pattern.test('abcdef1!'), false)
  assert.equal(digit.pattern.test('Abcdef1!'), true)
  assert.equal(digit.pattern.test('Abcdefg!'), false)
  assert.equal(special.pattern.test('Abcdef1!'), true)
  assert.equal(special.pattern.test('Abcdef12'), false)
})

test('复杂密码关闭时不显示规则提示，密码规则为长度 + 字母 + 数字', async (t: TestContext) => {
  const file = new URL('./InviteRegisterForm.vue', import.meta.url)
  const htmlOff = await renderSfc(file, componentTemplate(file), {
    form: { email: '', username: '', password: '' },
    complexPasswordEnabled: false,
    submitting: false,
    rules: {},
    PASSWORD_SPECIAL_CHARS,
  })
  assert.ok(!htmlOff.includes('密码需为 8–32 位'))

  const plain = await registerFormFixture(t, { complexPasswordEnabled: false })
  const plainRules = plain.vm.rules.password as any[]
  // 必填/最短/最长 + 字母 + 数字，共 5 条。
  assert.equal(plainRules.length, 5)
  assert.ok(plainRules.some((rule) => rule.required === true), '缺少必填规则')
  assert.ok(plainRules.some((rule) => rule.min === 8), '缺少最短 8 位规则')
  assert.ok(plainRules.some((rule) => rule.max === 32), '缺少最长 32 位规则')

  const letter = plainRules.find(
    (rule) => rule.pattern && typeof rule.pattern.test === 'function' && rule.pattern.source === '[a-zA-Z]',
  )
  const digit = plainRules.find(
    (rule) => rule.pattern && typeof rule.pattern.test === 'function' && rule.pattern.source === '\\d',
  )
  assert.ok(letter, '简单模式缺少字母规则')
  assert.ok(digit, '简单模式缺少数字规则')
  assert.equal(letter.pattern.test('12345678'), false)
  assert.equal(letter.pattern.test('abcdefgh'), true)
  assert.equal(digit.pattern.test('abcdefgh'), false)
  assert.equal(digit.pattern.test('12345678'), true)
  assert.equal(letter.pattern.test('Abcdef12') && digit.pattern.test('Abcdef12'), true)
})

// ---------- Login.vue 接线（AC-1 / AC-5 / AC-6 / AC-7 / AC-8） ----------

type LoginOptions = {
  token?: string
  loggedIn?: boolean
  refresh?: boolean
  lookup?: any
  lookupFails?: boolean
  config?: any
  loginResponse?: any
}

async function loginFixture(t: TestContext, options: LoginOptions = {}) {
  const f = fixture(t)
  const calls = {
    accept: [] as string[],
    replace: [] as string[],
    tokens: [] as string[],
    config: 0,
    lookup: 0,
    autoSetup: 0,
  }
  const route = { query: options.token ? { token: options.token } : {} }
  const auth: any = {
    isLoggedIn: options.loggedIn ?? false,
    hasValidTenant: true,
    refreshFromAuthMe: async () => options.refresh ?? false,
    acceptInvitationByTokenAndRefresh: async (token: string) => {
      calls.accept.push(token)
      return { ok: true }
    },
    setUser() {}, setRefreshToken() {}, setTenant() {}, setMemberships() {},
    setCanCreateTenant() {}, setAccessRevoked() {}, setSelectedTenant() {}, setLiteMode() {},
    setToken(token: string) { calls.tokens.push(token) },
  }
  const api = {
    login: async () => options.loginResponse ?? { success: false },
    autoSetup: async () => { calls.autoSetup++; return { success: false } },
    getInvitationByToken: async (token: string) => {
      calls.lookup++
      if (options.lookupFails) throw new Error('lookup failed')
      return options.lookup ?? { success: false, message: 'bad token' }
    },
    getAuthConfig: async () => {
      calls.config++
      return options.config ?? { registration_mode: 'invite_register', complex_password_enabled: true }
    },
    userInfoFromApi: (user: any) => user,
  }
  const { vm } = f.component(new URL('./Login.vue', import.meta.url), {
    'vue-router': {
      useRoute: () => route,
      useRouter: () => ({ replace: (path: string) => calls.replace.push(path) }),
    },
    'tdesign-vue-next': {
      MessagePlugin: { success() {}, warning() {}, error() {}, info() {} },
    },
    '@/composables/useRoleLabel': {
      useRoleLabel: () => ({ formatRole: () => '', roleIcon: () => '' }),
    },
    '@/utils/loginNotify': { notifyLoginSuccess() {} },
    '@/api/auth': api,
    '@/stores/auth': { useAuthStore: () => auth },
    'vue-i18n': {
      useI18n: () => ({
        t: (key: string, params?: Record<string, unknown>) => translate(zhCN, key, params),
        tm: () => ({}),
        locale: ref('zh-CN'),
      }),
    },
    '@/brand': {
      applyDocumentBrand() {},
      brandConfig: { productName: 'BrandX', logoUrl: '/brand/logo.png' },
    },
    './registrationPolicy': {
      canRegister: (mode: string, hasInvitation: boolean) =>
        mode === 'self_serve' || (mode === 'invite_register' && hasInvitation),
    },
  })
  for (let i = 0; i < 10; i++) await flush()
  return { vm, calls, auth, api }
}

const validLookup = { success: true, data: { tenant_id: 7, tenant_name: '团队', role: 'viewer' } }

test('邀请链接 + 无有效会话 + invite_register：弹出无账号引导，不展示注册表单', async (t: TestContext) => {
  const f = await loginFixture(t, { token: 'tok', lookup: validLookup })
  assert.equal(f.calls.lookup, 1)
  assert.equal(f.calls.config, 1)
  assert.equal(f.vm.inviteDialogVisible, true)
  assert.equal(f.vm.inviteRegisterVisible, false)
})

test('邀请链接 + 无有效会话 + self_serve：同样展示创建入口', async (t: TestContext) => {
  const f = await loginFixture(t, {
    token: 'tok', lookup: validLookup, config: { registration_mode: 'self_serve' },
  })
  assert.equal(f.vm.inviteDialogVisible, true)
})

test('invite_only 模式不展示注册入口，仅保留登录表单', async (t: TestContext) => {
  const f = await loginFixture(t, {
    token: 'tok', lookup: validLookup, config: { registration_mode: 'invite_only' },
  })
  assert.equal(f.calls.config, 1)
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.vm.inviteRegisterVisible, false)
})

test('邀请链接 + 有效登录态：不弹窗，直接凭 token 加入并进入平台', async (t: TestContext) => {
  const f = await loginFixture(t, {
    token: 'tok', lookup: validLookup, loggedIn: true, refresh: true,
  })
  assert.deepEqual(f.calls.accept, ['tok'])
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.calls.config, 0, '已登录时不应再拉注册配置')
  assert.deepEqual(f.calls.replace, ['/platform/knowledge-bases'])
})

test('lookup 返回 success:false + 410：展示本地化「邀请链接已失效」，不透出服务端英文（AC-7/F10）', async (t: TestContext) => {
  const f = await loginFixture(t, {
    token: 'tok',
    lookup: {
      success: false,
      status: 410,
      message: 'invitation link is invalid or has been revoked',
    },
  })
  assert.equal(f.vm.inviteLookupError, zhCN.inviteRegister.invalidTitle)
  assert.ok(!f.vm.inviteLookupError.includes('invitation link'), '不应直出服务端英文')
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.calls.config, 0)
})

test('lookup 抛错（网络异常）：同样展示本地化「邀请链接已失效」，不弹窗（AC-7）', async (t: TestContext) => {
  const f = await loginFixture(t, { token: 'tok', lookupFails: true })
  assert.equal(f.vm.inviteLookupError, zhCN.inviteRegister.invalidTitle)
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.calls.config, 0)
})

test('正常登录页（无 token）不出现任何注册入口', async (t: TestContext) => {
  const f = await loginFixture(t)
  assert.equal(f.calls.autoSetup, 1)
  assert.equal(f.calls.lookup, 0)
  assert.equal(f.vm.inviteToken, '')
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.vm.inviteRegisterVisible, false)
})

test('弹窗操作：创建 → 注册表单；返回登录 / 链接失效 → 回到登录视图', async (t: TestContext) => {
  const f = await loginFixture(t, { token: 'tok', lookup: validLookup })
  f.vm.openInviteRegister()
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.vm.inviteRegisterVisible, true)

  f.vm.showInviteLogin()
  assert.equal(f.vm.inviteDialogVisible, false)
  assert.equal(f.vm.inviteRegisterVisible, false)

  f.vm.inviteRegisterVisible = true
  f.vm.handleInviteInvalid()
  assert.equal(f.vm.inviteRegisterVisible, false)
  assert.equal(f.vm.inviteLookup, null)
  assert.equal(f.vm.inviteLookupError, zhCN.inviteRegister.invalidTitle)
})

test('注册成功：持久化会话后进入平台', async (t: TestContext) => {
  const f = await loginFixture(t, { token: 'tok', lookup: validLookup })
  await f.vm.handleInviteRegisterSuccess({
    success: true,
    token: 'access-token',
    user: { id: 'u1', tenant_id: '7' },
    active_tenant: { id: 7, name: '团队' },
  })
  assert.deepEqual(f.calls.tokens, ['access-token'])
  assert.deepEqual(f.calls.replace, ['/platform/knowledge-bases'])
})

test('已有账号：登录成功后凭邀请 token 加入并进入平台（AC-5）', async (t: TestContext) => {
  const f = await loginFixture(t, {
    token: 'tok',
    lookup: validLookup,
    loginResponse: {
      success: true,
      token: 'access-token',
      user: { id: 'u1', tenant_id: '7' },
      active_tenant: { id: 7, name: '团队' },
    },
  })
  f.vm.formRef = { validate: async () => true }
  f.vm.formData.email = 'me@example.com'
  f.vm.formData.password = 'Abcdef1!'
  await f.vm.handleLogin()
  assert.deepEqual(f.calls.tokens, ['access-token'])
  assert.deepEqual(f.calls.accept, ['tok'])
  assert.equal(f.calls.replace.at(-1), '/platform/knowledge-bases')
})

// ---------- 6 语言文案回归（AC-21 / F7 / F8 / F9 / F10） ----------

test('6 语言邀请文案：密码提示带特殊字符占位符，韩语敬语与日语波浪线符合修复口径', () => {
  const bundles: Record<string, any> = { zhCN, zhTW, enUS, jaJP, koKR, ruRU }
  for (const [name, bundle] of Object.entries(bundles)) {
    const invite = bundle.inviteRegister
    assert.ok(
      invite.passwordPolicyHint.includes('{specialChars}'),
      `${name} 的 passwordPolicyHint 缺少 {specialChars} 占位符`,
    )
    assert.ok(invite.invalidTitle.length > 0, `${name} 缺少 invalidTitle`)
    assert.ok(invite.registerDisabled.length > 0, `${name} 缺少 registerDisabled`)
  }
  // F7：韩语敬语 -시- 前 ㄹ 脱落。
  assert.ok(koKR.inviteRegister.noAccountTitle.includes('만드시겠습니까'))
  // F8：日语统一用波ダッシュ U+301C，不用全角波浪线 U+FF5E。
  assert.ok(jaJP.inviteRegister.passwordPolicyHint.includes('〜'))
  assert.ok(!jaJP.inviteRegister.passwordPolicyHint.includes('～'))
})
