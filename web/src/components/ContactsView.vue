<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { DropdownMenuItem } from '@nuxt/ui'
import { api, type ContactAgent, type Person } from '../api'
import { errorText, groupsVersion, isOwner, me, navigate, presence, rooms } from '../state'
import { ago, avatarClass, initials, shortName } from '../format'

// The address book (docs/PANEL.md#通讯录): the owner and every registered friend, with their agents
// and the groups each one is in. A friend shows up here as soon as they are registered, before any
// group; putting their agents into a group is a separate step.
const people = ref<Person[]>([])
const error = ref('')
const loading = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    people.value = (await api.contacts()).people
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(groupsVersion, load)

const groupNames = computed(() => rooms.value.filter((r) => r.kind === 'group' && !r.deleted).map((r) => r.name!))

const stateText = { online: '在线', sleeping: '休眠', offline: '离线' } as const
const chipColor = { online: 'success', sleeping: 'warning', offline: 'neutral' } as const
const state = (a: string) => presence.value[a]?.state ?? 'offline'

const busy = ref('')
async function setMember(group: string, addr: string, on: boolean) {
  busy.value = addr
  error.value = ''
  try {
    await api.changeGroup(group, on ? [addr] : [], on ? [] : [addr])
    await load()
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = ''
  }
}

function menu(a: ContactAgent): DropdownMenuItem[][] {
  if (!groupNames.value.length) return [[{ label: '还没有群组，先在左边建一个', disabled: true }]]
  return [
    groupNames.value.map((g) => ({
      label: `#${g}`,
      type: 'checkbox' as const,
      checked: a.groups.includes(g),
      onUpdateChecked: (on: boolean) => void setMember(g, a.address, on),
    })),
  ]
}
</script>

<template>
  <UDashboardPanel id="contacts" :ui="{ root: 'min-h-0' }">
    <template #header>
      <UDashboardNavbar title="通讯录" icon="i-lucide-book-user" />
    </template>
    <template #body>
      <div class="mx-auto flex w-full max-w-3xl flex-col gap-4">
        <p v-if="isOwner" class="text-sm text-muted">
          朋友登记后就出现在这里（在 hub 上 <code>khoomei owner 名字 --login 登录身份 --apply</code>，再重启 hub）。
          要让他们的 agent 和你的说话，把两边都拉进同一个群组。
        </p>
        <p v-else class="text-sm text-muted">这里只列出和你在同一个群组里的人和 agent。</p>

        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        <USkeleton v-if="loading && !people.length" class="h-32 w-full" />

        <section v-for="p in people" :key="p.handle" class="rounded-lg border border-default" :aria-label="p.handle">
          <header class="flex flex-wrap items-center gap-2 border-b border-default px-4 py-3">
            <UAvatar :text="initials(p.handle)" size="md" :class="avatarClass(p.handle)" :alt="p.handle" />
            <div class="min-w-0">
              <h2 class="truncate font-medium">
                {{ p.handle }}
                <UBadge v-if="p.me" label="主人" color="primary" variant="subtle" size="sm" class="ms-1 align-middle" />
              </h2>
              <p v-if="p.login" class="truncate text-xs text-muted">{{ p.login }}</p>
            </div>
            <p v-if="p.me && isOwner" class="ms-auto text-xs text-muted">
              <UIcon name="i-lucide-lock" class="me-1 size-3 align-[-1px]" />全部在私有空间，互相随时能发信
            </p>
            <p v-else-if="isOwner && p.agents.length && p.agents.every((a) => !a.groups.length)" class="ms-auto text-xs text-warning">
              还不在任何群组，和你的 agent 说不上话
            </p>
          </header>

          <ul class="divide-y divide-default">
            <li v-for="a in p.agents" :key="a.address" class="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-2.5">
              <UChip :color="chipColor[state(a.address)]" :show="state(a.address) !== 'offline'" position="bottom-right" inset>
                <UAvatar :text="initials(a.address)" size="sm" :class="avatarClass(a.address)" :alt="a.address" />
              </UChip>
              <div class="min-w-0 flex-1">
                <p class="truncate text-sm">{{ shortName(a.address, me) }}</p>
                <p class="truncate text-xs text-muted">
                  {{ stateText[state(a.address)] }}<template v-if="presence[a.address]?.last_seen"> · {{ ago(presence[a.address]?.last_seen) }}</template>
                  <template v-if="a.local"> · 在 hub 上</template>
                  <template v-else-if="a.node"> · {{ a.node }}</template>
                </p>
              </div>
              <div class="flex flex-wrap items-center gap-1">
                <UButton
                  v-for="g in a.groups"
                  :key="g"
                  :label="`#${g}`"
                  color="neutral"
                  variant="soft"
                  size="xs"
                  @click="navigate({ view: 'chat', room: `group/${g}` })"
                />
                <UDropdownMenu v-if="isOwner" :items="menu(a)" :content="{ align: 'end' }">
                  <UButton
                    icon="i-lucide-user-plus"
                    :label="a.groups.length ? undefined : '拉进群组'"
                    color="neutral"
                    variant="ghost"
                    size="xs"
                    :loading="busy === a.address"
                    :aria-label="`把 ${a.address} 拉进群组或移出`"
                  />
                </UDropdownMenu>
              </div>
            </li>
            <li v-if="!p.agents.length" class="px-4 py-3 text-sm text-muted">
              还没登记 agent。在 hub 上运行 <code>khoomei agent {{ p.handle }}/名字 --node 节点名 --apply</code>。
            </li>
          </ul>
        </section>
      </div>
    </template>
  </UDashboardPanel>
</template>
