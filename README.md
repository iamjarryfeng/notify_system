# notify_system

基于 Go 的事件驱动通知平台，将 HTTP 事件转化为可靠的邮件和 Webhook 通知。两个独立微服务结合 PostgreSQL 和 Redis，覆盖事件接入、异步投递、重试、幂等和端到端链路追踪。

<p align="center">
  <a href="https://github.com/iamjarryfeng/notify_system">
    <img alt="GitHub Stars" src="https://img.shields.io/github/stars/iamjarryfeng/notify_system">
  </a>
  <a href="https://github.com/iamjarryfeng/notify_system/fork">
    <img alt="GitHub Forks" src="https://img.shields.io/github/forks/iamjarryfeng/notify_system">
  </a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white">
  <img alt="PostgreSQL" src="https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white">
  <img alt="Redis" src="https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white">
  <img alt="Docker Compose" src="https://img.shields.io/badge/Docker_Compose-ok-2496ED?logo=docker&logoColor=white">
</p>

> 这是一个面向生产场景设计的参考实现。默认的 email 和 webhook dispatcher 是可替换实现，接入真实流量前请替换为你的真实供应商。

## 为什么选择 notify_system？

- **快速接入，不阻塞投递。** `POST /events` 完成事件持久化和入队后立即返回 `202 Accepted`，实际投递由后台 worker 处理。
- **默认具备持久化。** 事件先写入 PostgreSQL，再进入 Redis 队列；reconciler 会周期性重新入队过期的 `pending` 事件，弥补数据库写入和队列写入之间的间隙。
- **避免重复副作用。** 客户端可指定事件 UUID，重复提交返回 `409 Conflict`；通知按 `(event_id, channel)` 去重，避免重复邮件或 Webhook。
- **安全的重试机制。** worker 对永久性 4xx 响应不做重试，对 5xx 和网络错误使用带抖动的指数退避重试。
- **全链路可追踪。** Request ID 会贯穿 HTTP、PostgreSQL、Redis、worker 和下游调用。
- **开箱即运维。** 结构化 JSON 日志、`/health`、`/ready`、优雅退出和一键 Docker Compose。
- **架构清晰且可测试。** handler/service/repository 分层、声明式路由、dispatcher 抽象和集成测试让项目容易扩展。

## 服务组成

- `event_processor` 通过 HTTP 接收事件，持久化到 PostgreSQL，写入 Redis 队列，并异步处理事件。
- `notification_service` 接收处理完成的事件，解析通知渠道，投递 email/webhook 消息，并把结果写入 PostgreSQL。

## 系统架构

```mermaid
flowchart LR
    Client["客户端"] -->|"POST /events"| Ingest["event_processor"]
    Ingest -->|"INSERT pending"| DB[("PostgreSQL")]
    Ingest -->|"LPUSH event_id"| Queue[("Redis queue")]
    Worker["Worker"] -->|"BLPOP event_id"| Queue
    Worker -->|"find event + request_id"| DB
    Worker -->|"POST /notifications"| Notify["notification_service"]
    Notify -->|"INSERT pending"| DB
    Notify --> Email["email dispatcher"]
    Notify --> Webhook["webhook dispatcher"]
    Notify -->|"UPDATE sent / failed"| DB
    Reconciler["Reconciler"] -.->|"re-enqueue stale pending"| Queue
```

### 工作流程

1. 客户端向 `event_processor` 提交事件。服务校验后把事件持久化为 `pending`，把事件 ID 写入 Redis 队列，并返回 `202 Accepted`。
2. 后台 worker 从 Redis 取出事件 ID，从 PostgreSQL 加载完整事件，再调用 `notification_service`。
3. `notification_service` 根据事件类型解析路由，先持久化 `pending` 通知，再调用对应渠道投递，最后把状态更新为 `sent` 或 `failed`。
4. reconciler 会重新入队长期处于 `pending` 的事件，降低 PostgreSQL/Redis 写入间隙带来的丢失风险。

## 快速开始

前置条件：Docker 和 Docker Compose。

```bash
git clone https://github.com/iamjarryfeng/notify_system.git
cd notify_system
docker compose up --build
```

确认两个服务已经就绪：

```bash
curl -s http://localhost:8080/ready
curl -s http://localhost:8081/ready
```

创建一个事件：

```bash
curl -s -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "id": "11111111-1111-4111-8111-111111111111",
    "type": "user.registered",
    "payload": {
      "email": "user@example.com"
    }
  }'
```

查看事件进入队列后生成的通知：

```bash
curl -s http://localhost:8080/events/11111111-1111-4111-8111-111111111111
curl -s "http://localhost:8081/notifications?event_id=11111111-1111-4111-8111-111111111111"
```

如果不传 `id`，PostgreSQL 会自动生成 UUID。如果再次提交同一个 UUID，接口会返回 `409 Conflict`。

