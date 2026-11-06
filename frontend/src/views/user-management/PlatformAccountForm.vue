<template>
  <div class="platform-account">
    <t-radio-group v-model="mode" variant="default-filled" class="platform-account__modes">
      <t-radio-button value="provision">{{ t('userManagement.accountTools.provision') }}</t-radio-button>
      <t-radio-button value="inviteOwner">{{ t('userManagement.accountTools.inviteOwner') }}</t-radio-button>
      <t-radio-button value="platformUser">{{ t('userManagement.accountTools.platformUser') }}</t-radio-button>
    </t-radio-group>

    <p class="platform-account__desc">{{ description }}</p>

    <t-form ref="formRef" :data="form" :rules="rules" label-align="top" @submit.prevent>
      <template v-if="mode === 'provision'">
        <t-form-item :label="t('userManagement.addOwner.provisionTarget')" name="targetUserId">
          <t-select v-model="form.targetUserId" :options="candidateOptions" filterable
            :placeholder="t('userManagement.addOwner.provisionTargetPlaceholder')"
            :popup-props="{ zIndex: 6200 }" />
          <p v-if="candidatesLoaded && candidateOptions.length === 0" class="platform-account__hint">
            {{ t('userManagement.addOwner.noCandidates') }}
          </p>
        </t-form-item>
        <t-form-item :label="t('userManagement.dialogs.spaceName')" name="spaceName">
          <t-input v-model="form.spaceName" clearable />
        </t-form-item>
      </template>

      <template v-else-if="mode === 'inviteOwner'">
        <t-form-item :label="t('userManagement.dialogs.inviteEmail')" name="email">
          <t-input v-model="form.email" :placeholder="t('userManagement.addOwner.emailPlaceholder')" clearable />
        </t-form-item>
        <t-form-item :label="t('userManagement.dialogs.spaceName')" name="spaceName">
          <t-input v-model="form.spaceName" clearable />
        </t-form-item>
        <t-form-item :label="t('userManagement.dialogs.inviteExpires')" name="expiresInDays">
          <t-input v-model="form.expiresInDays" type="number" clearable />
        </t-form-item>
      </template>

      <template v-else>
        <t-form-item :label="t('userManagement.addUser.username')" name="username">
          <t-input v-model="form.username" :placeholder="t('userManagement.addUser.usernamePlaceholder')" clearable />
        </t-form-item>
        <t-form-item :label="t('userManagement.addUser.email')" name="email">
          <t-input v-model="form.email" :placeholder="t('userManagement.addUser.emailPlaceholder')" clearable />
        </t-form-item>
        <t-form-item :label="t('userManagement.accountTools.passwordOptional')" name="password">
          <t-input v-model="form.password" type="password" autocomplete="new-password"
            :placeholder="t('userManagement.accountTools.passwordOptionalPlaceholder')" />
        </t-form-item>
      </template>
    </t-form>

    <div v-if="resultText" class="platform-account__result">
      <p class="platform-account__hint">{{ resultLabel }}</p>
      <div class="platform-account__result-row">
        <t-input :value="resultText" readonly />
        <t-button variant="outline" @click="copyResult">{{ t('common.copy') }}</t-button>
      </div>
    </div>

    <div class="platform-account__footer">
      <t-button variant="outline" :disabled="submitting" @click="emit('close')">
        {{ t('userManagement.dialogs.cancel') }}
      </t-button>
      <t-button theme="primary" :loading="submitting" @click="submit">
        {{ t('userManagement.addUser.submit') }}
      </t-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin, type FormInstanceFunctions, type FormRule } from 'tdesign-vue-next'
import { createOwnerInvite, provisionOwnerSpace, resolveUserManagementError } from '@/api/user-management'
import { createSystemUser, listAllUsers } from '@/api/system'

type PlatformMode = 'provision' | 'inviteOwner' | 'platformUser'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ completed: []; close: [] }>()

const { t } = useI18n()
const formRef = ref<FormInstanceFunctions | null>(null)
const mode = ref<PlatformMode>('provision')
const submitting = ref(false)
const resultText = ref('')
const resultLabel = ref('')
const candidatesLoaded = ref(false)
const candidateOptions = ref<Array<{ label: string; value: string }>>([])
const form = reactive({
  targetUserId: '', spaceName: '', email: '', expiresInDays: '', username: '', password: '',
})

const description = computed(() => {
  if (mode.value === 'provision') return t('userManagement.dialogs.provisionBody')
  if (mode.value === 'inviteOwner') return t('userManagement.dialogs.inviteBody')
  return t('userManagement.accountTools.platformUserDesc')
})

function emailRules(required: boolean): FormRule[] {
  return [
    { required, message: t('userManagement.addUser.errors.emailRequired'), trigger: 'blur' },
    { email: true, message: t('userManagement.addUser.errors.emailFormat'), trigger: 'blur' },
  ]
}

