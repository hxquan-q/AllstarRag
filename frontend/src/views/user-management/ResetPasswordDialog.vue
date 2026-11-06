<template>
  <t-dialog
    :visible="target != null"
    width="440px"
    :header="t('userManagement.resetPassword.title')"
    :confirm-btn="{ content: t('userManagement.dialogs.confirm'), loading: submitting }"
    :cancel-btn="{ content: t('userManagement.dialogs.cancel') }"
    :close-on-overlay-click="!submitting"
    :close-on-esc-keydown="!submitting"
    @update:visible="onVisibleUpdate"
    @confirm="submit"
  >
    <p class="reset-password__desc">{{ t('userManagement.resetPassword.body') }}</p>
    <p v-if="target" class="reset-password__target">{{ target.email }}</p>
    <t-form ref="formRef" :data="form" :rules="rules" label-align="top" @submit.prevent>
      <t-form-item :label="t('userManagement.resetPassword.newPassword')" name="password">
        <t-input
          v-model="form.password"
          type="password"
          autocomplete="new-password"
          :placeholder="t('userManagement.resetPassword.placeholder')"
        />
      </t-form-item>
    </t-form>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin, type FormInstanceFunctions, type FormRule } from 'tdesign-vue-next'
import { resetMemberPassword } from '@/api/tenant/members'
import { resetUserPassword } from '@/api/system'
import { resolveUserManagementError } from '@/api/user-management'
import type { UserRow } from './types'

const props = defineProps<{
  target: UserRow | null
  tenantId: number | null
  /**
   * true：走系统级 reset-password（超管在 scope=other/all 下，或目标不在本空间）；
   * false：走本空间成员 reset-password（owner/超管在当前空间）。
   */
  usePlatformApi: boolean
}>()

const emit = defineEmits<{ close: []; completed: [] }>()

const { t } = useI18n()
const formRef = ref<FormInstanceFunctions | null>(null)
const submitting = ref(false)
const form = reactive({ password: '' })

// 与新增账号弹窗一致的 8–32 位约束；复杂度规则以后端校验为准。
const rules = computed<Record<string, FormRule[]>>(() => ({
  password: [
    { required: true, message: t('userManagement.resetPassword.errors.required'), trigger: 'blur' },
    { min: 8, max: 32, message: t('userManagement.resetPassword.errors.length'), trigger: 'blur' },
  ],
}))

watch(
  () => props.target,
  () => {
    form.password = ''
  },
)

function onVisibleUpdate(visible: boolean) {
  if (submitting.value || visible) return
  emit('close')
}

function errorText(error: unknown): string {
  const status = (error as { status?: number } | null)?.status
  if (status === 400) return t('userManagement.errors.passwordPolicy')
  return resolveUserManagementError(error)
}

async function submit() {
  const target = props.target
  if (submitting.value || !target) return
  const valid = await formRef.value?.validate?.()
  if (valid !== true) return
  submitting.value = true
  try {
    if (props.usePlatformApi) {
      await resetUserPassword({ email: target.email, new_password: form.password })
    } else {
      if (props.tenantId == null) throw new Error('tenant is required')
      const response = await resetMemberPassword(props.tenantId, target.user_id, form.password)
      if (!response.success) throw new Error(response.message || 'reset password failed')
    }
    // 密码只用于本次请求，成功后立刻从内存清掉。
    form.password = ''
    MessagePlugin.success(t('userManagement.resetPassword.success'))
    emit('completed')
    emit('close')
  } catch (error) {
    MessagePlugin.error(errorText(error))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped lang="less">
.reset-password__desc {
  margin: 0 0 8px;
  font-size: var(--app-text-base);
  line-height: 22px;
  color: var(--td-text-color-secondary);
}

.reset-password__target {
  margin: 0 0 12px;
  font-size: var(--app-text-base);
  font-weight: 600;
  color: var(--td-text-color-primary);
}
</style>
