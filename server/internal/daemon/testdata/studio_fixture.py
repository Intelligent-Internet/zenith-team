"""Disposable P1d HTTP fixture using Studio's production auth and domain owners.

Run with the Studio source on PYTHONPATH and its uv environment. The database
must be newly owned by this test; this script creates schema and synthetic rows.
No default application configuration or user credential home is used.
"""

import argparse
import asyncio
import hashlib
import json
import os
from pathlib import Path
import socket
from types import SimpleNamespace
import uuid
from datetime import datetime, timedelta, timezone

os.environ["CELERY_BROKER_URL"] = "memory://"
os.environ["CELERY_RESULT_BACKEND"] = "cache+memory://"
os.environ["REDIS_SESSION_ENABLED"] = "false"

import uvicorn
from fastapi import FastAPI
from sqlalchemy.ext.asyncio import create_async_engine, async_sessionmaker
from ii_agent.core.db.base import Base
from ii_agent.workers.celery.model_imports import import_model_modules

from ii_agent.core.serialization import json_serializer
from ii_agent.core.config.settings import get_settings
from ii_agent.core.dependencies import _db_session_dependency, _get_container
from ii_agent.core.exceptions import IIAgentError
from ii_agent.core.middleware.exception_handler import ii_agent_error_handler
from ii_agent.auth.cli_router import router as auth_router
from ii_agent.auth.cli_service import CliAuthService, _hash_secret
from ii_agent.auth.cli_repository import CliSessionRepository
from ii_agent.auth.cli_tokens import CliAccessTokenCodec
from ii_agent.auth.cli_types import CLI_CLIENT_ID
from ii_agent.factory.router import router as factory_router
from ii_agent.factory.external_media.router import router as media_router
from ii_agent.factory.project_service import FactoryProjectService
from ii_agent.factory.node_service import FactoryNodeService
from ii_agent.factory.edge_service import FactoryEdgeService
from ii_agent.factory.member_service import FactoryMemberService
from ii_agent.factory.repository import (
    FactoryProjectRepository,
    FactoryProjectMemberRepository,
    FactoryNodeRepository,
    FactoryEdgeRepository,
)
from ii_agent.factory.models import FactoryNode, FactoryProject, FactoryProjectMember
from ii_agent.audit.repository import AuditEventRepository
from ii_agent.audit.service import AuditService
from ii_agent.users.models import User
from ii_agent.credits.service import CreditService
from ii_agent.credits.repository import CreditBalanceRepository, CreditTransactionRepository
from ii_agent.credits.models import CreditBalance
import ii_agent.admin.audit.models  # noqa: F401 - registers the audit FK target
import ii_agent.factory.actions.node as node_actions


