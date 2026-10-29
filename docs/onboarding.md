# 同事自助接入指南

> 面向第一次使用本系统的同事：怎么自己注册一个账号、登录，以及注册之后该找谁做什么。
> 文中 `https://your-deployment.example.com` 是占位地址，请换成实际部署地址。

## 1. 注册（成为新工作区的 owner）

**注册 = 开一个全新的工作区，注册者自动成为这个工作区的 owner。**
每个工作区有自己独立的智能体、成员和会话数据，互不可见。

### 前提：服务端开放了自助注册

注册接口只在服务端注册模式为自助（`self_serve`，默认值）时可用。
如果管理员把模式改成了 `invite_only`（或设置了环境变量 `DISABLE_REGISTRATION=true`），
接口会直接返回 `403 Public registration is disabled`——这时请联系管理员开通，或走邀请链接。

### 接口与字段约束

- 接口：`POST /api/v1/auth/register`
- 请求体（JSON）：

| 字段 | 类型 | 约束 |
|---|---|---|
| `username` | string | 必填，2-50 个字符，全系统唯一 |
| `email` | string | 必填，合法邮箱格式，全系统唯一 |
| `password` | string | 必填，**8-32 位，必须同时包含字母和数字**；若服务端开启了复杂密码策略，还需同时含大写字母、小写字母和特殊字符 |

> 提示：注册后**登录用的是邮箱**，不是 username；用户名只是显示名。
> 邮箱或用户名已被注册过时，接口返回 400，换一个即可。

### curl 示例

```bash
curl -X POST "https://your-deployment.example.com/api/v1/auth/register" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "zhangsan",
    "email": "zhangsan@example.com",
    "password": "Abc12345"
  }'
```

成功响应（HTTP 201）：

```json
{
  "success": true,
  "message": "Registration successful",
  "user": {
    "id": "0b9e6f21-5c4a-4d7e-9f01-23a4b5c6d7e8",
    "username": "zhangsan",
    "email": "zhangsan@example.com",
    "tenant_id": 10007,
    "is_active": true
  }
}
```

`user.tenant_id` 就是刚建好的工作区 ID。响应里不返回密码相关字段。

> 网页说明：当前网页登录页没有放注册按钮，同事请直接用上面的接口（或让管理员代建账号）。

## 2. 登录

- 接口：`POST /api/v1/auth/login`
- 请求体：`{"email": "zhangsan@example.com", "password": "Abc12345"}`（**邮箱 + 密码**）

curl 示例：

```bash
curl -X POST "https://your-deployment.example.com/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"email": "zhangsan@example.com", "password": "Abc12345"}'
```

成功响应（HTTP 200）关键部分：

```json
{
  "success": true,
  "user": { "username": "zhangsan", "tenant_id": 10007 },
  "active_tenant": { "id": 10007 },
  "memberships": [],
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIs..."
}
```

- `token`：访问令牌，后续请求放在 `Authorization: Bearer <token>` 头里；
- `refresh_token`：刷新令牌，`token` 过期后用它换新的；
- `memberships`：你能进入的所有工作区及各自角色（新注册的账号这里就是自己的工作区 + owner）。

密码错误或账号不存在时返回 401。

## 3. 注册之后的路

新工作区一开始什么都没有，按下面三步走完就能用了：

1. **owner 建智能体**：owner 登录网页，在「智能体」页创建智能体（接口 `POST /api/v1/agents`，
   请求字段较多，建议直接在网页上建，或看 swagger）。建好后打开智能体详情，能看到它的
   **「智能体 ID」**（详情页有「智能体 ID」一栏，API 集成用的就是它）——把这个 ID 发给要用的同事。
2. **owner/admin 建游客账号**：在工作区「成员管理」里直接创建新账号并选角色「游客（viewer）」，
   表单里填的初始密码由创建者线下转交对方（接口 `POST /api/v1/tenants/{工作区ID}/members/create`）。
3. **游客绑定产线**：游客用收到的账号登录，进「设置 → 产线」，把 owner 给的 agent id 输进去点绑定。
   每绑一个智能体就多一条产线（上限 20 个），绑定关系跟人走，换设备也在。

## 4. 完整接口清单

本文只覆盖注册、登录和接入流程的快速入口；全部接口以部署机上的
**`https://your-deployment.example.com/swagger/index.html`** 为准（源码仓库 `docs/swagger.json`）。
