<script setup lang="ts">
import { ref, watch } from 'vue'
import { errorText, login } from '../state'

// A guest who is in fact the owner logs in from here and sees the whole home.
const open = defineModel<boolean>('open', { required: true })

const password = ref('')
const error = ref('')
const busy = ref(false)

watch(open, (o) => {
  if (o) password.value = error.value = ''
})

async function submit() {
  if (!password.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await login(password.value)
    open.value = false
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" title="主人登录" description="用面板密码登录后，能看到整个家：私有空间、所有群组和存储。">
    <template #body>
      <form id="owner-login" @submit.prevent="submit">
        <UFormField label="密码" :error="error || undefined">
          <UInput v-model="password" type="password" autocomplete="current-password" autofocus required class="w-full" />
        </UFormField>
      </form>
    </template>
    <template #footer="{ close }">
      <div class="flex w-full justify-end gap-2">
        <UButton label="取消" color="neutral" variant="ghost" @click="close" />
        <UButton type="submit" form="owner-login" label="登录" :loading="busy" :disabled="!password" />
      </div>
    </template>
  </UModal>
</template>
