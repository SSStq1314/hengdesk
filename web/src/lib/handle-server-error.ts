import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { ApiError } from '@/lib/api'

export function handleServerError(error: unknown) {
  if (import.meta.env.DEV) {
    // eslint-disable-next-line no-console
    console.log(error)
  }
  toast.error(error instanceof ApiError ? error.message : zh.common.opFailed)
}
