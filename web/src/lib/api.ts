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
  trace_id?: string
  /** D-41：受信代理传入的上游 trace-id，仅作关联 */
  upstream_trace_id?: string
  hash: string
  detail?: Record<string, unknown>
}

export interface AuditFilter {
  actor?: string
  action?: string
  outcome?: string
  trace_id?: string
  before_id?: number
}

export type AuditTrace = {
  trace_id: string
  start: string
  end: string
  duration_ms: number
  count: number
  truncated: boolean
  items: (AuditLog & { offset_ms: number })[]
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
  trace: (id: string) =>
    api<AuditTrace>(`/admin/audit-logs/traces/${encodeURIComponent(id)}`),
}

// 对话保留期与法律保全（D-40 第 2 条）。
export type RetentionSettings = { enabled: boolean; days: number }
export type RetentionResult = {
  cutoff: string
  oldest_at?: string
  newest_at?: string
  conversations: number
  messages: number
  files: number
  held_orgs: number
  held_users: number
  object_errors: number
}
export type LegalHold = {
  id: number
  scope: 'org' | 'user'
  user_id?: number
  user_email?: string
  reason: string
  created_by: number
  created_at: string
  released_at?: string
}
export const retentionApi = {
  get: () =>
    api<{
      settings: RetentionSettings
      default_days: number
      min_days: number
    }>('/admin/chat-retention'),
  save: (v: RetentionSettings) =>
    api<{ settings: RetentionSettings }>('/admin/chat-retention', {
      method: 'PUT',
      body: JSON.stringify(v),
    }),
  run: () =>
    api<RetentionResult>('/admin/chat-retention/run', { method: 'POST' }),
  holds: () => api<{ items: LegalHold[] }>('/admin/legal-holds'),
  createHold: (v: { user_email?: string; reason: string }) =>
    api<LegalHold>('/admin/legal-holds', {
      method: 'POST',
      body: JSON.stringify(v),
    }),
  releaseHold: (id: number) =>
    api<unknown>(`/admin/legal-holds/${id}`, { method: 'DELETE' }),
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
  kb_index?: KBIndexStatus | null
}

// 向量索引体积（D-42）：超过内存一半时提示检索延迟可能明显上升。
export type KBIndexStatus = {
  index_bytes: number
  memory_bytes: number
  over_half: boolean
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
    user_email?: string
    hold_id?: number
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
  fallback_acl_mismatch?: number[] | null
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
  any_port: boolean
  needs_review: boolean
  reviewed_at?: string
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
export type EgressBlocked = {
  host: string
  port: number
  reason: string
  reason_text: string
  count: number
  last_ts: string
  last_module: string
}

export const egressApi = {
  blocked: (days = 7) =>
    api<{ items: EgressBlocked[]; days: number }>(
      `/admin/egress/blocked?days=${days}`
    ),
  rules: () =>
    api<{ items: EgressRule[]; pending_review: number; default_port: number }>(
      '/admin/egress/rules'
    ),
  review: (id: number) =>
    api<EgressRule>(`/admin/egress/rules/${id}/review`, { method: 'POST' }),
  exportUrl: (f: EgressLogFilter) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(f))
      if (k !== 'before_id' && v !== undefined && v !== '') q.set(k, String(v))
    return `/api/v1/admin/egress/logs/export?${q}`
  },
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

// ---- 对话（M1 第三块 PR1）----
export type Conversation = {
  id: number
  title: string
  model_id?: number | null
  kb_ids?: number[]
  created_at: string
  updated_at: string
}
export type ChatMessage = {
  id: number
  role: 'user' | 'assistant'
  content: string
  status: 'streaming' | 'done' | 'stopped' | 'error'
  model_name?: string
  fallback_from?: string
  error_code?: string
  error?: string
  prompt_tokens: number
  completion_tokens: number
  created_at: string
  context_truncated?: boolean
  history_truncated?: boolean
  /** D-41：上游未返回 usage，token 为估算值 */
  usage_estimated?: boolean
  notice?: string
  // 知识库引用（第四块 PR3）；tool_calls 为第五块预留，当前始终不返回。
  citations?: ChatCitation[]
  tool_calls?: ChatToolCall[]
}
// 对话 SSE 扩展事件：citation 自第四块 PR3 起生效（在 token 之前逐条发出），
// tool_call / approval_pending 留给 MCP 与审批。解析器遇到不认识的事件一律忽略。
export const CHAT_RESERVED_EVENTS = [
  'citation',
  'tool_call',
  'approval_pending',
] as const
export type ChatCitation = {
  index: number
  kb_id: number
  kb_name: string
  file_id: number
  file_name: string
  seq: number
  start: number
  end: number
  heading?: string
  snippet: string
  score: number
}
export type ChatToolCallStatus =
  | 'pending'
  | 'approval'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'rejected'