async def main():
    import_model_modules()
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--company", required=True)
    parser.add_argument("--agent", required=True)
    args = parser.parse_args()
    database = os.environ["STUDIO_P1D_TEST_DATABASE_URL"]
    if "studio_p1d_fixture" not in database:
        raise RuntimeError("requires the explicitly named disposable database")
    root = Path(args.root)
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    origin = f"http://127.0.0.1:{sock.getsockname()[1]}"
    settings = SimpleNamespace(
        jwt_secret_key="p1d-disposable-fixture-secret",
        ii_issuer="https://fixture.invalid",
        access_token_expire_minutes=15,
        refresh_token_expire_days=7,
        oauth=SimpleNamespace(auth_cookie_name="ii_auth"),
    )
    engine = create_async_engine(
        database, json_serializer=lambda v: json.dumps(v, default=json_serializer)
    )
    async with engine.begin() as db:
        await db.run_sync(Base.metadata.create_all)
    sessions = async_sessionmaker(engine, expire_on_commit=False)
    service = CliAuthService(settings)
    user = User(
        id=uuid.uuid4(),
        email=f"{uuid.uuid4()}@fixture.invalid",
        first_name="Fixture",
        last_name="P1d",
        role="user",
        is_active=True,
        email_verified=True,
        language="en",
    )
    sid = uuid.uuid4()
    refresh = service._new_refresh_token(sid)
    scopes = ("factory.media.read", "factory.media.generate")
    project = FactoryProject(id=uuid.uuid4(), title="P1d fixture")
    node = FactoryNode(
        id=uuid.uuid4(),
        project_id=project.id,
        created_by=user.id,
        node_type="node_group",
        label="fixture",
        metadata_={"position": {"x": 0, "y": 0}},
    )
    async with sessions() as db:
        db.add_all([user, project])
        await db.flush()
        db.add(CreditBalance(user_id=user.id, credits=12, bonus_credits=3))
        db.add_all(
            [FactoryProjectMember(project_id=project.id, user_id=user.id, role="owner"), node]
        )
        await CliSessionRepository().create(
            db,
            session_id=sid,
            user_id=user.id,
            client_id=CLI_CLIENT_ID,
            refresh_token_hash=_hash_secret(refresh),
            scope=" ".join(scopes),
            expires_at=datetime.now(timezone.utc) + timedelta(days=1),
        )
        await db.commit()
    token, _ = CliAccessTokenCodec(settings).issue(
        user_id=user.id, session_id=sid, client_id=CLI_CLIENT_ID, scopes=scopes
    )
    home = (
        root
        / "accounts"
        / "companies"
        / args.company
        / "agents"
        / args.agent
        / "tools"
        / "zenith-studio"
        / "origins"
        / hashlib.sha256(origin.encode()).hexdigest()
    )
    home.mkdir(parents=True, mode=0o700)
    credential = home / "credentials.json"
    credential.write_text(
        json.dumps(
            {
                "version": 2,
                "credential": {
                    "client_id": CLI_CLIENT_ID,
                    "connection_id": str(uuid.uuid4()),
                    "api_url": origin,
                    "access_token": token,
                    "refresh_token": refresh,
                    "expires_at": (datetime.now(timezone.utc) + timedelta(minutes=10)).isoformat(),
                    "scope": scopes,
                    "user": {"id": str(user.id), "email": user.email},
                },
            }
        )
    )
    credential.chmod(0o600)
    # Additional disposable grants exercise real account/scope transitions.
    other = User(
        id=uuid.uuid4(),
        email=f"{uuid.uuid4()}@fixture.invalid",
        first_name="Other",
        last_name="P1d",
        role="user",
        is_active=True,
        email_verified=True,
        language="en",
    )
    async with sessions() as db:
        db.add(other)
        await db.commit()
    for name, account, granted in (
        ("other-account", other, scopes),
        ("collaborator", other, scopes),
        ("read-scope", user, ("factory.media.read",)),
    ):
        grant_id = uuid.uuid4()
        grant_refresh = service._new_refresh_token(grant_id)
        async with sessions() as db:
            await CliSessionRepository().create(
                db,
                session_id=grant_id,
                user_id=account.id,
                client_id=CLI_CLIENT_ID,
                refresh_token_hash=_hash_secret(grant_refresh),
                scope=" ".join(granted),
                expires_at=datetime.now(timezone.utc) + timedelta(days=1),
            )
            await db.commit()
        access, _ = CliAccessTokenCodec(settings).issue(
            user_id=account.id, session_id=grant_id, client_id=CLI_CLIENT_ID, scopes=granted
        )
        record = json.loads(credential.read_text())
        record["credential"].update(
            connection_id=str(uuid.uuid4()),
            access_token=access,
            refresh_token=grant_refresh,
            scope=granted,
            user={"id": str(account.id), "email": account.email},
        )
        target = root / f"{name}.json"
        target.write_text(json.dumps(record))
        target.chmod(0o600)
    nodes, edges, members = (
        FactoryNodeRepository(),
        FactoryEdgeRepository(),
        FactoryProjectMemberRepository(),
    )
    projects = FactoryProjectService(
        project_repo=FactoryProjectRepository(),
        member_repo=members,
        node_repo=nodes,
        edge_repo=edges,
    )

    async def no_realtime(*args, **kwargs):
        pass

    async def no_files(*args, **kwargs):
        return {}

    # P1d does not certify realtime delivery or binary file signing. Group-only
    # graphs avoid file URLs; all auth, ACL, receipt and position owners are real.
    node_actions.emit_node_updated = no_realtime
    node_actions.emit_history_if_needed = no_realtime
    # Assert that this process can never enqueue or dispatch paid generation.
    import ii_agent.factory.node_service as node_owner
    import ii_agent.factory.dispatchers as dispatchers
    import ii_agent.factory.actions.execute as execution_actions

    def forbid_queue(*args, **kwargs):
        raise AssertionError("No paid job may be queued by the control fixture")

    async def forbid_provider(*args, **kwargs):
        raise AssertionError("No provider may be called by the control fixture")

    async def metadata_without_signing(metadata):
        return metadata

    node_owner.queue_factory_media_job_task = forbid_queue
    dispatchers.dispatch_factory_node = forbid_provider
    execution_actions.hydrate_media_metadata = metadata_without_signing
    execution_actions.emit_history_if_needed = no_realtime
    container = SimpleNamespace(
        file_service=SimpleNamespace(resolve_urls_by_storage_path=no_files),
        factory_project_service=projects,
        factory_node_service=FactoryNodeService(
            node_repo=nodes, edge_repo=edges, project_service=projects
        ),
        factory_edge_service=FactoryEdgeService(edge_repo=edges, node_repo=nodes),
        factory_member_service=FactoryMemberService(member_repo=members),
        audit_service=AuditService(repo=AuditEventRepository()),
        credit_service=CreditService(
            balance_repo=CreditBalanceRepository(),
            transaction_repo=CreditTransactionRepository(),
            config=settings,
        ),
    )
    app = FastAPI()
    app.exception_handler(IIAgentError)(ii_agent_error_handler)
    app.include_router(auth_router)
    app.include_router(factory_router, prefix="/v1")
    app.include_router(media_router, prefix="/v1")
    # Expose only production endpoints needed by this fixture. No generation.
    allowed = (
        "/auth/cli/token",
        "/v1/factory/media/access",
        "/v1/factory/media/models",
        "/v1/factory/media/entitlement",
        "/v1/factory/media/projects/{project_id}/nodes/{node_id}/plan",
        "/v1/factory/media/projects/{project_id}/executions",
        "/v1/factory/media/projects/{project_id}/executions/{request_id}",
        "/v1/factory/media/projects/{project_id}/executions/{request_id}/cancel",
        "/v1/factory/media/projects/{project_id}/executions/{request_id}/output-url",
        "/v1/factory/media/projects",
        "/v1/factory/project-reservations/{namespace}/{reservation_id}",
        "/v1/factory/projects/{project_id}/bootstrap",
        "/v1/factory/nodes/{node_id}/position",
        "/v1/factory/nodes",
        "/v1/factory/nodes/{node_id}",
        "/v1/factory/edges",
        "/v1/factory/edges/{edge_id}",
        "/v1/factory/projects/{project_id}/layout",
    )
    app.router.routes[:] = [
        r
        for r in app.router.routes
        if getattr(r, "path", None) in allowed
    ]

    async def db_dependency():
        async with sessions() as db:
            try:
                yield db
                await db.commit()
            except BaseException:
                await db.rollback()
                raise

    app.dependency_overrides[_db_session_dependency] = db_dependency
    app.dependency_overrides[_get_container] = lambda: container
    app.dependency_overrides[get_settings] = lambda: settings
    (root / "ready.json").write_text(
        json.dumps(
            {
                "pid": os.getpid(),
                "origin": origin,
                "accountId": str(user.id),
                "projectId": str(project.id),
                "nodeId": str(node.id),
            }
        )
    )
    await uvicorn.Server(uvicorn.Config(app, log_level="error", access_log=False)).serve(
        sockets=[sock]
    )
    await engine.dispose()


asyncio.run(main())
