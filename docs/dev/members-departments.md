# 成员、部门与 CSV 导入（M1）

## 接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /api/v1/admin/users?q=&dept_id=&status=&limit=&before_id= | org.member.read | 按权限范围列成员；`dept_id` 包含下级部门；游标分页 |
| GET | /api/v1/admin/users/:id | org.member.read | 范围外 / 他组织 → 404 |
| POST | /api/v1/admin/users | org.member.manage | `{email, display_name, employee_no, department_ids, password?}`；不填密码则无本地密码 |
| PATCH | /api/v1/admin/users/:id | org.member.manage | 改姓名 / 工号 / 部门（整体替换）；特权目标需 assign_priv |
| GET | /api/v1/admin/departments | org.dept.read | 扁平列表 + 成员数；部门管理员只看到自己的子树 |
| POST | /api/v1/admin/departments | org.dept.manage | `{name, parent_id}`；同级重名 409；最多 10 级 |
| PATCH | /api/v1/admin/departments/:id | org.dept.manage | 只改名（移动部门待确认） |
| DELETE | /api/v1/admin/departments/:id | org.dept.manage | 仅空部门（无子部门、无成员 / 角色）；同时清理指向它的授权 |
| POST | /api/v1/admin/departments/import[?dry_run=1] | org.dept.manage（组织范围）+ org.member.manage | CSV 导入 |

部门归属用「成员」角色的 membership 表达：一人可属于多个部门，第一个为主部门。

## CSV 格式

```
dept_path,name,email,employee_no
研发部/后端组,张三,zhangsan@example.com,E001
```

- UTF-8（可带 BOM），请求体为 CSV 原文或 multipart 字段 `file`；≤ 4 MB、≤ 5000 行。
- `dept_path` 用 `/` 分隔层级，缺失的部门自动创建（闭包表同步维护）；`employee_no` 可空。
- 成员按邮箱（大小写不敏感）匹配：不存在则新建（无本地密码，需管理员重置密码或通过 SSO 登录），存在则更新姓名 / 工号并加入该部门，原有部门保留。
- 校验：邮箱格式、文件内邮箱 / 工号重复、邮箱属于其他组织、工号被其他成员占用、部门名非法或层级过深。**任何一行出错整批不写入**，返回 422 和逐行错误。
- 先 `dry_run=1` 预览（新建部门清单、新建 / 更新人数），再正式导入；正式导入前先写 `dept.import` 审计（fail-closed）。
- 重复导入同一文件是幂等的。
