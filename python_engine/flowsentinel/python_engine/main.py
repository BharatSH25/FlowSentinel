from __future__ import annotations

import asyncio
import contextlib
import os
import time
import uuid
from contextlib import asynccontextmanager
from dataclasses import dataclass

import asyncpg
import redis.asyncio as redis
import structlog
from fastapi import FastAPI, HTTPException, Request, Response
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest


def configure_logging() -> None:
    structlog.configure(
        processors=[
            structlog.processors.TimeStamper(fmt="iso", utc=True),
            structlog.processors.add_log_level,
            structlog.processors.StackInfoRenderer(),
            structlog.processors.format_exc_info,
            structlog.processors.JSONRenderer(),
        ]
    )


configure_logging()
log = structlog.get_logger()

REQUESTS_TOTAL = Counter(
    "flowsentinel_requests_total",
    "Total number of FlowSentinel /check requests.",
    labelnames=["client_id", "resource", "status"],
)
LATENCY_SECONDS = Histogram(
    "flowsentinel_latency_seconds",
    "Request latency in seconds for FlowSentinel /check.",
)
REDIS_ERRORS_TOTAL = Counter(
    "flowsentinel_redis_errors_total",
    "Total number of Redis errors during limiter checks.",
)

LUA_FIXED_WINDOW = """
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return current
"""


@dataclass(slots=True)
class Settings:
    listen_port: int
    postgres_dsn: str
    redis_addr: str
    redis_password: str | None
    redis_db: int
    rules_refresh_seconds: int


@dataclass(slots=True)
class Rule:
    rule_id: str
    client_id: str
    resource: str
    limit_count: int
    window_seconds: int


@dataclass(slots=True)
class AuditEvent:
    client_id: str
    resource: str
    status: str
    remaining_quota: int
    correlation_id: str


class CheckRequest(BaseModel):
    client_id: str
    resource: str
    action: str


class CheckResponse(BaseModel):
    allowed: bool
    remaining_quota: int
    reset_time: int


def load_settings() -> Settings:
    postgres_dsn = os.getenv("POSTGRES_DSN")
    if not postgres_dsn:
        raise RuntimeError("POSTGRES_DSN is required")

    return Settings(
        listen_port=int(os.getenv("LISTEN_PORT", "8002")),
        postgres_dsn=postgres_dsn,
        redis_addr=os.getenv("REDIS_ADDR", "redis:6379"),
        redis_password=os.getenv("REDIS_PASSWORD"),
        redis_db=int(os.getenv("REDIS_DB", "0")),
        rules_refresh_seconds=int(os.getenv("RULES_REFRESH_SECONDS", "30")),
    )


def generate_correlation_id() -> str:
    return uuid.uuid4().hex


def parse_redis_addr(addr: str) -> tuple[str, int]:
    host, _, port_str = addr.partition(":")
    return host, int(port_str or "6379")


class RuleCache:
    def __init__(self, pool: asyncpg.Pool, interval_seconds: int) -> None:
        self._pool = pool
        self._interval_seconds = interval_seconds
        self._rules: dict[str, dict[str, Rule]] = {}
        self._lock = asyncio.Lock()

    async def start(self) -> None:
        await self.refresh()

    async def loop(self, stop_event: asyncio.Event) -> None:
        while not stop_event.is_set():
            try:
                await asyncio.wait_for(stop_event.wait(), timeout=self._interval_seconds)
            except TimeoutError:
                try:
                    await self.refresh()
                except Exception as exc:  # pragma: no cover
                    log.error("rules_refresh_error", error=str(exc))

    async def refresh(self) -> None:
        query = """
        SELECT DISTINCT ON (client_id, resource)
          id::text, client_id, resource, limit_count, window_seconds, updated_at
        FROM rules
        ORDER BY client_id, resource, updated_at DESC;
        """
        async with self._pool.acquire() as conn:
            rows = await conn.fetch(query)

        next_rules: dict[str, dict[str, Rule]] = {}
        for row in rows:
            rule = Rule(
                rule_id=row["id"],
                client_id=row["client_id"],
                resource=row["resource"],
                limit_count=row["limit_count"],
                window_seconds=row["window_seconds"],
            )
            next_rules.setdefault(rule.client_id, {})[rule.resource] = rule

        async with self._lock:
            self._rules = next_rules
        log.info("rules_refreshed", count=len(rows))

    async def get(self, client_id: str, resource: str) -> Rule | None:
        async with self._lock:
            return self._rules.get(client_id, {}).get(resource)


