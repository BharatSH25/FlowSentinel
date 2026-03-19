from __future__ import annotations

import os
import time
import uuid

import structlog
from fastapi import FastAPI, Request, Response
from prometheus_client import Counter, Histogram, make_asgi_app

from flowsentinel.admin.routes.audit import router as audit_router
from flowsentinel.admin.routes.health import router as health_router
from flowsentinel.admin.routes.rules import router as rules_router


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
    "flowsentinel_admin_requests_total",
    "Total number of admin API requests.",
    labelnames=["path", "method", "status"],
)
LATENCY_SECONDS = Histogram(
    "flowsentinel_admin_latency_seconds",
    "Admin API request latency in seconds.",
    labelnames=["path", "method"],
)


def get_or_create_correlation_id(request: Request) -> str:
    cid = request.headers.get("X-Correlation-ID")
    if cid:
        return cid
    return uuid.uuid4().hex


app = FastAPI(title="FlowSentinel Admin API", version=os.getenv("VERSION", "0.1.0"))
app.include_router(rules_router)
app.include_router(audit_router)
app.include_router(health_router)

metrics_app = make_asgi_app()
app.mount("/metrics", metrics_app)


@app.middleware("http")
async def correlation_and_logging(request: Request, call_next):
    start = time.perf_counter()
    cid = get_or_create_correlation_id(request)
    request.state.correlation_id = cid

    try:
        response: Response = await call_next(request)
    finally:
        elapsed = time.perf_counter() - start
        status_code = getattr(locals().get("response", None), "status_code", 500)
        REQUESTS_TOTAL.labels(path=request.url.path, method=request.method, status=str(status_code)).inc()
        LATENCY_SECONDS.labels(path=request.url.path, method=request.method).observe(elapsed)
        log.info(
            "request",
            method=request.method,
            path=request.url.path,
            status=status_code,
            latency_seconds=elapsed,
            correlation_id=cid,
        )

    response.headers["X-Correlation-ID"] = cid
    return response

