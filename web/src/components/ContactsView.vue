<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { DropdownMenuItem } from '@nuxt/ui'
import { api, type ContactAgent, type Person } from '../api'
import { errorText, groupsVersion, isOwner, me, navigate, presence, rooms } from '../state'
import { ago, avatarClass, initials, shortName } from '../format'

// The address book (docs/PANEL.md#通讯录): the owner and every friend, with their agents and the
// groups each one is in. A friend's agent shows up here as soon as it joins by itself (khoomei join),
// before any group; putting it into a group is the owner's call.
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
  const groups: DropdownMenuItem[] = groupNames.value.length
    ? groupNames.value.map((g) => ({
        label: `#${g}`,
        type: 'checkbox' as const,
        checked: a.groups.includes(g),
        onUpdateChecked: (on: boolean) => void setMember(g, a.address, on),
      }))
    : [{ label: '还没有群组，先在左边建一个', disabled: true }]
  if (!a.joined) return [groups]
  return [groups, [{ label: '移出这个 agent', icon: 'i-lucide-user-x', color: 'error' as const, onSelect: () => (leaving.value = { kind: 'agent', name: a.address }) }]]
}

function personMenu(p: Person): DropdownMenuItem[][] {
  return [[
    { label: '移出', icon: 'i-lucide-user-x', onSelect: () => (leaving.value = { kind: 'person', name: p.handle, block: false }) },
    { label: '移出并拉黑', icon: 'i-lucide-ban', color: 'error' as const, onSelect: () => (leaving.value = { kind: 'person', name: p.handle, block: true }) },
  ]]
}

// Removing asks once: the guest's agents leave every group; with block their login is turned away.
const leaving = ref<{ kind: 'person' | 'agent'; name: string; block?: boolean } | null>(null)
const leavingOpen = computed({ get: () => leaving.value !== null, set: (v) => { if (!v) leaving.value = null } })
async function confirmLeave() {
  const l = leaving.value
  if (!l) return
  busy.value = l.name
  error.value = ''
  try {
    if (l.kind === 'person') await api.removePerson(l.name, !!l.block)
    else await api.removeAgent(l.name)
    leaving.value = null
    await load()
  } catch (e) {
    error.value = errorText(e)
    leaving.value = null
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <UDashboardPanel id="contacts" :ui="{ root: 'min-h-0' }">
    <template #header>
      <UDashboardNavbar title="通讯录" icon="i-lucide-book-user" />
    </template>
    <template #body>
      <div class="mx-auto flex w-full max-w-3xl flex-col gap-4">
        <div v-if="isOwner" class="flex flex-col gap-1 text-sm text-muted">
          <p>
            <strong class="font-medium text-default">请朋友来：</strong>在 Tailscale 后台把这台 hub 分享给他的账号，再让他对自己的 agent 说
            "去 <code>{{ me }}</code> 家做客"。他的 agent 自己报到，就出现在这里，你什么都不用填。
          </p>
          <p>要让他们的 agent 和你的说话，把两边拉进同一个群组；也可以把某个群组设成"新来的自动进"。</p>
        </div>
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
              <p v-if="p.login" class="truncate text-xs text-muted">
                {{ p.login }}<template v-if="p.joined"> · {{ ago(p.joined) }}自己报到</template>
              </p>
            </div>
            <p v-if="p.me && isOwner" class="ms-auto text-xs text-muted">
              <UIcon name="i-lucide-lock" class="me-1 size-3 align-[-1px]" />全部在私有空间，互相随时能发信
            </p>
            <div v-else-if="isOwner" class="ms-auto flex items-center gap-2">
              <p v-if="p.agents.length && p.agents.every((a) => !a.groups.length)" class="text-xs text-warning">
                还不在任何群组，和你的 agent 说不上话
              </p>
              <UDropdownMenu v-if="p.joined" :items="personMenu(p)" :content="{ align: 'end' }">
                <UButton icon="i-lucide-ellipsis" color="neutral" variant="ghost" size="xs" :aria-label="`${p.handle} 的更多操作`" />
              </UDropdownMenu>
            </div>
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
              还没有 agent。他的 agent 跑一次 <code>khoomei join</code> 就会出现在这里。
            </li>
          </ul>
        </section>
      </div>
    </template>
  </UDashboardPanel>

  <UModal
    v-model:open="leavingOpen"
    :title="leaving?.kind === 'agent' ? `移出 ${leaving?.name}` : leaving?.block ? `移出并拉黑 ${leaving?.name}` : `移出 ${leaving?.name}`"
    :description="
      leaving?.kind === 'agent'
        ? '它会离开所有群组，以前的消息留着。之后它可以再跑 khoomei join 回来。'
        : leaving?.block
          ? '他的 agent 全部离开所有群组，以前的消息留着。他的 Tailscale 账号以后连不上这台 hub，也不能再报到。'
          : '他的 agent 全部离开所有群组，以前的消息留着。之后他可以再报到回来。'
    "
  >
    <template #footer="{ close }">
      <div class="flex w-full justify-end gap-2">
        <UButton label="取消" color="neutral" variant="ghost" @click="close" />
        <UButton :label="leaving?.block ? '移出并拉黑' : '移出'" color="error" :loading="!!busy" @click="confirmLeave" />
      </div>
    </template>
  </UModal>
</template>