## 不使用 Docker 运行

先启动 PostgreSQL 和 Redis，然后在两个终端分别启动服务：

```bash
docker compose up postgres redis -d

# 终端 1：event_processor
cd event_processor
DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
REDIS_URL="redis://localhost:6379/0" \
NOTIFICATION_SERVICE_URL="http://localhost:8081" \
go run ./main.go

# 终端 2：notification_service
cd notification_service
DATABASE_URL="postgres://notify:notify@localhost:5432/notify?sslmode=disable" \
go run ./main.go
```

## 默认路由

| 事件类型 | 通知渠道 | 必须的 payload 字段 |
|------------|----------|------------------------|
| `user.registered` | email | `email` |
| `order.completed` | email + webhook | `email`、`webhook_url` |
| `payment.failed` | email | `email` |
| 其他事件 | webhook | `webhook_url` |

默认 dispatcher 只会记录发送成功日志。要接入真实供应商，请实现 `channels.Dispatcher` 并在 `notification_service/main.go` 中注册；重试包装器已经自动包裹在每个 dispatcher 外层。

## HTTP API

### event_processor

| 方法 | 路径 | 说明 |
|--------|------|-------------|
| `POST` | `/events` | 接收并入队事件，返回 `202 Accepted` |
| `GET` | `/events/:id` | 按 ID 查询事件 |
| `GET` | `/events` | 按 `status`、`limit`、`offset` 查询事件列表 |
| `GET` | `/health` | 存活探针 |
| `GET` | `/ready` | PostgreSQL 和 Redis 就绪探针 |

### notification_service

| 方法 | 路径 | 说明 |
|--------|------|-------------|
| `POST` | `/notifications` | 发送或去重通知 |
| `GET` | `/notifications/:id` | 按 ID 查询通知 |
| `GET` | `/notifications` | 按 `event_id`、`status`、`limit`、`offset` 查询通知列表 |
| `GET` | `/health` | 存活探针 |
| `GET` | `/ready` | PostgreSQL 就绪探针 |

列表接口会把 `limit` 上限限制为 `100`，避免查询无界增长。

## 配置

两个服务都通过环境变量读取配置。

| 变量 | 服务 | 默认值 | 说明 |
|----------|---------|---------|---------|
| `DATABASE_URL` | 两个服务 | 必填 | PostgreSQL 连接串 |
| `REDIS_URL` | event_processor | 必填 | Redis 连接串 |
| `NOTIFICATION_SERVICE_URL` | event_processor | 必填 | 下游通知服务地址 |
| `PORT` | 两个服务 | `8080` / `8081` | HTTP 监听端口 |
| `MAX_RETRIES` | event_processor | `3` | worker 调用通知服务的最大尝试次数 |
| `RETRY_BASE_DELAY_MS` | event_processor | `1000` | worker 重试的基础退避时间 |
| `DISPATCH_MAX_RETRIES` | notification_service | `3` | 每个通知 dispatcher 的最大尝试次数 |
| `DISPATCH_RETRY_BASE_DELAY_MS` | notification_service | `1000` | dispatcher 重试的基础退避时间 |

## 测试

```bash
make build
make vet
make test
make test-ci
make compose-up
```

测试覆盖表驱动单元测试、真实 PostgreSQL 集成测试、embedded PostgreSQL、miniredis 和 testcontainers。集成测试在依赖不可用时自动跳过；设置 `RUN_INTEGRATION=1` 或使用 `make test-ci` 可强制要求依赖。

## 目录结构

```
.
├── docker-compose.yml          # 本地开发环境：PostgreSQL、Redis 和两个服务
├── event_processor/            # 事件接入、队列、处理和转发
├── notification_service/       # 渠道路由和通知投递
├── Makefile                    # 构建、测试和 compose 辅助命令
├── SOLUTION.md                 # 设计决策、取舍和验证说明
└── CLAUDE.md                   # 面向开发者的协作说明
```

## Roadmap

- 在现有 `Dispatcher` 接口后接入真实 SMTP 和 HTTP Webhook 服务
- 增加死信队列和失败事件重放
- 增加 Prometheus 指标和 OpenTelemetry tracing
- 引入 transactional outbox 以获得更强的持久化一致性
- 增加 API 鉴权和微服务间认证
- 列表接口补充 `total`、`next_offset` 等分页元数据

欢迎针对以上方向提交 PR。

## 贡献指南

1. Fork 仓库并创建功能分支。
2. 保持改动小而聚焦。
3. 为行为变更补充或更新测试。
4. 运行 `make vet` 和 `make test`。
5. 提交清晰的 PR 描述。

## 更多文档

- `SOLUTION.md` 记录了设计决策、取舍和验证说明。
- `CLAUDE.md` 包含面向开发者的协作说明。
