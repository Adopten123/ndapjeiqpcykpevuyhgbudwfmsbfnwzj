"""Детерминированная HTTP-лента трёх учебных источников."""
from hashlib import sha256
from typing import Annotated, Literal

from fastapi import FastAPI, HTTPException, Path, Query


SCENARIOS = ("standard", "critical", "short-ice", "operations")
SOURCES = ("carrier-mock", "rail-mock", "ice-mock")
COMMON_EVENTS = (
    {
        "source": "carrier-mock",
        "event_id": "delay-a",
        "type": "vessel_schedule_changed",
        "available_tick": 1,
        "target_id": "A",
        "reason": "Задержка судна A",
        "parameters": {"load_from_tick": 9, "load_until_tick": 11, "departure_tick": 11},
    },
    {
        "source": "rail-mock",
        "event_id": "repair-oc",
        "type": "rail_edge_closed",
        "available_tick": 2,
        "target_id": "OC",
        "reason": "Ремонт железнодорожного участка OC",
        "parameters": {"from_tick": 2, "until_tick": 12},
    },
)


def scenario_events(scenario):
    if scenario == "standard":
        return COMMON_EVENTS
    return (*COMMON_EVENTS, {
        "source": "ice-mock",
        "event_id": "closure-uv",
        "type": "sea_edge_closed",
        "available_tick": 3,
        "target_id": "UV",
        "reason": "Закрытие пролива UV по ледовой обстановке",
        "parameters": {"from_tick": 8, "until_tick": 20 if scenario == "critical" else 13},
    })


def operations_events(run_id):
    """Вариант ленты воспроизводится по идентификатору прогона после перезапуска."""
    seed = sha256(f"rzd-operations-v1:{run_id}".encode("ascii")).digest()
    delay, repair, ice = scenario_events("short-ice")
    repair = {**repair, "parameters": {"from_tick": 2, "until_tick": 11 + seed[0] % 2}}
    if seed[1] % 2 == 0:
        return delay, repair
    ice = {**ice, "parameters": {"from_tick": 8, "until_tick": 12 + seed[2] % 2}}
    return delay, repair, ice


def page_events(events, run_id, through_tick, cursor, limit):
    """Курсор указывает число доставленных сообщений в неизменяемой ленте."""
    ordered = sorted(events, key=lambda event: (event["available_tick"], event["source"], event["event_id"]))
    if cursor > len(ordered) or (cursor and ordered[cursor - 1]["available_tick"] > through_tick):
        raise HTTPException(status_code=422, detail="Курсор не соответствует запрошенной границе ленты")

    available = [event for event in ordered[cursor:] if event["available_tick"] <= through_tick]
    page = available[:limit]
    next_cursor = cursor + len(page)
    next_tick = ordered[next_cursor]["available_tick"] if next_cursor < len(ordered) else None
    has_more = len(available) > limit
    # Нельзя гарантировать полноту частично возвращённого шага, включая случаи,
    # когда соседние страницы содержат события одного и того же шага.
    complete_through_tick = next_tick - 1 if has_more else through_tick
    return {
        "events": [{"run_id": run_id, **event} for event in page],
        "next_cursor": str(next_cursor),
        "complete_through_tick": complete_through_tick,
        "next_tick": next_tick,
        "has_more": has_more,
    }


app = FastAPI(title="Учебные источники «РЖД Арктика»", docs_url=None, redoc_url=None, openapi_url=None)


@app.get("/health")
def health():
    return {"status": "ok", "mode": "training", "sources": SOURCES, "scenarios": SCENARIOS}


@app.get("/v1/runs/{run_id}/events")
def events_for_run(
    run_id: Annotated[str, Path(pattern=r"^[a-zA-Z0-9_-]{1,64}$")],
    through_tick: Annotated[int, Query(ge=0, le=30)],
    after: Annotated[str, Query(pattern=r"^(?:0|[1-9][0-9]{0,2})?$")] = "0",
    scenario: Literal["standard", "critical", "short-ice", "operations"] = "standard",
    limit: Annotated[int, Query(ge=1, le=100)] = 100,
):
    events = operations_events(run_id) if scenario == "operations" else scenario_events(scenario)
    return page_events(events, run_id, through_tick, int(after or "0"), limit)
