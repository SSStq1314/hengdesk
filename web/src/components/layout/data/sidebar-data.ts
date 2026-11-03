import {
  Building2,
  LayoutDashboard,
  ScrollText,
  ShieldCheck,
  Users,
} from 'lucide-react'
import { type SidebarData } from '../types'

// 侧边栏导航。M0 仅保留「工作台」，后续模块（对话、知识库、智能体、审计中心…）按里程碑加入。
export const sidebarData: SidebarData = {
  navGroups: [
    {
      title: '通用',
      items: [{ title: '工作台', url: '/', icon: LayoutDashboard }],
    },
    {
      title: '组织',
      items: [
        {
          title: '成员',
          url: '/members',
          icon: Users,
          perm: 'org.member.read',
        },
        {
          title: '部门',
          url: '/departments',
          icon: Building2,
          perm: 'org.dept.read',
        },
        {
          title: '角色与权限',
          url: '/roles',
          icon: ShieldCheck,
          perm: 'org.role.read',
        },
      ],
    },
    {
      title: '安全',
      items: [
        {
          title: '审计日志',
          url: '/audit',
          icon: ScrollText,
          perm: 'audit.read',
        },
      ],
    },
  ],
}