export type ChatToolCall = {
  id: string
  tool: string
  server_id?: number
  status: ChatToolCallStatus
  args_summary?: string
  result_summary?: string
  error_code?: string
  approval_id?: number
}
export type ChatApprovalPending = {
  approval_id: number
  tool_call_id: string
  tool: string
  reason?: string
}
export type ConvFile = {
  id: number
  name: string
  kind: string
  size: number
  chars: number
  truncated: boolean
  created_at: string
}
export type MyModel = { id: number; name: string; is_default: boolean }

export const chatApi = {
  models: () => api<{ items: MyModel[] }>('/models'),
  list: () => api<{ items: Conversation[] }>('/conversations'),
  create: (b: { title?: string; model?: string; kb_ids?: number[] }) =>
    api<Conversation>('/conversations', json('POST', b)),
  get: (id: number) =>
    api<Conversation & { messages: ChatMessage[]; files: ConvFile[] }>(
      `/conversations/${id}`
    ),
  // 上传用 multipart（不能带 JSON 的 Content-Type，由浏览器生成 boundary）。
  upload: async (id: number, file: File): Promise<ConvFile> => {
    const fd = new FormData()
    fd.append('file', file)
    const res = await fetch(`/api/v1/conversations/${id}/files`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-CSRF-Token': csrfToken() },
      body: fd,
    })
    if (!res.ok) {
      let msg = res.statusText
      try {
        const b = (await res.json()) as { message?: string }
        if (b.message) msg = b.message
      } catch {
        /* 非 JSON 响应 */
      }
      throw new ApiError(res.status, msg)
    }
    return (await res.json()) as ConvFile
  },
  removeFile: (id: number, fid: number) =>
    api<void>(`/conversations/${id}/files/${fid}`, { method: 'DELETE' }),
  // 下载走后端代理（鉴权 + 审计），不暴露对象存储地址。
  fileURL: (id: number, fid: number) =>
    `/api/v1/conversations/${id}/files/${fid}/content`,
  rename: (id: number, title: string) =>
    api<Conversation>(`/conversations/${id}`, json('PATCH', { title })),
  remove: (id: number) =>
    api<void>(`/conversations/${id}`, { method: 'DELETE' }),
  setKBs: (id: number, kb_ids: number[]) =>
    api<Conversation>(`/conversations/${id}`, json('PATCH', { kb_ids })),
}

export type StreamHandlers = {
  onToken: (t: string) => void
  onCitation?: (c: ChatCitation) => void
  onDone: (m: ChatMessage) => void
  onError: (code: string, message: string) => void
}

// sendMessage 以 fetch 读取 SSE（EventSource 不支持 POST）。signal 中断即「停止生成」，
// 服务端会把已生成部分保存为 stopped。
export function sendMessage(
  id: number,
  body: { content: string; model?: string },
  h: StreamHandlers,
  signal: AbortSignal
): Promise<void> {
  return streamPost(`/api/v1/conversations/${id}/messages`, body, h, signal)
}

// regenerateMessage 重新生成 / 重试最后一条回复（D-40 第 6 条），事件与 sendMessage 相同。
export function regenerateMessage(
  id: number,
  messageId: number,
  h: StreamHandlers,
  signal: AbortSignal
): Promise<void> {
  return streamPost(
    `/api/v1/conversations/${id}/regenerate`,
    { message_id: messageId },
    h,
    signal
  )
}

async function streamPost(
  url: string,
  body: unknown,
  h: StreamHandlers,
  signal: AbortSignal
): Promise<void> {
  const res = await fetch(url, {
    method: 'POST',
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      'X-CSRF-Token': csrfToken(),
    },
    body: JSON.stringify(body),
    signal,
  })
  if (!res.ok || !res.body) {
    let code = String(res.status)
    let msg = res.statusText
    try {
      const b = (await res.json()) as {
        message?: string
        error?: { code?: string; message?: string }
      }
      code = b.error?.code ?? code
      msg = b.error?.message ?? b.message ?? msg
    } catch {
      /* 非 JSON 响应 */
    }
    h.onError(code, msg)
    return
  }
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    let i: number
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const raw = buf.slice(0, i)
      buf = buf.slice(i + 2)
      let event = 'message'
      let data = ''
      for (const line of raw.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data += line.slice(5).trim()
      }
      if (!data) continue
      let p: unknown
      try {
        p = JSON.parse(data)
      } catch {
        continue
      }
      if (event === 'token') {
        h.onToken((p as { content?: string }).content ?? '')
      } else if (event === 'citation') {
        h.onCitation?.(p as ChatCitation)
      } else if (event === 'done') {
        h.onDone(p as ChatMessage)
      } else if (event === 'error') {
        const e = (p as { error?: { code?: string; message?: string } }).error
        h.onError(e?.code ?? 'error', e?.message ?? '')
      }
      // 其余事件（含预留的 tool_call / approval_pending）暂时忽略。
    }
  }
}

