from __future__ import annotations

import datetime as dt
import uuid
from typing import Annotated

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from flowsentinel.admin.db.session import get_session
from flowsentinel.admin.models.tables import AuditLog

router = APIRouter(prefix="/audit", tags=["audit"])


class AuditOut(BaseModel):
    id: uuid.UUID
    client_id: str
    resource: str
    status: str
    timestamp: dt.datetime
    remaining_quota: int | None
    correlation_id: str | None

    model_config = {"from_attributes": True}


@router.get("", response_model=list[AuditOut])
async def list_audit(
    session: Annotated[AsyncSession, Depends(get_session)],
    limit: int = Query(default=100, ge=1, le=1000),
) -> list[AuditOut]:
    res = await session.execute(select(AuditLog).order_by(AuditLog.timestamp.desc()).limit(limit))
    rows = res.scalars().all()
    return [AuditOut.model_validate(r) for r in rows]