class AuditWriter:
    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool
        self._queue: asyncio.Queue[AuditEvent] = asyncio.Queue(maxsize=1024)

    async def loop(self, stop_event: asyncio.Event) -> None:
        while not stop_event.is_set():
            try:
                event = await asyncio.wait_for(self._queue.get(), timeout=0.5)
            except TimeoutError:
                continue

            try:
                await self._insert(event)
            except Exception as exc:  # pragma: no cover
                log.error(
                    "audit_insert_error",
                    error=str(exc),
                    client_id=event.client_id,
                    resource=event.resource,
                    status=event.status,
                    correlation_id=event.correlation_id,
                )

    async def insert(self, event: AuditEvent) -> None:
        try:
            self._queue.put_nowait(event)
        except asyncio.QueueFull:
            log.error(
                "audit_queue_full",
                client_id=event.client_id,
                resource=event.resource,
                status=event.status,
                correlation_id=event.correlation_id,
            )
            await self._insert(event)

    async def _insert(self, event: AuditEvent) -> None:
        query = """
        INSERT INTO audit_log (client_id, resource, status, remaining_quota, correlation_id)
        VALUES ($1, $2, $3, $4, $5);
        """
        async with self._pool.acquire() as conn:
            await conn.execute(
                query,
                event.client_id,
                event.resource,
                event.status,
                event.remaining_quota,
                event.correlation_id,
            )


class RedisFixedWindowLimiter:
    def __init__(self, client: redis.Redis) -> None:
        self._client = client
        self._script = self._client.register_script(LUA_FIXED_WINDOW)

    async def check(self, client_id: str, resource: str, window_seconds: int) -> tuple[int, int]:
        if window_seconds <= 0:
            raise ValueError("window_seconds must be > 0")

        now = int(time.time())
        bucket = now // window_seconds
        key = f"flowsentinel:{client_id}:{resource}:{bucket}"
        reset_time = (bucket + 1) * window_seconds

        try:
            count = await self._script(keys=[key], args=[window_seconds])
        except Exception:
            REDIS_ERRORS_TOTAL.inc()
            raise
        return int(count), reset_time

    async def health(self) -> tuple[bool, str | None]:
        try:
            await self._client.ping()
            return True, None
        except Exception as exc:
            return False, str(exc)


async def postgres_health(pool: asyncpg.Pool) -> tuple[bool, str | None]:
    try:
        async with pool.acquire() as conn:
            await conn.fetchval("SELECT 1")
        return True, None
    except Exception as exc:
        return False, str(exc)


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = load_settings()
    redis_host, redis_port = parse_redis_addr(settings.redis_addr)

    pg_pool = await asyncpg.create_pool(
        dsn=settings.postgres_dsn,
        min_size=1,
        max_size=10,
        command_timeout=5,
    )
    redis_client = redis.Redis(
        host=redis_host,
        port=redis_port,
        password=settings.redis_password,
        db=settings.redis_db,
        socket_connect_timeout=2,
        socket_timeout=2,
        decode_responses=True,
    )

    rule_cache = RuleCache(pg_pool, settings.rules_refresh_seconds)
    await rule_cache.start()
    limiter = RedisFixedWindowLimiter(redis_client)
    audit_writer = AuditWriter(pg_pool)
    stop_event = asyncio.Event()

    rule_task = asyncio.create_task(rule_cache.loop(stop_event))
    audit_task = asyncio.create_task(audit_writer.loop(stop_event))

    app.state.settings = settings
    app.state.pg_pool = pg_pool
    app.state.redis_client = redis_client
    app.state.rule_cache = rule_cache
    app.state.limiter = limiter
    app.state.audit_writer = audit_writer

    log.info("server_start", listen_port=settings.listen_port)
    try:
        yield
    finally:
        stop_event.set()
        for task in (rule_task, audit_task):
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task
        await redis_client.close()
        await pg_pool.close()
        log.info("server_stop")


