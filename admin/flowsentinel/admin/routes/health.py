from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, status
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession

from flowsentinel.admin.db.session import get_session

router = APIRouter(tags=["health"])


@router.get("/health", status_code=status.HTTP_200_OK)
async def health(
    session: Annotated[AsyncSession, Depends(get_session)],
) -> dict:
    await session.execute(text("SELECT 1"))
    return {"status": "ok", "postgres": {"ok": True}}

