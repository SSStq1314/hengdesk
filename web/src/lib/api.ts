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

export interface OIDCConfig {
  enabled: boolean
  display_name?: string
  enforced?: boolean
}

export const authApi = {
  oidcConfig: () => api<OIDCConfig>('/auth/oidc/config'),
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

export type HighRiskAlert = {
  id: number
  ts: string
  actor_email: string
  ip: string
  detail: { kind?: string; severity?: string }
}

// 审计员站内通知（D-34）：未读的高风险审计事件。
export const alertApi = {
  list: () => api<{ unread: number; items: HighRiskAlert[] }>('/admin/alerts'),
  markRead: (upToId: number) =>
    api<void>('/admin/alerts/read', {
      method: 'POST',
      body: JSON.stringify({ up_to_id: upToId }),
    }),
}

export const systemApi = {
  status: () => api<SystemStatus>('/admin/system/status'),
}

export type DeptRef = { id: number; name: string }
export type RoleRef = { key: string; name: string; department_id?: number }

export type Member = {
  id: number
  email: string
  display_name: string
  employee_no: string
  status: 'active' | 'disabled'
  has_password: boolean
  last_login_at: string | null
  departments: DeptRef[]
  roles: RoleRef[]
}

export type MemberFilter = {
  q?: string
  dept_id?: number
  status?: string
  before_id?: number
}

export type MemberInput = {
  email?: string
  display_name?: string
  employee_no?: string
  department_ids?: number[]
  password?: string
}

const json = (method: string, body: unknown): RequestInit => ({
  method,
  body: JSON.stringify(body),
})

export const memberApi = {
  list: (f: MemberFilter) =>
    api<{ items: Member[]; next_before_id: number }>(`/admin/users${qs(f)}`),
  create: (m: MemberInput) => api<Member>('/admin/users', json('POST', m)),
  update: (id: number, m: MemberInput) =>
    api<Member>(`/admin/users/${id}`, json('PATCH', m)),
  disable: (id: number) =>
    api<void>(`/admin/users/${id}/disable`, { method: 'POST' }),
  enable: (id: number) =>
    api<void>(`/admin/users/${id}/enable`, { method: 'POST' }),
  unlock: (id: number) =>
    api<void>(`/admin/users/${id}/unlock`, { method: 'POST' }),
  resetPassword: (id: number, password: string) =>
    api<void>(
      `/admin/users/${id}/password-reset`,
      json('POST', { new_password: password })
    ),
  assignRole: (id: number, role: string, department_id?: number) =>
    api<void>(
      `/admin/users/${id}/roles`,
      json('POST', { role, department_id })
    ),
  revokeRole: (id: number, role: string, department_id?: number) =>
    api<void>(
      `/admin/users/${id}/roles/${role}${department_id ? `?department_id=${department_id}` : ''}`,
      { method: 'DELETE' }
    ),
}

export type Department = {
  id: number
  name: string
  parent_id: number | null
  member_count: number
}

export type ImportResult = {
  dry_run: boolean
  applied: boolean
  rows: number
  new_departments: string[]
  created_users: number
  updated_users: number
  errors: { line: number; message: string }[]
}

// 导入接口在有行错误时返回 422 + 结果体；这里统一解析成 ImportResult。
async function postImport(csv: string, dryRun: boolean): Promise<ImportResult> {
  const res = await fetch(
    `/api/v1/admin/departments/import${dryRun ? '?dry_run=1' : ''}`,
    {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'text/csv', 'X-CSRF-Token': csrfToken() },
      body: csv,
    }
  )
  if (res.ok || res.status === 422) return (await res.json()) as ImportResult
  let msg = res.statusText
  try {
    msg = ((await res.json()) as { message?: string }).message ?? msg
  } catch {
    /* 非 JSON */
  }
  throw new ApiError(res.status, msg)
}

export const deptApi = {
  list: () => api<{ items: Department[] }>('/admin/departments'),
  create: (name: string, parent_id: number | null) =>
    api<Department>('/admin/departments', json('POST', { name, parent_id })),
  rename: (id: number, name: string) =>
    api<Department>(`/admin/departments/${id}`, json('PATCH', { name })),
  remove: (id: number) =>
    api<void>(`/admin/departments/${id}`, { method: 'DELETE' }),
  importCsv: postImport,
}

export type RoleMatrix = {
  roles: {
    key: string
    name: string
    permissions: string[]
    dept_scoped: boolean
    privileged: boolean
  }[]
  permissions: string[]
  exclusive: string[][] | null
}

export const roleApi = {
  matrix: () => api<RoleMatrix>('/admin/roles'),
}
