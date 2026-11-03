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
