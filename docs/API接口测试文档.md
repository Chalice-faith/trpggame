# TRPG Game API 接口测试文档（Swagger 风格）

> 版本：Phase 1 / M1.5 + Phase 2 / M2.5-F（多人房间、跨实例实时、V2 运行态、行动/计时器、生命周期与 Vue 多人游戏均已落地）
>
> 契约来源：当前 Go、Python 和 Vue 代码（2026-09-24 核对）。
>
> 状态：接口字段、错误码和手工验收项已整理至 M2.5-F；提交 `500b8ab` 的 CI #54 与确定性 AI fixture 边界内的三账号浏览器验收通过，目标环境验收见 [M2.5 主分支合并与真实验收清单](./M2.5主分支合并与真实验收清单.md)。

这份文档用于在 Swagger UI、Postman 或 `curl` 中手工验收。所有示例均使用 JSON 字段名，不使用 Go/Python 内部字段名。

机器可读的公共 Go REST 规范见：[OpenAPI 3.0.3](../go-backend/internal/openapi/openapi.yaml)。服务启动后可访问 `${GO_BASE_URL}/api/docs/index.html` 使用 Swagger UI，原始规范地址为 `${GO_BASE_URL}/api/openapi.yaml`。

## 1. 测试环境

### 1.1 地址变量

默认 Docker Compose 端口如下；部署到其他地址时只替换变量。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `GO_BASE_URL` | `http://127.0.0.1:8080` | Go REST API |
| `GO_WS_URL` | `ws://127.0.0.1:8080/ws` | Go 游戏 WebSocket |
| `IM_WS_URL` | `ws://127.0.0.1:8080/ws/im` | Phase 2 IM WebSocket |
| `AI_BASE_URL` | `http://127.0.0.1:8000` | Python AI 内部 API |

PowerShell 初始化：

```powershell
$GO_BASE_URL = "http://127.0.0.1:8080"
$GO_WS_URL = "ws://127.0.0.1:8080/ws"
$IM_WS_URL = "ws://127.0.0.1:8080/ws/im"
$AI_BASE_URL = "http://127.0.0.1:8000"
$INTERNAL_SECRET = "<与 INTERNAL_SHARED_SECRET 相同的值>"
$ACCESS_TOKEN = "<登录返回的 access_token>"
$ACCESS_TOKEN_B = "<第二个账号的 access_token>"
$REFRESH_TOKEN = "<登录返回的 refresh_token>"
$USER_ID_B = "<第二个账号的 user_id>"
```

### 1.2 通用响应格式（Go）

成功响应统一为：

```json
{"code":0,"message":"ok","data":{}}
```

异步受理接口可能返回 `message: "accepted"`。失败响应统一为：

```json
{"code": 1300, "message": "invalid game request"}
```

`code=0` 才表示业务成功；HTTP 状态码仍需同时校验。

### 1.3 鉴权

- Go 公开接口：注册、登录、刷新 Token 不需要鉴权。
- Go 用户、剧本和游戏接口：请求头 `Authorization: Bearer <access_token>`。
- Go WebSocket：浏览器不能可靠地设置 Authorization 请求头，使用查询参数 `?token=<access_token>&room_id=<room_id>`。
- Phase 2 IM WebSocket：使用查询参数 `?token=<access_token>`，不包含 `room_id`。
- Python AI 和 Go 内部回调：请求头 `X-Internal-Secret: <INTERNAL_SHARED_SECRET>`，仅服务间调用，不能暴露给浏览器。

### 1.4 常见鉴权错误

| HTTP | code | message | 触发条件 |
| --- | ---: | --- | --- |
| 401 | 1001 | `Missing authorization header` | Go REST 缺少 Authorization |
| 401 | 1001 | `Invalid authorization format, expected: Bearer <token>` | Go REST 请求头格式错误 |
| 401 | 1002 | `Invalid or expired token` | Go REST Token 无效或过期 |

## 2. 推荐端到端测试顺序

1. `POST /api/v1/auth/register` 创建测试用户。
2. `POST /api/v1/auth/login` 保存 `access_token` 和 `refresh_token`。
3. 再创建账号 B，用账号 A 搜索 B、发送申请，使用 B 接受申请。
4. 两个账号分别连接 `/ws/im`，验证好友关系与 online/offline 实时事件。
5. `POST /api/v1/scripts/upload` 上传一个真实 PDF，记录 `script_id`。
6. 轮询 `GET /api/v1/scripts/{id}`，直到 `status=ready`，并记录一个 `character.id`。
7. `POST /api/v1/games/solo/start` 创建单人房间，记录 `room_id`。
8. 连接 `GET /ws?token=...&room_id=...`，先验证 `subscribed`，再发送 `game_action`。
9. 验证行动事件顺序、存档、读档、暂停、恢复和结束。

仅验证鉴权或参数校验时，可以跳过真实 PDF、AI 和 Redis。

## 3. Go REST API

基础路径：`${GO_BASE_URL}/api/v1`。

### 3.1 用户与认证

#### POST `/api/v1/auth/register` — 注册

请求体：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `username` | string | 是 | 3–50 个字符 |
| `email` | string | 是 | 合法邮箱 |
| `password` | string | 是 | 6–100 个字符 |
| `nickname` | string | 否 | 昵称 |

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/auth/register" `
  -H "Content-Type: application/json" `
  --data-raw '{"username":"tester01","email":"tester01@example.com","password":"secret123","nickname":"测试员"}'
```

