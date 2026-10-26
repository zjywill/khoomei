// Types and calls for /admin/api (docs/PANEL.md#接口).

export interface Envelope {
  v: number
  id: string
  from_agent: string
  to_agent: string
  ts: string
  kind?: string
  text?: string
  reply_to?: string
  wake?: string
  data?: unknown
  files?: FileRef[]
  group?: string
}

export interface FileRef {
  sha256: string
  name: string
  size: number
  type?: string
}

/** Download link for an attachment; inline previews only what the hub sniffs as a safe image. */
export function blobURL(f: FileRef, inline = false): string {
  const q = new URLSearchParams({ name: f.name })
  if (inline) q.set('inline', '1')
  return `/admin/api/blob/${f.sha256}?${q}`
}

const previewable = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])
export const canPreview = (f: FileRef) => previewable.has((f.type ?? '').toLowerCase()) || /\.(png|jpe?g|gif|webp)$/i.test(f.name)

export interface Received {
  at: string
  seq: number
  peer: string
  login?: string
}

export interface Rec {
  envelope: Envelope
  received: Received
  seq: number
  room: string
  acked: boolean
  wake: '' | 'ok' | 'failed' | 'downgraded'
  /** Attachments already cleaned up on the hub, by sha256. */
  files_gone?: string[]
  wakeDetail?: string
}

export interface Room {
  id: string
  /** private: the owner's own agents; group: a shared group; earlier: friends' traffic from before groups. */
  kind: 'private' | 'group' | 'earlier'
  name?: string
  title: string
  /** A group that no longer exists; only the owner still sees its messages. */
  deleted?: boolean
  members: string[]
  count: number
  pending: number
  last_seq: number
}

/** Who is looking: the hub owner sees everything, a guest only the groups that hold an agent of theirs. */
export interface Viewer {
  role: 'owner' | 'guest'
  /** A guest's handle when they are a registered friend; empty for a stranger. */
  handle?: string
}

export interface ContactAgent {
  address: string
  groups: string[]
  node?: string
  local?: boolean
  wake?: string
}

export interface Person {
  handle: string
  me?: boolean
  login?: string
  agents: ContactAgent[]
}

export interface Group {
  name: string
  members: string[]
  created: string
}

export interface Presence {
  state: 'online' | 'sleeping' | 'offline'
  waiting: number
  last_seen?: string
  wake?: string
}

export interface PanelEvent {
  seq: number
  type: 'message' | 'ack' | 'wake' | 'presence' | 'pruned' | 'groups'
  room?: string
  record?: Rec
  agent?: string
  ids?: string[]
  id?: string
  to_agent?: string
  result?: Rec['wake']
  detail?: string
  state?: Presence['state']
}

export interface Usage {
  count: number
  bytes: number
  oldest?: string
}

export interface Storage {
  disk: { free_bytes: number; total_bytes: number }
  retention: { acked_days: number; max_mb: number; last_run?: string; last_pruned: number }
  rooms: { id: string; pending: Usage; acked: Usage; files: Usage }[]
  files: Usage
  blob: { total_mb: number; keep_hours: number; max_mb: number }
}

export interface PruneRequest {
  room?: string
  agent?: string
  what: 'acked' | 'pending'
  older_than_hours: number
  dry_run: boolean
}

const base = '/admin/api/'

export interface AuthStatus {
  password_set: boolean
  logged_in: boolean
  /** No password yet and this viewer is the hub owner, so the panel may set the first one. */
  can_set: boolean
  min_length: number
  viewer: Viewer
}

export class ApiError extends Error {
  constructor(public status: number, message: string, public code = '', public retryAfter = 0) {
    super(message)
  }
}

/** Called when the hub answers 401 login_required: the session ended (password changed, idle, restart). */
export let onLoginRequired = () => {}
export function setOnLoginRequired(fn: () => void) {
  onLoginRequired = fn
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(base + path, { credentials: 'same-origin', cache: 'no-store', ...init })
  let body: any = null
  try {
    body = await res.json()
  } catch {
    // fall through with the status
  }
  if (!res.ok || !body?.ok) {
    if (res.status === 401 && body?.code === 'login_required') onLoginRequired()
    throw new ApiError(res.status, body?.error || `HTTP ${res.status}`, body?.code ?? '', body?.retry_after ?? 0)
  }
  return body as T
}

function query(params: Record<string, string | number | boolean | undefined>) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '' && v !== false && v !== 0) q.set(k, String(v === true ? 1 : v))
  }
  const s = q.toString()
  return s ? '?' + s : ''
}

// Every panel write sends JSON with X-Khoomei-Panel, which a cross-site form cannot (docs/PANEL.md).
function post<T>(path: string, body: unknown) {
  return call<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Khoomei-Panel': '1' },
    body: JSON.stringify(body),
  })
}

export const api = {
  auth: () => call<AuthStatus>('auth'),
  login: (password: string) => post<{}>('auth/login', { password }),
  logout: () => post<{}>('auth/logout', {}),
  setPassword: (current: string, next: string) => post<{}>('auth/password', current ? { current, new: next } : { new: next }),
  rooms: () => call<{ owner: string; viewer: Viewer; rooms: Room[]; events_next: number }>('rooms'),
  contacts: () => call<{ people: Person[] }>('contacts'),
  groups: () => call<{ groups: Group[] }>('groups'),
  createGroup: (name: string, members: string[]) => post<{}>('groups', { name, members }),
  deleteGroup: (name: string) => post<{}>('groups/delete', { name }),
  changeGroup: (name: string, add: string[], remove: string[]) => post<{}>('groups/members', { name, add, remove }),
  messages: (p: { room: string; member?: string; kinds?: string; unread?: boolean; before?: number; limit?: number }) =>
    call<{ messages: Rec[]; more: boolean }>('messages' + query(p)),
  message: (id: string) => call<{ message: Rec }>('message' + query({ id })),
  presence: () => call<{ members: Record<string, Presence> }>('presence'),
  events: (after: number, timeout: number, signal?: AbortSignal) =>
    call<{ next: number; reset: boolean; events: PanelEvent[] }>(`events?after=${after}&timeout=${timeout}`, { signal }),
  storage: () => call<Storage>('storage'),
  prune: (req: PruneRequest) => post<{ dry_run: boolean; count: number; bytes: number }>('prune', req),
}
