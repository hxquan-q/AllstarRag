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

const status = computed(() => {
  const value = (props.error as { status?: unknown } | null | undefined)?.status
  return typeof value === 'number' ? value : 0
})

const serverMessage = computed(() => {
  const value = (props.error as { message?: unknown } | null | undefined)?.message
  return typeof value === 'string' ? value : ''
})

// 后端文案直接展示；没有文案才按状态码兜底本地化提示。
// 不用后端中文文案当逻辑判别键（PITFALLS G-20）。
const message = computed(() => {
  if (serverMessage.value) return serverMessage.value
  if (status.value === 400) return t('auth.yunzhijia.errorInvalidTicket')
  if (status.value === 429) return t('auth.yunzhijia.errorRateLimited')
  if (status.value === 503) return t('auth.yunzhijia.errorUnavailable')
  return t('auth.yunzhijia.errorGeneric')
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
