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

- ✅ **用户系统**：注册、登录、JWT 鉴权、个人信息管理
- ✅ **基础设施**：Docker Compose 一键启动（MySQL、Redis、Milvus、MinIO、Nginx）
- 🚧 **剧本系统**：PDF 剧本上传 → 解析清洗 → 结构化切片 → 向量化存储 → RAG 检索
- 🚧 **AI 叙事核心**：GLM-4-Long 推理、RAG 检索（含 MMR 去重）、Function Calling、摘要记忆
- 🚧 **单人游戏**：快速开始、自由文本交互、骰子检定、角色状态管理、存档读档
- 🚧 **前端**：Vue 3 SPA — 单人跑团聊天界面

### Phase 2 — 多人社交 📋 *规划中*

- 好友系统（添加/删除/在线状态）
- IM 系统（WebSocket 私聊/群聊/AI 拉群/心跳重连）
- 多人跑团（4-6 人回合制、角色选择、广播叙事）

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
│  │   (REST API)          │  │   │  │ 文本切片/元数据    │  │
│  └─────────┬─────────────┘  │   │  └────────┬──────────┘  │
│  ┌─────────▼─────────────┐  │   │  ┌────────▼──────────┐  │
│  │   WebSocket Hub       │  │   │  │ Embedding + RAG   │  │
│  │   (gorilla/websocket) │  │   │  │ + MMR 去重        │  │
│  └─────────┬─────────────┘  │   │  └────────┬──────────┘  │
│  ┌─────────▼─────────────┐  │   │  ┌────────▼──────────┐  │
│  │   Service Layer       │◄─┼──┼──►│ LLM 推理          │  │
│  │   User/Game/Script    │  │   │  │ (GLM-4-Long)      │  │
│  └───┬───┬───┬───────────┘  │   │  ├───────────────────┤  │
└──────┼───┼───┼──────────────┘   │  │ Function Calling   │
       │   │   │                   │  │ 摘要记忆/骰子服务  │
       ▼   ▼   ▼                   │  └───────────────────┘
┌────────┐ ┌──────┐ ┌──────────┐  └──────────┬────────────┘
│  MySQL   │ │Redis │ │  MinIO   │            │
│ (持久化) │ │(缓存)│ │(PDF存储) │            ▼
└────────┘ └──────┘ └──────────┘   ┌───────────────────┐
                                   │     Milvus        │
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
| **AI 模型** | GLM-4-Long（1M 上下文窗口） | 成本低、上下文长，适合长剧本场景 |
| **关系数据库** | MySQL 8.4 | 成熟稳定，支持 JSON 字段 |
| **缓存/状态** | Redis 7 | 会话状态、角色实时状态、Function Calling 缓存 |
| **向量数据库** | Milvus 2.4 | 高性能向量检索，支持 MMR 去重 |
| **对象存储** | MinIO | 自部署 S3 兼容文件存储 |
| **反向代理** | Nginx | HTTPS/WSS 终止 + 路由 |
| **容器化** | Docker + Docker Compose | 本地开发/测试 |

---

## 📁 项目结构

```
trpggame/
├── docker-compose.yml          # Docker 编排（全栈启动）
├── .gitignore
│
├── go-backend/                 # Go 业务后端
│   ├── cmd/server/main.go      # 入口
│   ├── internal/
│   │   ├── config/             # 配置加载 (Viper)
│   │   ├── middleware/         # JWT 鉴权、CORS、日志
│   │   ├── handler/            # HTTP + WS Handler
│   │   ├── service/            # 业务逻辑
│   │   ├── repo/               # 数据访问层 (GORM)
│   │   ├── model/              # 数据模型
│   │   ├── ws/                 # WebSocket Hub + Client
│   │   └── ai_client/          # Python AI 服务 HTTP 客户端
│   ├── pkg/
│   │   ├── jwt/                # JWT 生成/校验
│   │   └── response/           # 统一响应格式
│   ├── migrations/             # SQL 迁移脚本
│   ├── Dockerfile              # 多阶段构建
│   ├── go.mod
│   └── go.sum
│
├── python-ai/                  # Python AI 服务
│   ├── app/
│   │   ├── main.py             # FastAPI 入口
│   │   ├── config.py           # 配置
│   │   ├── routers/            # API 路由
│   │   ├── services/           # 业务服务（PDF 解析/RAG/LLM/摘要/骰子）
│   │   ├── models/             # Pydantic 模型
│   │   └── utils/              # 工具（文本清洗、MMR 算法）
│   ├── requirements.txt
│   ├── Dockerfile
│   └── tests/
│
├── vue-frontend/               # Vue 3 前端 SPA
│   ├── src/
│   │   ├── App.vue             # 根组件
│   │   ├── main.ts             # 入口
│   │   ├── router/             # Vue Router 路由
│   │   ├── stores/             # Pinia 状态管理
│   │   ├── views/              # 页面视图
│   │   ├── components/         # 通用组件
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
    ├── CLAUDE.md               # AI 开发辅助文档
    └── M1.3开发计划.md         # Phase 1.3 开发计划
```

