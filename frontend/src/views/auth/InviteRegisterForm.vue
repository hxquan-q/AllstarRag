<template>
  <div class="invite-register">
    <div class="invite-register__header">
      <h2 class="invite-register__title">{{ $t('inviteRegister.registerTitle') }}</h2>
      <p class="invite-register__hint">{{ $t('inviteRegister.registerHint') }}</p>
    </div>

    <t-form ref="formRef" :data="form" :rules="rules" label-align="top" class="invite-register__form"
      @submit="handleSubmit">
      <t-form-item :label="$t('inviteRegister.email')" name="email">
        <t-input v-model="form.email" type="text" autocomplete="email" size="large" :disabled="submitting"
          :placeholder="$t('inviteRegister.emailPlaceholder')" />
      </t-form-item>

      <t-form-item :label="$t('inviteRegister.username')" name="username">
        <t-input v-model="form.username" autocomplete="name" size="large" :disabled="submitting"
          :placeholder="$t('inviteRegister.usernamePlaceholder')" />
      </t-form-item>

      <t-form-item :label="$t('inviteRegister.password')" name="password">
        <t-input v-model="form.password" type="password" autocomplete="new-password" size="large"
          :disabled="submitting" :placeholder="$t('inviteRegister.passwordPlaceholder')" @enter="handleSubmit" />
        <p v-if="complexPasswordEnabled" class="invite-register__policy">
          {{ $t('inviteRegister.passwordPolicyHint', { specialChars: PASSWORD_SPECIAL_CHARS }) }}
        </p>
      </t-form-item>

      <t-button type="submit" theme="primary" size="large" block :loading="submitting" class="invite-register__submit">
        {{ submitting ? $t('inviteRegister.submitting') : $t('inviteRegister.submit') }}
      </t-button>
    </t-form>

    <t-button variant="text" theme="default" block class="invite-register__back" @click="emit('login')">
      {{ $t('inviteRegister.backToLogin') }}
    </t-button>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin, type FormInstanceFunctions, type FormRule } from 'tdesign-vue-next'
import { registerByInvite, type LoginResponse } from '@/api/auth'
import { PASSWORD_SPECIAL_CHARS, newPasswordRules } from '@/utils/passwordPolicy'

const props = defineProps<{
  token: string
  complexPasswordEnabled: boolean
}>()

const emit = defineEmits<{
  (e: 'success', response: LoginResponse): void
  (e: 'login'): void
  (e: 'invalid'): void
}>()

const { t } = useI18n()
const formRef = ref<FormInstanceFunctions | null>(null)
const submitting = ref(false)

const form = reactive({
  email: '',
  username: '',
  password: '',
})

// 复用与后端同口径的密码规则：8–32 位，简单模式要求字母+数字，
// 复杂模式再要求大小写字母与固定特殊字符集。
const rules = computed<Record<string, FormRule[]>>(() => ({
  email: [
    { required: true, message: t('inviteRegister.emailInvalid'), trigger: 'blur' },
    { email: true, message: t('inviteRegister.emailInvalid'), trigger: 'blur' },
  ],
  username: [
    {
      validator: (val: string) => (val ?? '').trim().length > 0,
      message: t('inviteRegister.usernameRequired'),
      trigger: 'blur',
    },
  ],
  password: newPasswordRules(t, props.complexPasswordEnabled),
}))

// 服务端密码策略错误固定带 "password must be 8-32 characters" 前缀，
// 命中时改用本地化提示，避免六种语言界面直出英文原文。
function isPasswordPolicyMessage(message?: string): boolean {
  return typeof message === 'string' && /password must be 8-32 characters/i.test(message)
}

// 从校验失败结果中取第一条错误消息。TDesign 的 validate() 失败有两种形态：
// resolve 一个 { 字段: [{ message }] } 对象，或（自定义校验器抛错时）
// reject 同形态对象——两种都走这里。字段级红字提示由表单自身渲染，
// 本函数只负责补一条全局 toast。
function firstValidationMessage(result: unknown): string {
  if (!result || typeof result !== 'object') return ''
  for (const field of Object.values(result)) {
    if (!Array.isArray(field)) continue
    for (const item of field) {
      const message = (item as { message?: unknown }).message
      if (typeof message === 'string' && message) return message
    }
  }
  return ''
}

// 校验失败结果中是否存在 password 字段条目（数组非空），
// 与 firstValidationMessage 消费同一形态的结果对象。
function hasPasswordError(result: unknown): boolean {
  if (!result || typeof result !== 'object') return false
  for (const [field, errors] of Object.entries(result)) {
    if (field === 'password' && Array.isArray(errors) && errors.length > 0) return true
  }
  return false
}

async function handleSubmit() {
  // 先置位再校验：表单 submit 与密码框 Enter 可能同时触发，置位后第二次直接返回。
  if (submitting.value) return
  submitting.value = true
  try {
    // 校验失败可能 reject 而非 resolve false，统一接住后按同一分支提示，
    // 避免 rejection 穿透后被外层 catch 吞成通用失败（或完全无提示）。
    let valid: unknown
    try {
      valid = await formRef.value?.validate?.()
    } catch (errors) {
      valid = errors
    }
    if (valid !== true) {
      // 密码类校验失败统一提示合规要求，不直出具体规则：
      // TDesign 结果里 password 数组可能带出 required 规则消息（「请输入密码」），会误导用户。
      const message = hasPasswordError(valid)
        ? t('inviteRegister.passwordInvalid')
        : firstValidationMessage(valid) || t('inviteRegister.formInvalid')
      MessagePlugin.error(message)
      return
    }

    const response = await registerByInvite({
      token: props.token,
      email: form.email.trim(),
      username: form.username.trim(),
      password: form.password,
    })

    if (response.success) {
      form.password = ''
      emit('success', response)
      return
    }

    // 409：邮箱已有账号，切回登录；410：链接失效；403：注册被禁用。
    if (response.status === 409) {
      MessagePlugin.info(t('inviteRegister.emailExists'))
      emit('login')
      return
    }
    if (response.status === 403) {
      MessagePlugin.warning(t('inviteRegister.registerDisabled'))
      emit('login')
      return
    }
    if (response.status === 410) {
      emit('invalid')
      return
    }
    if (response.status === 400 && isPasswordPolicyMessage(response.message)) {
      // 简单模式只要求字母+数字，只有复杂模式才提示特殊字符集，避免误导。
      MessagePlugin.error(props.complexPasswordEnabled
        ? t('inviteRegister.passwordPolicyHint', { specialChars: PASSWORD_SPECIAL_CHARS })
        : t('inviteRegister.passwordPolicyHintSimple'))
      return
    }
    MessagePlugin.error(response.message || t('inviteRegister.failed'))
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('inviteRegister.failed'))
  } finally {
    submitting.value = false
  }
}
</script>

<style lang="less" scoped>
.invite-register__header {
  margin-bottom: 20px;
}

.invite-register__title {
  margin: 0 0 6px;
  font-size: var(--td-font-size-title-large);
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.invite-register__hint {
  margin: 0;
  font-size: var(--app-text-md);
  line-height: 1.55;
  color: var(--td-text-color-secondary);
}

.invite-register__policy {
  margin: 6px 0 0;
  font-size: var(--td-font-size-body-small);
  line-height: 1.5;
  color: var(--td-text-color-placeholder);
}

.invite-register__submit {
  margin-top: 4px;
}

.invite-register__back {
  margin-top: 8px;
  color: var(--td-text-color-secondary);
}
</style>
