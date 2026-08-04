# CLAUDE.md — AI 驱动 TRPG 主持人

> 本文档记录项目上下文、技术架构决策和开发里程碑，供 Claude 及开发者参考。

---

## 项目身份

- **项目名称**：AI 驱动桌面角色扮演游戏（TRPG）主持人
- **项目简称**：trpggame
- **项目类型**：Web 全栈应用（SPA + 双后端服务）
- **核心目标**：AI 代理传统人类 GM，7×24 小时提供高质量跑团体验
- **参考文档**：[需求文档.md](./需求文档.md) V1.1 ｜ [技术设计文档.md](./技术设计文档.md) V1.0

---

## 技术栈总览

| 层 | 技术 | 角色 |
|---|------|------|
| 前端 | Vue 3 (Composition API) + Vite + TypeScript + Pinia | SPA 客户端 |
| UI 组件 | Element Plus / Naive UI | 开箱即用中文组件 |
| 后端-业务 | Go 1.22+ (Gin + gorilla/websocket + GORM) | 用户/房间/剧本管理 + WebSocket Hub |
| 后端-AI | Python 3.11+ (FastAPI) | PDF 解析、RAG 检索、LLM 推理、Function Calling |
| AI 模型 | GLM-4-Long (1M 上下文窗口) | 叙事生成 + 规则裁定 |
| 关系数据库 | MySQL 8.4 | 持久化存储 |
| 缓存/状态 | Redis 7 | 会话状态、角色实时状态、Function Calling 缓存 |
| 向量数据库 | Milvus 2.4 | 剧本片段向量检索 |
| 对象存储 | MinIO | PDF 文件存储 |
| 反向代理 | Nginx | HTTPS/WSS 终止 + 路由 |
| 容器化 | Docker + Docker Compose | 本地开发/测试环境 |

---

## 架构概览

```
Vue SPA (Web) ──WSS──► Nginx ──► Go Backend (Gin + WebSocket Hub)
                                      │
                                      ├──► MySQL (持久化)
                                      ├──► Redis (实时状态/会话)
                                      ├──► MinIO (PDF 存储)
                                      │
                                      └──HTTP──► Python AI (FastAPI)
                                                      │
                                                      ├──► Milvus (向量检索)
                                                      ├──► Redis (状态读写)
                                                      └──► GLM-4 API (推理)
```

**关键设计原则**：
- MVP 优先：Phase 1 只做单人跑团闭环，不建 IM/好友系统
- 服务分离：Go（业务+实时通信）+ Python（AI+向量检索）
- 同步调用：MVP 阶段 Go↔Python 走 HTTP，不引入消息队列
- 流式优先：AI 推理结果通过 WebSocket 流式推送到前端
- 无状态 Go：Go 服务不持有会话状态，状态全部外存到 Redis/MySQL

---

## 开发里程碑

### Phase 1 — MVP：单人 AI 跑团闭环

**目标**：1 个玩家 + AI 主持人，完成一个完整剧本。

#### M1.1 项目初始化与基础设施

- [x] 创建 Go 后端项目骨架 (`go-backend/cmd/server/main.go` + `internal/` 目录树)
- [x] 创建 Python AI 服务骨架 (`python-ai/app/main.py` + `routers/` + `services/`)
- [x] 创建 Vue 前端项目骨架 (`Vite + Vue 3 + TypeScript + Pinia`)
- [x] 编写 `docker-compose.yml`（含 Nginx、MySQL、Redis、Milvus、MinIO、etcd）
- [x] 配置管理实现：Go 端 Viper、Python 端 config.py、前端 Vite 环境变量
- [x] 基础 CI：Go 测试/静态检查、Python 测试、Vue 测试与构建、Compose 配置检查
- [ ] 生产容器与 lint：补齐前端/Nginx 生产镜像及三端 lint 配置

#### M1.2 用户系统