app = FastAPI(title="FlowSentinel Python Engine", version=os.getenv("VERSION", "0.1.0"), lifespan=lifespan)


@app.middleware("http")
async def correlation_and_logging(request: Request, call_next):
    start = time.perf_counter()
    cid = request.headers.get("X-Correlation-ID") or generate_correlation_id()
    request.state.correlation_id = cid

    response: Response | None = None
    try:
        response = await call_next(request)
        return response
    finally:
        elapsed = time.perf_counter() - start
        status_code = response.status_code if response is not None else 500
        log.info(
            "request",
            method=request.method,
            path=request.url.path,
            status=status_code,
            latency_ms=elapsed * 1000,
            correlation_id=cid,
        )
        if response is not None:
            response.headers["X-Correlation-ID"] = cid


@app.post("/check", response_model=CheckResponse)
async def check(request: Request, payload: CheckRequest) -> CheckResponse:
    start = time.perf_counter()
    cid = request.state.correlation_id

    if not payload.client_id or not payload.resource or not payload.action:
        REQUESTS_TOTAL.labels(client_id=payload.client_id, resource=payload.resource, status="denied").inc()
        raise HTTPException(status_code=400, detail="client_id, resource, action are required")

    rule = await request.app.state.rule_cache.get(payload.client_id, payload.resource)
    if rule is None:
        REQUESTS_TOTAL.labels(client_id=payload.client_id, resource=payload.resource, status="allowed").inc()
        LATENCY_SECONDS.observe(time.perf_counter() - start)
        response = CheckResponse(allowed=True, remaining_quota=-1, reset_time=0)
        await request.app.state.audit_writer.insert(
            AuditEvent(
                client_id=payload.client_id,
                resource=payload.resource,
                status="allowed",
                remaining_quota=response.remaining_quota,
                correlation_id=cid,
            )
        )
        return response

    try:
        count, reset_time = await request.app.state.limiter.check(
            payload.client_id,
            payload.resource,
            rule.window_seconds,
        )
    except Exception as exc:
        log.error(
            "redis_check_error",
            error=str(exc),
            client_id=payload.client_id,
            resource=payload.resource,
            correlation_id=cid,
        )
        REQUESTS_TOTAL.labels(client_id=payload.client_id, resource=payload.resource, status="denied").inc()
        LATENCY_SECONDS.observe(time.perf_counter() - start)
        await request.app.state.audit_writer.insert(
            AuditEvent(
                client_id=payload.client_id,
                resource=payload.resource,
                status="denied",
                remaining_quota=0,
                correlation_id=cid,
            )
        )
        raise HTTPException(status_code=503, detail="limiter_unavailable")

    allowed = count <= rule.limit_count
    remaining = max(rule.limit_count - count, 0)
    status = "allowed" if allowed else "denied"

    REQUESTS_TOTAL.labels(client_id=payload.client_id, resource=payload.resource, status=status).inc()
    LATENCY_SECONDS.observe(time.perf_counter() - start)

    await request.app.state.audit_writer.insert(
        AuditEvent(
            client_id=payload.client_id,
            resource=payload.resource,
            status=status,
            remaining_quota=remaining,
            correlation_id=cid,
        )
    )
    return CheckResponse(allowed=allowed, remaining_quota=remaining, reset_time=reset_time)


@app.get("/health")
async def health(request: Request) -> dict[str, object]:
    redis_ok, redis_error = await request.app.state.limiter.health()
    postgres_ok, postgres_error = await postgres_health(request.app.state.pg_pool)

    response: dict[str, object] = {
        "status": "ok",
        "redis": {"ok": redis_ok},
        "postgres": {"ok": postgres_ok},
        "correlation_id": request.state.correlation_id,
    }
    redis_section = response["redis"]
    postgres_section = response["postgres"]

    if redis_error is not None and isinstance(redis_section, dict):
        redis_section["error"] = redis_error
    if postgres_error is not None and isinstance(postgres_section, dict):
        postgres_section["error"] = postgres_error

    if not redis_ok or not postgres_ok:
        response["status"] = "degraded"
        return JSONResponse(content=response, status_code=503)

    return response


@app.get("/metrics")
async def metrics() -> Response:
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
