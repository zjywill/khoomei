<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { blobURL, canPreview, type Rec } from '../api'
import { avatarClass, bytes, fullTime, initials, kindTone, shortName, shortTime } from '../format'
import { lookup, owner } from '../state'

const props = defineProps<{ rec: Rec; grouped: boolean; selected: boolean }>()
const emit = defineEmits<{ select: []; jump: [id: string] }>()

const env = computed(() => props.rec.envelope)
const kind = computed(() => env.value.kind || 'text')
const text = computed(() => env.value.text ?? '')
const lines = computed(() => text.value.split('\n').length)
const long = computed(() => lines.value > 20 || text.value.length > 2000)
const expanded = ref(false)

// The quoted original, from the loaded page or fetched once; null when it was pruned.
const quote = ref<Rec | null | undefined>(undefined)
watchEffect(async () => {
  const id = env.value.reply_to
  quote.value = id ? await lookup(id) : undefined
})
const quoteText = computed(() => {
  const t = quote.value?.envelope.text ?? ''
  return [...t].length > 80 ? [...t].slice(0, 80).join('') + '…' : t
})

async function openQuote() {
  const id = env.value.reply_to!
  emit('jump', id)
}

const wakeLine = computed(() => {
  if (props.rec.wake === 'failed') return { text: `唤醒 ${shortName(env.value.to_agent, owner.value)} 失败`, detail: props.rec.wakeDetail, tone: 'text-error' }
  if (props.rec.wake === 'downgraded')
    return { text: `${shortName(env.value.to_agent, owner.value)} 本小时的唤醒次数用完了，这条只排队不唤醒`, tone: 'text-muted' }
  return null
})
</script>