- [x] MySQL 迁移脚本：`users` 表 + 索引
- [x] Go: `user_repo.go` — 用户 CRUD（GORM）
- [x] Go: `user_service.go` — 注册/登录/个人信息 业务逻辑
- [x] Go: `jwt.go` — JWT 生成/校验/刷新（Access 15min + Refresh 7d）
- [x] Go: `user_handler.go` — `/api/v1/auth/*` + `/api/v1/users/*` REST 端点
- [x] Go: `auth.go` 中间件 — JWT 鉴权
- [x] Vue: `stores/auth.ts` — 认证状态管理
- [x] Vue: `LoginView.vue` + `RegisterView.vue`
- [x] Vue: Axios 请求拦截器（自动附带 Token）

#### M1.3 剧本系统

- [x] MySQL 迁移脚本：`scripts`、`script_characters` 表及 `chunk_count`
- [x] Go: `script_repo.go` — 剧本 CRUD、状态回写和原子重试
- [x] Go: `script_service.go` — MinIO 上传、解析触发、详情、删除和失败重试
- [x] Go: `script_handler.go` — `/api/v1/scripts/*` REST 端点
- [x] Go: `ai_client/client.go` — 调用 Python 解析及向量清理端点
- [x] Go: Python 内部状态回写接口及共享密钥鉴权
- [x] Python: `pdf_parser.py` — PyMuPDF 逐页提取及不可提取文本识别
- [x] Python: `text_cleaner.py` — 重复页眉页脚、页码、空白及控制字符清洗
- [x] Python: `chunker.py` — 按章节标题分片（500-2000 字符，约 100 字符重叠）
- [x] Python: `embedder.py` — BGE 1024 维向量化及 Milvus 幂等写入
- [x] Python: `script.py` router — 后台解析编排、回调及向量删除端点
- [x] Vue: Dashboard 剧本列表/上传及 `ScriptDetailView.vue` 详情、轮询、删除、重试
- [x] 自动化：Go、Python、Vue 测试与统一 M1.3 验证脚本
- [ ] Docker Compose 真实端到端验收（2026-08-03 按当前安排暂缓，不视为通过）
- [ ] WebSocket 推送剧本解析进度（增强项；当前使用详情轮询）

#### M1.4 AI 推理核心

- [x] Python: `llm_client.py` — GLM-4-Long 调用封装（完整响应 + SSE 流式响应 + Function Tool Call 解析）
- [x] Python: `retriever.py` — RAG 检索 + MMR 重排序（Top-20 → MMR → Top-5）
- [x] Python: `function_calling.py` — 7 个函数定义、严格参数校验与可注入执行器
- [x] Python: `summarizer.py` — 每 5 轮触发、旧摘要合并与 200-500 字结果校验
- [x] Python: `dice.py` — D20/D100 服务端真随机、目标边界与大成功/大失败判定
- [x] Python: `inference.py` router — AI 推理 API
  - [x] 开场叙事：内部鉴权 → RAG → 上下文组装 → GLM 完整响应
  - [x] 玩家行动：Redis 只读上下文 → RAG → Function Calling → 服务端骰子 → 最终叙事
  - [x] 状态变更边界：严格校验并返回结构化 `status_changes`；实际 Redis 写入由 M1.5 游戏状态层统一处理
- [x] 系统提示词模板（含角色设定、规则裁定、Markdown 格式指令和动态数据边界）
- [x] 上下文组装逻辑：系统提示词 + 摘要记忆 + 最近 10 条 + RAG Top-5 + 角色状态

#### M1.5 游戏系统（单人）

- [x] MySQL 迁移脚本：`game_rooms` 表 + `room_players` 表 + `game_saves` 表
- [x] Go: `game_repo.go` — 房间、玩家与存档 CRUD
- [ ] Go: `game_service.go` — 单人游戏核心逻辑
  - [x] 快速开始（创建房间 → 调 AI 生成开场叙事 → 初始化 Redis）
  - [x] 处理行动（权限校验 → Python AI → 状态解释 → Redis CAS 原子提交）
  - [x] 手动存档 Service（Redis 一致性快照 → MySQL 持久化）
  - [ ] 存档列表与读档恢复
  - [ ] 自动存档（每 10 轮）
