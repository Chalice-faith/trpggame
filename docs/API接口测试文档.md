# TRPG Game API 接口测试文档（Swagger 风格）

> 版本：Phase 1 / M1.5 + Phase 2 / M2.0-D
>
> 契约来源：当前 Go、Python 和 Vue 代码（2026-09-09 核对）。
>
> 状态：接口字段和错误码已按源码整理；M2.0-D 自动化回归结果见 [M2.0 验收记录](./M2.0验收记录.md)，真实 Docker 服务、MySQL、Redis、MinIO、Milvus、DeepSeek 和浏览器联调尚未执行。

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
$REFRESH_TOKEN = "<登录返回的 refresh_token>"
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
3. `POST /api/v1/scripts/upload` 上传一个真实 PDF，记录 `script_id`。
4. 轮询 `GET /api/v1/scripts/{id}`，直到 `status=ready`，并记录一个 `character.id`。
5. `POST /api/v1/games/solo/start` 创建单人房间，记录 `room_id`。
6. 连接 `GET /ws?token=...&room_id=...`，先验证 `subscribed`，再发送 `game_action`。
7. 验证行动事件顺序、存档、读档、暂停、恢复和结束。

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

### 3.3 单人游戏

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

`dice_roll` 可以为 `null`；各效果数组可能为空。主要失败：`400/1310`；`404/1311` 房间不存在、`1312` 玩家不存在；`409/1313` 房间非 playing、`1314` 回合冲突、`1315` request_id 冲突、`1316` 道具不足；`503/1317` AI 不可用、`1318` 运行态不可用；`502/1319` AI 效果非法；`500/1320` 内部错误。

> 前端当前优先使用 WebSocket 获取流式叙事；这个 REST 接口保留为同步调用和故障排查入口。

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

### 3.4 Go 内部回调（仅 Python → Go）

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

连接成功后，服务端第一条消息为 `subscribed`。业务投递事件包含房间 `seq` 并按房间单调递增；`pong`、`error`、`sync_batch` 外壳和 `subscribed` 不占用该序号（`sync_batch.data.messages` 内的历史业务事件仍带 `seq`）。重连后用 `sync` 补推 `seq` 大于本地值的消息。客户端不应自行填写 `room_id`、`user_id` 或 `seq`。

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

### 4.4 浏览器控制台冒烟测试

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

### 4.5 Phase 2 IM WebSocket

M2.0-C 已注册用户级 IM 实时通道，连接地址：

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

当前已启用：`1700` 缺少 Token、`1701` Token 无效、`1702` Origin 不允许、`1703` 信封解析失败、`1704` 字段或 Ping 载荷校验失败、`1705` 类型未支持。`1706`、`1707` 保留到业务消息接入后启用。

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

M2.0-C 只支持 `ping`。`chat_message`、好友、群聊、presence 和离线同步尚未实现，发送这些类型会返回 `error / 1705`。

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

## 7. 当前已知边界

- 本文是基于当前源码的接口契约，不等同于已经部署的 Swagger UI；真实地址、密钥和依赖可用性以部署环境为准。
- `load` 接口当前返回房间/存档 ID、状态和回合，不返回完整 Redis 快照；直接刷新浏览器后的完整状态恢复仍需后续状态同步契约或客户端重新拉取能力。
- 游戏通道中的 `chat_message` 仍未接通；独立 `/ws/im` 已提供连接、接管和 Ping/Pong 基础，但好友、聊天、群组及离线同步仍未实现。
- Python 的 422 校验响应遵循 FastAPI 默认格式；Go 错误响应遵循 `{code,message}` 格式，两者不要混用。