成功：`200`，`data` 为 `{user_id, username, access_token, refresh_token}`。

主要失败：`400/1000` 参数无效；`409/1100` 用户名或邮箱已存在。

#### POST `/api/v1/auth/login` — 登录

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/auth/login" `
  -H "Content-Type: application/json" `
  --data-raw '{"username":"tester01","password":"secret123"}'
```

成功：`200`，`data` 为 `{user_id, username, access_token, refresh_token}`。

主要失败：`400/1000` 参数无效；`401/1101` 用户名或密码错误。

#### POST `/api/v1/auth/refresh` — 刷新 Token

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/auth/refresh" `
  -H "Content-Type: application/json" `
  --data-raw "{\"refresh_token\":\"$REFRESH_TOKEN\"}"
```

成功：`200`，返回新的 `{user_id, username, access_token, refresh_token}`。

主要失败：`400/1000` 参数无效；`401/1102` refresh token 无效。

#### GET `/api/v1/users/me` — 获取当前用户

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/users/me" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 为用户对象 `{id, username, email, nickname, avatar_url, created_at, updated_at}`；密码哈希不会返回。

主要失败：`401/1001` 或 `1002`；`404/1103` 用户不存在。

#### PUT `/api/v1/users/me` — 更新当前用户

请求体字段均可选；空字符串字段不会覆盖原值。

```powershell
curl.exe -sS -X PUT "$GO_BASE_URL/api/v1/users/me" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw '{"nickname":"新昵称","avatar_url":"https://example.com/avatar.png"}'
```

成功：`200`，返回更新后的用户对象。主要失败：`400/1000`；`500/1104` 更新失败。

### 3.2 剧本

#### POST `/api/v1/scripts/upload` — 上传 PDF 剧本

`multipart/form-data`，字段如下：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | file | 是 | PDF，默认最大 50 MiB |
| `title` | string | 否 | 不填时使用文件名；最多 200 个字符 |
| `description` | string | 否 | 剧本描述 |

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/scripts/upload" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -F "file=@.\fixtures\sample.pdf;type=application/pdf" `
  -F "title=测试剧本" `
  -F "description=接口验收用剧本"
```

成功：`202`，`data` 为 `{id, title, description, file_size, status, created_at}`，初始状态通常为 `parsing`。解析在后台执行。

主要失败：`400/1200` 缺少文件；`413/1201` 文件过大；`400/1202` 扩展名、Content-Type、文件签名或标题不合法；`500/1203` 服务内部错误。

#### GET `/api/v1/scripts?page=1&page_size=20` — 剧本列表

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/scripts?page=1&page_size=20" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 为 `{items, total, page, page_size}`。列表项字段为 `id, title, description, cover_url, file_size, status, parse_error?, chunk_count, created_at, updated_at`。

`page` 和 `page_size` 必须为正整数，默认 `1` 和 `20`；`page_size` 服务端最多取 `100`。非法分页：`400/1204`。

#### GET `/api/v1/scripts/{id}` — 剧本详情

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/scripts/$SCRIPT_ID" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，除列表项字段外增加 `characters`：`[{id, name, description, attributes}]`。

主要失败：`400/1205` ID 非法；`404/1206` 剧本不存在或不属于当前用户。

#### POST `/api/v1/scripts/{id}/retry` — 重试失败解析

仅允许当前用户且当前状态为 `failed` 的剧本。

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/scripts/$SCRIPT_ID/retry" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`202`，`data` 为 `{id, status:"parsing"}`。主要失败：`400/1205`；`404/1206`；`409/1211` 状态不允许重试。

#### DELETE `/api/v1/scripts/{id}` — 删除剧本

删除前会清理向量、对象存储文件并软删除数据库记录；仅允许 `ready` 或 `failed` 状态。

```powershell
curl.exe -sS -X DELETE "$GO_BASE_URL/api/v1/scripts/$SCRIPT_ID" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`{"code":0,"message":"ok"}`。主要失败：`400/1205`；`404/1206`；`409/1211`；`500/1203`。

### 3.3 游戏（单人和多人公共端点）

游戏状态枚举：`waiting`、`playing`、`paused`、`ended`。

#### POST `/api/v1/games/solo/start` — 开始单人游戏

要求剧本已 `ready`，且 `character_id` 属于该剧本。

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/solo/start" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"script_id\":$SCRIPT_ID,\"character_id\":$CHARACTER_ID}"
```

成功：`201`，`data` 为 `{room_id, game_status:"playing", opening_narrative}`。

主要失败：`400/1300` 请求无效；`404/1301` 剧本不存在；`409/1302` 剧本未就绪；`404/1303` 角色不存在；`503/1304` AI 开场叙事不可用；`409/1305` 创建冲突；`500/1306` 内部错误。

#### POST `/api/v1/games/{roomId}/action` — 同步提交行动

请求体：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `request_id` | string | 是 | 必须是可解析 UUID；用于幂等 |
| `expected_turn` | integer | 是 | `>=0`，客户端当前回合 |
| `action_text` | string | 是 | 非空，最多 2000 个字符 |

```powershell
$REQUEST_ID = [guid]::NewGuid().ToString()
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/action" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"request_id\":\"$REQUEST_ID\",\"expected_turn\":0,\"action_text\":\"观察房间并寻找出口\"}"
```

成功：`200`，`data` 为：