// ---- 知识库（M1 第四块）----
export type KBPerm = 'view' | 'use' | 'edit' | 'manage'
export type KB = {
  id: number
  name: string
  description: string
  embedding_model: string
  chunk_size: number
  chunk_overlap: number
  perms: KBPerm[]
  file_count: number
  created_at: string
  updated_at: string
}
export type KBFileStatus =
  | 'pending'
  | 'parsing'
  | 'indexing'
  | 'ready'
  | 'failed'
export type KBFile = {
  id: number
  kb_id: number
  name: string
  kind: string
  size: number
  sha256: string
  status: KBFileStatus
  error_code?: string
  error?: string
  chars: number
  chunk_count: number
  acl_custom: boolean
  embedding_model?: string
  attempts: number
  indexed_at?: string
  created_at: string
}
export type KBACLRow = {
  subject_type: 'user' | 'dept' | 'role'
  subject_id: number
  subject_name: string
  include_children: boolean
  perms: KBPerm[]
}
export type KBACLInput = {
  subject_type: 'user' | 'dept' | 'role'
  subject_id: number
  include_children?: boolean
  perm: KBPerm
}
export type KBInput = {
  name?: string
  description?: string
  embedding_model?: string
  chunk_size?: number
  chunk_overlap?: number
}

export const kbApi = {
  list: () => api<{ items: KB[]; can_create: boolean }>('/kbs'),
  get: (id: number) => api<KB>(`/kbs/${id}`),
  create: (b: KBInput) => api<KB>('/kbs', json('POST', b)),
  update: (id: number, b: KBInput) => api<KB>(`/kbs/${id}`, json('PATCH', b)),
  remove: (id: number) => api<void>(`/kbs/${id}`, { method: 'DELETE' }),
  acl: (id: number) => api<{ items: KBACLRow[] }>(`/kbs/${id}/acl`),
  grant: (id: number, b: KBACLInput) =>
    api<void>(`/kbs/${id}/acl`, json('POST', b)),
  revoke: (id: number, t: string, sid: number) =>
    api<void>(`/kbs/${id}/acl/${t}/${sid}`, { method: 'DELETE' }),
  files: (id: number) => api<{ items: KBFile[] }>(`/kbs/${id}/files`),
  upload: async (
    id: number,
    file: File
  ): Promise<{ duplicate?: boolean; message?: string; file: KBFile }> => {
    const fd = new FormData()
    fd.append('file', file)
    const res = await fetch(`/api/v1/kbs/${id}/files`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-CSRF-Token': csrfToken() },
      body: fd,
    })
    if (!res.ok) {
      let msg = res.statusText
      try {
        const b = (await res.json()) as { message?: string }
        if (b.message) msg = b.message
      } catch {
        /* 非 JSON 响应 */
      }
      throw new ApiError(res.status, msg)
    }
    return await res.json()
  },
  removeFile: (id: number, fid: number) =>
    api<void>(`/kbs/${id}/files/${fid}`, { method: 'DELETE' }),
  retryFile: (id: number, fid: number) =>
    api<KBFile>(`/kbs/${id}/files/${fid}/retry`, { method: 'POST' }),
  fileACL: (id: number, fid: number) =>
    api<{ items: KBACLRow[] }>(`/kbs/${id}/files/${fid}/acl`),
  grantFile: (id: number, fid: number, b: KBACLInput) =>
    api<void>(`/kbs/${id}/files/${fid}/acl`, json('POST', b)),
  revokeFile: (id: number, fid: number, t: string, sid: number) =>
    api<void>(`/kbs/${id}/files/${fid}/acl/${t}/${sid}`, {
      method: 'DELETE',
    }),
  setACLMode: (id: number, fid: number, custom: boolean) =>
    api<KBFile>(`/kbs/${id}/files/${fid}/acl-mode`, json('PUT', { custom })),
}
