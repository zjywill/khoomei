<script setup lang="ts">
import { computed, ref } from 'vue'
import { authStatus, logout, navigate, owner, passwordOpen, presence, rooms, route } from '../state'
import { ago, avatarClass, initials, shortName } from '../format'

const room = computed(() => rooms.value.find((r) => r.id === route.room))

const stateText = { online: '在线', sleeping: '休眠，可唤醒', offline: '离线' } as const
const chipColor = { online: 'success', sleeping: 'warning', offline: 'neutral' } as const

function memberLine(addr: string) {
  const p = presence.value[addr]
  if (!p) return '没见过'
  if (p.state === 'online') return p.waiting > 0 ? '在线，等待中' : '在线'
  if (p.state === 'sleeping') return `休眠 · ${ago(p.last_seen)}`
  return ago(p.last_seen)
}

// On small screens the sidebar is a slideover; picking something closes it.
const open = ref(false)
function go(patch: Parameters<typeof navigate>[0]) {
  navigate(patch)
  open.value = false
}

function toggleMember(addr: string) {
  go({ view: 'chat', member: route.member === addr ? '' : addr })
}
</script>

<template>
  <UDashboardSidebar id="nav" resizable collapsible :default-size="16" :min-size="13" :max-size="24" v-model:open="open" mode="slideover">
    <template #header="{ collapsed }">
      <div class="flex min-w-0 items-center gap-2">
        <UIcon name="i-lucide-audio-lines" class="size-5 shrink-0 text-primary" />
        <span v-if="!collapsed" class="truncate font-semibold">khoomei</span>
        <span v-if="!collapsed && owner" class="truncate text-sm text-muted">{{ owner }}</span>
      </div>
    </template>

    <template #default="{ collapsed }">
      <nav aria-label="群" class="flex flex-col gap-0.5">
        <p v-if="!collapsed" class="px-2 pb-1 text-xs font-medium text-dimmed">群</p>
        <UButton
          v-for="r in rooms"
          :key="r.id"
          :icon="r.peer ? 'i-lucide-users' : 'i-lucide-hash'"
          :label="collapsed ? undefined : r.title"
          :aria-label="r.title"
          :aria-current="route.view === 'chat' && route.room === r.id ? 'page' : undefined"
          color="neutral"
          :variant="route.view === 'chat' && route.room === r.id ? 'soft' : 'ghost'"
          :square="collapsed"
          class="w-full"
          @click="go({ view: 'chat', room: r.id })"
        >
          <template v-if="!collapsed && r.pending" #trailing>
            <UBadge :label="String(r.pending)" color="primary" variant="subtle" size="sm" class="ms-auto" />
          </template>
        </UButton>
      </nav>

      <nav v-if="room && route.view === 'chat'" aria-label="成员" class="mt-4 flex flex-col gap-0.5">
        <p v-if="!collapsed" class="px-2 pb-1 text-xs font-medium text-dimmed">成员 · 点一下只看它</p>
        <UTooltip v-for="m in room.members" :key="m" :text="collapsed ? m : undefined" :content="{ side: 'right' }">
          <button
            type="button"
            class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-start hover:bg-elevated/60 focus-visible:outline-2 focus-visible:outline-primary"
            :class="route.member === m ? 'bg-elevated' : ''"
            :aria-pressed="route.member === m"
            @click="toggleMember(m)"
          >
            <UChip
              :color="chipColor[presence[m]?.state ?? 'offline']"
              :show="(presence[m]?.state ?? 'offline') !== 'offline'"
              position="bottom-right"
              inset
            >
              <UAvatar :text="initials(m)" size="sm" :class="avatarClass(m)" :alt="m" />
            </UChip>
            <span v-if="!collapsed" class="min-w-0">
              <span class="block truncate text-sm">{{ shortName(m, owner) }}</span>
              <span class="block truncate text-xs text-muted">
                <span class="sr-only">{{ stateText[presence[m]?.state ?? 'offline'] }}，</span>{{ memberLine(m) }}
              </span>
            </span>
          </button>
        </UTooltip>
      </nav>
    </template>

    <template #footer="{ collapsed }">
      <div class="flex w-full flex-col gap-1">
        <UButton
          icon="i-lucide-hard-drive"
          :label="collapsed ? undefined : '存储'"
          aria-label="存储"
          color="neutral"
          :variant="route.view === 'storage' ? 'soft' : 'ghost'"
          :square="collapsed"
          class="w-full"
          @click="go({ view: 'storage' })"
        />
        <UButton
          v-if="authStatus && !authStatus.password_set"
          icon="i-lucide-lock-open"
          :label="collapsed ? undefined : '未设密码'"
          aria-label="未设密码，点这里设置"
          color="warning"
          variant="soft"
          :square="collapsed"
          class="w-full"
          @click="passwordOpen = true"
        />
        <template v-else-if="authStatus?.logged_in">
          <UButton
            icon="i-lucide-key-round"
            :label="collapsed ? undefined : '修改密码'"
            aria-label="修改密码"
            color="neutral"
            variant="ghost"
            :square="collapsed"
            class="w-full"
            @click="passwordOpen = true"
          />
          <UButton
            icon="i-lucide-log-out"
            :label="collapsed ? undefined : '退出登录'"
            aria-label="退出登录"
            color="neutral"
            variant="ghost"
            :square="collapsed"
            class="w-full"
            @click="logout"
          />
        </template>
        <UColorModeSelect v-if="!collapsed" size="sm" class="w-full" />
      </div>
    </template>
  </UDashboardSidebar>
</template>
