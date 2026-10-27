// Panel state: the route in location.hash, the rooms, the current timeline, presence, and the
// long-poll loop that keeps them live (docs/PANEL.md#数据流).
import { computed, reactive, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { api, ApiError, setOnLoginRequired, type AuthStatus, type PanelEvent, type Presence, type Rec, type Room, type Viewer } from './api'

// --------------------------------------------------------------------------- route

export interface Route {
  view: 'chat' | 'storage' | 'contacts'
  room: string
  member: string
  kinds: string[]
  unread: boolean
}

export const route = reactive<Route>({ view: 'chat', room: '', member: '', kinds: [], unread: false })

function parseHash(): Route {
  const [path = '', qs = ''] = location.hash.replace(/^#\/?/, '').split('?')
  const q = new URLSearchParams(qs)
  if (path === 'storage' || path === 'contacts') return { view: path, room: route.room, member: '', kinds: [], unread: false }
  const room = path.startsWith('room/') ? decodeURIComponent(path.slice(5)) : ''
  return {
    view: 'chat',
    room,
    member: q.get('member') ?? '',
    kinds: (q.get('kinds') ?? '').split(',').filter(Boolean),
    unread: q.get('unread') === '1',
  }
}

function formatHash(r: Route): string {
  if (r.view === 'storage' || r.view === 'contacts') return `#/${r.view}`
  const q = new URLSearchParams()
  if (r.member) q.set('member', r.member)
  if (r.kinds.length) q.set('kinds', r.kinds.join(','))
  if (r.unread) q.set('unread', '1')
  const s = q.toString()
  return `#/room/${encodeURIComponent(r.room)}${s ? '?' + s : ''}`
}

export function navigate(patch: Partial<Route>) {
  const next = { ...route, ...patch }
  if (patch.room !== undefined && patch.room !== route.room) Object.assign(next, { member: '', kinds: [], unread: false })
  const h = formatHash(next)
  if (h !== location.hash) location.hash = h
  else applyHash()
}

function applyHash() {
  Object.assign(route, parseHash())
}

// --------------------------------------------------------------------------- data

export const owner = ref('')
export const viewer = ref<Viewer>({ role: 'owner' })
export const isOwner = computed(() => viewer.value.role === 'owner')
/** Whose names are shown without their handle: the owner's, or in a guest view the guest's own. */
export const me = computed(() => (isOwner.value ? owner.value : (viewer.value.handle ?? '')))
export const rooms = ref<Room[]>([])
/** Bumped whenever groups or the contacts change, so views that list them reload. */
export const groupsVersion = ref(0)
/** Agents that joined by themselves while the panel was open; App turns each into a toast. */
export const arrivals = ref<string[]>([])
export const presence = ref<Record<string, Presence>>({})
export const connection = ref<'connecting' | 'live' | 'retrying'>('connecting')
export const fatal = ref('') // 403 and friends: nothing to retry

/** The panel password (docs/PANEL.md#密码). null until the first answer. */
export const authStatus = ref<AuthStatus | null>(null)
// A stranger needs the password; the owner on loopback and registered friends do not.
export const needLogin = computed(
  () => !!authStatus.value?.password_set && authStatus.value.viewer.role === 'guest' && !authStatus.value.viewer.handle,
)
export const passwordOpen = ref(false)

export const timeline = reactive({
  items: [] as Rec[],
  more: false,
  loading: false,
  loadingOlder: false,
  error: '',
  /** Bumped when the list is replaced, so the view scrolls to the bottom. */
  generation: 0,
})

export const selected = ref<Rec | null>(null)

/** Messages reached through a reply quote but not on the loaded page, by id. null: pruned. */
export const quoted = reactive(new Map<string, Rec | null>())

export const recKey = (r: Rec) => `${r.envelope.to_agent}/${r.envelope.id}`

const PAGE = 50
const KEEP = 1000

function matches(r: Rec): boolean {
  if (r.room !== route.room) return false
  if (route.member && r.envelope.from_agent !== route.member && r.envelope.to_agent !== route.member) return false
  if (route.kinds.length && !route.kinds.includes(r.envelope.kind || 'text')) return false
  if (route.unread && r.acked) return false
  return true
}

function filterParams() {
  return { room: route.room, member: route.member, kinds: route.kinds.join(','), unread: route.unread }
}

let pageToken = 0

export async function loadTimeline() {
  if (route.view !== 'chat' || needLogin.value || !route.room) return
  const token = ++pageToken
  timeline.loading = true
  timeline.error = ''
  try {
    const res = await api.messages({ ...filterParams(), limit: PAGE })
    if (token !== pageToken) return
    timeline.items = res.messages
    timeline.more = res.more
    timeline.generation++
    selected.value = null
  } catch (e) {
    if (token === pageToken) timeline.error = errorText(e)
  } finally {
    if (token === pageToken) timeline.loading = false
  }
}

export async function loadOlder() {
  if (timeline.loadingOlder || !timeline.more || !timeline.items.length) return
  const token = pageToken
  timeline.loadingOlder = true
  try {
    const res = await api.messages({ ...filterParams(), limit: PAGE, before: timeline.items[0].seq })
    if (token !== pageToken) return
    timeline.items = [...res.messages, ...timeline.items].slice(0, KEEP)
    timeline.more = res.more || timeline.items.length >= KEEP
  } catch (e) {
    timeline.error = errorText(e)
  } finally {
    timeline.loadingOlder = false
  }
}

export async function loadRooms() {
  const res = await api.rooms()
  owner.value = res.owner
  viewer.value = res.viewer
  rooms.value = res.rooms
  // A room that is gone, or one this viewer cannot see, falls back to the first one.
  if (route.view === 'chat' && !res.rooms.some((r) => r.id === route.room)) {
    const first = res.rooms[0]?.id ?? ''
    if (first !== route.room) navigate({ room: first })
  }
  return res
}

export async function loadPresence() {
  try {
    presence.value = (await api.presence()).members
  } catch {
    // the event loop reports connection trouble
  }
}

export async function lookup(id: string): Promise<Rec | null> {
  const local = timeline.items.find((r) => r.envelope.id === id)
  if (local) return local
  if (quoted.has(id)) return quoted.get(id)!
  try {
    const { message } = await api.message(id)
    quoted.set(id, message)
    return message
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) quoted.set(id, null)
    return null
  }
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'wrong_password') return e.status === 403 ? '当前密码不对' : '密码不对'
    if (e.code === 'locked') return `错太多次了，${e.retryAfter} 秒后再试`
    if (e.code === 'weak_password') return `密码至少 ${authStatus.value?.min_length ?? 8} 个字符`
    if (e.code === 'not_owner') return '只有 hub 的主人能设第一个密码：在 hub 上运行 khoomei panel-password'
    if (e.code === 'login_required') return '需要重新登录'
    if (e.code === 'owner_only') return '只有 hub 的主人能这么做'
    if (e.status === 403) return `没有权限：${e.message}`
    return e.message
  }
  return '连不上 hub'
}

