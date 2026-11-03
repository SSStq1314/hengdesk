import { useEffect, useState } from 'react'
import { z } from 'zod'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useNavigate } from '@tanstack/react-router'
import { KeyRound, Loader2, LogIn } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import { ApiError, authApi, type OIDCConfig } from '@/lib/api'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { PasswordInput } from '@/components/password-input'

const formSchema = z.object({
  email: z.email({ error: '请输入有效的邮箱' }),
  password: z.string().min(1, '请输入密码'),
})

interface UserAuthFormProps extends React.HTMLAttributes<HTMLFormElement> {
  redirectTo?: string
}

export function UserAuthForm({
  className,
  redirectTo,
  ...props
}: UserAuthFormProps) {
  const [isLoading, setIsLoading] = useState(false)
  const navigate = useNavigate()
  const { auth } = useAuthStore()
  const [sso, setSso] = useState<OIDCConfig>({ enabled: false })

  useEffect(() => {
    authApi
      .oidcConfig()
      .then(setSso)
      .catch(() => setSso({ enabled: false }))
    if (new URLSearchParams(window.location.search).get('error') === 'sso') {
      toast.error('单点登录失败：账号未开通或已停用，请联系管理员')
    }
  }, [])

  const form = useForm<z.infer<typeof formSchema>>({
    resolver: zodResolver(formSchema),
    defaultValues: { email: '', password: '' },
  })

  async function onSubmit(data: z.infer<typeof formSchema>) {
    setIsLoading(true)
    try {
      const me = await authApi.login(data.email, data.password)
      auth.setUser(me)
      toast.success(`欢迎回来，${me.display_name || me.email}`)
      // 只允许站内相对路径跳转，避免开放重定向
      const target =
        redirectTo && redirectTo.startsWith('/') && !redirectTo.startsWith('//')
          ? redirectTo
          : '/'
      navigate({ to: target, replace: true })
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : '登录失败，请稍后重试')
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit(onSubmit)}
        className={cn('grid gap-3', className)}
        {...props}
      >
        <FormField
          control={form.control}
          name='email'
          render={({ field }) => (
            <FormItem>
              <FormLabel>邮箱</FormLabel>
              <FormControl>
                <Input
                  placeholder='name@example.com'
                  autoComplete='username'
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='password'
          render={({ field }) => (
            <FormItem>
              <FormLabel>密码</FormLabel>
              <FormControl>
                <PasswordInput
                  placeholder='********'
                  autoComplete='current-password'
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <Button className='mt-2' disabled={isLoading}>
          {isLoading ? <Loader2 className='animate-spin' /> : <LogIn />}
          登录
        </Button>
        {sso.enabled && (
          <Button
            type='button'
            variant='outline'
            onClick={() => {
              window.location.href = '/api/v1/auth/oidc/login'
            }}
          >
            <KeyRound />
            使用{sso.display_name || '企业单点登录'}
          </Button>
        )}
      </form>
    </Form>
  )
}
