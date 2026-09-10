<p align="center">
  <h1 align="center">🎲 TRPGGame</h1>
  <p align="center"><strong>AI 驱动的桌面角色扮演游戏（TRPG）主持人平台</strong></p>
  <p align="center">
    AI 代理传统人类 GM（Game Master），7×24 小时提供高质量跑团体验
  </p>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22-blue?logo=go" alt="Go" />
  <img src="https://img.shields.io/badge/Vue-3.5-brightgreen?logo=vue.js" alt="Vue" />
  <img src="https://img.shields.io/badge/Python-3.11-yellow?logo=python" alt="Python" />
  <img src="https://img.shields.io/badge/MySQL-8.4-blue?logo=mysql" alt="MySQL" />
  <img src="https://img.shields.io/badge/Status-Development-orange" alt="Status" />
</p>

---

## 📖 项目简介

**TRPGGame** 是一个基于多人在线聊天平台的 **AI 驱动 TRPG 主持人**系统，支持玩家通过文本聊天的方式随时随地进行单人或多人的桌面角色扮演游戏。

AI 承担传统人类 GM 的职责——叙事推进、NPC 扮演、规则裁定——让玩家无需寻找专业主持人即可获得高质量的跑团体验。

### 核心价值

| 痛点 | 解决方案 |
|------|----------|
| 找 GM 难，专业主持人稀缺 | AI 代理 GM，7×24 小时可用 |
| 跑团门槛高（规则复杂、准备耗时） | 导入 PDF 剧本即可开始，AI 自动理解剧情 |
| 线下凑人难、时间协调成本高 | 在线异步/同步结合，随时开团 |
| 长剧本 AI 记忆衰退、幻觉严重 | RAG + MMR + 摘要记忆，稳定 100+ 轮 |

---

## ✨ 功能特性

### Phase 1 — MVP（单人 AI 跑团闭环）🚧 *开发中*

**已完成：**

- ✅ **M1.1 项目骨架与基础设施**：Docker Compose 一键启动（MySQL、Redis、Milvus、MinIO、Nginx）
- ✅ **M1.2 用户系统**：注册、登录、JWT 鉴权（Access 15min + Refresh 7d）、个人信息
- ✅ **M1.3 剧本系统**：PDF 上传 → MinIO 存储 → Python 解析（提取/清洗/切片）→ BGE 向量化 → Milvus 检索；剧本列表/详情/删除/重新解析；解析进度回写（内部回调 + 共享密钥鉴权）
- ✅ **M1.4 AI 推理核心**：DeepSeek-V4-Flash 推理、RAG 检索（Top-20 → MMR → Top-5）、Function Calling、摘要记忆（每 5 轮）、服务端骰子检定
- ✅ **M1.5 单人游戏后端闭环**：
  - 单人快速开始、玩家行动同步 REST 链路
  - 手动存档 / 存档列表 / 读档 / 暂停 / 恢复 / 结束
  - 每 10 回合自动存档（MySQL 幂等唯一约束 + Redis 待持久化快照）
  - Redis 玩家状态（HP/MP/SAN/道具/Buff）、摘要记忆、最近消息、行动幂等缓存
  - 运行态世代隔离，防止旧 AI 结果污染新时间线

**进行中：**

- 🚧 **M1.6 联调与验收**：自动化与 CI 已通过，真实依赖部署和端到端接口测试待执行

### Phase 2 — 多人社交 🚧 *M2.0 代码完成，等待部署验收*

- ✅ **M2.0-A**：Origin 白名单、公共 JWT 鉴权和 IM 消息契约
- ✅ **M2.0-B**：游戏 WebSocket 接入公共握手组件
- ✅ **M2.0-C**：独立 `/ws/im` 通道、Ping/Pong 和单连接接管基础
- ✅ **M2.0-D**：D1—D4 已完成并提交；GitHub Actions CI #27（含 targeted race）通过，真实部署验收待执行
- 🚧 **M2.1**：好友后端、Redis 在线状态与实时事件已完成；Vue 界面和最终验收尚未开始
- 📋 **M2.2—M2.4**：聊天、群组和多人跑团尚未开始

