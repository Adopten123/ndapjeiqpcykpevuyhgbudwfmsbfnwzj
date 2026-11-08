"""Внутренний HTTP-адаптер ограниченного учебного планировщика."""
from __future__ import annotations

import asyncio
import copy
import importlib.util
import json
import logging
import math
from pathlib import Path
import sys
import threading
import time
from typing import Any, Literal
from uuid import UUID

import simulation
import workflow

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict, field_validator
from starlette.exceptions import HTTPException as StarletteHTTPException

ROOT = Path(__file__).resolve().parent
PROFILE = "research-demo.v1"
MAX_REQUEST_BYTES = 1024 * 1024
BODY_TIMEOUT_SECONDS = 5
logger = logging.getLogger("rzd.compute")


def _research_module(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / "planner" / filename)
    if spec is None or spec.loader is None:
        raise RuntimeError("research module unavailable")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


planner = _research_module("rzd_research_planner", "solver.py")
checker = _research_module("rzd_research_checker", "check_demo.py")
demo = _research_module("rzd_research_scenarios", "run_demo.py")


def error(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse(status_code=status, content={"error": {"code": code, "message": message}})


def _reject_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _reject_constant(_):
    raise ValueError("non-finite JSON number")


def _finite_float(value):
    result = float(value)
    if not math.isfinite(result):
        raise ValueError("non-finite JSON number")
    return result


def _integer_profile_numbers(value):
    # В этом профиле все числовые параметры — целые шаги/слоты.
    if isinstance(value, float):
        raise planner.UnsupportedInput("floating point parameter outside HTTP demo profile")
    if isinstance(value, dict):
        for child in value.values():
            _integer_profile_numbers(child)
    elif isinstance(value, list):
        for child in value:
            _integer_profile_numbers(child)


class JSONGuard:
    """Ограничить тело до декодирования и не принимать неоднозначный JSON."""

    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or scope["method"] != "POST":
            await self.app(scope, receive, send)
            return
        headers = {name.lower(): value for name, value in scope["headers"]}
        media_type = headers.get(b"content-type", b"").split(b";", 1)[0].strip().lower()
        if media_type != b"application/json" or headers.get(b"content-encoding", b"identity").lower() != b"identity":
            await error(415, "unsupported_media_type", "Ожидается несжатый application/json.")(scope, receive, send)
            return
        try:
            length = int(headers.get(b"content-length", b"0"))
        except ValueError:
            await error(400, "invalid_request", "Некорректный размер запроса.")(scope, receive, send)
            return
        if length < 0:
            await error(400, "invalid_request", "Некорректный размер запроса.")(scope, receive, send)
            return
        if length > MAX_REQUEST_BYTES:
            await error(413, "request_too_large", "Превышен допустимый размер учебного запроса.")(scope, receive, send)
            return
        body = bytearray()
        deadline = time.monotonic() + BODY_TIMEOUT_SECONDS
        while True:
            try:
                message = await asyncio.wait_for(receive(), timeout=max(0, deadline - time.monotonic()))
            except TimeoutError:
                await error(408, "request_timeout", "Тело запроса не получено вовремя.")(scope, receive, send)
                return
            if message["type"] == "http.disconnect":
                return
            if message["type"] != "http.request":
                await error(400, "invalid_request", "Некорректное тело запроса.")(scope, receive, send)
                return
            body.extend(message.get("body", b""))
            if len(body) > MAX_REQUEST_BYTES:
                await error(413, "request_too_large", "Превышен допустимый размер учебного запроса.")(scope, receive, send)
                return
            if not message.get("more_body", False):
                break
        try:
            parsed = json.loads(body.decode("utf-8"), object_pairs_hook=_reject_duplicates, parse_constant=_reject_constant, parse_float=_finite_float)
            json.dumps(parsed, ensure_ascii=False).encode("utf-8")
        except (ValueError, UnicodeError, RecursionError):
            await error(400, "invalid_json", "Ожидается однозначный JSON в UTF-8.")(scope, receive, send)
            return
        replayed = False

        async def replay():
            nonlocal replayed
            if not replayed:
                replayed = True
                return {"type": "http.request", "body": bytes(body), "more_body": False}
            return await receive()

        await self.app(scope, replay, send)


class SolveRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, hide_input_in_errors=True)
    request_id: str
    engine_profile: Literal[PROFILE, workflow.PROFILE, simulation.PROFILE]
    payload: dict[str, Any]

    @field_validator("request_id")
    @classmethod
    def canonical_uuid(cls, value: str) -> str:
        if len(value) != 36 or str(UUID(value)) != value:
            raise ValueError("canonical UUID required")
        return value