- [ ] Go: `game_handler.go` — `/api/v1/games/*` REST 端点
  - [x] `POST /games/solo/start` 快速开始
  - [x] `POST /games/:roomId/action` 同步行动提交
  - [ ] 手动存档、存档列表、读档、暂停、恢复与结束
- [ ] Go: `ws/hub.go` + `ws/client.go` — WebSocket 连接管理
- [ ] Go: `ws_handler.go` — WS 鉴权 + 消息路由
- [ ] WebSocket 消息流：`game_action` → AI → `narrative_chunk`×N → `narrative_complete`
- [ ] Redis 数据结构落地：
  - [x] 玩家状态 HASH (`room:{id}:player:{uid}`)
  - [x] 道具 SET、BUFF HASH
  - [x] 摘要记忆 STRING
  - [x] 最近 10 条消息 LIST (`LPUSH + LTRIM`)
  - [x] 行动 UUID/指纹幂等缓存与预期回合 CAS
  - [x] 带版本快照的原子读取与恢复（恢复时清空行动缓存）
- [ ] Vue: `stores/game.ts` — 游戏运行态管理
- [ ] Vue: `stores/websocket.ts` — WebSocket 连接 + 心跳 + 重连
- [ ] Vue: `GameSoloView.vue` + `CharacterSelectPanel.vue`
- [ ] Vue: `GamePlayView.vue`
  - [ ] `NarrativePanel.vue` — Markdown 渲染 + `ChatBubble.vue`
  - [ ] `ActionInput.vue` — 行动输入 + 发送
  - [ ] `PlayerStatusSidebar.vue` — HP/MP/SAN/道具
  - [ ] `GameToolbar.vue` — 存档/读档/骰子
  - [ ] `DiceAnimation.vue` — 骰子动画
- [ ] Vue: 前端路由 (`/login`, `/register`, `/dashboard`, `/game/solo/:id`, `/game/play/:id`)

#### M1.6 联调与验收

- [ ] Docker Compose 一键启动全栈
- [ ] 注册 → 登录 → 上传 PDF → 等待解析完成 → 快速开始 → AI 生成开场 → 多轮交互 全流程走通
- [ ] AI 流式输出在前端逐字渲染
- [ ] 骰子检定动画正常
- [ ] 角色状态实时更新
- [ ] 存档/读档正常恢复
- [ ] WebSocket 断线重连测试
- [ ] 性能指标验收（AI 首 Token < 3s、WS 延迟 < 200ms）

---

### Phase 2 — 多人社交

**目标**：完整的 IM + 多人跑团。

#### M2.1 好友系统

- [ ] MySQL 迁移：`friendships` 表
- [ ] Go: `friend_repo.go` + `friend_service.go` + `friend_handler.go`
- [ ] REST 端点：好友申请/接受/拒绝/列表/删除
- [ ] Vue: `FriendListView.vue`
- [ ] WebSocket 在线状态推送（`presence` 消息类型）

#### M2.2 IM 聊天系统

- [ ] Go: `chat_service.go` — 私聊/群聊/消息持久化
- [ ] Go: `chat_handler.go` — 消息 REST 端点（历史消息拉取）
- [ ] WebSocket `chat_message` 类型双向通信
- [ ] 离线消息 Redis 队列 + 重连补推
- [ ] Vue: `stores/chat.ts` — 聊天状态管理
- [ ] Vue: `ChatPanel.vue` + `GroupChatView.vue`

#### M2.3 群组系统

- [ ] MySQL 迁移：`groups` 表 + `group_members` 表
- [ ] Go: Group CRUD + 成员管理 Service/Handler
- [ ] Vue: 群聊列表 + 群管理界面
- [ ] AI 拉群功能（AI 主持人账号加入群聊）

#### M2.4 多人游戏房间

- [ ] Go: `room_service.go` — 多人房间核心逻辑
  - [ ] 创建房间 + 加入/离开
  - [ ] 角色选择 + 准备状态
  - [ ] 回合队列（按 `turn_order` 轮转）
  - [ ] 回合计时器（可配置，默认 120s，超时 skip）
  - [ ] AI 叙事广播给全房间
