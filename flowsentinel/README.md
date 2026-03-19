# FlowSentinel — Distributed Rate Limiting Service

FlowSentinel is a production-oriented, distributed rate limiting service with a **hot-path Go engine** for request checks and a **cold-path Python FastAPI admin API** for rule management and audit visibility.

## Architecture

```
Client Request
      ↓
ALB (local: nginx)
      ↓
Go Service (FlowSentinel Engine) — hot path
      ↓
Redis (atomic Lua INCR + EXPIRE per window bucket)
      ↓
Allow / Deny

Python FastAPI (Admin API) — cold path
      ↓
PostgreSQL (rules CRUD, audit logs)

Go Engine also writes audit logs → PostgreSQL
```

## Services (local)

- Engine (Go): `http://localhost:8080` (or via nginx `http://localhost:8088`)
- Admin (FastAPI): `http://localhost:8001` (or via nginx `http://localhost:8088`)
- Prometheus: `http://localhost:9090`
- Grafana: `http://localhost:3000`

## Endpoints

### Engine

- `POST /check` — body: `{ "client_id": "...", "resource": "...", "action": "..." }`
  - Returns: `{ "allowed": true|false, "remaining_quota": <int>, "reset_time": <unix_seconds> }`
  - If no matching rule exists: `allowed=true`, `remaining_quota=-1`, `reset_time=0`.
- `GET /health` — checks Redis + PostgreSQL.
- `GET /metrics` — Prometheus metrics.

### Admin

- `POST /rules` — create rule: `client_id, resource, limit, window_seconds`
- `GET /rules` — list rules
- `PUT /rules/{id}` — update rule
- `DELETE /rules/{id}` — delete rule
- `GET /audit` — list recent engine audit events from PostgreSQL
- `GET /health` — checks PostgreSQL.
- `GET /metrics` — Prometheus metrics.

## Local Setup (Docker Compose)

1) Create an env file:

```bash
cp flowsentinel/infra/.env.example flowsentinel/infra/.env
```

2) Start everything:

```bash
cd flowsentinel/infra
docker compose --env-file .env up --build
```

3) Use nginx reverse proxy (optional):

- Engine via nginx: `POST http://localhost:8088/check`
- Admin via nginx: `POST http://localhost:8088/rules`

## Prometheus Metrics (Engine)

- `flowsentinel_requests_total{client_id,resource,status="allowed|denied"}`
- `flowsentinel_latency_seconds` (histogram)
- `flowsentinel_redis_errors_total`

## AWS ECS Fargate Deployment (target)

Artifacts:
- Engine Dockerfile: `flowsentinel/engine/Dockerfile`
- Admin Dockerfile: `flowsentinel/admin/Dockerfile`
- ECS task definitions: `flowsentinel/infra/ecs-task-definition.json` (contains two task definitions in a JSON array)

High-level steps:
1) Build and push images to ECR: `flowsentinel-engine` and `flowsentinel-admin`.
2) Store `POSTGRES_DSN` (engine) and `DATABASE_URL` (admin) in SSM Parameter Store or Secrets Manager.
3) Register the task definitions, create ECS services behind an ALB, and attach security groups so:
   - Engine can reach Redis (ElastiCache) and PostgreSQL.
   - Admin can reach PostgreSQL.
4) Configure ALB listener rules to route `/check` and `/health` to the engine and `/rules` and `/audit` to the admin.

