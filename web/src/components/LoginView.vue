<script setup lang="ts">
import { ref } from 'vue'
import { errorText, login } from '../state'

const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  if (!password.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await login(password.value)
    password.value = ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="flex min-h-svh items-center justify-center p-4">
    <form class="flex w-full max-w-sm flex-col gap-5" @submit.prevent="submit">
      <div class="flex items-center gap-2">
        <UIcon name="i-lucide-audio-lines" class="size-6 text-primary" />
        <h1 class="text-lg font-semibold">khoomei</h1>
      </div>
      <p class="text-sm text-muted">这个面板设了密码。登录后在这台浏览器上保持 30 天，7 天不用会自动退出。</p>

      <UFormField label="密码" :error="error || undefined">
        <UInput
          v-model="password"
          type="password"
          autocomplete="current-password"
          autofocus
          required
          class="w-full"
          :disabled="busy"
        />
      </UFormField>

      <UButton type="submit" label="登录" block :loading="busy" :disabled="!password" />
      <p class="text-xs text-dimmed">忘了密码：在 hub 上运行 <code>khoomei panel-password</code> 重新设一个。</p>
    </form>
  </main>
</template>