### Phase 3 — 体验增强 📋 *规划中*

- 记忆增强（关键事件标记、语义记忆检索）
- 体验优化（回合计时器、自定义角色、投票决策）
- 移动端适配

---

## 🏗 技术架构

```
┌──────────────────────────────────────────────────────────┐
│                   Vue 3 SPA (前端)                        │
│             Vue 3 + TypeScript + Pinia + Element Plus      │
└──────────────────────┬───────────────────────────────────┘
                       │  HTTPS / WSS
                       ▼
┌──────────────────────────────────────────────────────────┐
│                  Nginx (反向代理)                          │
│             /api/* → Go :8080  /ws/* → Go :8080          │
└──────────────┬───────────────────────────────┬───────────┘
               │                               │
               ▼                               ▼
┌─────────────────────────────┐   ┌─────────────────────────┐
│      Go Backend (Gin)       │   │   Python AI (FastAPI)   │
│  ┌───────────────────────┐  │   │  ┌───────────────────┐  │
│  │   HTTP Handler        │  │   │  │ PDF 解析/清洗     │  │
│  │   (REST API + WS)     │  │   │  │ 文本切片/元数据    │  │
│  └─────────┬─────────────┘  │   │  └────────┬──────────┘  │
│  ┌─────────▼─────────────┐  │   │  ┌────────▼──────────┐  │
│  │   WebSocket Hub       │  │   │  │ Embedding + RAG   │  │
│  │   (gorilla/websocket) │  │   │  │ + MMR 去重        │  │
│  └─────────┬─────────────┘  │   │  └────────┬──────────┘  │
│  ┌─────────▼─────────────┐  │   │  ┌────────▼──────────┐  │
│  │   Service Layer       │◄─┼──┼──►│ LLM 推理          │  │
│  │   User/Script/Game    │  │   │  │ (DeepSeek-V4)     │  │
│  │   (含存档/自动存档)    │  │   │  ├───────────────────┤  │
│  └───┬───┬───┬───────────┘  │   │  │ Function Calling   │  │
└──────┼───┼───┼──────────────┘   │  │ 摘要记忆/骰子服务  │  │
       │   │   │                   │  └───────────────────┘  │
       ▼   ▼   ▼                  └──────────┬────────────┘
┌────────┐ ┌──────┐ ┌──────────┐             │
│ MySQL  │ │Redis │ │  MinIO   │             ▼
│ (持久化)│ │(运行态)│ │(PDF存储) │   ┌───────────────────┐
└────────┘ └──────┘ └──────────┘   │     Milvus        │
                                   │  (向量检索)        │
                                   └───────────────────┘
```

### 技术选型

| 层次 | 技术 | 选型理由 |
|------|------|----------|
| **前端** | Vue 3 (Composition API) + TypeScript + Vite | 开箱即用，生态完善 |
| **UI** | Element Plus + Pinia + Axios | 中文友好组件，状态管理，HTTP 封装 |
| **业务后端** | Go 1.22 + Gin + GORM + gorilla/websocket | 高并发、低延迟，天然适合 IM 场景 |
| **AI 服务** | Python 3.11 + FastAPI | 生态丰富，LLM/向量/PDF 库齐全 |
| **AI 模型** | DeepSeek-V4-Flash | 成本低、1M 上下文窗口，适合长剧本场景 |
| **关系数据库** | MySQL 8.4 | 成熟稳定，npm 迁移器 + advisory lock |
| **缓存/运行态** | Redis 7 | 玩家实时状态、Function Calling 缓存、幂等缓存 |
| **向量数据库** | Milvus 2.4 | 高性能向量检索，支持 MMR 去重 |
| **对象存储** | MinIO | 自部署 S3 兼容文件存储 |
| **反向代理** | Nginx | HTTPS/WSS 终止 + 路由 |
| **容器化** | Docker + Docker Compose | 本地开发/测试环境 |