```json
{
  "narrative": "……",
  "dice_roll": {
    "type": "D20", "result": 14, "target": 10, "success": true,
    "critical_hit": false, "critical_miss": false,
    "description": "检定成功", "reason": "感知"
  },
  "effects": {
    "player_state_changes": {"hp": "10", "location": "走廊"},
    "items": [{"name": "钥匙", "quantity_delta": 1, "description": "旧钥匙"}],
    "buffs": [{"name": "警觉", "duration": 2}],
    "events": [{"name": "发现线索", "description": "墙上有划痕"}]
  },
  "current_turn": 1
}
```

`dice_roll` 可以为 `null`；各效果数组可能为空。单人模式主要失败：`400/1310`；`404/1311` 房间不存在、`1312` 玩家不存在；`409/1313` 房间非 playing、`1314` 回合冲突、`1315` request_id 冲突、`1316` 道具不足；`503/1317` AI 不可用、`1318` 运行态不可用；`502/1319` AI 效果非法；`500/1320` 内部错误。多人模式还可能返回 `403/1922` 非当前行动者、`409/1921` generation/回合冲突、`1923` 已有行动、`1924` request ID 冲突，以及 `503/1920` 运行态不可用、`1927` 多人 AI 不可用。

> 前端当前优先使用 WebSocket 获取流式叙事；这个 REST 接口保留为同步调用和故障排查入口。

#### GET `/api/v1/games/{roomId}/state` — 获取多人 V2 权威状态

仅冻结成员可读；`playing`、`paused` 和运行态保留期内的 `ended` 房间均可读取。

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/games/$ROOM_ID/state" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 包含 `{seq, version:2, room_id, status, generation, current_turn, round_number, turn_order, current_actor_id, deadline_at, players, summary_memory, recent_messages}`。`paused`、`ended` 或行动正在生成时 `deadline_at` 可以为 `null`。主要失败：`400/1900`；`404/1901` 房间不存在或不是冻结成员；`503/1920` 运行态不可用或与 MySQL 冻结阵容不一致。

#### POST `/api/v1/games/{roomId}/skip` — 当前行动者主动跳过

```powershell
$REQUEST_ID = [guid]::NewGuid().ToString()
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/skip" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"request_id\":\"$REQUEST_ID\",\"expected_turn\":0}"
```

成功：`200`，`data` 包含 `{generation, skipped_user_id, current_turn, round_number, current_actor_id, deadline_at, reason:"manual"}`；相同请求幂等重放不会再次推进。主要失败：`400/1310`；`403/1922` 不是当前行动者；`404/1311`；`409/1921`、`1923`、`1924`；`503/1920`。服务端超时使用同一权威推进逻辑，广播结果的 `reason` 为 `timeout`。

#### POST `/api/v1/games/{roomId}/save` — 手动存档

请求体：`{"save_name":"进入密室前"}`，`save_name` 必填。

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/save" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw '{"save_name":"进入密室前"}'
```

成功：`201`，`data` 为 `{save_id}`。主要失败：`400/1321`；`404/1311`；`409/1322`；`503/1318`；`500/1323`。

#### GET `/api/v1/games/{roomId}/saves` — 存档列表

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/games/$ROOM_ID/saves" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 为 `{items, total}`；每项为 `{id, save_name, round_number, is_auto, created_at}`。完整 Redis 快照和消息正文不会通过列表接口返回。主要失败：`400/1324`；`404/1311`；`500/1325`。

#### POST `/api/v1/games/{roomId}/load` — 读档

请求体：`{"save_id":91}`。成功读档后房间保持 `paused`，需要再调用 resume。

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/load" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"save_id\":$SAVE_ID}"
```

成功：`200`，`data` 为 `{room_id, save_id, status:"paused", turn}`。主要失败：`400/1332`；`404/1311` 房间不存在、`1333` 存档不存在；`409/1334` 存档损坏、`1335` 当前状态不可读档；`503/1318`；`500/1336`。

#### POST `/api/v1/games/{roomId}/pause` — 暂停

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/pause" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 为 `{room_id, status:"paused"}`。主要失败：`400/1326`；`404/1311`；`409/1327`；`503/1318`；`500/1328`。

#### POST `/api/v1/games/{roomId}/resume` — 恢复

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/resume" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 为 `{room_id, status:"playing"}`。主要失败：`400/1329`；`404/1311`；`409/1330`；`503/1318`；`500/1331`。

#### POST `/api/v1/games/{roomId}/end` — 结束游戏

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/games/$ROOM_ID/end" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data` 为 `{room_id, status:"ended"}`。主要失败：`400/1337`；`404/1311`；`409/1338`；`503/1318`；`500/1339`。

### 3.4 好友

以下接口都需要 Access Token。搜索结果和好友对象只公开 `id, username, nickname, avatar_url`，不会返回邮箱。

#### GET `/api/v1/users/search?keyword=...` — 搜索用户

`keyword` 可为用户 ID、用户名或昵称，去除首尾空格后长度为 1–50；最多返回 20 项，并排除当前账号和软删除账号。

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/users/search?keyword=$USER_ID_B" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data.items` 每项为 `{user, friendship}`。`friendship` 无关系时为 `null`；已有关系时含 `{id,status,direction?}`，`direction` 只在 pending 时为 `incoming` 或 `outgoing`。

