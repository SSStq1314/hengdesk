import { createFileRoute } from '@tanstack/react-router'
import { ChatPage } from '@/features/chat'

export const Route = createFileRoute('/_authenticated/chat')({
  component: ChatPage,
})
