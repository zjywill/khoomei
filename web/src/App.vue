<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { breakpointsTailwind, useBreakpoints } from '@vueuse/core'
import { zh_cn } from '@nuxt/ui/locale'
import RoomNav from './components/RoomNav.vue'
import Timeline from './components/Timeline.vue'
import MessageDetail from './components/MessageDetail.vue'
import StorageView from './components/StorageView.vue'
import { fatal, route, selected, start } from './state'

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
</script>

<template>
  <UApp :locale="zh_cn" :tooltip="{ delayDuration: 300 }">
    <div v-if="fatal" class="flex min-h-svh items-center justify-center p-6">
      <UEmpty icon="i-lucide-lock" title="打不开面板" :description="fatal" />
    </div>
    <UDashboardGroup v-else storage="local" storage-key="khoomei-panel" unit="rem">
      <RoomNav />
      <StorageView v-if="route.view === 'storage'" />
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

    <template v-if="!wide && selected">
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
  </UApp>
</template>
