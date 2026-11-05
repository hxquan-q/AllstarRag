<template>
  <t-dialog
    :visible="mode !== null"
    width="480px"
    :header="dialogTitle"
    :confirm-btn="confirmButton"
    :cancel-btn="{ content: t('userManagement.dialogs.cancel') }"
    :close-on-overlay-click="!submitting"
    :close-on-esc-keydown="!submitting"
    @update:visible="onVisibleUpdate"
    @confirm="submit"
  >
    <p v-if="dialogBody" class="user-action__body">{{ dialogBody }}</p>
    <p v-if="targetLabel" class="user-action__target">{{ targetLabel }}</p>

    <t-form
      v-if="mode === 'ban' || mode === 'unban' || mode === 'purge'"
      :data="form"
      label-align="top"
      class="user-action__form"
      @submit.prevent
    >
      <t-form-item
        v-if="mode === 'purge'"
        :label="t('userManagement.dialogs.purgeConfirm')"
        name="confirmEmail"
      >
        <t-input
          v-model="form.confirmEmail"
          :placeholder="target?.email ?? ''"
          clearable
        />
      </t-form-item>
      <t-form-item
        :label="mode === 'purge'
          ? t('userManagement.dialogs.purgeReason')
          : t('userManagement.dialogs.banReason')"
        name="reason"
      >
        <t-input
          v-model="form.reason"
          :placeholder="t('userManagement.dialogs.reasonPlaceholder')"
          clearable
        />
      </t-form-item>
    </t-form>

    <t-form
      v-if="mode === 'transfer'"
      :data="transferForm"
      label-align="top"
      class="user-action__form"
      @submit.prevent
    >
      <t-form-item :label="t('userManagement.dialogs.transferTarget')" name="toUserId">
        <t-select
          v-model="transferForm.toUserId"
          :options="transferOptions"
          filterable
          :placeholder="t('userManagement.dialogs.transferTarget')"
          :popup-props="{ zIndex: 6200 }"
        />
      </t-form-item>
      <p v-if="transferOptions.length === 0" class="user-action__empty">
        {{ t('userManagement.dialogs.transferNoCandidates') }}
      </p>
    </t-form>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  banMember,
  purgeMember,
  resolveUserManagementError,
  transferOwner,
  unbanMember,
} from '@/api/user-management'
import type { UserActionMode, UserRow } from './types'

const props = defineProps<{
  tenantId: number | string | null
  target: UserRow | null
  mode: UserActionMode | null
  /** 交接候选人：本空间 active 成员。 */
  candidates: UserRow[]
}>()

const emit = defineEmits<{
  close: []
  completed: []
}>()

const { t } = useI18n()
const submitting = ref(false)

const form = reactive({
  confirmEmail: '',
  reason: '',
})

const transferForm = reactive({
  toUserId: '',
})

const dialogTitle = computed(() => {
  if (props.mode === 'ban') return t('userManagement.dialogs.banTitle')
  if (props.mode === 'unban') return t('userManagement.dialogs.unbanTitle')
  if (props.mode === 'purge') return t('userManagement.dialogs.purgeTitle')
  if (props.mode === 'transfer') return t('userManagement.dialogs.transferTitle')
  return ''
})

const dialogBody = computed(() => {
  if (props.mode === 'ban') return t('userManagement.dialogs.banBody')
  if (props.mode === 'unban') return t('userManagement.dialogs.unbanBody')
  if (props.mode === 'purge') return t('userManagement.dialogs.purgeBody')
  if (props.mode === 'transfer') return t('userManagement.dialogs.transferBody')
  return ''
})

const targetLabel = computed(() => {
  if (!props.target) return ''
  const name = props.target.username?.trim() || props.target.email
  return `${name} · ${props.target.email}`
})

const transferOptions = computed(() =>
  props.candidates
    .filter((row) => row.user_id !== props.target?.user_id)
    .map((row) => ({
      label: row.username?.trim() ? `${row.username} (${row.email})` : row.email,
      value: row.user_id,
    })),
)

const confirmButton = computed(() => ({
  content: t('userManagement.dialogs.confirm'),
  loading: submitting.value,
  theme: props.mode === 'ban' || props.mode === 'purge' ? 'danger' : 'primary',
}))

function resetForm() {
  form.confirmEmail = ''
  form.reason = ''
  transferForm.toUserId = ''
}

// 每次打开/切换目标都清空上一次的输入，避免把 A 的原因带到 B。
watch(
  () => [props.mode, props.target?.user_id],
  () => resetForm(),
)

function close() {
  if (submitting.value) return
  emit('close')
}

function onVisibleUpdate(visible: boolean) {
  if (!visible) close()
}

function toastSuccess(key: string) {
  MessagePlugin.success(t(key))
}

function toastFailure(error: unknown) {
  MessagePlugin.error(resolveUserManagementError(error))
}

async function submitBanOrUnban(): Promise<boolean> {
  if (props.tenantId == null || !props.target) return false
  const reason = form.reason.trim() || undefined
  const action = props.mode === 'unban' ? unbanMember : banMember
  const response = await action(props.tenantId, props.target.user_id, reason)
  if (!response.success) throw new Error(response.message || 'member status change failed')
  toastSuccess(
    props.mode === 'unban'
      ? 'userManagement.toasts.unbanSuccess'
      : 'userManagement.toasts.banSuccess',
  )
  return true
}

async function submitPurge(): Promise<boolean> {
  if (props.tenantId == null || !props.target) return false
  const email = form.confirmEmail.trim()
  if (email !== props.target.email.trim()) {
    MessagePlugin.error(t('userManagement.dialogs.purgeEmailMismatch'))
    return false
  }
  const response = await purgeMember(props.tenantId, props.target.user_id, {
    confirm_email: email,
    reason: form.reason.trim() || undefined,
  })
  if (!response.success) throw new Error(response.message || 'purge failed')
  toastSuccess('userManagement.toasts.purgeSuccess')
  return true
}

async function submitTransfer(): Promise<boolean> {
  if (props.tenantId == null || !props.target) return false
  if (!transferForm.toUserId) {
    MessagePlugin.error(t('userManagement.dialogs.transferTargetRequired'))
    return false
  }
  const response = await transferOwner(props.tenantId, {
    from_user_id: props.target.user_id,
    to_user_id: transferForm.toUserId,
  })
  if (!response.success) throw new Error(response.message || 'transfer failed')
  toastSuccess('userManagement.toasts.transferSuccess')
  return true
}

async function submit() {
  if (submitting.value || props.mode === null) return
  submitting.value = true
  try {
    let succeeded = false
    if (props.mode === 'ban' || props.mode === 'unban') {
      succeeded = await submitBanOrUnban()
    } else if (props.mode === 'purge') {
      succeeded = await submitPurge()
    } else {
      succeeded = await submitTransfer()
    }
    if (!succeeded) return
    emit('completed')
    emit('close')
  } catch (error) {
    // 失败时保持弹窗与本地状态不变，让用户修正后重试。
    toastFailure(error)
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped lang="less">
.user-action__body {
  margin: 0;
  font-size: var(--app-text-base);
  line-height: 22px;
  color: var(--td-text-color-primary);
}

.user-action__target {
  margin: 8px 0 0;
  font-size: var(--app-text-md);
  line-height: 20px;
  color: var(--td-text-color-secondary);
  word-break: break-all;
}

.user-action__form {
  margin-top: 16px;
}

.user-action__empty {
  margin: 4px 0 0;
  font-size: var(--app-text-md);
  color: var(--td-text-color-secondary);
}
</style>