---

## 🚀 快速开始

### 前置要求

- [Docker](https://docs.docker.com/engine/install/) + [Docker Compose](https://docs.docker.com/compose/install/)
- [Go 1.22+](https://go.dev/dl/)（本地开发）
- [Node.js 18+](https://nodejs.org/)（前端开发）
- [Python 3.11+](https://www.python.org/)（AI 服务开发）
- GLM-4 API Key（需要配置到 `docker-compose.yml` 的 `GLM_API_KEY` 环境变量）

### Docker Compose 一键启动（推荐）

```bash
# 克隆项目
cd trpggame

# 启动全栈
docker compose up -d

# 仅启动基础设施（本地开发时使用）
docker compose up -d mysql redis milvus etcd minio

# 查看日志
docker compose logs -f go-backend python-ai
```

启动后访问：
- **前端**：http://localhost:5173
- **MinIO 控制台**：http://localhost:9001（admin / adminadmin）
- **Go 后端**：http://localhost:8080
- **Python AI**：http://localhost:8000

### 本地开发

#### Go 后端

```bash
cd go-backend

# 确保基础设施已启动（MySQL + Redis）
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

---

## 🔌 API 概述

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
| GET | `/api/v1/scripts` | 剧本列表 |
| GET | `/api/v1/scripts/:id` | 剧本详情 |
| DELETE | `/api/v1/scripts/:id` | 删除剧本 |

### 游戏模块

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/games/solo/start` | 单人快速开始 |
| POST | `/api/v1/games/:roomId/action` | 提交行动 |
| POST | `/api/v1/games/:roomId/save` | 手动存档 |
| GET | `/api/v1/games/:roomId/saves` | 存档列表 |
| POST | `/api/v1/games/:roomId/load` | 读档 |

### WebSocket

连接：`ws://localhost:8080/ws?token=<JWT>`

**客户端 → 服务端：**

| type | 说明 | Phase |
|------|------|-------|
| `ping` | 心跳 | 1 |
| `game_action` | 提交游戏行动 | 1 |
| `sync` | 重连补推请求 | 1 |

**服务端 → 客户端：**

| type | 说明 | Phase |
|------|------|-------|
| `pong` | 心跳响应 | 1 |
| `narrative_chunk` | AI 流式输出片段 | 1 |
| `narrative_complete` | AI 输出完毕 | 1 |
| `dice_roll` | 骰子检定结果 | 1 |
| `status_update` | 角色状态变更 | 1 |
| `script_progress` | 剧本解析进度 | 1 |
| `system` | 系统通知 | 1 |
| `error` | 错误消息 | 1 |

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
│  - GLM-4-Long    │
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
| `roll_dice` | `dice_type, modifier` | 触发骰子检定 |

---

## 🗺 开发路线图

### Phase 1 — MVP（单人 AI 跑团闭环）🎯 **进行中**

- M1.1 项目骨架与基础设施 ✅
- M1.2 用户系统 ✅
- M1.3 剧本系统 🚧
- M1.4 AI 推理核心 🚧
- M1.5 游戏系统（单人）🚧
- M1.6 联调与验收

### Phase 2 — 多人社交 📋 *规划中*

- M2.1 好友系统
- M2.2 IM 聊天系统
- M2.3 群组系统
- M2.4 多人游戏房间

### Phase 3 — 体验增强 📋 *规划中*

- M3.1 记忆增强
- M3.2 体验优化

---

## ⚙️ 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `GLM_API_KEY` | GLM-4-Long API Key | - |
| `TRPG_SERVER_PORT` | Go 服务端口 | `8080` |
| `TRPG_SERVER_MODE` | Go 运行模式 | `debug` |
| `TRPG_DATABASE_*` | MySQL 连接配置 | 见 docker-compose.yml |
| `TRPG_REDIS_*` | Redis 连接配置 | 见 docker-compose.yml |
| `TRPG_JWT_SECRET` | JWT 签名密钥 | `dev-secret-change-in-production` |
| `TRPG_MINIO_*` | MinIO 连接配置 | 见 docker-compose.yml |
| `TRPG_AI_BASEURL` | Python AI 服务地址 | `http://python-ai:8000` |

---

## 🤝 参与贡献

本项目目前为个人开发项目，欢迎 Issues 和 PR！

---

## 📄 许可证

本项目采用 MIT 许可证。
