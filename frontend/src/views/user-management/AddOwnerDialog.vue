<template>
  <t-dialog :visible="visible" width="460px" :header="title"
    :confirm-btn="{ content: t('userManagement.addOwner.submit'), loading: submitting }"
    :cancel-btn="{ content: t('userManagement.dialogs.cancel') }"
    :close-on-overlay-click="!submitting" :close-on-esc-keydown="!submitting"
    @update:visible="onVisibleUpdate" @confirm="submit">
    <p class="add-owner__desc">{{ description }}</p>

    <t-form ref="formRef" :data="form" :rules="rules" label-align="top" @submit.prevent>
      <template v-if="mode === 'createCoOwner'">
        <t-form-item :label="t('userManagement.addOwner.username')" name="username">
          <t-input v-model="form.username" :placeholder="t('userManagement.addOwner.usernamePlaceholder')" clearable />
        </t-form-item>
        <t-form-item :label="t('userManagement.addOwner.email')" name="email">
          <t-input v-model="form.email" :placeholder="t('userManagement.addOwner.emailPlaceholder')" clearable />
        </t-form-item>
        <t-form-item :label="t('userManagement.addOwner.password')" name="password">
          <t-input v-model="form.password" type="password" autocomplete="new-password"
            :placeholder="t('userManagement.addOwner.passwordPlaceholder')" />
        </t-form-item>
      </template>

      <t-form-item v-else-if="mode === 'inviteCoOwner'" :label="t('userManagement.addOwner.email')" name="email">
        <t-input v-model="form.email" :placeholder="t('userManagement.addOwner.emailPlaceholder')" clearable />
      </t-form-item>

      <template v-else-if="mode === 'provision'">
        <t-form-item :label="t('userManagement.addOwner.provisionTarget')" name="targetUserId">
          <t-select v-model="form.targetUserId" :options="candidateOptions" filterable
            :placeholder="t('userManagement.addOwner.provisionTargetPlaceholder')"
            :popup-props="{ zIndex: 6200 }" />
          <p v-if="candidatesLoaded && candidateOptions.length === 0" class="add-owner__empty">
            {{ t('userManagement.addOwner.noCandidates') }}
          </p>
        </t-form-item>
        <t-form-item :label="t('userManagement.dialogs.spaceName')" name="spaceName">
          <t-input v-model="form.spaceName" clearable />
        </t-form-item>
      </template>

      <template v-else>
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
    </t-form>

    <div v-if="generatedLink" class="add-owner__link">
      <p class="add-owner__link-label">{{ t('userManagement.addOwner.linkLabel') }}</p>
      <div class="add-owner__link-row">
        <t-input :value="generatedLink" readonly />
        <t-button variant="outline" @click="copyLink">{{ t('common.copy') }}</t-button>
      </div>
    </div>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin, type FormInstanceFunctions, type FormRule } from 'tdesign-vue-next'
import { createInvitation } from '@/api/tenant/invitations'
import {
  createCoOwner, createOwnerInvite, provisionOwnerSpace, resolveUserManagementError,
} from '@/api/user-management'
import { listAllUsers } from '@/api/system'
import type { AddOwnerMode } from './types'

const props = defineProps<{ visible: boolean; tenantId: number | null; mode: AddOwnerMode }>()
const emit = defineEmits<{ 'update:visible': [visible: boolean]; completed: [] }>()

const { t } = useI18n()
const formRef = ref<FormInstanceFunctions | null>(null)
const submitting = ref(false)
const generatedLink = ref('')
const candidatesLoaded = ref(false)
const candidateOptions = ref<Array<{ label: string; value: string }>>([])
const form = reactive({
  username: '', email: '', password: '',
  targetUserId: '', spaceName: '', expiresInDays: '',
})

const title = computed(() => {
  if (props.mode === 'createCoOwner') return t('userManagement.addOwner.createTitle')
  if (props.mode === 'inviteCoOwner') return t('userManagement.addOwner.inviteCoOwnerTitle')
  if (props.mode === 'provision') return t('userManagement.dialogs.provisionTitle')
  return t('userManagement.dialogs.inviteTitle')
})

const description = computed(() => {
  if (props.mode === 'createCoOwner') return t('userManagement.addOwner.createDesc')
  if (props.mode === 'inviteCoOwner') return t('userManagement.addOwner.inviteCoOwnerDesc')
  if (props.mode === 'provision') return t('userManagement.dialogs.provisionBody')
  return t('userManagement.dialogs.inviteBody')
})

function emailRule(required: boolean): FormRule[] {
  return [
    { required, message: t('userManagement.addOwner.errors.emailRequired'), trigger: 'blur' },
    { email: true, message: t('userManagement.addOwner.errors.emailFormat'), trigger: 'blur' },
  ]
}

