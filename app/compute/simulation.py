"""Адаптер учебного графа S1 для абсолютного времени и сохранённых операций."""
from __future__ import annotations

import copy
from functools import lru_cache

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

import workflow

PROFILE = "simulation-demo.v1"
HORIZON = 30
EVENT_SOURCES = {
    "rail_edge_closed": "rail-mock",
    "sea_edge_closed": "ice-mock",
    "vessel_schedule_changed": "carrier-mock",
}
CLOSURE_MODES = {"rail_edge_closed": "rail", "sea_edge_closed": "sea"}
PAYLOAD_FIELDS = {
    "run_id", "snapshot_id", "calculation_id", "attempt_id", "decision_tick",
    "condition_revision", "state_version", "plan_version", "baseline", "events",
    "fixed_operations",
}
EVENT_FIELDS = {"run_id", "event_id", "source", "type", "available_tick", "target_id", "reason", "parameters"}


def _tick(value):
    return type(value) is int and 0 <= value <= HORIZON


def _text(value, limit=128):
    return isinstance(value, str) and bool(value.strip()) and len(value) <= limit


def _received(event):
    return event.get("received_tick", event["available_tick"])


def _check_event(event, payload, services, edges, planner):
    if not isinstance(event, dict) or EVENT_FIELDS - event.keys() or event.keys() - (EVENT_FIELDS | {"received_tick"}):
        raise planner.UnsupportedInput("invalid simulation event fields")
    kind, params = event["type"], event["parameters"]
    if not isinstance(kind, str) or kind not in EVENT_SOURCES or event["source"] not in (EVENT_SOURCES[kind], "manual"):
        raise planner.UnsupportedInput("invalid simulation event type/source")
    if (event["run_id"] != payload["run_id"] or not _text(event["event_id"]) or not _text(event["reason"], 500)
            or not _text(event["target_id"]) or not _tick(event["available_tick"]) or not _tick(_received(event))
            or not event["available_tick"] <= _received(event) <= payload["decision_tick"]):
        raise planner.UnsupportedInput("invalid simulation event identity or release time")
    if not isinstance(params, dict) or not all(_tick(value) for value in params.values()):
        raise planner.UnsupportedInput("invalid simulation event parameters")
    target = event["target_id"]
    if kind in CLOSURE_MODES:
        if (target not in edges or edges[target]["mode"] != CLOSURE_MODES[kind]
                or set(params) != {"from_tick", "until_tick"} or not params["from_tick"] < params["until_tick"]):
            raise planner.UnsupportedInput("invalid closure target or interval")
    elif (target not in services or set(params) != {"load_from_tick", "load_until_tick", "departure_tick"}
          or not params["load_from_tick"] < params["load_until_tick"] <= params["departure_tick"]):
        raise planner.UnsupportedInput("invalid schedule target, load window or departure")


def _check_fixed(payload, operations, planner):
    times = payload["baseline"]["plan"]["scheduled_operations"]
    now = payload["decision_tick"]
    expected = {
        row["operation_id"]: {**row, "state": "completed" if row["end_tick"] <= now else "in_progress"}
        for row in times
        if row["start_tick"] < now or operations[row["operation_id"]]["state"] != "planned"
    }
    actual = planner.index(payload["fixed_operations"], "operation_id")
    for fact in actual.values():
        if type(fact.get("start_tick")) is not int or type(fact.get("end_tick")) is not int:
            raise planner.UnsupportedInput("simulation fact times must be integers")
    if actual != expected:
        raise planner.UnsupportedInput("simulation facts differ from the history of the active plan")
    return actual


def _conflict(event, operation, reason):
    return {"event_id": event["event_id"], "source": event["source"], "operation_id": operation, "reason": reason}


@lru_cache(maxsize=4)
def _subset_baseline_schema(planner, count):
    folder = planner.ROOT / "contracts"
    request = planner.load(folder / "planning-modes/replan-request.schema.json")
    common = planner.load(folder / "replan-v1/common.schema.json")
    common["$defs"]["plan"]["properties"]["party_plans"].update(minItems=count, maxItems=count)
    registry = Registry().with_resource(common["$id"], Resource.from_contents(common))
    fields = {key: request["properties"][key] for key in ("snapshot_id", "calculation_id", "attempt_id", "baseline")}
    return Draft202012Validator({"properties": fields}, registry=registry, format_checker=FormatChecker())


def _select_parties(p, payload, planner):
    known = set(planner.index(p["parties"]))
    requested = payload.get("party_ids", list(known))
    if (not isinstance(requested, list) or not 1 <= len(requested) <= len(known)
            or any(not isinstance(ident, str) or ident not in known for ident in requested)
            or len(set(requested)) != len(requested)):
        raise planner.UnsupportedInput("invalid simulation party selection")
    chosen = set(requested)
    if chosen == known:
        return False
    for key in ("containers", "route_candidates", "operations", "facts", "controllable_groups", "reservations",
                "receipt_constraints"):
        p[key] = [row for row in p[key] if row["party_id"] in chosen]
    p["parties"] = [row for row in p["parties"] if row["id"] in chosen]
    p["transport_groups"] = [row for row in p["transport_groups"] if chosen.issuperset(row["party_ids"])]
    operations = planner.index(p["operations"])
    p["commitments"] = [row for row in p["commitments"]
                        if row["kind"] != "operation_start_window" or row["operation_id"] in operations]
    return True


