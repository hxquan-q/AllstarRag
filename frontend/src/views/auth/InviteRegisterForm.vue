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
          {{ $t('inviteRegister.passwordPolicyHint') }}
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

// 复杂密码开启时，与后端同口径预校验：8–32 位，含大小写字母、数字、特殊字符。
const COMPLEX_PASSWORD_PATTERN = /^(?=.*[a-z])(?=.*[A-Z])(?=.*\d)(?=.*[!@#$%^&*()_+\-=[\]{}|;:,.<>?]).{8,32}$/

const rules = computed<Record<string, FormRule[]>>(() => {
  const passwordRules: FormRule[] = [
    { required: true, message: t('inviteRegister.passwordTooShort'), trigger: 'blur' },
    { min: 8, max: 32, message: t('inviteRegister.passwordTooShort'), trigger: 'blur' },
  ]
  if (props.complexPasswordEnabled) {
    passwordRules.push({
      validator: (val: string) => COMPLEX_PASSWORD_PATTERN.test(val ?? ''),
      message: t('inviteRegister.passwordPolicyHint'),
      trigger: 'blur',
    })
  }
  return {
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
    password: passwordRules,
  }
})

async function handleSubmit() {
  if (submitting.value) return
  const valid = await formRef.value?.validate?.()
  if (valid !== true) return

  submitting.value = true
  try {
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
    // 三种情况都按票面语义交给父组件处理，避免把密码错误混进登录表单。
    if (response.status === 409) {
      MessagePlugin.info(t('inviteRegister.emailExists'))
      emit('login')
      return
    }
    if (response.status === 410 || response.status === 403) {
      emit('invalid')
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