const rules = computed<Record<string, FormRule[]>>(() => {
  if (props.mode === 'createCoOwner') {
    const createRules: Record<string, FormRule[]> = {
      username: [
        { required: true, message: t('userManagement.addOwner.errors.usernameRequired'), trigger: 'blur' },
        { min: 2, max: 50, message: t('userManagement.addOwner.errors.usernameLength'), trigger: 'blur' },
      ],
      email: emailRule(true),
      password: [
        { required: true, message: t('userManagement.addOwner.errors.passwordRequired'), trigger: 'blur' },
        { min: 8, max: 32, message: t('userManagement.addOwner.errors.passwordLength'), trigger: 'blur' },
      ],
    }
    return createRules
  }
  if (props.mode === 'inviteCoOwner') return { email: emailRule(true) }
  if (props.mode === 'provision') {
    const provisionRules: Record<string, FormRule[]> = { targetUserId: [
      { required: true, message: t('userManagement.addOwner.errors.targetRequired'), trigger: 'change' },
    ] }
    return provisionRules
  }
  return { email: emailRule(false) }
})

async function loadCandidates() {
  if (props.mode !== 'provision') return
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
  form.username = ''
  form.email = ''
  form.password = ''
  form.targetUserId = ''
  form.spaceName = ''
  form.expiresInDays = ''
  generatedLink.value = ''
  candidatesLoaded.value = false
}

watch(
  () => [props.visible, props.mode],
  ([visible]) => {
    if (!visible) return
    resetForm()
    void loadCandidates()
  },
)

function onVisibleUpdate(visible: boolean) {
  if (submitting.value) return
  emit('update:visible', visible)
}

function errorText(error: unknown): string {
  const status = (error as { status?: number } | null)?.status
  const message = (error as { message?: string } | null)?.message ?? ''
  if (status === 409) {
    return /username/i.test(message)
      ? t('userManagement.errors.usernameExists')
      : t('userManagement.errors.emailExists')
  }
  if (status === 400) {
    return /password/i.test(message)
      ? t('userManagement.errors.passwordPolicy')
      : t('userManagement.errors.invalidRequest')
  }
  return resolveUserManagementError(error)
}

function positiveDays(): number | undefined {
  const value = Number(form.expiresInDays)
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined
}

async function submitCreateCoOwner() {
  if (props.tenantId == null) throw new Error('tenant is required')
  const response = await createCoOwner(props.tenantId, {
    username: form.username.trim(), email: form.email.trim(), password: form.password,
  })
  if (!response.success) throw new Error(response.message || 'create co-owner failed')
  form.password = ''
  MessagePlugin.success(t('userManagement.addOwner.toasts.coOwnerCreated'))
}

async function submitInviteCoOwner() {
  if (props.tenantId == null) throw new Error('tenant is required')
  const response = await createInvitation(props.tenantId, { email: form.email.trim(), role: 'owner' })
  if (!response.success) throw new Error(response.message || 'invite co-owner failed')
  MessagePlugin.success(t('userManagement.addOwner.toasts.coOwnerInvited'))
}

async function submitProvision() {
  const response = await provisionOwnerSpace(form.targetUserId, {
    space_name: form.spaceName.trim() || undefined,
  })
  if (!response.success) throw new Error(response.message || 'provision owner failed')
  MessagePlugin.success(t('userManagement.toasts.ownerProvisioned'))
}

async function submitInviteOwner() {
  const response = await createOwnerInvite({
    email: form.email.trim() || undefined,
    space_name: form.spaceName.trim() || undefined,
    expires_in_days: positiveDays(),
  })
  if (!response.success || !response.data) throw new Error(response.message || 'create owner invite failed')
  generatedLink.value = response.data.invite_url
  MessagePlugin.success(t('userManagement.toasts.inviteCreated'))
  emit('completed')
}

async function copyLink() {
  try {
    await navigator.clipboard.writeText(generatedLink.value)
    MessagePlugin.success(t('common.copied'))
  } catch {
    MessagePlugin.error(t('userManagement.errors.unknown'))
  }
}

async function submit() {
  if (submitting.value) return
  const valid = await formRef.value?.validate?.()
  if (valid !== true) return
  submitting.value = true
  try {
    if (props.mode === 'createCoOwner') await submitCreateCoOwner()
    else if (props.mode === 'inviteCoOwner') await submitInviteCoOwner()
    else if (props.mode === 'provision') await submitProvision()
    else await submitInviteOwner()
    if (props.mode !== 'inviteOwner') emit('completed')
    emit('update:visible', false)
  } catch (error) {
    MessagePlugin.error(errorText(error))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped lang="less">
.add-owner__desc { margin: 0 0 12px; font-size: var(--app-text-base); line-height: 22px; color: var(--td-text-color-secondary); }
.add-owner__empty { margin: 4px 0 0; font-size: var(--app-text-md); color: var(--td-text-color-secondary); }
.add-owner__link { margin-top: 12px; }
.add-owner__link-label { margin: 0 0 6px; font-size: var(--app-text-md); color: var(--td-text-color-secondary); }
.add-owner__link-row { display: flex; gap: 8px; }
</style>
