// 衡台后端 API 客户端（同源，会话使用 HttpOnly cookie）。
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string
  ) {
    super(message)
  }
}

// CSRF：登录后服务端下发 hd_csrf cookie（非 HttpOnly），变更类请求回传到 X-CSRF-Token 头。
function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)hd_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...((init.headers as Record<string, string>) ?? {}),
  }
  if (!SAFE_METHODS.has(method)) {
    const t = csrfToken()
    if (t) headers['X-CSRF-Token'] = t
  }
  const res = await fetch(`/api/v1${path}`, {
    credentials: 'include',
    ...init,
    headers,
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const body = (await res.json()) as { message?: string }
      if (body.message) msg = body.message
    } catch {
      /* 非 JSON 响应 */
    }
    throw new ApiError(res.status, msg)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export interface Me {
  id: number
  org_id: number
  email: string
  display_name: string
  is_super_admin: boolean
  roles: string[]
  permissions: string[]
  csrf_token?: string
}

export const authApi = {
  login: (email: string, password: string) =>
    api<Me>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }),
  logout: () => api<void>('/auth/logout', { method: 'POST' }),
  me: () => api<Me>('/auth/me'),
  changePassword: (current_password: string, new_password: string) =>
    api<Me>('/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current_password, new_password }),
    }),
}

export interface AuditLog {
  id: number
  ts: string
  actor_email: string
  action: string
  phase: string
  outcome: string
  resource_type: string
  resource_id: string
  ip: string
  request_id: string
  hash: string
  detail?: Record<string, unknown>
}

export interface AuditFilter {
  actor?: string
  action?: string
  outcome?: string
  before_id?: number
}

function qs(f: object): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(f)) if (v) p.set(k, String(v))
  const s = p.toString()
  return s ? `?${s}` : ''
}

export const auditApi = {
  list: (f: AuditFilter) =>
    api<{ items: AuditLog[]; next_before_id: number }>(
      `/admin/audit-logs${qs(f)}`
    ),
  verify: () =>
    api<{ ok: boolean; checked: number; legacy: number; broken_at?: number }>(
      '/admin/audit-logs/verify'
    ),
  exportUrl: (f: AuditFilter) => `/api/v1/admin/audit-logs/export${qs(f)}`,
}

export type SystemStatus = {
  auditor_assigned: boolean
  backup_keep: number
  disk_free_percent: number | null
  disk_low_threshold: number
  disk_low: boolean
  https: boolean
}

export const systemApi = {
  status: () => api<SystemStatus>('/admin/system/status'),
}
