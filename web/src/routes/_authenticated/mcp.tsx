import { createFileRoute } from '@tanstack/react-router'
import { McpPage } from '@/features/mcp'

export const Route = createFileRoute('/_authenticated/mcp')({
  component: McpPage,
})
