<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type PruneRequest, type Storage } from '../api'
import { bytes, shortName } from '../format'
import { errorText, owner, rooms } from '../state'

const props = defineProps<{ row: Storage['rooms'][number]; title: string }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ done: [] }>()

const what = ref<'acked' | 'pending'>('acked')
const hours = ref(24)
const agent = ref('')
const confirmText = ref('')

const ageItems = [
  { label: '收到超过 1 天的（全部可清理的）', value: 24 },
  { label: '7 天前的', value: 7 * 24 },
  { label: '30 天前的', value: 30 * 24 },
]

// Pending records may only be pruned per agent, and only for the owner's own agents.
const ownAgents = computed(() => {
  const room = rooms.value.find((r) => r.id === props.row.id)
  return (room?.members ?? []).filter((m) => m.split('/')[0] === owner.value || !m.includes('/'))
})
const canPending = computed(() => props.row.id !== 'hidden' && ownAgents.value.length > 0)
const whatItems = computed(() => [
  { label: '已读消息', value: 'acked', description: '收件方已经 ack 过的。' },
  ...(canPending.value
    ? [{ label: '某个 agent 的未读积压', value: 'pending', description: '从没被取走过的信。只在 agent 已经废弃时用。' }]
    : []),
])

const request = computed<PruneRequest>(() =>
  what.value === 'pending'
    ? { agent: agent.value, what: 'pending', older_than_hours: hours.value, dry_run: true }
    : { room: props.row.id, what: 'acked', older_than_hours: hours.value, dry_run: true },
)

const preview = ref<{ count: number; bytes: number } | null>(null)
const error = ref('')
const busy = ref(false)

watch(
  [request, open],
  async () => {
    preview.value = null
    error.value = ''
    if (!open.value || (what.value === 'pending' && !agent.value)) return
    try {
      preview.value = await api.prune(request.value)
    } catch (e) {
      error.value = errorText(e)
    }
  },
  { immediate: true, deep: true },
)

watch(what, (w) => {
  confirmText.value = ''
  if (w === 'pending' && !agent.value) agent.value = ownAgents.value[0] ?? ''
})

const needsName = computed(() => what.value === 'pending')
const confirmed = computed(() => !needsName.value || confirmText.value.trim() === shortName(agent.value, owner.value) || confirmText.value.trim() === agent.value)

async function run() {
  busy.value = true
  error.value = ''
  try {
    const res = await api.prune({ ...request.value, dry_run: false })
    open.value = false
    emit('done')
    useToast().add({ title: `删掉 ${res.count} 条，释放 ${bytes(res.bytes)}`, icon: 'i-lucide-check', color: 'success' })
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="`清理 ${title}`" description="删掉的消息找不回来；回复里引用到它的地方会显示「原消息已清理」。">
    <template #body>
      <div class="flex flex-col gap-4">
        <URadioGroup v-model="what" :items="whatItems" legend="清理什么" />

        <UFormField v-if="what === 'pending'" label="哪个 agent">
          <USelect v-model="agent" :items="ownAgents" class="w-full" />
        </UFormField>

        <UFormField label="多久以前的">
          <USelect v-model="hours" :items="ageItems" class="w-full" />
        </UFormField>

        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        <p v-else-if="preview" class="text-sm" role="status">
          将删除 <strong>{{ preview.count }}</strong> 条，释放 <strong>{{ bytes(preview.bytes) }}</strong>。
        </p>
        <USkeleton v-else class="h-5 w-48" />

        <UFormField
          v-if="needsName && preview?.count"
          :label="`这些信从没被 ${shortName(agent, owner)} 取走过。输入「${shortName(agent, owner)}」确认`"
        >
          <UInput v-model="confirmText" class="w-full" autocomplete="off" />
        </UFormField>
      </div>
    </template>
    <template #footer="{ close }">
      <div class="flex w-full justify-end gap-2">
        <UButton label="取消" color="neutral" variant="ghost" @click="close" />
        <UButton
          label="删除"
          color="error"
          icon="i-lucide-trash-2"
          :loading="busy"
          :disabled="!preview?.count || !confirmed"
          @click="run"
        />
      </div>
    </template>
  </UModal>
</template>
