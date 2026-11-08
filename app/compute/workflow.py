"""Расчёт от сохранённой принятой базы при неизменной учебной топологии.

Подлинность принятия и связь ID с содержимым проверяет Go по своей БД.
Здесь проверяются структура и внутренняя согласованность переданной базы.
Её старые сроки намеренно не проверяются по новым окнам/закрытиям источника.
Новый кандидат проверяется полностью по текущему снимку другим алгоритмом.
"""
from __future__ import annotations

import copy

PROFILE = "workflow-demo.v1"


def _unique(rows, key, planner):
    result = {row[key]: row for row in rows}
    if len(result) != len(rows):
        raise planner.UnsupportedInput("duplicate baseline object")
    return result


def check_baseline(p, planner, checker):
    baseline = p["baseline"]
    canonical = planner.load(planner.ROOT / "contracts/planning-modes/examples/replan-request.fixture.json")["payload"]["baseline"]
    if baseline["plan_id"] == "DEMO-v0":
        if baseline != canonical:
            raise planner.UnsupportedInput("reserved initial baseline has different content")
        return
    plan = baseline["plan"]
    parties = planner.index(p["parties"])
    routes = planner.index(p["route_candidates"])
    ops = planner.index(p["operations"])
    estimates = planner.index(p["duration_estimates"])
    plans = _unique(plan["party_plans"], "party_id", planner)
    times = _unique(plan["scheduled_operations"], "operation_id", planner)
    if plans.keys() != parties.keys():
        raise planner.UnsupportedInput("baseline must cover exactly the input parties")
    selected = {}
    for ident, row in plans.items():
        route = routes.get(row["route_id"])
        if route is None or route["party_id"] != ident or row["service_id"] not in parties[ident]["allowed_service_ids"]:
            raise planner.UnsupportedInput("baseline route/service does not belong to party")
        selected[ident] = row["route_id"]
    expected = {ident for ident, op in ops.items() if selected[op["party_id"]] in op["route_candidate_ids"]}
    if times.keys() != expected:
        raise planner.UnsupportedInput("baseline must contain the complete selected schedule")
    fact_ids = [f["id"] for f in p["facts"]]
    if len(plan["fixed_fact_ids"]) != len(fact_ids) or set(plan["fixed_fact_ids"]) != set(fact_ids):
        raise planner.UnsupportedInput("baseline fixed facts differ")
    now = p["time_basis"]["now_tick"]
    horizon = p["time_basis"]["horizon_end_tick"]
    for ident, row in times.items():
        op = ops[ident]
        start, end = row["start_tick"], row["end_tick"]
        if type(start) is not int or type(end) is not int or not -2 <= start <= end <= horizon:
            raise planner.UnsupportedInput("baseline time outside integer horizon")
        duration = op["duration"]
        if duration["kind"] == "estimate":
            if end - start != estimates[duration["estimate_id"]]["value_tick"]:
                raise planner.UnsupportedInput("baseline duration differs")
        elif not duration["min_tick"] <= end - start <= duration["max_tick"]:
            raise planner.UnsupportedInput("baseline decision duration differs")
        if op["state"] == "planned" and start < now:
            raise planner.UnsupportedInput("baseline planned operation starts in the past")
        if op["state"] != "planned" and start != op["fixed_start_tick"]:
            raise planner.UnsupportedInput("baseline fixed start changed")
        if op["state"] == "completed" and end != op["fixed_end_tick"]:
            raise planner.UnsupportedInput("baseline completed end changed")
        predecessors = [key for key in op["predecessor_ids"] if key in times]
        if op["predecessor_ids"] and len(predecessors) != 1:
            raise planner.UnsupportedInput("baseline predecessor set differs")
        if any(start != times[key]["end_tick"] + op["min_predecessor_lag_tick"] for key in predecessors):
            raise planner.UnsupportedInput("baseline has an unaccounted gap or overlap")
    for fact in p["facts"]:
        if fact["event_kind"] in {"operation_started", "operation_completed"}:
            key = "start_tick" if fact["event_kind"] == "operation_started" else "end_tick"
            if times[fact["operation_id"]][key] != fact["event_tick"]:
                raise planner.UnsupportedInput("baseline contradicts fixed fact")
    departures = {}
    for ident, party in parties.items():
        row = plans[ident]
        if (row["port_receipt_tick"], row["recipient_receipt_tick"]) != (
                times[party["port_receipt_operation_id"]]["end_tick"], times[party["recipient_receipt_operation_id"]]["end_tick"]):
            raise planner.UnsupportedInput("baseline receipt differs from schedule")
        if row["recipient_receipt_tick"] > party["due_tick"]:
            raise planner.UnsupportedInput("baseline receipt exceeds the fixed due time")
        roots = [op for op in ops.values() if op["id"] in times and op["party_id"] == ident and op["state"] != "completed" and not any(key in times for key in op["predecessor_ids"])]
        if len(roots) != 1:
            raise planner.UnsupportedInput("baseline remaining chain has no unique root")
        root = roots[0]
        current = party["current"]
        if current["kind"] == "at_location":
            received = [f["event_tick"] for f in p["facts"] if f["party_id"] == ident and f["event_kind"] == "terminal_received" and f["location_id"] == current["location_id"]]
            if times[root["id"]]["start_tick"] != max([now, party["ready_tick"]] + received):
                raise planner.UnsupportedInput("baseline readiness/root changed")
        elif root["id"] != current["operation_id"] or times[root["id"]]["start_tick"] != current["started_tick"]:
            raise planner.UnsupportedInput("baseline in-progress root changed")
        # Один рейс должен иметь один момент выхода для всех его партий, даже
        # когда новый источник уже задаёт другой момент выхода этого рейса.
        sea = [op for op in ops.values() if op["id"] in times and op["party_id"] == ident and op["kind"] == "move" and op["service_id"] == row["service_id"]]
        if not sea:
            raise planner.UnsupportedInput("baseline sea service is missing")
        first = min(times[op["id"]]["start_tick"] for op in sea)
        previous = departures.setdefault(row["service_id"], first)
        if first != previous:
            raise planner.UnsupportedInput("baseline parties disagree on service departure")
    assignments = _unique(baseline["assignments"], "group_id", planner)
    expected_assignments = {row["group_id"]: row for row in checker._group_decisions(p, plans, times)}
    if assignments != expected_assignments:
        raise planner.UnsupportedInput("baseline assignments differ from its selected schedule")


def check_profile(payload, *, planner, checker):
    if payload.get("mode") != "replan":
        raise planner.UnsupportedInput("workflow profile only supports replan")
    planner.check_profile(payload, dynamic_baseline=True)
    try:
        check_baseline(payload, planner, checker)
    except (KeyError, TypeError, IndexError) as exc:
        raise planner.UnsupportedInput("malformed workflow baseline") from exc


def solve(payload, *, planner, checker, time_limit_seconds=None, max_tiers=None):
    p = copy.deepcopy(payload)
    check_profile(p, planner=planner, checker=checker)
    return planner._solve_checked_profile(p, time_limit_seconds, max_tiers, engine_profile=PROFILE)


def validate(payload, result, *, checker):
    return checker.validate(payload, result, engine_profile=PROFILE)
