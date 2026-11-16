<template>
  <t-tag :theme="theme" variant="light">{{ label }}</t-tag>
</template>

<script setup lang="ts">
defineProps<{
  label: string
  theme: 'success' | 'danger' | 'warning'
}>()
</script>