- [ ] Go: `room_handler.go` — `/api/v1/rooms/*` REST 端点
- [ ] Redis Pub/Sub 跨实例广播（多机部署场景）
- [ ] WebSocket 新增消息类型：`turn_start`、`turn_skip`
- [ ] Vue: `RoomLobbyView.vue` — 等待大厅（角色选择 + 准备）
- [ ] Vue: `TurnQueueIndicator.vue` — 回合顺序指示器
- [ ] Vue: 多人游戏界面（复用 `GamePlayView.vue` 并扩展）

---

### Phase 3 — 体验增强

**目标**：记忆增强 + 社交增强 + 体验优化。

#### M3.1 记忆增强

- [ ] MySQL 迁移：`key_events` 表
- [ ] Python: 关键事件自动标记（角色死亡、重大抉择、剧情分支点）
- [ ] Python: 语义记忆检索（基于语义相似度检索更早期的历史记忆）

#### M3.2 体验优化

- [ ] Vue: 回合计时器 UI + 倒计时动画
- [ ] Vue: 自定义角色功能（C-05）
- [ ] Vue: 投票决策功能（MP-03）
- [ ] 移动端适配（响应式布局 / 触摸优化）

---

## 数据库索引规划

迁移脚本位于 `go-backend/migrations/`，命名格式 `NNN_description.sql`：

```
001_create_users.sql
002_create_scripts.sql
003_create_script_characters.sql
004_add_script_chunk_count.sql
005_create_game_rooms.sql         # 已实现
006_create_room_players.sql       # 已实现
007_create_game_saves.sql         # 已实现
008_create_messages.sql           # 规划
009_create_friendships.sql        # Phase 2
010_create_groups.sql              # Phase 2
011_create_group_members.sql       # Phase 2
012_create_key_events.sql          # Phase 3
```

---

## API 端点速查

### Phase 1

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/auth/register` | 注册 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/auth/refresh` | 刷新 Token |
| GET | `/api/v1/users/me` | 获取个人信息 |
| PUT | `/api/v1/users/me` | 更新个人信息 |
| POST | `/api/v1/scripts/upload` | 上传 PDF 剧本 |
| GET | `/api/v1/scripts` | 剧本列表 |
| GET | `/api/v1/scripts/:id` | 剧本详情 |
| POST | `/api/v1/scripts/:id/retry` | 重新解析失败剧本 |
| DELETE | `/api/v1/scripts/:id` | 删除剧本 |
| POST | `/api/v1/ai/parse-script` | 接收后台解析任务（内部共享密钥） |
| DELETE | `/api/v1/ai/scripts/:id/vectors` | 删除剧本向量（内部共享密钥） |
| POST | `/api/v1/internal/scripts/:id/status` | Python 回写最终解析状态 |
| POST | `/api/v1/games/solo/start` | 单人快速开始 |
| POST | `/api/v1/games/:roomId/action` | 提交行动 |
| POST | `/api/v1/games/:roomId/save` | 手动存档 |
| GET | `/api/v1/games/:roomId/saves` | 存档列表 |
| POST | `/api/v1/games/:roomId/load` | 读档 |
| POST | `/api/v1/games/:roomId/pause` | 暂停 |
| POST | `/api/v1/games/:roomId/resume` | 恢复 |
| POST | `/api/v1/games/:roomId/end` | 结束 |

---

## WebSocket 消息类型速查

### 客户端 → 服务端

| type | 用途 | Phase |
|------|------|-------|
| `ping` | 心跳 | 1 |
| `game_action` | 提交游戏行动 | 1 |
| `sync` | 重连补推请求 | 1 |
| `chat_message` | 私聊/群聊消息 | 2 |

### 服务端 → 客户端

