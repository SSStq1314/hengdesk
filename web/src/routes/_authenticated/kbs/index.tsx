import { createFileRoute } from '@tanstack/react-router'
import { KBListPage } from '@/features/kb'

export const Route = createFileRoute('/_authenticated/kbs/')({
  component: KBListPage,
})
