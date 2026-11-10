import { createFileRoute } from '@tanstack/react-router'
import { MeUsagePage } from '@/features/me-usage'

export const Route = createFileRoute('/_authenticated/me/usage')({
  component: MeUsagePage,
})
