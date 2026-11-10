import { createFileRoute } from '@tanstack/react-router'
import { AigcSettingsPage } from '@/features/aigc'

export const Route = createFileRoute('/_authenticated/aigc')({
  component: AigcSettingsPage,
})