#### POST `/api/v1/friend-requests` — 发送好友申请

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/friend-requests" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"target_user_id\":$USER_ID_B}"
```

成功：`200`，`data` 为申请对象 `{id,status,direction,requested_by,peer,created_at,updated_at,responded_at?}`。记录 `data.id` 为 `$REQUEST_ID`。重复同向请求保持幂等；反向 pending 请求会按状态机合并，不会创建第二条关系。

#### GET `/api/v1/friend-requests` — 查询申请

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/friend-requests?direction=incoming&status=pending&limit=20" `
  -H "Authorization: Bearer $ACCESS_TOKEN_B"
```

`direction` 为 `incoming` 或 `outgoing`，`status` 为 `pending`、`accepted` 或 `rejected`；默认查询 incoming pending。成功：`200`，`data={items,next_cursor?}`。下一页把 `next_cursor` 原样传入 `cursor`；`limit` 为 1–100。

#### POST `/api/v1/friend-requests/{requestId}/accept|reject` — 响应申请

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/friend-requests/$REQUEST_ID/accept" `
  -H "Authorization: Bearer $ACCESS_TOKEN_B"
```

只有收到申请的一方可以响应 pending 关系。成功：`200`，返回更新后的申请对象；接受后 `status=accepted`。将路径末尾改为 `reject` 可拒绝。

#### GET `/api/v1/friends` — 查询好友与在线状态

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/friends?limit=50" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`data={items,next_cursor?}`，每项为 `{id,peer,presence,updated_at}`。`presence` 为 `online`、`offline` 或 `unknown`；Redis 查询降级时返回 unknown。REST 按关系 ID 稳定分页，客户端可对已加载项做在线优先排序。

#### DELETE `/api/v1/friends/{friendUserId}` — 删除好友

```powershell
curl.exe -sS -X DELETE "$GO_BASE_URL/api/v1/friends/$USER_ID_B" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`，`{"code":0,"message":"ok"}`。关系软删除后双方均不再是好友，可重新发起申请。

好友接口主要失败：`400/1600` 请求非法、`404/1601` 目标不存在、`400/1602` 不能添加自己、`404/1603` 申请不存在、`403/1604` 无操作权限、`409/1605` 状态冲突、`404/1606` 好友关系不存在、`400/1607` 分页非法、`500/1699` 内部错误。

### 3.5 群组与群成员（M2.3-B）

以下接口均需要 Access Token。创建群组会在一个事务内创建唯一群会话、群主的两套成员关系和首条系统消息；响应中的 `version` 是后续写操作必须携带的乐观并发版本。

#### POST/GET `/api/v1/groups` — 创建和列出群组

```powershell
$group = curl.exe -sS -X POST "$GO_BASE_URL/api/v1/groups" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw '{"name":"周五夜调查局","avatar_url":""}' | ConvertFrom-Json
$GROUP_ID = $group.data.id
$GROUP_VERSION = $group.data.version
$CONVERSATION_ID = $group.data.conversation_id

curl.exe -sS "$GO_BASE_URL/api/v1/groups?limit=20" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

成功：`200`。群对象为 `{id,name,avatar_url,owner_id,conversation_id,current_user_role,member_count,version,created_at,updated_at}`；列表按群 ID 倒序稳定分页。

#### GET/PATCH `/api/v1/groups/{groupId}` — 查询和修改群资料

```powershell
curl.exe -sS "$GO_BASE_URL/api/v1/groups/$GROUP_ID" `
  -H "Authorization: Bearer $ACCESS_TOKEN"

curl.exe -sS -X PATCH "$GO_BASE_URL/api/v1/groups/$GROUP_ID" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"name\":\"周六夜调查局\",\"expected_version\":$GROUP_VERSION}"
```

只有群主可以修改。真实变化使版本加一并写入系统消息；相同值保持幂等，不递增版本。

#### POST/GET `/api/v1/groups/{groupId}/members` — 邀请和列出成员

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/groups/$GROUP_ID/members" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"user_ids\":[$USER_ID_B],\"expected_version\":$GROUP_VERSION}"

curl.exe -sS "$GO_BASE_URL/api/v1/groups/$GROUP_ID/members?limit=50" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

群主和管理员每次最多邀请 20 名自己的当前好友；群最多 50 名有效成员，批量邀请全成或全败。新成员可读取完整群历史。成员页每项为 `{id,user,role,joined_at}`。

#### PATCH/DELETE `/api/v1/groups/{groupId}/members/{userId}` — 角色与离群

```powershell
curl.exe -sS -X PATCH "$GO_BASE_URL/api/v1/groups/$GROUP_ID/members/$USER_ID_B" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"role\":\"admin\",\"expected_version\":$GROUP_VERSION}"

curl.exe -sS -X DELETE "$GO_BASE_URL/api/v1/groups/$GROUP_ID/members/$USER_ID_B?expected_version=$GROUP_VERSION" `
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

群主可调整 `admin/member` 并移除任意非群主成员；管理员只能移除普通成员；成员把路径用户 ID 设为自己即可退群。群主必须先转让，不能直接退出。离群后群详情、历史、已读、同步和发送权限立即失效。

#### POST `/api/v1/groups/{groupId}/transfer` — 转让群主

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/groups/$GROUP_ID/transfer" `
  -H "Authorization: Bearer $ACCESS_TOKEN" `
  -H "Content-Type: application/json" `
  --data-raw "{\"new_owner_user_id\":$USER_ID_B,\"expected_version\":$GROUP_VERSION}"
