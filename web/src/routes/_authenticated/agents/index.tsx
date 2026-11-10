import { createFileRoute } from '@tanstack/react-router'
import { AgentPlazaPage } from '@/features/agents'

export const Route = createFileRoute('/_authenticated/agents/')({
  component: AgentPlazaPage,
})