---

## 📁 项目结构

```
trpggame/
├── docker-compose.yml          # Docker 编排（全栈启动）
├── .env.example                # 环境变量模板（复制为 .env 使用）
├── .gitignore
│
├── go-backend/                 # Go 业务后端
│   ├── cmd/server/main.go      # 入口
│   ├── internal/
│   │   ├── config/             # 配置加载 (Viper + 环境变量)
│   │   ├── middleware/         # JWT 鉴权、内部回调鉴权、CORS、日志
│   │   ├── handler/            # HTTP + WS Handler（含内部脚本回调）
│   │   ├── service/            # 业务逻辑（游戏行动/存档/自动存档/暂停恢复/结束）
│   │   ├── repo/               # 数据访问层 (GORM + Redis 运行态)
│   │   ├── model/              # 数据模型
│   │   ├── ws/                 # WebSocket Hub + Client
│   │   ├── ai_client/          # Python AI 服务 HTTP 客户端
│   │   └── storage/            # MinIO 存储
├── database/                    # npm 数据库迁移工具
│   ├── migrations/               # SQL 迁移 (001-010)
│   ├── scripts/migrate.mjs       # 迁移执行入口
│   └── lib/                      # checksum、锁与兼容迁移逻辑
││   ├── Dockerfile              # 多阶段构建
│   ├── go.mod
│   └── go.sum
│
├── python-ai/                  # Python AI 服务
│   ├── app/
│   │   ├── main.py             # FastAPI 入口
│   │   ├── config.py           # 配置（环境变量 + 默认值）
│   │   ├── dependencies.py     # 依赖注入
│   │   ├── routers/            # 剧本解析 / AI 推理 API
│   │   └── services/           # PDF 解析/RAG/LLM/摘要/骰子/上下文组装/对象存储
│   ├── tests/                  # 42+ 单元测试
│   ├── requirements.txt
│   └── Dockerfile
│
├── vue-frontend/               # Vue 3 前端 SPA
│   ├── src/
│   │   ├── App.vue             # 根组件
│   │   ├── main.ts             # 入口
│   │   ├── api/                # API 封装
│   │   ├── router/             # Vue Router 路由
│   │   ├── stores/             # Pinia 状态管理
│   │   ├── views/              # 页面视图
│   │   ├── features/           # 功能模块
│   │   ├── composables/        # 组合式函数
│   │   └── style.css           # 全局样式
│   ├── index.html
│   ├── .env                    # 环境变量
│   ├── vite.config.ts
│   ├── Dockerfile.dev
│   └── package.json
│
├── nginx/                      # Nginx 配置
│   ├── nginx.conf
│   └── conf.d/default.conf
│
└── docs/                       # 项目文档
    ├── 需求文档.md             # 产品需求文档 V1.1
    ├── 技术设计文档.md         # 技术设计文档 V1.1
    ├── 部署指南.md             # 部署快速开始手册
    ├── M1.3验收记录.md         # M1.3 剧本系统验收记录
    ├── M2.0验收记录.md         # M2.0 契约与实时通信基础验收记录
    ├── API接口测试文档.md       # Swagger 风格 REST / WebSocket / AI 接口测试文档
    ├── 未完成事项.md           # Phase 1 待办、责任方与完成标准
    ├── Phase2规划设计.md       # 多人社交阶段范围、架构与开发顺序
    ├── M2.0实施方案.md         # Phase 2 契约与实时通信基础实施步骤
    ├── M2.1实施方案.md         # 好友与在线状态契约、状态机和分块计划
    ├── 开发暂停交接.md         # 开发交接与恢复说明
    ├── 开发问题记录.md         # 开发问题记录
    └── CLAUDE.md               # AI 开发辅助文档
```

---

## 🚀 快速开始

### 前置要求

