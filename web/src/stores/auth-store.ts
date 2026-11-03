import { create } from 'zustand'
import { type Me } from '@/lib/api'

interface AuthState {
  auth: {
    user: Me | null
    setUser: (user: Me | null) => void
    reset: () => void
  }
}

// 会话 token 存于后端下发的 HttpOnly cookie，前端只缓存当前用户信息。
export const useAuthStore = create<AuthState>()((set) => ({
  auth: {
    user: null,
    setUser: (user) => set((s) => ({ ...s, auth: { ...s.auth, user } })),
    reset: () => set((s) => ({ ...s, auth: { ...s.auth, user: null } })),
  },
}))
