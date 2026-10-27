<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type Person } from '../api'
import { errorText, navigate, owner, rooms } from '../state'
import { avatarClass, initials, shortName } from '../format'

// Without a group: create one. With a group: change who is in it, or delete it. The owner picks
// every member, friends' agents included (docs/GROUPS.md).
const props = defineProps<{ group?: string }>()
const open = defineModel<boolean>('open', { required: true })

const name = ref('')
const picked = ref<Set<string>>(new Set())
const initial = ref<Set<string>>(new Set())
const people = ref<Person[]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const confirmDelete = ref(false)
const open_ = ref(false) // new arrivals land in this group
const initialOpen = ref(false)

watch(open, async (o) => {
  if (!o) return
  name.value = ''
  error.value = ''
  confirmDelete.value = false
  const members = rooms.value.find((r) => r.kind === 'group' && r.name === props.group)?.members ?? []
  initial.value = new Set(members)
  picked.value = new Set(members)
  open_.value = initialOpen.value = false
  loading.value = true
  try {
    const [book, groups] = await Promise.all([api.contacts(), props.group ? api.groups() : Promise.resolve({ groups: [] })])
    people.value = book.people
    open_.value = initialOpen.value = !!groups.groups.find((g) => g.name === props.group)?.open
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
})

function toggle(addr: string, on: boolean | 'indeterminate') {
  const next = new Set(picked.value)
  if (on === true) next.add(addr)
  else next.delete(addr)
  picked.value = next
}

const editing = computed(() => !!props.group)
const added = computed(() => [...picked.value].filter((a) => !initial.value.has(a)))
const removed = computed(() => [...initial.value].filter((a) => !picked.value.has(a)))
const ready = computed(() =>
  editing.value
    ? added.value.length + removed.value.length > 0 || open_.value !== initialOpen.value
    : name.value.trim() !== '' && !name.value.includes('/'),
)
const withAgents = computed(() => people.value.filter((p) => p.agents.length))
const withoutAgents = computed(() => people.value.filter((p) => !p.agents.length && !p.me))

async function save() {
  if (!ready.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    if (editing.value) {
      if (added.value.length + removed.value.length) await api.changeGroup(props.group!, added.value, removed.value)
      if (open_.value !== initialOpen.value) await api.setGroupOpen(props.group!, open_.value)
    } else {
      const n = name.value.trim()
      await api.createGroup(n, [...picked.value])
      if (open_.value) await api.setGroupOpen(n, true)
      navigate({ view: 'chat', room: `group/${n}` })
    }
    open.value = false
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}

async function remove() {
  if (!confirmDelete.value) {
    confirmDelete.value = true
    return
  }
  busy.value = true
  error.value = ''
  try {
    await api.deleteGroup(props.group!)
    open.value = false
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="editing ? `群组 ${group} 的成员` : '新建群组'"
    :description="
      editing
        ? '勾上的 agent 能在这个群组里互相发信；群里有谁的 agent，谁就能在面板里看到这个群组。'
        : '先起个名字，再从通讯录里挑 agent。群里有谁的 agent，谁就能在面板里看到这个群组。'
    "
  >
    <template #body>
      <form id="group-form" class="flex flex-col gap-4" @submit.prevent="save">
        <UFormField v-if="!editing" label="名字" hint="不能有 /">
          <UInput v-model="name" autofocus required maxlength="64" class="w-full" placeholder="比如：项目X、家人" />
        </UFormField>

        <USkeleton v-if="loading" class="h-24 w-full" />
        <fieldset v-for="p in withAgents" v-else :key="p.handle" class="flex flex-col gap-1.5">
          <legend class="mb-1 text-xs font-medium text-dimmed">{{ p.me ? `${p.handle}（我）` : p.handle }}</legend>
          <label
            v-for="a in p.agents"
            :key="a.address"
            class="flex cursor-pointer items-center gap-2.5 rounded-md px-1 py-1 hover:bg-elevated/60"
          >
            <UCheckbox :model-value="picked.has(a.address)" @update:model-value="toggle(a.address, $event)" />
            <UAvatar :text="initials(a.address)" size="xs" :class="avatarClass(a.address)" :alt="a.address" />
            <span class="min-w-0 truncate text-sm">{{ shortName(a.address, owner) }}</span>
            <span v-if="a.groups.length" class="ms-auto truncate text-xs text-muted">{{ a.groups.map((g) => '#' + g).join(' ') }}</span>
          </label>
        </fieldset>
        <p v-if="!loading && withoutAgents.length" class="text-xs text-muted">
          {{ withoutAgents.map((p) => p.handle).join('、') }} 还没登记 agent，登记后才能拉进群组。
        </p>

        <USwitch
          v-model="open_"
          label="新来的 agent 自动进这个群组"
          description="朋友的 agent 报到后直接就在这里，能和群里的 agent 说话，不用你再拉。"
        />

        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
      </form>
    </template>
    <template #footer="{ close }">
      <div class="flex w-full items-center gap-2">
        <UButton
          v-if="editing"
          :label="confirmDelete ? '确定删除？消息会留下，只有你看得到' : '删除群组'"
          icon="i-lucide-trash-2"
          color="error"
          :variant="confirmDelete ? 'solid' : 'ghost'"
          :loading="busy && confirmDelete"
          @click="remove"
        />
        <div class="ms-auto flex gap-2">
          <UButton label="取消" color="neutral" variant="ghost" @click="close" />
          <UButton type="submit" form="group-form" :label="editing ? '保存' : '建立'" :loading="busy && !confirmDelete" :disabled="!ready" />
        </div>
      </div>
    </template>
  </UModal>
</template>
