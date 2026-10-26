<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { breakpointsTailwind, useBreakpoints } from '@vueuse/core'
import { zh_cn } from '@nuxt/ui/locale'
import RoomNav from './components/RoomNav.vue'
import Timeline from './components/Timeline.vue'
import MessageDetail from './components/MessageDetail.vue'
import StorageView from './components/StorageView.vue'
import ContactsView from './components/ContactsView.vue'
import LoginView from './components/LoginView.vue'
import PasswordModal from './components/PasswordModal.vue'
import { authStatus, fatal, needLogin, passwordOpen, route, selected, start } from './state'

const bp = useBreakpoints(breakpointsTailwind)
const wide = bp.greaterOrEqual('lg') // detail as a third column
const phone = bp.smaller('md') // detail from the bottom

const detailOpen = computed({
  get: () => selected.value !== null,
  set: (v) => {
    if (!v) selected.value = null
  },
})

onMounted(start)

// No password: say so on every visit, until one is set.
const toast = useToast()
const unwatch = watch(authStatus, (st) => {
  if (!st) return
  unwatch()
  if (st.password_set || st.viewer.role !== 'owner') return
  toast.add({
    id: 'no-password',
    title: '面板还没设密码',
    description: st.can_set
      ? '用你的 Tailscale 账号登录的设备都能直接看到整个家，包括私有空间。朋友只看得到自己在的群组。'
      : '在 hub 上运行 khoomei panel-password 设置。',
    icon: 'i-lucide-lock-open',
    color: 'warning',
    duration: 0,
    actions: st.can_set ? [{ label: '设置密码', color: 'warning', variant: 'solid', onClick: () => void (passwordOpen.value = true) }] : [],
  })
})
watch(
  () => authStatus.value?.password_set,
  (set) => set && toast.remove('no-password'),
)
</script>

<template>
  <UApp :locale="zh_cn" :tooltip="{ delayDuration: 300 }">
    <div v-if="fatal" class="flex min-h-svh items-center justify-center p-6">
      <UEmpty icon="i-lucide-lock" title="打不开面板" :description="fatal" />
    </div>
    <LoginView v-else-if="needLogin" />
    <div v-else-if="!authStatus" class="min-h-svh" aria-busy="true" />
    <UDashboardGroup v-else storage="local" storage-key="khoomei-panel" unit="rem">
      <RoomNav />
      <StorageView v-if="route.view === 'storage'" />
      <ContactsView v-else-if="route.view === 'contacts'" />
      <template v-else>
        <Timeline />
        <UDashboardPanel v-if="wide && selected" id="detail" :default-size="24" :min-size="18" :max-size="36" resizable>
          <template #header>
            <UDashboardNavbar title="消息详情" :toggle="false">
              <template #right>
                <UButton icon="i-lucide-x" color="neutral" variant="ghost" aria-label="关闭详情" @click="selected = null" />
              </template>
            </UDashboardNavbar>
          </template>
          <template #body>
            <MessageDetail :rec="selected" />
          </template>
        </UDashboardPanel>
      </template>
    </UDashboardGroup>

    <template v-if="!wide && selected && route.view === 'chat'">
      <UDrawer v-if="phone" v-model:open="detailOpen" title="消息详情">
        <template #body>
          <MessageDetail :rec="selected" />
        </template>
      </UDrawer>
      <USlideover v-else v-model:open="detailOpen" title="消息详情">
        <template #body>
          <MessageDetail :rec="selected" />
        </template>
      </USlideover>
    </template>

    <PasswordModal v-model:open="passwordOpen" />
  </UApp>
</template>
