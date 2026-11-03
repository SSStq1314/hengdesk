import { toast } from 'sonner'
import { ApiError } from '@/lib/api'

export function handleServerError(error: unknown) {
  if (import.meta.env.DEV) {
    // eslint-disable-next-line no-console
    console.log(error)
  }
  toast.error(
    error instanceof ApiError ? error.message : '操作失败，请稍后重试'
  )
}