def create_app() -> FastAPI:
    # Ошибка импорта останавливает запуск вместо ложного healthy-состояния.
    import ortools
    from ortools.sat.python import cp_model

    cp_model.CpSolver()
    scenarios = demo.scenarios()
    for payload in scenarios.values():
        planner.check_profile(payload)
    names = {"S0": "Первый план", "S1": "Задержка судна", "S2": "Закрытие пролива", "S3": "Ремонт пути"}
    gate = threading.BoundedSemaphore(1)
    app = FastAPI(title="РЖД Арктика — внутренний учебный расчёт", docs_url=None, redoc_url=None, openapi_url=None)
    app.add_middleware(JSONGuard)

    @app.exception_handler(RequestValidationError)
    async def invalid_request(_: Request, __: RequestValidationError):
        # Не возвращать значения полей из стандартного validation error.
        return error(422, "invalid_request", "Некорректная структура учебного запроса.")

    @app.exception_handler(StarletteHTTPException)
    async def protocol_error(_: Request, exc: StarletteHTTPException):
        code = "not_found" if exc.status_code == 404 else "method_not_allowed" if exc.status_code == 405 else "http_error"
        response = error(exc.status_code, code, "Операция расчётного сервиса недоступна.")
        response.headers.update(exc.headers or {})
        return response

    @app.get("/health")
    async def health():
        return {"status": "ok", "engine_profile": PROFILE, "engine_profiles": [PROFILE, workflow.PROFILE, simulation.PROFILE], "data_mode": "demo", "ortools_version": ortools.__version__}

    @app.get("/v1/demo/scenarios")
    async def list_scenarios():
        return [{"scenario_id": ident, "name": names[ident], "mode": payload["mode"], "engine_profile": PROFILE,
                 "snapshot_id": payload["snapshot_id"], "input_sha256": planner.canonical_hash(payload)}
                for ident, payload in scenarios.items()]

    @app.get("/v1/demo/scenarios/{scenario_id}")
    async def scenario_detail(scenario_id: str):
        if scenario_id not in scenarios:
            return error(404, "scenario_not_found", "Учебный сценарий не найден.")
        payload = copy.deepcopy(scenarios[scenario_id])
        return {"scenario_id": scenario_id, "engine_profile": PROFILE, "input_sha256": planner.canonical_hash(payload),
                "snapshot_id": payload["snapshot_id"], "payload": payload}

    @app.post("/v1/research/solve")
    def solve_request(request: SolveRequest):
        if not gate.acquire(blocking=False):
            return error(503, "compute_busy", "Расчётный процесс занят. Запрос можно повторить.")
        try:
            payload = copy.deepcopy(request.payload)
            _integer_profile_numbers(payload)
            if request.engine_profile == simulation.PROFILE:
                result = simulation.solve(payload, source=scenarios["S1"], planner=planner, checker=checker)
                violations = simulation.validate(payload, result, source=scenarios["S1"], planner=planner, checker=checker)
            elif request.engine_profile == workflow.PROFILE:
                result = workflow.solve(payload, planner=planner, checker=checker)
                violations = workflow.validate(payload, result, checker=checker)
            else:
                result = planner.solve(payload)
                violations = checker.validate(payload, result)
            if violations:
                logger.error("Candidate validation failed; request_id=%s", request.request_id)
                return error(500, "invalid_solver_result", "Результат не прошёл независимую проверку.")
            return {"request_id": request.request_id, "engine_profile": request.engine_profile,
                    "input_sha256": planner.canonical_hash(payload), "result": result, "validation_errors": []}
        except planner.UnsupportedInput:
            return error(422, "unsupported_or_invalid_input", "Вход не соответствует ограниченному учебному профилю.")
        except Exception:
            logger.error("Compute request failed; request_id=%s", request.request_id)
            return error(500, "compute_failed", "Не удалось завершить учебный расчёт.")
        finally:
            gate.release()

    return app