| type | 用途 | Phase |
|------|------|-------|
| `pong` | 心跳响应 | 1 |
| `narrative_chunk` | AI 流式输出片段 | 1 |
| `narrative_complete` | AI 输出完毕 | 1 |
| `dice_roll` | 骰子检定结果 | 1 |
| `status_update` | 角色状态变更 | 1 |
| `script_progress` | 剧本解析进度 | 1 |
| `system` | 系统通知 | 1 |
| `error` | 错误消息 | 1 |
| `sync_batch` | 离线消息补推 | 1 |
| `presence` | 在线状态 | 2 |
| `chat_message` | 聊天消息 | 2 |
| `turn_start` | 回合开始 | 2 |
| `turn_skip` | 回合跳过 | 2 |

---

## 错误码范围

| 区间 | 模块 |
|------|------|
| 1000-1099 | 通用（参数错误、未授权等） |
| 1100-1199 | 用户模块 |
| 1200-1299 | 剧本模块 |
| 1300-1399 | 游戏模块 |
| 1400-1499 | AI 服务 |
| 1500-1599 | WebSocket |
| 1600-1699 | 好友/IM（Phase 2） |

统一响应格式：`{"code": 0, "message": "ok", "data": {...}}`

---

## 关键设计决策记录

| # | 事项 | 决策 | 日期 |
|---|------|------|------|
| 1 | TRPG 规则系统 | 轻量通用规则，AI 灵活裁定 | 2026-06-29 |
| 2 | 前端框架 | Vue 3（Composition API + TypeScript） | 2026-06-29 |
| 3 | 部署策略 | 本地开发优先，暂不考虑部署 | 2026-06-29 |
| 4 | 部署环境 | 无需微信小程序，仅 Web 端 | 2026-06-29 |
| 5 | 单房间人数 | 4-6 人（含 1 AI GM 槽） | 2026-06-29 |
| 6 | 向量数据库 | Milvus 2.4 | 2026-06-29 |
| 7 | 开发节奏 | Phase 1 → 2 → 3 渐进迭代 | 2026-06-29 |
| 8 | 市场定位 | 面向海外市场，无需国内合规 | 2026-06-29 |
| 9 | 对象存储 | MinIO 自部署 | 2026-06-29 |
| 10 | Go↔Python 通信 | HTTP 同步调用（非消息队列） | 2026-06-29 |
| 11 | AI 模型 | GLM-4-Long（1M 上下文，成本低） | 2026-06-29 |
| 12 | 流式输出 | Python → SSE/NDJSON → Go → WebSocket → 前端 | 2026-06-29 |
| 13 | 记忆系统 | 摘要记忆（每5轮）+ 最近10轮保留 | 2026-06-29 |
| 14 | 认证方案 | JWT Access(15min) + Refresh(7d)，bcrypt cost=12 | 2026-06-29 |

---

## 开发常用命令

```bash
# 启动全栈（开发环境）
docker compose up -d

# 仅启动基础设施（MySQL + Redis + Milvus + MinIO）
docker compose up -d mysql redis milvus etcd minio

# Go 后端开发
cd go-backend && go run cmd/server/main.go

# Python AI 开发
cd python-ai && uvicorn app.main:app --reload --port 8000

# Vue 前端开发
cd vue-frontend && npm run dev

# 数据库迁移
cd go-backend && go run cmd/migrate/main.go up

# 查看日志
docker compose logs -f go-backend python-ai
```

---

## 当前项目状态

- **当前阶段**：Phase 1 / M1.5 单人游戏系统开发中，于 2026-08-04 暂停
- **文档状态**：技术设计、M1.5 进度、已知问题及暂停交接已同步到当前代码
- **代码状态**：M1.5 已完成 MySQL 数据层、Redis 运行态/快照、快速开始、同步行动接口和手动存档 Service；手动存档 HTTP 入口及读档闭环尚未完成
- **验证状态**：当前 Go 全量测试、`go vet ./...` 与 `git diff --check` 通过；专项并发/幂等/快照测试通过；真实 MySQL、Redis、Python 与 Docker 端到端联调未执行
- **暂停状态**：已提交当前完成代码，恢复顺序见 [开发暂停交接.md](./开发暂停交接.md)
- **下一步**：先实现手动存档 HTTP Handler，再依次实现存档列表、读档恢复、自动存档与房间生命周期接口
