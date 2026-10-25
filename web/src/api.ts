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
  title: string
  peer?: string
  members: string[]
  count: number
  pending: number
  last_seq: number
}

export interface Presence {
  state: 'online' | 'sleeping' | 'offline'
  waiting: number
  last_seen?: string
  wake?: string
}

export interface PanelEvent {
  seq: number
  type: 'message' | 'ack' | 'wake' | 'presence' | 'pruned'
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

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
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
    throw new ApiError(res.status, body?.error || `HTTP ${res.status}`)
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

export const api = {
  rooms: () => call<{ owner: string; rooms: Room[]; events_next: number }>('rooms'),
  messages: (p: { room: string; member?: string; kinds?: string; unread?: boolean; before?: number; limit?: number }) =>
    call<{ messages: Rec[]; more: boolean }>('messages' + query(p)),
  message: (id: string) => call<{ message: Rec }>('message' + query({ id })),
  presence: () => call<{ members: Record<string, Presence> }>('presence'),
  events: (after: number, timeout: number, signal?: AbortSignal) =>
    call<{ next: number; reset: boolean; events: PanelEvent[] }>(`events?after=${after}&timeout=${timeout}`, { signal }),
  storage: () => call<Storage>('storage'),
  prune: (req: PruneRequest) =>
    call<{ dry_run: boolean; count: number; bytes: number }>('prune', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Khoomei-Panel': '1' },
      body: JSON.stringify(req),
    }),
}
