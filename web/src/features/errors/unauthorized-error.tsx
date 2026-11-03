import { useNavigate, useRouter } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { Button } from '@/components/ui/button'

export function UnauthorisedError() {
  const navigate = useNavigate()
  const { history } = useRouter()
  return (
    <div className='h-svh'>
      <div className='m-auto flex h-full w-full flex-col items-center justify-center gap-2'>
        <h1 className='text-[7rem] leading-tight font-bold'>401</h1>
        <span className='font-medium'>{zh.errors.unauthorizedTitle}</span>
        <p className='text-center text-muted-foreground'>
          {zh.errors.unauthorizedDesc}
        </p>
        <div className='mt-6 flex gap-4'>
          <Button variant='outline' onClick={() => history.go(-1)}>
            {zh.common.goBack}
          </Button>
          <Button onClick={() => navigate({ to: '/' })}>
            {zh.common.goHome}
          </Button>
        </div>
      </div>
    </div>
  )
}
