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
  tls: TLSInfo | null
  master_key?: MasterKeyStatus
}

// 主密钥备份确认（D-36）：configured 且未 acknowledged 时常驻提示。
export type MasterKeyStatus = {
  configured: boolean
  path: string
  fingerprint: string
  acknowledged: boolean
  acknowledged_at?: string
  acknowledged_by?: string
  warning: string
}

// 部署证书信息（D-34 第 3 点）。
export type TLSInfo = {
  present: boolean
  self_signed: boolean
  subject?: string
  sans?: string[]
  not_after?: string
  days_left: number
  expiring: boolean
  expired: boolean
  warn_days: number
}

export type HighRiskAlert = {
  id: number
  ts: string
  actor_email: string
  ip: string
  detail: {
    kind?: string
    severity?: string
    due_at?: string
    days_left?: number
  }
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
  tls: () => api<TLSInfo>('/admin/system/tls'),
  ackMasterKey: (fingerprint: string) =>
    api<SystemStatus>('/admin/system/master-key/ack', {
      method: 'POST',
      body: JSON.stringify({ fingerprint }),
    }),
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

// ---- 模型网关（M1 第二块 PR2）----
export type Provider = {
  id: number
  name: string
  kind: string
  dialect: string
  base_url: string
  has_api_key: boolean
  enabled: boolean
  timeout_seconds: number
  egress?: EgressHint
}
export type ProviderInput = Partial<{
  name: string
  kind: string
  dialect: string
  base_url: string
  api_key: string
  enabled: boolean
  timeout_seconds: number
}>
export type Model = {
  id: number
  name: string
  provider_id: number
  upstream_model: string
  thinking: string
  tool_choice_policy: string
  context_window: number
  max_output_tokens: number
  enabled: boolean
  is_default: boolean
  price_input_micros: number
  price_cached_micros: number
  price_output_micros: number
  currency: string
  // 备用模型（按顺序）：首字节前遇到连接错误 / 429 / 5xx 且重试用尽时依次切换
  fallback_model_ids: number[] | null
}
export type ModelInput = Partial<Omit<Model, 'id' | 'is_default'>>
export type ModelACL = {
  subject_type: 'user' | 'dept' | 'role'
  subject_id: number
  subject_name: string
  include_children: boolean
  perms: number
}
export type TestResult = {
  ok: boolean
  latency_ms: number
  models?: string[]
  reply?: string
  usage?: { total_tokens: number; estimated?: boolean }
  error?: { code: string; message: string }
}
export type UsageTotals = {
  calls: number
  error_calls: number
  prompt_tokens: number
  cached_tokens: number
  completion_tokens: number
  reasoning_tokens: number
  total_tokens: number
  cost_micros: number
  estimated_calls: number
  unknown_calls?: number
  estimated_tokens: number
  estimated_cost_micros: number
}
export type UsageGroup = 'user' | 'department' | 'model'
export type UsageSummary = {
  group_by: string
  items: (UsageTotals & { key: number; name: string })[]
  totals: UsageTotals
  currency: string
}
export type UsageRecord = {
  id: number
  user_id: number
  department_id?: number
  model_name: string
  purpose: string
  stream?: boolean
  status: string
  error_code?: string
  total_tokens?: number
  cost_micros?: number
  estimated?: boolean
  usage_unknown?: boolean
  latency_ms?: number
  created_at: string
}
export type UsageFilter = { from?: string; to?: string }

export const modelApi = {
  providers: () => api<{ items: Provider[] }>('/admin/model-providers'),
  createProvider: (p: ProviderInput) =>
    api<Provider>('/admin/model-providers', json('POST', p)),
  updateProvider: (id: number, p: ProviderInput) =>
    api<Provider>(`/admin/model-providers/${id}`, json('PATCH', p)),
  removeProvider: (id: number) =>
    api<void>(`/admin/model-providers/${id}`, { method: 'DELETE' }),
  testProvider: (id: number) =>
    api<TestResult>(`/admin/model-providers/${id}/test`, { method: 'POST' }),
  models: () => api<{ items: Model[] }>('/admin/models'),
  createModel: (m: ModelInput) => api<Model>('/admin/models', json('POST', m)),
  updateModel: (id: number, m: ModelInput) =>
    api<Model>(`/admin/models/${id}`, json('PATCH', m)),
  removeModel: (id: number) =>
    api<void>(`/admin/models/${id}`, { method: 'DELETE' }),
  setDefault: (id: number) =>
    api<Model>(`/admin/models/${id}/default`, { method: 'POST' }),
  testModel: (id: number) =>
    api<TestResult>(`/admin/models/${id}/test`, { method: 'POST' }),
  acl: (id: number) => api<{ items: ModelACL[] }>(`/admin/models/${id}/acl`),
  grant: (
    id: number,
    a: { subject_type: string; subject_id: number; include_children: boolean }
  ) => api<unknown>(`/admin/models/${id}/acl`, json('POST', a)),
  revoke: (id: number, type: string, sid: number) =>
    api<unknown>(`/admin/models/${id}/acl/${type}/${sid}`, {
      method: 'DELETE',
    }),
  usageSummary: (group_by: UsageGroup, f: UsageFilter) =>
    api<UsageSummary>(`/admin/usage/summary${qs({ group_by, ...f })}`),
  usageRecords: (f: UsageFilter) =>
    api<{ items: UsageRecord[] }>(`/admin/usage/records${qs(f)}`),
}

// ---- 预算与配额（M1 第二块 PR3） ----
export type BudgetScope = 'org' | 'dept' | 'user' | 'model'

export type Budget = {
  id: number
  scope: BudgetScope
  scope_id: number
  scope_name: string
  period: 'day' | 'month'
  metric: 'tokens' | 'cost'
  limit: number
  threshold_pct: number
  action: 'block' | 'alert_only'
  enabled: boolean
  note: string
  used: number
  pct: number
  period_key: string
  reset_at: string
  counter_unavailable?: boolean
  updated_at: string
}

export type BudgetInput = Pick<
  Budget,
  | 'scope'
  | 'scope_id'
  | 'period'
  | 'metric'
  | 'limit'
  | 'threshold_pct'
  | 'action'
  | 'enabled'
  | 'note'
>

export type BudgetRecon = {
  budget_id: number
  period: string
  counter: number | null
  db: number
  drift: number
}

export const budgetApi = {
  list: () => api<Budget[]>('/admin/budgets'),
  create: (b: BudgetInput) => api<Budget>('/admin/budgets', json('POST', b)),
  update: (id: number, b: BudgetInput) =>
    api<Budget>(`/admin/budgets/${id}`, json('PUT', b)),
  remove: (id: number) =>
    api<void>(`/admin/budgets/${id}`, { method: 'DELETE' }),
  reconcile: () =>
    api<BudgetRecon[]>('/admin/budgets/reconcile', { method: 'POST' }),
  mine: () => api<Budget[]>('/budgets/me'),
}

export type AppNotification = {
  id: number
  kind: string
  title: string
  read: boolean
  created_at: string
  detail?: {
    scope?: BudgetScope
    scope_id?: number
    scope_name?: string
    period?: string
    metric?: 'tokens' | 'cost'
    limit?: number
    used?: number
    level?: number
    action?: string
    budget_id?: number
  }
}

// 站内提醒（只含发给自己的）。
export const notificationApi = {
  list: () =>
    api<{ unread: number; items: AppNotification[] }>('/notifications'),
  markRead: () => api<void>('/notifications/read', { method: 'POST' }),
}

// ---- 出站控制（M1 第二块 PR4，AC-09）----
export type EgressHint = { allowed: boolean; reason?: string; message?: string }
export type EgressKind = 'host' | 'wildcard' | 'ip' | 'cidr'
export type EgressRule = {
  id: number
  kind: EgressKind
  value: string
  ports: string
  note: string
  source: string
  private: boolean
  created_at: string
  updated_at: string
}
export type EgressRuleInput = Pick<
  EgressRule,
  'kind' | 'value' | 'ports' | 'note'
>
export type EgressLog = {
  id: number
  ts: string
  actor_email: string
  module: string
  method: string
  scheme: string
  host: string
  port: number
  path: string
  ip: string
  decision: 'allow' | 'deny'
  reason: string
  reason_text?: string
  status: number
  duration_ms: number
  bytes_out: number
  bytes_in: number
  error: string
}
export type EgressLogFilter = {
  decision?: string
  host?: string
  module?: string
  reason?: string
  from?: string
  to?: string
  before_id?: number
}
export const egressApi = {
  rules: () => api<{ items: EgressRule[] }>('/admin/egress/rules'),
  create: (r: EgressRuleInput) =>
    api<EgressRule>('/admin/egress/rules', json('POST', r)),
  update: (id: number, r: EgressRuleInput) =>
    api<EgressRule>(`/admin/egress/rules/${id}`, json('PATCH', r)),
  remove: (id: number) =>
    api<void>(`/admin/egress/rules/${id}`, { method: 'DELETE' }),
  check: (url: string) =>
    api<EgressHint>('/admin/egress/check', json('POST', { url })),
  logs: (f: EgressLogFilter) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(f))
      if (v !== undefined && v !== '') q.set(k, String(v))
    return api<{
      items: EgressLog[]
      has_more: boolean
      next_before_id?: number
    }>(`/admin/egress/logs?${q}`)
  },
}
