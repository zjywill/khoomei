// Display helpers shared by the components.

const pad = (n: number) => String(n).padStart(2, '0')

/** Today: HH:mm. This year: M月D日 HH:mm. Otherwise with the year. */
export function shortTime(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const now = new Date()
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  if (d.toDateString() === now.toDateString()) return hm
  const md = `${d.getMonth() + 1}月${d.getDate()}日`
  return d.getFullYear() === now.getFullYear() ? `${md} ${hm}` : `${d.getFullYear()}年${md} ${hm}`
}

export function fullTime(iso: string): string {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? iso : d.toLocaleString('zh-CN', { hour12: false })
}

/** "3 分钟前", for presence. */
export function ago(iso?: string): string {
  if (!iso) return '没见过'
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  return `${Math.floor(s / 86400)} 天前`
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1 << 30) return `${(n / (1 << 20)).toFixed(1)} MB`
  return `${(n / (1 << 30)).toFixed(1)} GB`
}

/** The part after the slash; the owner is shown only when it is not the hub's. */
export function shortName(addr: string, owner: string): string {
  const i = addr.indexOf('/')
  if (i < 0) return addr
  return addr.slice(0, i) === owner ? addr.slice(i + 1) : addr
}

export function initials(addr: string): string {
  const name = addr.slice(addr.indexOf('/') + 1)
  return [...name][0]?.toUpperCase() ?? '?'
}

const palette = ['primary', 'secondary', 'success', 'info', 'warning', 'error', 'neutral'] as const
export type Tone = (typeof palette)[number]

const avatarClasses = [
  'bg-amber-100 text-amber-800 dark:bg-amber-900/60 dark:text-amber-200',
  'bg-sky-100 text-sky-800 dark:bg-sky-900/60 dark:text-sky-200',
  'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/60 dark:text-emerald-200',
  'bg-violet-100 text-violet-800 dark:bg-violet-900/60 dark:text-violet-200',
  'bg-rose-100 text-rose-800 dark:bg-rose-900/60 dark:text-rose-200',
  'bg-teal-100 text-teal-800 dark:bg-teal-900/60 dark:text-teal-200',
  'bg-orange-100 text-orange-800 dark:bg-orange-900/60 dark:text-orange-200',
  'bg-indigo-100 text-indigo-800 dark:bg-indigo-900/60 dark:text-indigo-200',
]

/** The same agent always gets the same avatar color. */
export function avatarClass(addr: string): string {
  let h = 0
  for (const ch of addr) h = (h * 31 + ch.codePointAt(0)!) >>> 0
  return avatarClasses[h % avatarClasses.length]
}

export const kindTone: Record<string, Tone> = { request: 'info', reply: 'success', notice: 'warning' }
