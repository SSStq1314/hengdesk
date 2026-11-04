import { createFileRoute } from '@tanstack/react-router'
import { KBDetailPage } from '@/features/kb/detail'

export const Route = createFileRoute('/_authenticated/kbs/$id')({
  component: function KBDetailRoute() {
    const { id } = Route.useParams()
    return <KBDetailPage id={Number(id)} />
  },
})
