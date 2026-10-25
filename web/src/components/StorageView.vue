<script setup lang="ts">
import { computed, h, onMounted, ref, resolveComponent } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import { api, type Storage } from '../api'
import { bytes, fullTime, ago } from '../format'
import { errorText, rooms } from '../state'
import PruneModal from './PruneModal.vue'

const UButton = resolveComponent('UButton')

const storage = ref<Storage | null>(null)
const error = ref('')
const loading = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    storage.value = await api.storage()
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function roomTitle(id: string) {
  if (id === 'hidden') return '看不到的（朋友之间）'
  return rooms.value.find((r) => r.id === id)?.title ?? id
}

type Row = Storage['rooms'][number]

const target = ref<Row | null>(null)
const pruneOpen = computed({ get: () => target.value !== null, set: (v) => { if (!v) target.value = null } })

const columns: TableColumn<Row>[] = [
  { accessorKey: 'id', header: '群', cell: ({ row }) => roomTitle(row.original.id) },
  { id: 'pending', header: '未读', cell: ({ row }) => `${row.original.pending.count} 条 · ${bytes(row.original.pending.bytes)}` },
  { id: 'acked', header: '已读', cell: ({ row }) => `${row.original.acked.count} 条 · ${bytes(row.original.acked.bytes)}` },
  { id: 'files', header: '附件', cell: ({ row }) => (row.original.files.count ? `${row.original.files.count} 个 · ${bytes(row.original.files.bytes)}` : '—') },
  {
    id: 'oldest',
    header: '最旧的已读',
    cell: ({ row }) => (row.original.acked.oldest ? ago(row.original.acked.oldest) : '—'),
  },
  {
    id: 'actions',
    header: () => h('span', { class: 'sr-only' }, '操作'),
    cell: ({ row }) =>
      h(UButton, {
        label: '清理',
        icon: 'i-lucide-trash-2',
        size: 'xs',
        color: 'neutral',
        variant: 'outline',
        disabled: row.original.acked.count + row.original.pending.count === 0,
        'aria-label': `清理 ${roomTitle(row.original.id)}`,
        onClick: () => (target.value = row.original),
      }),
  },
]

const totals = computed(() => {
  const s = storage.value
  if (!s) return null
  const sum = (k: 'pending' | 'acked') => s.rooms.reduce((n, r) => n + r[k].bytes, 0)
  return { pending: sum('pending'), acked: sum('acked') }
})
</script>

<template>
  <UDashboardPanel id="storage">
    <template #header>
      <UDashboardNavbar title="存储" icon="i-lucide-hard-drive">
        <template #right>
          <UButton icon="i-lucide-refresh-cw" color="neutral" variant="ghost" aria-label="刷新" :loading="loading" @click="load" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />

      <template v-if="storage && totals">
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <UCard>
            <p class="text-xs text-muted">未读（inbox/）</p>
            <p class="mt-1 text-xl font-semibold">{{ bytes(totals.pending) }}</p>
          </UCard>
          <UCard>
            <p class="text-xs text-muted">已读（acked/）</p>
            <p class="mt-1 text-xl font-semibold">{{ bytes(totals.acked) }}</p>
          </UCard>
          <UCard>
            <p class="text-xs text-muted">附件（blobs/）</p>
            <p class="mt-1 text-xl font-semibold">{{ bytes(storage.files.bytes) }}</p>
            <p class="text-xs text-muted">{{ storage.files.count }} 个文件，跟着最后一封引用它的信一起清理</p>
          </UCard>
          <UCard>
            <p class="text-xs text-muted">磁盘剩余</p>
            <p class="mt-1 text-xl font-semibold">
              {{ storage.disk.total_bytes ? bytes(storage.disk.free_bytes) : '未知' }}
            </p>
            <p v-if="storage.disk.total_bytes" class="text-xs text-muted">共 {{ bytes(storage.disk.total_bytes) }}</p>
          </UCard>
        </div>

        <UAlert
          color="neutral"
          variant="subtle"
          icon="i-lucide-timer-reset"
          :title="storage.retention.acked_days > 0 ? `已读消息保留 ${storage.retention.acked_days} 天，每小时自动清理一次` : '自动清理已关闭'"
          :description="[
            storage.retention.max_mb ? `已读总量上限 ${storage.retention.max_mb} MB。` : '',
            storage.retention.last_run ? `上次自动清理：${fullTime(storage.retention.last_run)}，删掉 ${storage.retention.last_pruned} 条。` : 'hub 启动后还没跑过自动清理。',
            '未读消息不会被自动清理；收到不满 24 小时的消息都不会被清理。改保留期在 hub 的 config.json 里设 retention.acked_days。',
          ].join('')"
        />

        <UTable :data="storage.rooms" :columns="columns" class="rounded-lg border border-default" />
      </template>
      <USkeleton v-else-if="loading" class="h-48 w-full" />

      <PruneModal v-if="target" v-model:open="pruneOpen" :row="target" :title="roomTitle(target.id)" @done="load" />
    </template>
  </UDashboardPanel>
</template>
