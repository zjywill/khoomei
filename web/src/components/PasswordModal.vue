<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { authStatus, errorText, setPassword } from '../state'

const open = defineModel<boolean>('open', { required: true })

const changing = computed(() => !!authStatus.value?.password_set)
const minLength = computed(() => authStatus.value?.min_length ?? 8)

const current = ref('')
const next = ref('')
const again = ref('')
const error = ref('')
const busy = ref(false)

watch(open, (o) => {
  if (o) {
    current.value = next.value = again.value = error.value = ''
  }
})

const tooShort = computed(() => next.value.length > 0 && [...next.value].length < minLength.value)
const mismatch = computed(() => again.value.length > 0 && again.value !== next.value)
const ready = computed(
  () => (!changing.value || current.value) && next.value && !tooShort.value && again.value === next.value,
)

async function submit() {
  if (!ready.value || busy.value) return
  busy.value = true
  error.value = ''
  const wasChange = changing.value
  try {
    await setPassword(changing.value ? current.value : '', next.value)
    open.value = false
    useToast().add({
      title: wasChange ? '密码改好了' : '密码设好了',
      description: wasChange ? '其他浏览器都已退出登录。' : '以后打开面板都要先登录。',
      icon: 'i-lucide-check',
      color: 'success',
    })
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="changing ? '修改面板密码' : '设置面板密码'"
    :description="
      changing
        ? '改完之后，其他浏览器都要用新密码重新登录。'
        : '设好之后，打开面板都要先登录。忘了可以在 hub 上运行 khoomei panel-password 重设。'
    "
  >
    <template #body>
      <form id="password-form" class="flex flex-col gap-4" @submit.prevent="submit">
        <UFormField v-if="changing" label="当前密码">
          <UInput v-model="current" type="password" autocomplete="current-password" required class="w-full" />
        </UFormField>
        <UFormField
          label="新密码"
          :hint="tooShort ? undefined : `至少 ${minLength} 个字符`"
          :error="tooShort ? `至少 ${minLength} 个字符` : undefined"
        >
          <UInput v-model="next" type="password" autocomplete="new-password" required class="w-full" />
        </UFormField>
        <UFormField label="再输一次" :error="mismatch ? '两次不一样' : undefined">
          <UInput v-model="again" type="password" autocomplete="new-password" required class="w-full" />
        </UFormField>
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
      </form>
    </template>
    <template #footer="{ close }">
      <div class="flex w-full justify-end gap-2">
        <UButton label="取消" color="neutral" variant="ghost" @click="close" />
        <UButton type="submit" form="password-form" :label="changing ? '修改' : '设置'" :loading="busy" :disabled="!ready" />
      </div>
    </template>
  </UModal>
</template>