- [Docker](https://docs.docker.com/engine/install/) + [Docker Compose](https://docs.docker.com/compose/install/)
- [Go 1.22+](https://go.dev/dl/)（本地开发）
- [Node.js 18+](https://nodejs.org/)（前端开发）
- [Python 3.11+](https://www.python.org/)（AI 服务开发）
- DeepSeek API Key（可选，缺省则 AI 推理不可用）

### 1. 准备环境变量

```bash
# 在项目根目录
cp .env.example .env
```

编辑 `.env`，至少填写：

| 变量 | 说明 | 是否必填 |
|------|------|---------|
| `DEEPSEEK_API_KEY` | DeepSeek 的 API Key | 否（缺省则 AI 推理不可用） |
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码 | 建议改强密码 |
| `MYSQL_PASSWORD` | 业务账号 `trpg` 的密码 | 建议改强密码 |
| `INTERNAL_SHARED_SECRET` | Go ↔ Python 内部回调密钥 | 建议改随机值 |

### 2. Docker Compose 一键启动（推荐）

```bash
docker compose up -d

# 仅启动基础设施（本地开发时使用）
docker compose up -d mysql redis minio
docker compose up -d mysql redis minio etcd milvus   # 含 AI 所需
```

> **关键依赖关系**：`go-backend` 启动时会**依次连接 MySQL、Redis、MinIO，任一连不上都会直接退出**。因此无论哪种部署方式，这三个服务都必须先就绪。

启动后访问：
- **前端**：http://localhost:5173
- **Go 后端**：http://localhost:8080
- **Python AI**：http://localhost:8000/health
- **MinIO 控制台**：http://localhost:9001（默认 `minioadmin/minioadmin`）

### 3. 本地开发

#### Go 后端

```bash
cd go-backend
go mod download
go run cmd/server/main.go
```

#### Python AI 服务

```bash
cd python-ai
python -m venv venv
source venv/bin/activate  # Windows: venv\Scripts\activate
pip install -r requirements.txt
uvicorn app.main:app --reload --port 8000
```

#### Vue 前端

```bash
cd vue-frontend
npm install
npm run dev
```

### 4. 验证

```bash
# Go 后端测试
cd go-backend && go test ./... && go vet ./...

# Python AI 测试
cd python-ai && python -m unittest discover tests

# Vue 前端测试
cd vue-frontend && npm run build && npm test

# Docker Compose 配置校验
docker compose config --quiet
```

---

## 🔌 API 概述

完整请求/响应字段、错误码、`curl` 示例和 WebSocket 测试步骤见：[API接口测试文档](docs/API接口测试文档.md)。Go 服务启动后可通过 `/api/docs/index.html` 使用 Swagger UI。

### 认证模块

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/auth/register` | 注册 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/auth/refresh` | 刷新 Token |

### 用户模块

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/users/me` | 获取个人信息 |
| PUT | `/api/v1/users/me` | 更新个人信息 |

### 剧本模块

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/scripts/upload` | 上传 PDF 剧本 |
| GET | `/api/v1/scripts` | 剧本列表（分页） |
| GET | `/api/v1/scripts/:id` | 剧本详情 |
| POST | `/api/v1/scripts/:id/retry` | 重新解析失败剧本 |
| DELETE | `/api/v1/scripts/:id` | 删除剧本（级联清理 Milvus/MinIO/MySQL） |

### 游戏模块（M1.5 单人闭环）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/games/solo/start` | 单人快速开始 |
| POST | `/api/v1/games/:roomId/action` | 提交行动 |
| POST | `/api/v1/games/:roomId/save` | 手动存档 |
| GET | `/api/v1/games/:roomId/saves` | 存档列表 |
| POST | `/api/v1/games/:roomId/load` | 读档 |
| POST | `/api/v1/games/:roomId/pause` | 暂停 |
| POST | `/api/v1/games/:roomId/resume` | 恢复 |
| POST | `/api/v1/games/:roomId/end` | 结束游戏 |

### 内部回调（Go ↔ Python）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/internal/scripts/:id/status` | 剧本解析状态回写（`INTERNAL_SHARED_SECRET` 鉴权） |

### WebSocket

连接：`ws://localhost:8080/ws?token=<JWT>&room_id=<ROOM_ID>`

**客户端 → 服务端：**

| type | 说明 | 状态 |
|------|------|------|
| `ping` | 心跳 | Phase 1 |
| `game_action` | 提交游戏行动 | 开发中 |
| `sync` | 重连补推请求 | 开发中 |

**服务端 → 客户端：**

| type | 说明 | 状态 |
|------|------|------|
| `pong` | 心跳响应 | Phase 1 |
| `narrative_chunk` | AI 流式输出片段 | 代码完成，待真实验收 |
| `narrative_complete` | AI 输出完毕 | 代码完成，待真实验收 |
| `dice_roll` | 骰子检定结果 | 代码完成，待真实验收 |
| `status_update` | 角色状态变更 | 代码完成，待真实验收 |
| `script_progress` | 剧本解析进度 | 开发中 |
| `system` | 系统通知 | Phase 1 |
| `error` | 错误消息 | Phase 1 |

---

## 🧠 核心 AI 流程

```
玩家输入行动
      │
      ▼
┌─────────────────┐
│ 1. 上下文组装     │
│  - 系统提示词    │
│  - 摘要记忆      │
│  - 最近 10 轮对话 │
│  - RAG 检索片段  │ ← 向量检索 + MMR 去重
│  - 角色当前状态  │ ← 从 Redis 读取
└───────┬─────────┘
        ▼
┌─────────────────┐
│ 2. LLM 推理      │
│  - DeepSeek-V4  │
│  - 叙事生成      │
│  - 规则裁定      │
│  - Function Call │ → 需要状态变更时调用
└───────┬─────────┘
        ▼
┌─────────────────┐
│ 3. 后处理        │
│  - 执行 FC 写Redis│
│  - 更新摘要记忆  │
│  - 返回叙事文本  │
└───────┬─────────┘
        ▼
     广播给玩家
```

AI 可调用的 Function Calling 函数：

| 函数名 | 参数 | 描述 |
|--------|------|------|
| `update_player_status` | `player_id, field, value` | 更新角色 HP/MP/SAN 等数值 |
| `add_item` | `player_id, item_name, quantity` | 角色获得道具 |
| `remove_item` | `player_id, item_name, quantity` | 角色失去道具 |
| `add_buff` | `player_id, buff_name, duration` | 角色获得 BUFF/DEBUFF |
| `set_location` | `player_id, location` | 更新角色当前位置 |
| `trigger_event` | `event_name, description` | 记录关键剧情事件 |
| `roll_dice` | `dice_type, modifier` | 触发骰子检定（服务端真随机） |

---

## 🗺 开发路线图

### Phase 1 — MVP（单人 AI 跑团闭环）🎯 **进行中**

- ✅ M1.1 项目骨架与基础设施
- ✅ M1.2 用户系统
- ✅ M1.3 剧本系统（代码级验收通过，Docker 端到端验收暂缓）
- ✅ M1.4 AI 推理核心
- ✅ M1.5 单人游戏后端闭环
- ✅ M1.5 WebSocket 行动流式事件与 Vue 单人游戏最小闭环
- ✅ M1.5 前端收尾：存档工具栏、完整状态面板、骰子反馈与操作状态
- ✅ Vue 单人游戏页面（最小闭环）
- 🚧 M1.6 真实依赖部署与端到端接口验收（暂缓）

### Phase 2 — 多人社交 🚧 *M2.0 代码完成，等待部署验收*

- ✅ M2.0-A 实时通信契约与安全配置
- ✅ M2.0-B WebSocket 公共握手组件
- ✅ M2.0-C IM WebSocket 连接基础
- ✅ M2.0-D 并发、异常关闭、竞态与收尾验证（D1—D4 已提交，GitHub Actions CI #27 含 targeted race 全部通过）
- ✅ M2.1-A 好友与在线状态契约、状态机和 presence 设计已冻结
- ✅ M2.1-B 好友迁移、持久化、REST、OpenAPI 与 MySQL 8.4 并发验证
- ✅ M2.1-C Redis 在线租约、IM 生命周期观察与实时事件
- ⬜ M2.1-D Vue 与验收收尾
- ⬜ M2.2 IM 聊天系统
- ⬜ M2.3 群组系统
- ⬜ M2.4 多人游戏房间

### Phase 3 — 体验增强 📋 *规划中*

- M3.1 记忆增强
- M3.2 体验优化

---

## ⚙️ 环境变量

### .env（项目根目录）

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `DEEPSEEK_API_KEY` | DeepSeek API Key | 空（AI 推理不可用） |
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码 | - |
| `MYSQL_DATABASE` | 业务数据库名 | `trpggame` |
| `MYSQL_USER` | 业务账号 | `trpg` |
| `MYSQL_PASSWORD` | 业务账号密码 | - |
| `INTERNAL_SHARED_SECRET` | Go ↔ Python 内部回调密钥 | - |
| `WEBSOCKET_ALLOWED_ORIGINS` | 浏览器 WebSocket Origin 白名单 | 本地 Vue 与 Nginx 地址 |

### Go 后端（docker-compose 注入）

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `TRPG_SERVER_PORT` | Go 服务端口 | `8080` |
| `TRPG_SERVER_MODE` | Go 运行模式 | `debug` |
| `TRPG_DATABASE_*` | MySQL 连接配置 | 见 docker-compose.yml |
| `TRPG_REDIS_*` | Redis 连接配置 | 见 docker-compose.yml |
| `TRPG_JWT_SECRET` | JWT 签名密钥 | `dev-secret-change-in-production` |
| `TRPG_MINIO_*` | MinIO 连接配置 | 见 docker-compose.yml |
| `TRPG_AI_BASEURL` | Python AI 服务地址 | `http://python-ai:8000` |
| `TRPG_AI_TIMEOUT` | AI 请求超时 | `60` |
| `TRPG_WEBSOCKET_ALLOWEDORIGINS` | 游戏与 IM WebSocket Origin 白名单 | 本地 Vue 与 Nginx 地址 |
| `TRPG_INTERNAL_SHARED_SECRET` | 内部回调密钥 | `dev-internal-secret-change-in-production` |

### Python AI（docker-compose 注入）

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `TRPG_AI_DEBUG` | 调试模式 | `true` |
| `TRPG_AI_MILVUS_HOST/PORT` | Milvus 连接 | `milvus:19530` |
| `TRPG_AI_REDIS_URL` | Redis 连接 | `redis://redis:6379/0` |
| `TRPG_AI_DEEPSEEK_API_KEY` | DeepSeek API Key | `${DEEPSEEK_API_KEY}` |
| `TRPG_AI_DEEPSEEK_MODEL` | DeepSeek 模型 | `deepseek-v4-flash` |
| `TRPG_AI_MINIO_*` | MinIO 连接配置 | `minioadmin/minioadmin` |
| `TRPG_AI_GO_CALLBACK_BASE_URL` | Go 内部回调地址 | `http://go-backend:8080/api/v1/internal` |
| `TRPG_AI_PARSE_TASK_TIMEOUT` | 剧本解析超时 | `600` |

---

## 🧪 测试

| 项目 | 命令 | 覆盖范围 |
|------|------|---------|
| Go | `go test ./...` + `go vet ./...` | Repository/Service/Handler/AI Client |
| 数据库 | `npm run db:test` | npm 迁移器、checksum、锁与特殊迁移兼容性 |
| Python | `python -m unittest discover tests` | 42+ 测试：PDF 解析、切片、RAG、LLM、骰子、上下文组装 |
| Vue | `npm run build` + `npm test` | TypeScript 检查 + Vitest 组件测试 |

---

## 🤝 参与贡献

本项目目前为个人开发项目，欢迎 Issues 和 PR！

---

## 📄 许可证

本项目采用 MIT 许可证。
