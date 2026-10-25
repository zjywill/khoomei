<script setup lang="ts">
import { computed } from 'vue'
import type { Rec } from '../api'
import { fullTime } from '../format'

const props = defineProps<{ rec: Rec }>()

const rows = computed(() => {
  const e = props.rec.envelope
  const r = props.rec.received
  return [
    ['id', e.id],
    ['from_agent', e.from_agent],
    ['to_agent', e.to_agent],
    ['kind', e.kind || 'text'],
    ['发件方时间 ts', fullTime(e.ts)],
    ['hub 收到', fullTime(r.at)],
    ['来自节点', r.peer],
    ['登录身份', r.login || '—'],
    ['请求的唤醒', e.wake || 'batch'],
    ['唤醒结果', { ok: '已唤醒', failed: '失败', downgraded: '降级为排队', '': '—' }[props.rec.wake]],
    ['状态', props.rec.acked ? '已读' : '未读'],
    ['回复', e.reply_to || '—'],
  ] as const
})

const data = computed(() => (props.rec.envelope.data === undefined ? '' : JSON.stringify(props.rec.envelope.data, null, 2)))
const raw = computed(() => JSON.stringify({ envelope: props.rec.envelope, received: props.rec.received }, null, 2))
</script>

<template>
  <div class="flex flex-col gap-5 text-sm">
    <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5">
      <template v-for="[k, v] in rows" :key="k">
        <dt class="text-muted">{{ k }}</dt>
        <dd class="break-all font-mono text-xs leading-5">{{ v }}</dd>
      </template>
    </dl>

    <section v-if="rec.envelope.text">
      <h3 class="mb-1.5 text-xs font-medium text-muted">正文</h3>
      <p class="whitespace-pre-wrap break-words rounded-md bg-elevated/50 p-3">{{ rec.envelope.text }}</p>
    </section>

    <section v-if="data">
      <h3 class="mb-1.5 text-xs font-medium text-muted">data</h3>
      <pre class="overflow-x-auto rounded-md bg-elevated/50 p-3 font-mono text-xs">{{ data }}</pre>
    </section>

    <UCollapsible>
      <UButton label="原始记录" trailing-icon="i-lucide-chevron-down" color="neutral" variant="ghost" size="sm" class="px-0" />
      <template #content>
        <pre class="mt-2 overflow-x-auto rounded-md bg-elevated/50 p-3 font-mono text-xs">{{ raw }}</pre>
      </template>
    </UCollapsible>
  </div>
</template>
