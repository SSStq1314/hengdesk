import { createFileRoute } from '@tanstack/react-router'
import { RetentionPage } from '@/features/retention'

export const Route = createFileRoute('/_authenticated/retention')({
  component: RetentionPage,
})