```

转让会原子更新 `groups.owner_id`、双方在 `group_members`/`conversation_members` 的角色、版本和系统消息。主要失败：`400/1800` 请求非法、`404/1801` 群不可见、`403/1802` 权限不足、`409/1803` 非好友、`409/1804` 群已满、`409/1805` 群主冲突、`409/1806` 版本冲突、`500/1807` 群服务不可用。

### 3.6 Go 内部回调（仅 Python → Go）

#### POST `/api/v1/internal/scripts/{id}/status`

需要 `X-Internal-Secret`，请求体：

```json
{"status":"ready","chunk_count":12,"error_message":""}
```

`status` 只接受 `ready` 或 `failed` 的终态：`ready` 要求 `chunk_count>0` 且无错误；`failed` 要求 `chunk_count=0` 且 `error_message` 非空。

```powershell
curl.exe -sS -X POST "$GO_BASE_URL/api/v1/internal/scripts/$SCRIPT_ID/status" `
  -H "X-Internal-Secret: $INTERNAL_SECRET" `
  -H "Content-Type: application/json" `
  --data-raw '{"status":"ready","chunk_count":12,"error_message":""}'
```

成功：`200`。主要失败：`400/1205` ID 非法、`1210` 状态载荷非法；`401/1212` 内部密钥错误；`404/1206` 剧本不存在；`409/1211` 状态冲突；`500/1203` 业务内部错误或 `500/1213` 内部鉴权未配置。

## 4. Go 游戏 WebSocket API

连接地址：

```text
${GO_WS_URL}?token=<ACCESS_TOKEN>&room_id=<ROOM_ID>
```

连接成功后，服务端第一条消息为 `subscribed`。多人房间随后返回当前权威 `room_snapshot` 基线，该基线沿用当前水位且不推进序号。业务变更事件包含由 Redis 分配的房间全局 `seq` 并按房间单调递增；`pong`、`error`、`sync_batch` 外壳、`snapshot_required`、`subscribed` 和初始 `room_snapshot` 不占用新序号（`sync_batch.data.messages` 内的历史业务事件仍带 `seq`）。重连后用 `sync` 补推 `seq` 大于本地值的消息；当 `since_seq` 已早于有界日志或高于当前水位时，服务端返回 `snapshot_required / {next_seq}`，客户端必须重新拉取 REST 快照。状态合并只接受更高的 `version`。客户端不应自行填写 `room_id`、`user_id` 或 `seq`。

游戏 WebSocket 已启用 `TRPG_WEBSOCKET_ALLOWEDORIGINS` 白名单。浏览器 Origin 必须精确匹配配置；无 Origin 的原生客户端仍可继续 JWT 鉴权。不允许的 Origin 优先返回 `403 / 1508 / origin not allowed`，不会暴露 Token 或房间状态。

### 4.1 消息外壳

```json
{
  "type":"narrative_chunk",
  "room_id":42,
  "data":{},
  "timestamp":1720000000000,
  "seq":7,
  "request_id":"uuid"
}
```

### 4.2 客户端 → 服务端

#### 心跳

```json
{"type":"ping"}
```

服务端返回 `type: "pong"`。连接层也会发送 WebSocket Ping 帧，客户端应保持读取循环。

#### 重连补推

```json
{"type":"sync","data":{"since_seq":6}}
```

服务端返回：

```json
{
  "type":"sync_batch",
  "room_id":42,
  "data":{"messages":[{"type":"dice_roll","seq":7,"data":{}}],"next_seq":7},
  "timestamp":1720000000000
}
```

#### 提交行动

```json
{
  "type":"game_action",
  "data":{
    "request_id":"550e8400-e29b-41d4-a716-446655440000",
    "expected_turn":0,
    "action_text":"观察房间并寻找出口"
  }
}
```

### 4.3 服务端 → 客户端行动事件

同一次行动的事件通过外壳 `request_id` 关联，典型顺序为：

1. 一个或多个 `narrative_chunk`：`data={content,is_final:false}`。
2. 可选 `dice_roll`：字段同 REST 的 `dice_roll`。
3. 可选 `status_update`：`data={player_id,changes}`，`changes` 包含 `player_state_changes/items/buffs/events`。
4. `narrative_complete`：`data={narrative,current_turn,duplicate?}`。

错误事件：

```json
{
  "type":"error",
  "room_id":42,
  "request_id":"550e8400-e29b-41d4-a716-446655440000",
  "data":{"code":1314,"message":"game action rejected","request_id":"550e8400-e29b-41d4-a716-446655440000"}
}
```

WebSocket 专用错误码：`1500` 缺少 token、`1501` token 无效、`1502` room_id 非法、`1503` 无房间访问权、`1504` sync 请求非法、`1505` 不支持的消息类型、`1506` 行动载荷非法、`1507` 行动处理器不可用、`1508` Origin 不允许。行动业务错误沿用 `1310`–`1319`，未知错误为 `1317`。

### 4.4 多人等待大厅事件

单人房间仍只允许房主连接；多人房间允许有效成员连接。连接后快照与每次提交成功的大厅事件都在 `data` 中携带 REST 同结构的房间、成员和候选角色权威快照：

```json
{"type":"room_snapshot","room_id":42,"data":{"id":42,"status":"waiting","version":5,"owner_id":7,"members":[],"characters":[]},"timestamp":1720000000000}
```

变更事件类型：`room_member_joined`、`room_member_left`、`room_ready_changed`、`room_character_selected`、`room_snapshot`（房主转让）和 `game_started`。事务失败或幂等无变化时不推送。

