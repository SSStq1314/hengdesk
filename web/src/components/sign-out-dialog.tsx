import { useNavigate } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { useAuthStore } from '@/stores/auth-store'
import { authApi } from '@/lib/api'
import { ConfirmDialog } from '@/components/confirm-dialog'

interface SignOutDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function SignOutDialog({ open, onOpenChange }: SignOutDialogProps) {
  const navigate = useNavigate()
  const { auth } = useAuthStore()

  const handleSignOut = async () => {
    try {
      await authApi.logout()
    } finally {
      auth.reset()
      navigate({ to: '/sign-in', replace: true })
    }
  }

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={zh.common.signOut}
      desc={zh.signOut.desc}
      confirmText={zh.signOut.confirm}
      cancelBtnText={zh.common.cancel}
      destructive
      handleConfirm={handleSignOut}
      className='sm:max-w-sm'
    />
  )
}