def _prepare(payload, source, planner, checker):
    if (not isinstance(payload, dict) or set(payload) - {"party_ids"} != PAYLOAD_FIELDS
            or any(not _text(payload[key]) for key in ("run_id", "snapshot_id", "calculation_id", "attempt_id"))
            or not _tick(payload["decision_tick"])
            or any(type(payload[key]) is not int or payload[key] < 1 for key in ("condition_revision", "state_version", "plan_version"))
            or not isinstance(payload["events"], list) or not isinstance(payload["fixed_operations"], list)):
        raise planner.UnsupportedInput("invalid simulation snapshot")
    p = copy.deepcopy(source)
    p.update({key: payload[key] for key in ("snapshot_id", "calculation_id", "attempt_id", "baseline")})
    try:
        # Базовый план проверяется по графу. Его сроки могли быть приняты при других условиях.
        if _select_parties(p, payload, planner):
            if not _subset_baseline_schema(planner, len(p["parties"])).is_valid(p):
                raise planner.UnsupportedInput("invalid simulation subset baseline structure")
            workflow.check_baseline(p, planner, checker)
        else:
            workflow.check_profile(p, planner=planner, checker=checker)
        operations = planner.index(p["operations"])
        fixed = _check_fixed(payload, operations, planner)
    except (KeyError, TypeError, IndexError) as exc:
        raise planner.UnsupportedInput("malformed simulation baseline/facts") from exc

    edges, services = planner.index(p["edges"]), planner.index(p["services"])
    seen = set()
    for event in payload["events"]:
        _check_event(event, payload, services, edges, planner)
        key = (event["source"], event["event_id"])
        if key in seen:
            raise planner.UnsupportedInput("duplicate event in simulation snapshot")
        seen.add(key)

    events = sorted(payload["events"], key=lambda event: (_received(event), event["available_tick"], event["source"], event["event_id"]))
    conflicts, schedules = [], {}
    for n, event in enumerate(events, 1):
        kind, target, params, released = event["type"], event["target_id"], event["parameters"], _received(event)
        if kind == "vessel_schedule_changed":
            key = (released, target)
            if schedules.setdefault(key, params) != params:
                conflicts.append(_conflict(event, None, "conflicting_service_schedule"))
            for ident, fact in fixed.items():
                op = operations[ident]
                if op["kind"] == "load" and op["service_id"] == target and fact["start_tick"] < released:
                    conflicts.append(_conflict(event, ident, "loading_already_started"))
            replacement = {
                "load_window": {"start_tick": params["load_from_tick"], "end_tick": params["load_until_tick"]},
                "departure_tick": params["departure_tick"],
            }
            services[target].update(replacement)
            for commitment in p["commitments"]:
                if commitment.get("service_id") == target:
                    commitment.update(replacement)
        else:
            for ident, fact in fixed.items():
                if (fact["state"] == "in_progress" and operations[ident]["edge_id"] == target
                        and fact["start_tick"] < params["until_tick"] and params["from_tick"] < fact["end_tick"]):
                    conflicts.append(_conflict(event, ident, "movement_already_started"))
            start, end = max(params["from_tick"], released), params["until_tick"]
            if start < end:
                p["restrictions"].append({
                    "id": f"SIM-CLOSURE-{n}", "edge_id": target, "start_tick": start,
                    "end": {"kind": "known", "end_tick": end}, "confirmation": "confirmed",
                    "evidence_ids": ["DEMO-CASE"],
                })
    return p, conflicts


def solve(payload, *, source, planner, checker, time_limit_seconds=None):
    p, conflicts = _prepare(payload, source, planner, checker)
    if conflicts:
        return {
            "engine_profile": PROFILE, "execution": "not_executed", "synthetic": True, "mode": "replan",
            **{key: payload[key] for key in ("snapshot_id", "calculation_id", "attempt_id")},
            "input_hash_format": "sha256-canonical-json-sort-keys-utf8", "input_sha256": planner.canonical_hash(payload),
            "solver_status": "CONFLICT", "conflicts": conflicts, "solver_invocations": 0, "proven_prefix": 0,
            "candidate": None, "objective": None, "group_decisions": [], "resolved_reservations": [],
            "changed_group_ids": [], "optimization_stages": [],
        }
    return planner._solve_checked_profile(p, time_limit_seconds, engine_profile=PROFILE, simulation=payload)


def validate(payload, result, *, source, planner, checker):
    p, conflicts = _prepare(payload, source, planner, checker)
    if conflicts:
        fields = ("snapshot_id", "calculation_id", "attempt_id")
        if (result.get("engine_profile") != PROFILE or result.get("solver_status") != "CONFLICT"
                or result.get("conflicts") != conflicts or result.get("candidate") is not None
                or any(result.get(key) != payload[key] for key in fields)
                or result.get("input_sha256") != planner.canonical_hash(payload)):
            return ["invalid simulation conflict result"]
        return []
    return checker.validate(p, result, engine_profile=PROFILE, simulation=payload)
