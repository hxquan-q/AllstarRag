<template>
  <div class="yzj-error">
    <div class="yzj-error__card">
      <p class="yzj-error__brand">{{ brandConfig.productName }}</p>
      <h1 class="yzj-error__title">{{ t('auth.yunzhijia.errorTitle') }}</h1>
      <p class="yzj-error__message">{{ message }}</p>
      <a class="yzj-error__action" href="/login">{{ t('auth.yunzhijia.backToLogin') }}</a>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { brandConfig } from '@/brand'

const props = defineProps<{ error?: unknown }>()

const { t } = useI18n()

type ErrorReason =
  | 'invalidTicket'
  | 'provisioning'
  | 'duplicateName'
  | 'accountBlocked'
  | 'unavailable'
  | 'upstream'
  | 'rateLimited'
  | 'generic'

// 后端 401 的业务拒绝对应登录服务里的固定中文文案（当前没有稳定的
// 错误码）。按文案映射到本地化提示；未知文案走状态码兜底。
const SERVER_MESSAGE_REASONS: Record<string, ErrorReason> = {
  '登录凭证已失效，请从云之家重新打开': 'invalidTicket',
  '账号开通中，请明天再试': 'provisioning',
  '在职员工存在同名，请联系管理员': 'duplicateName',
  '账号不可用，请联系管理员': 'accountBlocked',
  '系统暂不可用，请联系管理员': 'unavailable',
  '云之家服务暂时不可用，请稍后再试': 'upstream',
}

const status = computed(() => {
  const value = (props.error as { status?: unknown } | null | undefined)?.status
  return typeof value === 'number' ? value : 0
})

const serverMessage = computed(() => {
  const value = (props.error as { message?: unknown } | null | undefined)?.message
  return typeof value === 'string' ? value : ''
})

const reason = computed<ErrorReason>(() => {
  const mapped = SERVER_MESSAGE_REASONS[serverMessage.value]
  if (mapped) return mapped
  if (status.value === 400) return 'invalidTicket'
  if (status.value === 503) return 'unavailable'
  if (status.value === 429) return 'rateLimited'
  return 'generic'
})

const message = computed(() => {
  switch (reason.value) {
    case 'invalidTicket':
      return t('auth.yunzhijia.errorInvalidTicket')
    case 'provisioning':
      return t('auth.yunzhijia.errorProvisioning')
    case 'duplicateName':
      return t('auth.yunzhijia.errorDuplicateName')
    case 'accountBlocked':
      return t('auth.yunzhijia.errorAccountBlocked')
    case 'unavailable':
      return t('auth.yunzhijia.errorUnavailable')
    case 'upstream':
      return t('auth.yunzhijia.errorUpstream')
    case 'rateLimited':
      return t('auth.yunzhijia.errorRateLimited')
    default:
      return t('auth.yunzhijia.errorGeneric')
  }
})
</script>

<style scoped>
.yzj-error {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background:
    radial-gradient(circle at 20% 20%, color-mix(in srgb, var(--td-brand-color) 16%, transparent), transparent 55%),
    var(--td-bg-color-page);
}

.yzj-error__card {
  width: 100%;
  max-width: 420px;
  padding: 40px 32px;
  border-radius: var(--app-radius-xl);
  background: var(--td-bg-color-container);
  box-shadow: 0 18px 48px rgba(15, 23, 42, 0.12);
  text-align: center;
}

.yzj-error__brand {
  margin: 0 0 12px;
  color: var(--td-brand-color);
  font-size: var(--app-text-sm);
  font-weight: 600;
}

.yzj-error__title {
  margin: 0;
  color: var(--td-text-color-primary);
  font-size: var(--app-text-3xl);
  line-height: 1.4;
}

.yzj-error__message {
  margin: 16px 0 28px;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
  line-height: 1.7;
}

.yzj-error__action {
  display: inline-block;
  padding: 10px 24px;
  border-radius: var(--app-radius-pill);
  background: var(--td-brand-color);
  color: #fff;
  font-size: var(--app-text-sm);
  text-decoration: none;
}

.yzj-error__action:hover {
  background: var(--td-brand-color-hover);
}
</style>
