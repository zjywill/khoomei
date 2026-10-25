<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useIntersectionObserver, useScroll } from '@vueuse/core'
import MessageItem from './MessageItem.vue'
import {
  connection, following, loadOlder, lookup, loadTimeline, navigate, owner, recKey, rooms, route, selected, timeline, unseen,
} from '../state'
import { shortName } from '../format'
import type { Rec } from '../api'

const room = computed(() => rooms.value.find((r) => r.id === route.room))

const kindItems = [
  { label: 'text', value: 'text' },
  { label: 'request', value: 'request' },
  { label: 'reply', value: 'reply' },
  { label: 'notice', value: 'notice' },
]
const kinds = computed({ get: () => route.kinds, set: (v: string[]) => navigate({ kinds: v }) })
const unread = computed({ get: () => route.unread, set: (v: boolean) => navigate({ unread: v }) })

// Consecutive messages from one sender within two minutes share a header.
const GROUP_MS = 2 * 60 * 1000
const rows = computed(() =>
  timeline.items.map((r, i) => {
    const prev = timeline.items[i - 1]
    const grouped =
      !!prev &&
      prev.envelope.from_agent === r.envelope.from_agent &&
      prev.envelope.to_agent === r.envelope.to_agent &&
      r.seq - prev.seq < GROUP_MS * 1e6
    return { rec: r, grouped }
  }),
)

const scroller = ref<HTMLElement | null>(null)
const top = ref<HTMLElement | null>(null)
const { arrivedState } = useScroll(scroller, { offset: { bottom: 48 } })
watch(() => arrivedState.bottom, (b) => {
  following.value = b
  if (b) unseen.value = 0
})

function toBottom(smooth = false) {
  const el = scroller.value
  if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
  unseen.value = 0
}

// A fresh list starts at the bottom; new arrivals keep it there only when the reader already was.
watch(() => timeline.generation, async () => {
  await nextTick()
  toBottom()
})
watch(() => timeline.items.length, async (n, old) => {
  if (n > old && following.value) {
    await nextTick()
    toBottom(true)
  }
})

// Loading older messages must not move what the reader is looking at.
useIntersectionObserver(top, async ([e]) => {
  if (!e?.isIntersecting || !timeline.more || timeline.loadingOlder) return
  const el = scroller.value!
  const before = el.scrollHeight - el.scrollTop
  await loadOlder()
  await nextTick()
  el.scrollTop = el.scrollHeight - before
}, { root: scroller })

// Scroll to a quoted message; one that is not on the loaded page opens in the detail view instead.
async function jump(id: string) {
  const el = document.querySelector<HTMLElement>(`[data-msg-id="${CSS.escape(id)}"]`)
  if (!el) {
    const r = await lookup(id)
    if (r) selected.value = r
    return false
  }
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  el.classList.remove('flash')
  void el.offsetWidth
  el.classList.add('flash')
  el.focus({ preventScroll: true })
  return true
}

function select(r: Rec) {
  selected.value = selected.value && recKey(selected.value) === recKey(r) ? null : r
}

const statusText = computed(() =>
  connection.value === 'retrying' ? '连接中断，正在重连' : connection.value === 'connecting' ? '正在连接' : '',
)
</script>

<template>
  <UDashboardPanel id="timeline" :ui="{ root: 'min-h-0' }">
    <UDashboardNavbar :title="room?.title ?? '群'" icon="i-lucide-messages-square">
      <template #trailing>
        <UBadge v-if="room" :label="`${room.members.length} 个成员`" color="neutral" variant="subtle" size="sm" class="hidden sm:inline-flex" />
      </template>
      <template #right>
        <UBadge v-if="statusText" :label="statusText" color="warning" variant="subtle" icon="i-lucide-wifi-off" role="status" />
        <UButton
          v-if="route.member"
          :label="shortName(route.member, owner)"
          trailing-icon="i-lucide-x"
          color="primary"
          variant="soft"
          size="sm"
          :aria-label="`取消只看 ${route.member}`"
          @click="navigate({ member: '' })"
        />
        <USelect
          v-model="kinds"
          :items="kindItems"
          multiple
          placeholder="全部类型"
          icon="i-lucide-filter"
          size="sm"
          class="w-36 hidden sm:flex"
          aria-label="按类型筛选"
        />
        <USwitch v-model="unread" label="只看未读" size="sm" />
      </template>
    </UDashboardNavbar>

    <div
      ref="scroller"
      class="relative min-h-0 flex-1 overflow-y-auto px-2 py-3 sm:px-4"
      role="log"
      aria-live="polite"
      aria-relevant="additions"
      :aria-busy="timeline.loading"
    >
      <div ref="top" class="h-px" aria-hidden="true" />
      <p v-if="timeline.loadingOlder" class="py-2 text-center text-xs text-muted">正在加载更早的消息…</p>
      <p v-else-if="!timeline.more && timeline.items.length" class="py-2 text-center text-xs text-dimmed">没有更早的消息了</p>

      <div v-if="timeline.loading && !timeline.items.length" class="flex flex-col gap-4 p-2">
        <div v-for="i in 4" :key="i" class="flex gap-3">
          <USkeleton class="size-8 rounded-full" />
          <div class="flex flex-1 flex-col gap-2">
            <USkeleton class="h-3 w-40" />
            <USkeleton class="h-4 w-3/4" />
          </div>
        </div>
      </div>

      <UAlert v-else-if="timeline.error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="timeline.error" class="m-2">
        <template #actions>
          <UButton label="重试" size="xs" color="error" variant="outline" @click="loadTimeline()" />
        </template>
      </UAlert>

      <UEmpty
        v-else-if="!timeline.items.length"
        icon="i-lucide-message-circle-off"
        :title="route.member || route.kinds.length || route.unread ? '没有符合筛选的消息' : '这里还没有消息'"
        :description="route.member || route.kinds.length || route.unread ? '换个筛选条件试试。' : 'agent 之间一来一回，就会出现在这里。'"
        class="mt-16"
      />

      <ul v-else class="flex flex-col">
        <li v-for="row in rows" :key="recKey(row.rec)">
          <MessageItem
            :rec="row.rec"
            :grouped="row.grouped"
            :selected="!!selected && recKey(selected) === recKey(row.rec)"
            @select="select(row.rec)"
            @jump="jump"
          />
        </li>
      </ul>
    </div>

    <div v-if="unseen > 0" class="pointer-events-none absolute inset-x-0 bottom-4 flex justify-center">
      <UButton
        class="pointer-events-auto shadow-md"
        icon="i-lucide-arrow-down"
        :label="`${unseen} 条新消息`"
        color="primary"
        @click="toBottom(true)"
      />
    </div>
  </UDashboardPanel>
</template>