成员离房或被踢后，失权连接会先收到最终的 `room_member_left`，随后以应用关闭码 `4003`、reason `room_access_revoked` 断开；该连接不会收到更高版本事件，重新握手返回 `403 / 1503`。

### 4.5 浏览器控制台冒烟测试

```javascript
const ws = new WebSocket(
  `${GO_WS_URL}?token=${encodeURIComponent(ACCESS_TOKEN)}&room_id=${ROOM_ID}`
);
ws.onmessage = (event) => console.log(JSON.parse(event.data));
ws.onopen = () => {
  ws.send(JSON.stringify({type: "ping"}));
  ws.send(JSON.stringify({
    type: "game_action",
    data: {
      request_id: crypto.randomUUID(),
      expected_turn: 0,
      action_text: "观察房间并寻找出口"
    }
  }));
};
```

### 4.6 Phase 2 IM WebSocket

用户级 IM 实时通道连接地址：

```text
${IM_WS_URL}?token=<ACCESS_TOKEN>
```

客户端基础信封：

```json
{"type":"ping","request_id":"550e8400-e29b-41d4-a716-446655440000","data":{}}
```

服务端基础信封：

```json
{
  "type":"connected",
  "timestamp":1788796800000,
  "data":{"user_id":7,"connection_id":"550e8400-e29b-41d4-a716-446655440000"}
}
```

当前已启用：`1700` 缺少 Token、`1701` Token 无效、`1702` Origin 不允许、`1703` 信封解析失败、`1704` 字段或载荷校验失败、`1705` 类型未支持，以及聊天业务错误 `1709` 会话不可用、`1710` 需要好友关系、`1711` 消息载荷非法、`1712` 消息正文非法、`1715` 同步请求非法、`1716` 聊天服务不可用、`1717` 每连接限流。`1706`、`1707` 仍为保留码。

连接成功后的第一条消息必须为 `connected`。同一账号建立第二条 IM 连接时，旧连接先收到：

```json
{"type":"connection_replaced","timestamp":1788796800000,"data":{"reason":"connection_replaced"}}
```

随后旧连接以 WebSocket 应用关闭码 `4001`、reason `connection_replaced` 关闭，新连接继续可用。游戏 `/ws` 和 IM `/ws/im` 使用独立连接表，可以同时连接。

浏览器控制台冒烟测试：

```javascript
const im = new WebSocket(
  `${IM_WS_URL}?token=${encodeURIComponent(ACCESS_TOKEN)}`
);
im.onmessage = (event) => console.log(JSON.parse(event.data));
im.onopen = () => im.send(JSON.stringify({
  type: "ping",
  request_id: crypto.randomUUID(),
  data: {}
}));
im.onclose = (event) => console.log(event.code, event.reason);
```

好友状态变化时，双方在线连接会收到权威关系刷新提示：

```json
{
  "type":"friendship_updated",
  "timestamp":1789056000000,
  "data":{
    "friendship_id":31,
    "status":"accepted",
    "requested_by":7,
    "peer":{"id":8,"username":"player08","nickname":"调查员","avatar_url":""},
    "updated_at":"2026-09-10T10:00:00Z"
  }
}
```

好友的 IM 连接上线或下线时会收到：

```json
{"type":"presence","timestamp":1789056000000,"data":{"user_id":8,"status":"online"}}
```

`friendship_updated` 的 `status` 可为 `pending`、`accepted`、`rejected`、`removed`；收到后应重新查询受影响的 REST 列表。`presence` 在 M2.1 推送中只使用 online/offline，unknown 只用于 REST 查询降级。

M2.2-C 起客户端可主动发送 `ping`、`chat_message` 和 `im_sync`。IM 文本帧上限为 32 KiB；`chat_message.request_id` 必须是规范 UUID，并直接作为发送幂等键：

```json
{
  "type":"chat_message",
  "request_id":"550e8400-e29b-41d4-a716-446655440000",
  "data":{"conversation_id":41,"message_type":"text","content":"今晚开团吗？"}
}
```

发送成功后当前连接收到同 `request_id` 的 `chat_ack`；首次发送时 `duplicate=false`，用相同 UUID 和相同载荷重试时返回原消息且 `duplicate=true`，不会再次广播。私聊对端或群聊中除发送者外的全部在线有效成员收到 `chat_message`，全部参与者还会收到按各自视角生成的 `conversation_updated`。

断线或检测到 seq 缺口时发送：

```json
{
  "type":"im_sync",
  "request_id":"8c21c14d-cf36-4fd2-845d-1496d9c154b2",
  "data":{"conversation_id":41,"since_seq":8,"limit":100}
}
```

服务端返回同 `request_id` 的 `im_sync_batch`，其中 `messages` 按 seq 升序，`next_seq` 为本批最后 seq；`has_more=true` 时继续以 `next_seq` 请求。有效群成员与私聊参与者使用同一补推协议；退出或被移除后立即返回会话不可用。

群资料或成员事务提交后，在线有效成员会收到 `group_updated` 或 `group_member_changed`；退出或被移除者也会单独收到对应的成员失效事件，但不会再收到该事务的系统消息或会话摘要：

```json
{"type":"group_updated","timestamp":1789464000000,"data":{"group":{"id":9,"name":"周五夜调查局","avatar_url":"","owner_id":7,"member_count":3,"version":4}}}
```

