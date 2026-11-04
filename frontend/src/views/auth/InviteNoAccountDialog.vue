<template>
  <!-- 邀请落地页无账号引导：主操作去注册，次操作回到登录表单。 -->
  <!-- placement="center"：TDesign 1.19.x 支持，弹窗垂直居中（默认顶部）。 -->
  <t-dialog :visible="visible" width="460px" placement="center" :footer="false" :close-on-overlay-click="true"
    :close-on-esc-keydown="true" @update:visible="onVisibleUpdate">
    <template #header>
      <span class="invite-no-account__header">
        <t-icon name="user-add" size="20px" class="invite-no-account__header-icon" aria-hidden="true" />
        <span>{{ $t('inviteRegister.registerTitle') }}</span>
      </span>
    </template>

    <p class="invite-no-account__text">
      {{ $t('inviteRegister.noAccountTitle', { brand: brandName }) }}
    </p>

    <div class="invite-no-account__actions">
      <t-button theme="primary" size="large" block @click="emit('create')">
        {{ $t('inviteRegister.createAccount') }}
      </t-button>
      <t-button variant="text" theme="default" block class="invite-no-account__login"
        @click="emit('login')">
        {{ $t('inviteRegister.loginExisting') }}
      </t-button>
    </div>
  </t-dialog>
</template>

<script setup lang="ts">
defineProps<{
  visible: boolean
  brandName: string
}>()

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  (e: 'create'): void
  (e: 'login'): void
}>()

const onVisibleUpdate = (next: boolean) => {
  emit('update:visible', next)
}
</script>

<style lang="less" scoped>
.invite-no-account__header {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.invite-no-account__header-icon {
  flex-shrink: 0;
  color: var(--td-brand-color);
}

.invite-no-account__text {
  margin: 0 0 20px;
  font-size: var(--app-text-md);
  line-height: 1.65;
  color: var(--td-text-color-secondary);
}

.invite-no-account__actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border-radius: var(--app-radius-md);
}

.invite-no-account__login {
  color: var(--td-text-color-secondary);
}
</style>
