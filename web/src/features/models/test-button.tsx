import { useMutation } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import type { TestResult } from '@/lib/api'
import { Button } from '@/components/ui/button'

// 「测试连接」：结果用 toast 展示；失败信息已由后端脱敏。
export function TestButton(props: { run: () => Promise<TestResult> }) {
  const m = useMutation({
    mutationFn: props.run,
    onSuccess: (r) =>
      r.ok
        ? toast.success(zh.models.testOK(r.latency_ms), {
            description: r.reply ?? r.models?.join(', '),
          })
        : toast.error(zh.models.testFail(r.error?.message ?? '')),
    onError: (e: Error) => toast.error(zh.models.testFail(e.message)),
  })
  return (
    <Button
      size='sm'
      variant='ghost'
      disabled={m.isPending}
      onClick={() => m.mutate()}
    >
      {m.isPending ? zh.models.testing : zh.models.test}
    </Button>
  )
}
