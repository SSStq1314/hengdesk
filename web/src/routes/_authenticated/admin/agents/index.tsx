import { createFileRoute } from '@tanstack/react-router'
import { AgentAdminPage } from '@/features/agents'

export const Route = createFileRoute('/_authenticated/admin/agents/')({
  component: AgentAdminPage,
})