<template>
  <div>
    <article
      :data-msg-id="env.id"
      tabindex="-1"
      class="group flex gap-3 rounded-lg px-2 outline-none transition-colors hover:bg-elevated/50 focus-visible:ring-2 focus-visible:ring-primary"
      :class="[grouped ? 'pt-0.5 pb-1' : 'pt-3 pb-1', selected ? 'bg-elevated' : '']"
      :aria-label="`${env.from_agent} 发给 ${env.to_agent}`"
    >
      <div class="w-8 shrink-0">
        <UAvatar v-if="!grouped" :text="initials(env.from_agent)" size="md" :class="avatarClass(env.from_agent)" :alt="env.from_agent" />
      </div>

      <div class="min-w-0 flex-1">
        <header v-if="!grouped" class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
          <span class="font-semibold text-highlighted">{{ shortName(env.from_agent, owner) }}</span>
          <span class="inline-flex items-center gap-1 text-muted">
            <UIcon name="i-lucide-arrow-right" class="size-3 shrink-0" aria-hidden="true" />
            <span class="sr-only">发给</span>
            <span class="text-primary">@{{ shortName(env.to_agent, owner) }}</span>
          </span>
          <UBadge v-if="kind !== 'text'" :label="kind" :color="kindTone[kind] ?? 'neutral'" variant="subtle" size="sm" />
          <UTooltip :text="`${fullTime(rec.received.at)} · 来自 ${rec.received.peer}${rec.received.login ? '（' + rec.received.login + '）' : ''}`">
            <time :datetime="rec.received.at" class="text-xs text-dimmed">{{ shortTime(rec.received.at) }}</time>
          </UTooltip>
        </header>

        <button
          v-if="env.reply_to"
          type="button"
          class="mt-1 flex max-w-full items-start gap-1.5 border-s-2 border-accented ps-2 text-start text-xs text-muted hover:text-default"
          :disabled="!quote"
          @click="openQuote"
        >
          <UIcon name="i-lucide-reply" class="mt-0.5 size-3 shrink-0" aria-hidden="true" />
          <span v-if="quote === undefined" class="italic">正在找原消息…</span>
          <span v-else-if="quote === null" class="italic">原消息已清理</span>
          <span v-else class="truncate">
            <span class="font-medium">{{ shortName(quote.envelope.from_agent, owner) }}：</span>{{ quoteText }}
          </span>
        </button>

        <div class="mt-0.5 flex items-end gap-2">
          <div class="min-w-0 flex-1">
            <p
              v-if="text"
              class="whitespace-pre-wrap break-words text-sm leading-relaxed text-default"
              :class="long && !expanded ? 'line-clamp-[20]' : ''"
            >{{ text }}</p>
            <UButton
              v-if="long"
              :label="expanded ? '收起' : '展开全文'"
              variant="link"
              size="xs"
              class="px-0"
              @click="expanded = !expanded"
            />
            <UBadge v-if="env.data !== undefined" label="data" icon="i-lucide-braces" color="neutral" variant="outline" size="sm" class="mt-1" />
            <ul v-if="env.files?.length" class="mt-1.5 flex flex-wrap gap-2" aria-label="附件">
              <li v-for="f in env.files" :key="f.sha256">
                <span
                  v-if="rec.files_gone?.includes(f.sha256)"
                  class="flex max-w-72 items-center gap-2 rounded-md border border-dashed border-default px-2.5 py-1.5 text-sm text-dimmed"
                  :title="`${f.name} 已在 hub 上清理`"
                >
                  <UIcon name="i-lucide-file-x" class="size-4 shrink-0" aria-hidden="true" />
                  <span class="truncate line-through">{{ f.name }}</span>
                  <span class="shrink-0 text-xs">已清理</span>
                </span>
                <a
                  v-else-if="canPreview(f)"
                  :href="blobURL(f, true)"
                  target="_blank"
                  rel="noopener"
                  class="block overflow-hidden rounded-md border border-default hover:border-accented"
                  :title="`${f.name} · ${bytes(f.size)}`"
                >
                  <img :src="blobURL(f, true)" :alt="f.name" loading="lazy" class="h-40 w-auto max-w-60 object-contain" />
                </a>
                <a
                  v-else
                  :href="blobURL(f)"
                  :download="f.name"
                  class="flex max-w-72 items-center gap-2 rounded-md border border-default px-2.5 py-1.5 text-sm hover:border-accented hover:bg-elevated/50"
                >
                  <UIcon name="i-lucide-file" class="size-4 shrink-0 text-muted" aria-hidden="true" />
                  <span class="truncate">{{ f.name }}</span>
                  <span class="shrink-0 text-xs text-dimmed">{{ bytes(f.size) }}</span>
                  <UIcon name="i-lucide-download" class="size-3.5 shrink-0 text-muted" aria-hidden="true" />
                </a>
              </li>
            </ul>
          </div>

          <div class="flex shrink-0 items-center gap-1 pb-0.5 text-dimmed">
            <UTooltip v-if="rec.wake === 'ok'" text="触发了唤醒">
              <UIcon name="i-lucide-zap" class="size-3.5 text-warning" />
            </UTooltip>
            <span class="sr-only" v-if="rec.wake === 'ok'">触发了唤醒，</span>
            <UTooltip :text="rec.acked ? '已读（收件方已 ack）' : '已送达，未读'">
              <UIcon :name="rec.acked ? 'i-lucide-check-check' : 'i-lucide-check'" class="size-4" :class="rec.acked ? 'text-primary' : ''" />
            </UTooltip>
            <span class="sr-only">{{ rec.acked ? '已读' : '未读' }}</span>
            <UButton
              icon="i-lucide-panel-right-open"
              color="neutral"
              variant="ghost"
              size="xs"
              :aria-label="selected ? '关闭详情' : '查看详情'"
              :aria-expanded="selected"
              class="opacity-60 group-hover:opacity-100 focus-visible:opacity-100"
              @click="emit('select')"
            />
          </div>
        </div>
      </div>
    </article>

    <p v-if="wakeLine" class="my-1 text-center text-xs" :class="wakeLine.tone">
      {{ wakeLine.text }}<span v-if="wakeLine.detail">：{{ wakeLine.detail }}</span>
    </p>
  </div>
</template>
