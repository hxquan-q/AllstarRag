import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import { createSSRApp, ref } from 'vue'
import { renderToString } from '@vue/server-renderer'
import zhCN from '../../i18n/locales/zh-CN'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

// T05 整体回归：邀请落地页弹窗 / 注册表单 / Login 接线。
// 组件行为用 vueFixture 驱动真实 setup；模板文案用 SSR 渲染真实 SFC 模板，
// 避免只断言源码字符串。

function translate(message: any, key: string, params: Record<string, unknown> = {}): string {
  const value = key.split('.').reduce<any>((node, part) => node?.[part], message)
  if (typeof value !== 'string') throw new Error(`缺少 i18n key: ${key}`)
  return value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
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
}

async function registerFormFixture(t: TestContext, options: RegisterFormOptions = {}) {
  const f = fixture(t)
  const calls: any[] = []
  const messages: Array<[string, string]> = []
  const events: string[] = []
  const { vm } = f.component(new URL('./InviteRegisterForm.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': {
      MessagePlugin: {
        info: (message: string) => messages.push(['info', message]),
        error: (message: string) => messages.push(['error', message]),
      },
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

for (const status of [410, 403]) {
  test(`${status} 统一走链接失效分支，不展示表单`, async (t: TestContext) => {
    const f = await registerFormFixture(t, {
      response: { success: false, status, message: 'gone' },
    })
    await f.vm.handleSubmit()
    assert.deepEqual(f.events, ['invalid'])
    assert.deepEqual(f.messages, [], '不应额外提示')
  })
}

test('400 展示服务端字段/密码提示并停留在表单', async (t: TestContext) => {
  const f = await registerFormFixture(t, {
    response: { success: false, status: 400, message: '密码不合规' },
  })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [['error', '密码不合规']])
  assert.deepEqual(f.events, [])
})

test('网络异常展示错误并停留表单，不误发 login/invalid', async (t: TestContext) => {
  const f = await registerFormFixture(t, { throwError: new Error('网络错误') })
  await f.vm.handleSubmit()
  assert.deepEqual(f.messages, [['error', '网络错误']])
  assert.deepEqual(f.events, [])
})

test('复杂密码开启时展示规则提示，并追加大小写/数字/特殊字符校验', async (t: TestContext) => {
  const file = new URL('./InviteRegisterForm.vue', import.meta.url)
  const htmlOn = await renderSfc(file, componentTemplate(file), {
    form: { email: '', username: '', password: '' },
    complexPasswordEnabled: true,
    submitting: false,
    rules: {},
  })
  assert.ok(htmlOn.includes('密码需为 8–32 位，并包含大写字母、小写字母、数字和特殊字符。'))

  const weak = await registerFormFixture(t, { complexPasswordEnabled: true })
  const strengthen = weak.vm.rules.password
  assert.equal(strengthen.length, 3)
  assert.equal(strengthen[2].validator('Abcdef1!'), true)
  assert.equal(strengthen[2].validator('abcdefg1!'), false)
  assert.equal(strengthen[2].validator('ABCDEFG1!'), false)
  assert.equal(strengthen[2].validator('Abcdefg!!'), false)
  assert.equal(strengthen[2].validator('Abcdef12'), false)
})

test('复杂密码关闭时不显示规则提示，密码规则只有长度校验', async (t: TestContext) => {
  const file = new URL('./InviteRegisterForm.vue', import.meta.url)
  const htmlOff = await renderSfc(file, componentTemplate(file), {
    form: { email: '', username: '', password: '' },
    complexPasswordEnabled: false,
    submitting: false,
    rules: {},
  })
  assert.ok(!htmlOff.includes('密码需为 8–32 位，并包含大写字母、小写字母、数字和特殊字符。'))

  const plain = await registerFormFixture(t, { complexPasswordEnabled: false })
  assert.equal(plain.vm.rules.password.length, 2)
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
      useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: ref('zh-CN') }),
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

test('lookup 失败：展示链接失效错误，不弹窗也不拉注册配置', async (t: TestContext) => {
  const f = await loginFixture(t, { token: 'tok', lookupFails: true })
  assert.equal(f.vm.inviteLookupError, 'inviteRegister.invalidBody')
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
  assert.equal(f.vm.inviteLookupError, 'inviteRegister.invalidBody')
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