```json
{"type":"group_member_changed","timestamp":1789464000000,"data":{"group_id":9,"event":"member_removed","actor_user_id":7,"target_user_id":8,"version":5}}
```

`chat_message` 与 `im_sync` 共用当前物理连接的令牌桶：每秒恢复 10、突发容量 20，成本分别为 1 和 5；`ping` 与未知类型不消耗。令牌不足时返回带原 `request_id` 的 `error / 1717 rate limited`，不会调用业务 Service、不会写库、也不会关闭连接；连接接管后的新连接使用新桶。其他未支持入站类型返回 `error / 1705`。

安全要求：服务端默认访问日志不记录 `/ws` 与 `/ws/im` 的查询串，接口测试和问题反馈中也不要复制包含真实 Token 的完整连接地址。

## 5. Python AI 内部 API

这些接口直接监听 `${AI_BASE_URL}`，只供 Go 或运维排查使用，不应由前端调用。除 `/health` 外都需要 `X-Internal-Secret`。

### GET `/health`

```powershell
curl.exe -sS "$AI_BASE_URL/health"
```

成功：`200`，返回 `{status:"ok", service, version}`。

### POST `/api/v1/ai/parse-script`

请求体：`{"script_id":9,"file_path":"scripts/7/9/uuid.pdf"}`。

```powershell
curl.exe -sS -X POST "$AI_BASE_URL/api/v1/ai/parse-script" `
  -H "X-Internal-Secret: $INTERNAL_SECRET" `
  -H "Content-Type: application/json" `
  --data-raw '{"script_id":9,"file_path":"scripts/7/9/uuid.pdf"}'
```

成功：`200`，`{"success":true,"message":"script parsing accepted"}`。解析在后台执行，最终通过 Go 内部回调写入 `ready` 或 `failed`。

### DELETE `/api/v1/ai/scripts/{script_id}/vectors`

```powershell
curl.exe -sS -X DELETE "$AI_BASE_URL/api/v1/ai/scripts/9/vectors" `
  -H "X-Internal-Secret: $INTERNAL_SECRET"
```

成功：`200`，`{"success":true,"message":"script vectors deleted"}`；向量清理失败：`503`。

### POST `/api/v1/ai/inference/start`

请求体：`{room_id, script_id, character_id, user_id}`，四个字段均为正整数。

```powershell
curl.exe -sS -X POST "$AI_BASE_URL/api/v1/ai/inference/start" `
  -H "X-Internal-Secret: $INTERNAL_SECRET" `
  -H "Content-Type: application/json" `
  --data-raw "{\"room_id\":$ROOM_ID,\"script_id\":$SCRIPT_ID,\"character_id\":$CHARACTER_ID,\"user_id\":$USER_ID}"
```

成功：`200`，`{"narrative":"..."}`；AI 生成失败：`503`。

### POST `/api/v1/ai/inference/action`

请求体：`{room_id,user_id,action,script_id,character_id}`，`action` 去除首尾空白后必须为 1–2000 个字符。

```powershell
curl.exe -sS -X POST "$AI_BASE_URL/api/v1/ai/inference/action" `
  -H "X-Internal-Secret: $INTERNAL_SECRET" `
  -H "Content-Type: application/json" `
  --data-raw "{\"room_id\":$ROOM_ID,\"user_id\":$USER_ID,\"action\":\"观察房间\",\"script_id\":$SCRIPT_ID,\"character_id\":$CHARACTER_ID}"
```

成功：`200`，`{narrative,dice_roll?,status_changes?}`；请求校验失败：`422`；推理失败：`503`。

### POST `/api/v1/ai/inference/action/stream`

请求体同上，响应类型为 `application/x-ndjson`，每行一个 JSON：

```powershell
curl.exe -N -sS -X POST "$AI_BASE_URL/api/v1/ai/inference/action/stream" `
  -H "X-Internal-Secret: $INTERNAL_SECRET" `
  -H "Content-Type: application/json" `
  --data-raw "{\"room_id\":$ROOM_ID,\"user_id\":$USER_ID,\"action\":\"观察房间\",\"script_id\":$SCRIPT_ID,\"character_id\":$CHARACTER_ID}"