const rules = computed<Record<string, FormRule[]>>(() => {
  if (mode.value === 'provision') {
    const provisionRules: Record<string, FormRule[]> = { targetUserId: [
      { required: true, message: t('userManagement.addOwner.errors.targetRequired'), trigger: 'change' },
    ] }
    return provisionRules
  }
  if (mode.value === 'inviteOwner') {
    const inviteRules: Record<string, FormRule[]> = { email: emailRules(false) }
    return inviteRules
  }
  const createRules: Record<string, FormRule[]> = {
    username: [
      { required: true, message: t('userManagement.addUser.errors.usernameRequired'), trigger: 'blur' },
      { min: 2, max: 50, message: t('userManagement.addUser.errors.usernameLength'), trigger: 'blur' },
    ],
    email: emailRules(true),
    // 留空由后端生成随机密码；填写时沿用 8–32 位策略。
    password: [{ min: 8, max: 32, message: t('userManagement.addUser.errors.passwordLength'), trigger: 'blur' }],
  }
  return createRules
})

async function loadCandidates() {
  candidatesLoaded.value = false
  try {
    const response = await listAllUsers({ offset: 0, limit: 200 })
    candidateOptions.value = (response.users ?? [])
      .filter((user) => user.is_active !== false)
      .map((user) => ({
        label: user.username ? `${user.username} (${user.email})` : user.email,
        value: user.id,
      }))
  } catch (error) {
    MessagePlugin.error(resolveUserManagementError(error))
  } finally {
    candidatesLoaded.value = true
  }
}

function resetForm() {
  Object.assign(form, { targetUserId: '', spaceName: '', email: '', expiresInDays: '', username: '', password: '' })
  resultText.value = ''
  resultLabel.value = ''
}

watch(() => props.visible, (visible) => {
  if (!visible) return
  resetForm()
  void loadCandidates()
}, { immediate: true })

watch(mode, () => {
  resetForm()
  formRef.value?.clearValidate?.()
})

function positiveDays(): number | undefined {
  const value = Number(form.expiresInDays)
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined
}

function errorText(error: unknown): string {
  const status = (error as { status?: number } | null)?.status
  const message = (error as { message?: string } | null)?.message ?? ''
  if (status === 409) {
    return /username/i.test(message) ? t('userManagement.errors.usernameExists') : t('userManagement.errors.emailExists')
  }
  if (status === 400 && /password/i.test(message)) return t('userManagement.errors.passwordPolicy')
  return resolveUserManagementError(error)
}

async function submitProvision() {
  const response = await provisionOwnerSpace(form.targetUserId, { space_name: form.spaceName.trim() || undefined })
  if (!response.success) throw new Error(response.message || 'provision owner failed')
  MessagePlugin.success(t('userManagement.toasts.ownerProvisioned'))
  emit('completed')
  emit('close')
}

async function submitInviteOwner() {
  const response = await createOwnerInvite({
    email: form.email.trim() || undefined,
    space_name: form.spaceName.trim() || undefined,
    expires_in_days: positiveDays(),
  })
  if (!response.success || !response.data) throw new Error(response.message || 'create owner invite failed')
  resultLabel.value = t('userManagement.addOwner.linkLabel')
  resultText.value = response.data.invite_url
  MessagePlugin.success(t('userManagement.toasts.inviteCreated'))
  emit('completed')
}

async function submitPlatformUser() {
  const password = form.password
  const result = await createSystemUser({
    username: form.username.trim(),
    email: form.email.trim(),
    ...(password ? { password } : {}),
  })
  form.password = ''
  MessagePlugin.success(t('userManagement.accountTools.platformUserCreated'))
  emit('completed')
  if (result.generated_password) {
    // 随机密码只返回一次：留在弹窗里让超管复制，不自动关闭。
    resultLabel.value = t('userManagement.accountTools.generatedPassword')
    resultText.value = result.generated_password
    return
  }
  emit('close')
}

async function submit() {
  if (submitting.value) return
  const valid = await formRef.value?.validate?.()
  if (valid !== true) return
  submitting.value = true
  try {
    if (mode.value === 'provision') await submitProvision()
    else if (mode.value === 'inviteOwner') await submitInviteOwner()
    else await submitPlatformUser()
  } catch (error) {
    MessagePlugin.error(errorText(error))
  } finally {
    submitting.value = false
  }
}

async function copyResult() {
  try {
    await navigator.clipboard.writeText(resultText.value)
    MessagePlugin.success(t('common.copied'))
  } catch {
    MessagePlugin.error(t('userManagement.errors.unknown'))
  }
}
</script>

<style scoped lang="less">
.platform-account__modes { margin-bottom: 12px; }
.platform-account__desc { margin: 0 0 12px; font-size: var(--app-text-base); line-height: 22px; color: var(--td-text-color-secondary); }
.platform-account__hint { margin: 4px 0 6px; font-size: var(--app-text-md); color: var(--td-text-color-secondary); }
.platform-account__result { margin-top: 12px; }
.platform-account__result-row { display: flex; gap: 8px; }
.platform-account__footer { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
</style>
