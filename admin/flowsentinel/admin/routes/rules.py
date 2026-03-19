from __future__ import annotations

import datetime as dt
import uuid
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Response, status
from pydantic import BaseModel, Field
from sqlalchemy import select, update, delete
from sqlalchemy.ext.asyncio import AsyncSession

from flowsentinel.admin.db.session import get_session
from flowsentinel.admin.models.tables import Rule

router = APIRouter(prefix="/rules", tags=["rules"])


class RuleCreate(BaseModel):
    client_id: str = Field(min_length=1)
    resource: str = Field(min_length=1)
    limit: int = Field(ge=1)
    window_seconds: int = Field(ge=1)


class RuleUpdate(BaseModel):
    client_id: str | None = Field(default=None, min_length=1)
    resource: str | None = Field(default=None, min_length=1)
    limit: int | None = Field(default=None, ge=1)
    window_seconds: int | None = Field(default=None, ge=1)


class RuleOut(BaseModel):
    id: uuid.UUID
    client_id: str
    resource: str
    limit_count: int
    window_seconds: int
    created_at: dt.datetime
    updated_at: dt.datetime

    model_config = {"from_attributes": True}


@router.post("", response_model=RuleOut, status_code=status.HTTP_201_CREATED)
async def create_rule(
    payload: RuleCreate,
    session: Annotated[AsyncSession, Depends(get_session)],
) -> RuleOut:
    now = dt.datetime.utcnow()
    rule = Rule(
        id=uuid.uuid4(),
        client_id=payload.client_id,
        resource=payload.resource,
        limit_count=payload.limit,
        window_seconds=payload.window_seconds,
        created_at=now,
        updated_at=now,
    )
    session.add(rule)
    await session.commit()
    await session.refresh(rule)
    return RuleOut.model_validate(rule)


@router.get("", response_model=list[RuleOut])
async def list_rules(
    session: Annotated[AsyncSession, Depends(get_session)],
) -> list[RuleOut]:
    res = await session.execute(select(Rule).order_by(Rule.updated_at.desc()))
    rules = res.scalars().all()
    return [RuleOut.model_validate(r) for r in rules]


@router.put("/{rule_id}", response_model=RuleOut)
async def update_rule(
    rule_id: uuid.UUID,
    payload: RuleUpdate,
    session: Annotated[AsyncSession, Depends(get_session)],
) -> RuleOut:
    rule = await session.get(Rule, rule_id)
    if rule is None:
        raise HTTPException(status_code=404, detail="rule_not_found")

    if payload.client_id is not None:
        rule.client_id = payload.client_id
    if payload.resource is not None:
        rule.resource = payload.resource
    if payload.limit is not None:
        rule.limit_count = payload.limit
    if payload.window_seconds is not None:
        rule.window_seconds = payload.window_seconds
    rule.updated_at = dt.datetime.utcnow()

    await session.commit()
    await session.refresh(rule)
    return RuleOut.model_validate(rule)



@router.delete("/{rule_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_rule(
    rule_id: uuid.UUID,
    session: Annotated[AsyncSession, Depends(get_session)],
) -> Response:
    rule = await session.get(Rule, rule_id)
    if rule is None:
        raise HTTPException(status_code=404, detail="rule_not_found")
    await session.delete(rule)
    await session.commit()
    return Response(status_code=status.HTTP_204_NO_CONTENT)