```

成功行序列：

```json
{"type":"narrative_chunk","content":"你看到……"}
{"type":"complete","narrative":"你看到……","dice_roll":null,"status_changes":null}
```

流开始后若推理失败，会返回 `{"type":"error","message":"player action inference unavailable"}` 行。

## 6. 接口验收清单

### 6.1 鉴权与基础

- [ ] 注册成功，并能拒绝重复用户名/邮箱。
- [ ] 登录成功，access/refresh token 均可返回。
- [ ] refresh token 可换取新 token；错误 token 返回 401。
- [ ] 缺失、错误格式和过期 JWT 均返回预期错误码。
- [ ] Python `/health` 返回服务健康状态；Go 服务健康检查按部署探针或端口可达性执行（当前 Go 路由未提供 `/health`）。

### 6.2 剧本链路

- [ ] 非 PDF、缺少文件、超大文件被拒绝。
- [ ] 上传返回 202，详情状态能从 `parsing` 进入 `ready` 或 `failed`。
- [ ] 列表只返回当前用户剧本，分页字段正确。
- [ ] `ready` 剧本包含可用角色；失败剧本可以 retry。
- [ ] 删除剧本后对象、向量和数据库记录均按部署日志确认已清理。

### 6.3 单人游戏链路

- [ ] ready 剧本可以开始单人游戏并返回开场叙事。
- [ ] WebSocket 收到 `subscribed`，行动收到 chunk/dice/status/complete 事件。
- [ ] 重复 `request_id` 不重复推进回合；旧 `expected_turn` 被拒绝。
- [ ] 手动存档、列表、读档、暂停、恢复、结束状态正确。
- [ ] 断线重连后 `sync` 能补齐缺失 `seq`，不会重复应用事件。

### 6.4 好友与在线状态

- [x] 使用两个本地验收账号完成 ID/用户名/昵称搜索、申请、接受、拒绝、删除与删除后重加。
- [x] 两个账号同时连接 `/ws/im`，验证 online/offline 实时变化。
- [x] 关系变化触发 `friendship_updated`，刷新 REST 后双方列表一致。
- [x] 同账号第二页面接管连接，旧页面收到事件并以 4001 关闭，且不再重连。
- [x] 关闭临时 Redis 时好友 REST 将 presence 降级为 unknown；非好友状态隔离由自动化测试覆盖。

### 6.5 私聊与可靠消息

- [ ] 两个真实账号创建或复用私聊会话，历史分页按 seq 升序且无重复遗漏。
- [ ] 发送端收到 `chat_ack`，接收端收到 `chat_message`，双方收到各自视角的 `conversation_updated`。
- [ ] 使用相同 `request_id` 重试只产生一条持久化消息，返回 `duplicate=true` 且不重复广播。
- [ ] 接收端离线后发送多条消息，重连使用 `im_sync` 分批补齐，并验证 `next_seq` 与 `has_more`。
- [ ] 非好友发送、非成员同步、非法正文及非法同步参数返回对应 1709—1716 错误且连接保持可用。
- [x] Vue 聊天页面的乐观发送、ack、未读、刷新恢复、离线消息恢复、删好友后历史只读及重新加好友后会话复用已通过真实双账号浏览器验收。

### 6.6 群组持久化与权限

- [ ] 三个真实账号完成创建群、批量邀请、成员列表和群资料更新。
- [ ] 验证 owner/admin/member 权限矩阵、群主转让和原群主转让后退群。
- [ ] 验证非好友邀请、50 人容量、过期版本和越权操作返回 1800—1807 对应错误。
- [ ] 验证新成员可读取完整历史；离群或被移除后群详情、历史、同步和发送立即不可用。
- [ ] 验证删除好友不影响共同群聊，重新入群复用成员行且不恢复管理员角色。
- [x] 临时 MySQL 8.4 空库已通过 001—015 迁移、群完整生命周期、群会话查询和双成员表一致性自动化测试。
- [ ] 三个账号同时连接 `/ws/im`，验证群文本扇出、每人视角的 `conversation_updated`、重复发送不重播及离线 `im_sync` 补齐。
- [ ] 验证 `group_updated`、`group_member_changed`、系统消息顺序，以及被移除者只收到失效事件。
- [ ] 连续突发发送与高成本同步触发 1717；确认限流请求未写库且同连接恢复后可继续使用。
- [x] 自动化已覆盖三连接真实 WebSocket 群生命周期、批量会话视角、移除失权通知和加权令牌桶边界。

### 6.7 多人房间与等待大厅

- [x] 本地三账号已完成建房、房间码加入、不同角色选择、准备与房主开局。
- [x] 自动化和浏览器验收已覆盖重复加入、重复选角、非房主开局、踢人、房主转让和开局后阵容冻结。
- [x] 三端实时大厅按更高 `version` 合并权威快照，旧快照不会误撤销仍有效的房间访问权。

### 6.8 多人回合与生命周期

- [x] 确定性 AI fixture 边界内三端看到相同开场、行动队列、当前行动者、deadline、叙事和 seq。
- [x] 当前行动者的流式 chunk 只进入临时态，完成后才写入正式叙事；非当前玩家伪造提交返回 `403/1922`。
- [x] 主动跳过、30 秒服务端超时、暂停/恢复、手动存档/读档、五轮自动存档、断线恢复和结束态只读回看通过。
- [x] 提交 `500b8ab` 的 CI #54 七个作业与 Linux targeted race 通过。
- [ ] 使用真实 DeepSeek、Milvus/RAG、Redis 双实例和目标域名/Nginx/TLS 按 [M2.5 主分支合并与真实验收清单](./M2.5主分支合并与真实验收清单.md) 复验。

## 7. 当前已知边界

- 本文是基于当前源码的接口契约，不等同于已经部署的 Swagger UI；真实地址、密钥和依赖可用性以部署环境为准。
- `load` 接口只返回房间/存档 ID、状态和回合；客户端随后通过 `GET /games/{roomId}/state` 拉取完整 V2 权威快照。结束态运行记录仅在 Redis 运行态保留期内可读取，长期历史以存档为准。
- 游戏通道 `/ws` 不承载 IM 聊天；`/ws` 与 `/ws/im` 均已接入 Redis 跨实例总线和连接所有权。IM 离线恢复仍以 MySQL seq 为权威，游戏事件缺口由 Redis 有界日志补推，超出窗口时回退到 REST 快照。
- 本地 M2.5 浏览器验收使用确定性 AI fixture；真实 DeepSeek、Milvus、Nginx/TLS 和生产依赖可用性尚不能由该结果推断。
- Python 的 422 校验响应遵循 FastAPI 默认格式；Go 错误响应遵循 `{code,message}` 格式，两者不要混用。
