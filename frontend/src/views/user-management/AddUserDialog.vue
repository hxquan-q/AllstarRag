<template>
  <t-dialog
    :visible="visible"
    width="480px"
    :header="t('userManagement.addUser.title')"
    :confirm-btn="{ content: t('userManagement.addUser.submit'), loading: submitting }"
    :cancel-btn="{ content: t('userManagement.dialogs.cancel') }"
    :close-on-overlay-click="!submitting"
    :close-on-esc-keydown="!submitting"
    @update:visible="onVisibleUpdate"
    @confirm="submit"
  >
    <t-tabs v-model="activeTab" class="add-user__tabs">
      <t-tab-panel v-if="canCreate" value="create" :label="t('userManagement.addUser.createTab')" />
      <t-tab-panel v-if="canInvite" value="invite" :label="t('userManagement.addUser.inviteTab')" />
    </t-tabs>

    <p class="add-user__desc">{{ description }}</p>

    <t-form ref="formRef" :data="form" :rules="rules" label-align="top" @submit.prevent>
      <template v-if="activeTab === 'create'">
        <t-form-item :label="t('userManagement.addUser.username')" name="username">
          <t-input
            v-model="form.username"
            :placeholder="t('userManagement.addUser.usernamePlaceholder')"
            clearable
          />
        </t-form-item>
        <t-form-item :label="t('userManagement.addUser.email')" name="email">
          <t-input
            v-model="form.email"
            :placeholder="t('userManagement.addUser.emailPlaceholder')"
            clearable
          />
        </t-form-item>
        <t-form-item :label="t('userManagement.addUser.password')" name="password">
          <t-input
            v-model="form.password"
            type="password"
            autocomplete="new-password"
            :placeholder="t('userManagement.addUser.passwordPlaceholder')"
          />
        </t-form-item>
      </template>

      <t-form-item
        v-else
        :label="t('userManagement.addUser.email')"
        name="email"
      >
        <t-input
          v-model="form.email"
          :placeholder="t('userManagement.addUser.emailPlaceholder')"
          clearable
        />
      </t-form-item>

      <t-form-item :label="t('userManagement.addUser.role')" name="role">
        <t-select
          v-model="form.role"
          :options="roleOptions"
          :placeholder="t('userManagement.addUser.rolePlaceholder')"
          :popup-props="{ zIndex: 6200 }"
        />
      </t-form-item>
    </t-form>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin, type FormInstanceFunctions, type FormRule } from 'tdesign-vue-next'
import {
  createMemberAccount,
  type TenantRole,
} from '@/api/tenant/members'
import { createInvitation } from '@/api/tenant/invitations'
import { resolveUserManagementError } from '@/api/user-management'
import { allowedCreateRoles } from '@/utils/memberRoles'

type AddUserTab = 'create' | 'invite'

const props = defineProps<{
  visible: boolean
  tenantId: number | null
  /** 当前空间内的角色；系统管理员由 isSystemAdmin 旁路判定。 */
  actorRole: TenantRole | ''
  isSystemAdmin: boolean
  canCreate: boolean
  canInvite: boolean
}>()

const emit = defineEmits<{
  'update:visible': [visible: boolean]
  completed: []
}>()

const { t } = useI18n()
const formRef = ref<FormInstanceFunctions | null>(null)
const submitting = ref(false)
const activeTab = ref<AddUserTab>('create')
const form = reactive<{ username: string; email: string; password: string; role: TenantRole }>({
  username: '',
  email: '',
  password: '',
  role: 'contributor',
})

const allowedRoles = computed<TenantRole[]>(() =>
  props.isSystemAdmin ? ['admin', 'contributor', 'viewer'] : allowedCreateRoles(props.actorRole),
)

const roleOptions = computed(() =>
  allowedRoles.value.map((role) => ({
    label: t('userManagement.roles.' + role),
    value: role,
  })),
)

const description = computed(() =>
  activeTab.value === 'create'
    ? t('userManagement.addUser.createDesc')
    : t('userManagement.addUser.inviteDesc'),
)

function emailRules(): FormRule[] {
  return [
    { required: true, message: t('userManagement.addUser.errors.emailRequired'), trigger: 'blur' },
    { email: true, message: t('userManagement.addUser.errors.emailFormat'), trigger: 'blur' },
  ]
}

// 与 MemberCreateAccountForm / 后端密码策略保持一致（8–32 位），不另立规则。
const rules = computed<Record<string, FormRule[]>>(() => {
  const next: Record<string, FormRule[]> = {
    email: emailRules(),
    role: [
      { required: true, message: t('userManagement.addUser.errors.roleRequired'), trigger: 'change' },
    ],
  }
  if (activeTab.value === 'invite') return next
  next.username = [
    { required: true, message: t('userManagement.addUser.errors.usernameRequired'), trigger: 'blur' },
    { min: 2, max: 50, message: t('userManagement.addUser.errors.usernameLength'), trigger: 'blur' },
  ]
  next.password = [
    { required: true, message: t('userManagement.addUser.errors.passwordRequired'), trigger: 'blur' },
    { min: 8, max: 32, message: t('userManagement.addUser.errors.passwordLength'), trigger: 'blur' },
  ]
  return next
})

function resetForm() {
  form.username = ''
  form.email = ''
  form.password = ''
  const fallback = allowedRoles.value[0] ?? 'contributor'
  form.role = allowedRoles.value.includes('contributor') ? 'contributor' : fallback
  activeTab.value = props.canCreate ? 'create' : 'invite'
}

watch(
  () => props.visible,
  (visible) => {
    if (visible) resetForm()
  },
)

function onVisibleUpdate(visible: boolean) {
  if (submitting.value) return
  emit('update:visible', visible)
}

/** 400/409/404 优先给具体提示；其余交给统一的状态码映射。 */
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
  if (status === 404 && activeTab.value === 'invite') {
    return t('userManagement.addUser.errors.inviteNotFound')
  }
  return resolveUserManagementError(error)
}

async function submit() {
  if (submitting.value) return
  if (props.tenantId == null) {
    MessagePlugin.error(t('userManagement.errors.unknown'))
    return
  }
  const valid = await formRef.value?.validate?.()
  if (valid !== true) return

  submitting.value = true
  try {
    if (activeTab.value === 'create') {
      await submitCreate()
    } else {
      await submitInvite()
    }
    emit('completed')
    emit('update:visible', false)
  } catch (error) {
    MessagePlugin.error(errorText(error))
  } finally {
    submitting.value = false
  }
}

async function submitCreate() {
  const tenantId = props.tenantId
  if (tenantId == null) return
  const response = await createMemberAccount(tenantId, {
    username: form.username.trim(),
    email: form.email.trim(),
    password: form.password,
    role: form.role,
  })
  if (!response.success) throw new Error(response.message || 'create member account failed')
  // 密码只用于本次请求，成功后立刻从内存清掉。
  form.password = ''
  MessagePlugin.success(t('userManagement.addUser.toasts.created'))
}

async function submitInvite() {
  const tenantId = props.tenantId
  if (tenantId == null) return
  const response = await createInvitation(tenantId, {
    email: form.email.trim(),
    role: form.role,
  })
  if (!response.success) throw new Error(response.message || 'create invitation failed')
  MessagePlugin.success(t('userManagement.addUser.toasts.invited'))
}
</script>

<style scoped lang="less">
.add-user__tabs {
  margin-bottom: 4px;
}

.add-user__desc {
  margin: 0 0 12px;
  font-size: var(--app-text-base);
  line-height: 22px;
  color: var(--td-text-color-secondary);
}
</style>