// --------------------------------------------------------------------------- live updates

/** Incremented for every message that arrives while the reader is scrolled up. */
export const unseen = ref(0)
export const following = ref(true) // the timeline is scrolled to the bottom

// Room counters are refetched rather than patched, so they cannot drift from the hub's.
let roomsTimer: ReturnType<typeof setTimeout> | undefined
function refreshRooms() {
  clearTimeout(roomsTimer)
  roomsTimer = setTimeout(() => void loadRooms().catch(() => {}), 400)
}

function apply(ev: PanelEvent) {
  switch (ev.type) {
    case 'message': {
      const rec = ev.record!
      refreshRooms()
      if (matches(rec) && !timeline.items.some((r) => recKey(r) === recKey(rec))) {
        timeline.items.push(rec)
        if (timeline.items.length > KEEP) {
          timeline.items.splice(0, timeline.items.length - KEEP)
          timeline.more = true
        }
        if (!following.value) unseen.value++
      }
      break
    }
    case 'ack': {
      const ids = new Set(ev.ids)
      for (const r of timeline.items) {
        if (r.envelope.to_agent === ev.agent && ids.has(r.envelope.id) && !r.acked) r.acked = true
      }
      if (route.unread) timeline.items = timeline.items.filter((r) => !r.acked)
      refreshRooms()
      break
    }
    case 'wake': {
      const r = timeline.items.find((r) => r.envelope.id === ev.id && r.envelope.to_agent === ev.to_agent)
      if (r) {
        r.wake = ev.result!
        r.wakeDetail = ev.detail
      }
      break
    }
    case 'presence': {
      const p = presence.value[ev.agent!]
      if (p) p.state = ev.state as Presence['state']
      void loadPresence()
      break
    }
    case 'groups': {
      groupsVersion.value++
      refreshRooms()
      void loadPresence()
      break
    }
    case 'joined': {
      groupsVersion.value++
      refreshRooms()
      void loadPresence()
      if (ev.state === 'new') arrivals.value = [...arrivals.value, ev.agent!]
      break
    }
    case 'pruned': {
      const ids = new Set(ev.ids)
      timeline.items = timeline.items.filter((r) => !ids.has(r.envelope.id))
      if (selected.value && ids.has(selected.value.envelope.id)) selected.value = null
      for (const id of ids) if (quoted.has(id)) quoted.set(id, null)
      refreshRooms()
      break
    }
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

const visibility = useDocumentVisibility()

function visible(): Promise<void> {
  if (visibility.value === 'visible') return Promise.resolve()
  return new Promise((resolve) => {
    const stop = watch(visibility, (v) => {
      if (v === 'visible') {
        stop()
        resolve()
      }
    })
  })
}

// A session is one logged-in stretch: logging out, or the hub asking for a login, ends its event
// loop and presence timer, and a new login starts fresh ones.
let session = 0
let presenceTimer: ReturnType<typeof setInterval> | undefined

async function eventLoop(start: number, gen: number) {
  let next = start
  let backoff = 1000
  while (gen === session) {
    await visible()
    try {
      const res = await api.events(next, 50)
      if (gen !== session) return
      if (connection.value !== 'live') {
        if (connection.value === 'retrying') void resync()
        connection.value = 'live'
      }
      backoff = 1000
      if (res.reset) {
        next = res.next
        await resync()
        continue
      }
      next = res.next
      for (const ev of res.events) apply(ev)
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return
      if (e instanceof ApiError && e.status === 403) {
        fatal.value = errorText(e)
        return
      }
      connection.value = 'retrying'
      await sleep(backoff)
      backoff = Math.min(backoff * 2, 30_000)
    }
  }
}

async function resync() {
  await Promise.all([loadRooms(), loadPresence(), loadTimeline()])
}

/** Forget everything shown: the viewer is no longer logged in. */
function endSession() {
  session++
  clearInterval(presenceTimer)
  rooms.value = []
  presence.value = {}
  timeline.items = []
  timeline.more = false
  selected.value = null
  quoted.clear()
  connection.value = 'connecting'
}

setOnLoginRequired(() => {
  if (authStatus.value && !needLogin.value) {
    authStatus.value = { ...authStatus.value, logged_in: false }
    endSession()
  }
})

export async function start() {
  applyHash()
  window.addEventListener('hashchange', applyHash)
  watch(
    () => [route.view, route.room, route.member, route.kinds.join(','), route.unread],
    () => {
      unseen.value = 0
      void loadTimeline()
    },
  )
  await boot()
}

/** Ask the hub where the password stands, then load the panel unless a login is needed first. */
async function boot() {
  const gen = ++session
  for (;;) {
    try {
      authStatus.value = await api.auth()
      if (needLogin.value) return
      const res = await loadRooms()
      await Promise.all([loadPresence(), loadTimeline()])
      if (gen !== session) return
      connection.value = 'live'
      clearInterval(presenceTimer)
      presenceTimer = setInterval(loadPresence, 30_000) // online grace expires without an event
      void eventLoop(res.events_next, gen)
      return
    } catch (e) {
      if (gen !== session || needLogin.value) return
      if (e instanceof ApiError && e.status === 403) {
        fatal.value = errorText(e)
        return
      }
      connection.value = 'retrying'
      await sleep(3000)
    }
  }
}

/** Log in as the owner, from the login page or from a guest view: everything reloads as the owner. */
export async function login(password: string) {
  await api.login(password)
  endSession()
  await boot()
}

export async function logout() {
  try {
    await api.logout()
  } finally {
    endSession()
    await boot()
  }
}

/** Set the first password, or change it. The hub logs this browser in under the new one. */
export async function setPassword(current: string, next: string) {
  await api.setPassword(current, next)
  authStatus.value = await api.auth()
}
