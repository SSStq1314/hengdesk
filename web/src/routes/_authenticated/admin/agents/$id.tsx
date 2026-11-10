import { createFileRoute } from '@tanstack/react-router'
import { AgentDetailPage } from '@/features/agents'

export const Route = createFileRoute('/_authenticated/admin/agents/$id')({
  component: AgentDetailPage,
})
