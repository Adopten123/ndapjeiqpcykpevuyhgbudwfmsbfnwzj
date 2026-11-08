"""Настоящий CP-SAT для ограниченного учебного профиля, не серверный API."""
from __future__ import annotations

import argparse
import hashlib
import json
import math
from pathlib import Path
import platform
import sys
import time
import uuid
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]
PROFILE = "research-demo.v1"


class UnsupportedInput(ValueError):
    """Некорректный/неподдерживаемый вход не означает INFEASIBLE."""


def load(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def canonical_hash(value):
    raw = json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def index(rows, key="id"):
    result = {r[key]: r for r in rows}
    if len(result) != len(rows):
        raise UnsupportedInput(f"duplicate {key}")
    return result


def check_profile(p, *, dynamic_baseline=False):
    """Закрытая схема плюс явная граница поддерживаемой учебной топологии."""
    from jsonschema import Draft202012Validator, FormatChecker
    from referencing import Registry, Resource
    folder = ROOT / "contracts/planning-modes"
    schemas = [load(f) for d in (folder, folder.parent / "replan-v1") for f in d.glob("*.schema.json")]
    registry = Registry().with_resources((s["$id"], Resource.from_contents(s)) for s in schemas)
    if p.get("mode") not in {"initial", "replan"}:
        raise UnsupportedInput("mode must be initial/replan")
    schema = load(folder / ("initial-request.schema.json" if p["mode"] == "initial" else "replan-request.schema.json"))
    errors = list(Draft202012Validator(schema, registry=registry, format_checker=FormatChecker()).iter_errors(p))
    if errors:
        error = errors[0]
        raise UnsupportedInput(f"schema {list(error.absolute_path)}: {error.message}")
    initial = load(folder / "examples/initial-request.fixture.json")["payload"]
    replan = load(folder / "examples/replan-request.fixture.json")["payload"]
    if p["data_mode"] != "demo" or p["time_basis"] != initial["time_basis"]:
        raise UnsupportedInput("only fixed scenario_steps t0=0/horizon=30 profile")
    # These arrays define this profile, not additional data hidden in the solver.
    for key in ("parties", "containers", "transport_groups", "locations", "edges", "route_candidates",
                "operations", "facts", "duration_estimates"):
        if p[key] != initial[key]:
            raise UnsupportedInput(f"unsupported graph/current-state change: {key}")
    expected = initial if p["mode"] == "initial" else replan
    if p["limits"] != initial["limits"]:
        raise UnsupportedInput("profile requires the original 5000 ms / exact-gap limits; use CLI for a smaller budget")
    if p.get("controllable_groups", []) != expected.get("controllable_groups", []):
        raise UnsupportedInput("unsupported controllable groups")
    if p["policy"] != expected["policy"] or (p["mode"] == "replan" and not dynamic_baseline and p["baseline"] != replan["baseline"]):
        raise UnsupportedInput("unsupported policy/baseline")
    evidence = index(p["evidence"])
    if any(ref not in evidence for item in p["receipt_constraints"] for w in item["windows"] for ref in w["evidence_ids"]):
        raise UnsupportedInput("unknown receipt evidence")
    for key in ("services", "resources"):
        values, original = index(p[key]), index(initial[key])
        if values.keys() != original.keys():
            raise UnsupportedInput(f"unsupported {key} set")
        changing = {"load_window", "departure_tick", "capacity"} if key == "services" else {"calendar"}
        for ident, value in values.items():
            if {k: v for k, v in value.items() if k not in changing} != {k: v for k, v in original[ident].items() if k not in changing}:
                raise UnsupportedInput(f"unsupported {key} structure: {ident}")
    # Reservations are canonical references and include the release lag; don't
    # allow a caller to remove occupancy while retaining the same route graph.
    if p["reservations"] != initial["reservations"]:
        raise UnsupportedInput("unsupported reservation topology/amount/release_lag")
    receipts = index(p["receipt_constraints"], "party_id")
    if receipts.keys() != index(p["parties"]).keys():
        raise UnsupportedInput("receipt constraints must cover every party")
    for row in receipts.values():
        windows = sorted(row["windows"], key=lambda w: w["start_tick"])
        if any(w["start_tick"] >= w["end_tick"] or w["status"] != "demo_precondition" for w in windows):
            raise UnsupportedInput("invalid/non-demo receipt windows")
        if any(a["end_tick"] > b["start_tick"] for a, b in zip(windows, windows[1:])):
            raise UnsupportedInput("overlapping receipt windows")
    for resource in p["resources"]:
        calendar = sorted(resource["calendar"], key=lambda c: c["start_tick"])
        if any(c["start_tick"] >= c["end_tick"] or c["start_tick"] < -2 or c["end_tick"] > 30 for c in calendar):
            raise UnsupportedInput("calendar outside supported finite horizon")
        if any(a["end_tick"] > b["start_tick"] for a, b in zip(calendar, calendar[1:])):
            raise UnsupportedInput("overlapping resource calendar")
    for service in p["services"]:
        if not 0 <= service["load_window"]["start_tick"] < service["load_window"]["end_tick"] <= service["departure_tick"] <= 30:
            raise UnsupportedInput("invalid service window/departure")
    index(p["restrictions"])
    for restriction in p["restrictions"]:
        if restriction["confirmation"] != "confirmed" or restriction["end"]["kind"] != "known" or restriction["edge_id"] not in index(p["edges"]):
            raise UnsupportedInput("only confirmed known-end closures are supported")
        if restriction["start_tick"] >= restriction["end"]["end_tick"]:
            raise UnsupportedInput("empty/reversed closure")
    index(p["commitments"])
    services, operations = index(p["services"]), index(p["operations"])
    for c in p["commitments"]:
        if c["status"] != "demo_precondition" or not c["validity"]["start_tick"] <= 0 < c["validity"]["end_tick"]:
            raise UnsupportedInput("stale/non-demo commitment")
        if any(e not in evidence for e in c["evidence_ids"]):
            raise UnsupportedInput("unknown commitment evidence")
        if c["kind"] == "service_conditions":
            if c["service_id"] not in services or any(c[k] != services[c["service_id"]][k] for k in ("departure_tick", "load_window")):
                raise UnsupportedInput("commitment conflicts with service")
        elif c["kind"] == "operation_start_window":
            if c["operation_id"] not in operations or operations[c["operation_id"]]["state"] != "planned" or c["window"]["start_tick"] >= c["window"]["end_tick"]:
                raise UnsupportedInput("invalid future operation commitment")
        else:
            raise UnsupportedInput("unsupported commitment kind")


def solve(request, time_limit_seconds=None, max_tiers=None):
    """Получить исследовательский envelope; не имитировать response.schema.json."""
    p = request.get("payload", request)
    check_profile(p)
    return _solve_checked_profile(p, time_limit_seconds, max_tiers, engine_profile=PROFILE)


def _solve_checked_profile(p, time_limit_seconds=None, max_tiers=None, *, engine_profile, simulation=None):
    """Чистое ядро: вызывается только после проверки соответствующего профиля.

    Legacy solve() всегда проверяет исходную фиксированную базу.
    Внутренние адаптеры проверяют принятую базу и, для симуляции, факты прогона.
    """
    from ortools.sat.python import cp_model
    import ortools
    if engine_profile not in {PROFILE, "workflow-demo.v1", "simulation-demo.v1"}:
        raise UnsupportedInput("unsupported computation profile")
    if (engine_profile == "simulation-demo.v1") != (simulation is not None):
        raise UnsupportedInput("simulation profile requires a simulation snapshot")
    fixed = index(simulation["fixed_operations"], "operation_id") if simulation is not None else {}
    decision_tick = simulation["decision_tick"] if simulation is not None else 0
    tier_limit = len(p["policy"]["objective_order"]) if max_tiers is None else max_tiers
    if type(tier_limit) is not int or not 1 <= tier_limit <= len(p["policy"]["objective_order"]):
        raise UnsupportedInput("invalid maximum number of objective tiers")
    budget = p["limits"]["attempt_budget_ms"] / 1000
    if time_limit_seconds is not None:
        if not math.isfinite(time_limit_seconds) or time_limit_seconds < 0:
            raise UnsupportedInput("time limit must be finite and nonnegative")
        budget = min(budget, time_limit_seconds)
    started_at = datetime.now(timezone.utc).isoformat()
    started = time.perf_counter()
    m = cp_model.CpModel()
    parties, routes, ops = index(p["parties"]), index(p["route_candidates"]), index(p["operations"])
    services, edges, estimates = index(p["services"]), index(p["edges"]), index(p["duration_estimates"])
    selected = {r: m.new_bool_var("route:" + r) for r in routes}
    for party in parties:
        m.add_exactly_one(selected[r] for r in routes if routes[r]["party_id"] == party)
    active, start, end = {}, {}, {}
    for ident, op in ops.items():
        active[ident] = m.new_bool_var("active:" + ident)
        m.add(active[ident] == sum(selected[r] for r in op["route_candidate_ids"]))
        start[ident], end[ident] = m.new_int_var(-2, 30, "start:" + ident), m.new_int_var(-2, 30, "end:" + ident)
        m.add(start[ident] == 0).only_enforce_if(active[ident].Not())
        m.add(end[ident] == 0).only_enforce_if(active[ident].Not())
        d = op["duration"]
        if d["kind"] == "estimate":
            m.add(end[ident] - start[ident] == estimates[d["estimate_id"]]["value_tick"]).only_enforce_if(active[ident])
        else:
            m.add(end[ident] - start[ident] >= d["min_tick"]).only_enforce_if(active[ident])
            m.add(end[ident] - start[ident] <= d["max_tick"]).only_enforce_if(active[ident])
        if ident in fixed:
            fact = fixed[ident]
            m.add(active[ident] == 1)
            m.add(start[ident] == fact["start_tick"])
            if fact["state"] == "in_progress" and op["kind"] == "wait":
                m.add(end[ident] >= decision_tick)
            else:
                m.add(end[ident] == fact["end_tick"])
        elif op["state"] == "planned":
            m.add(start[ident] >= decision_tick).only_enforce_if(active[ident])
        else:
            m.add(start[ident] == op["fixed_start_tick"]).only_enforce_if(active[ident])
            if op["state"] == "completed":
                m.add(end[ident] == op["fixed_end_tick"]).only_enforce_if(active[ident])
    for ident, op in ops.items():
        for pred in op["predecessor_ids"]:
            m.add(start[ident] == end[pred] + op["min_predecessor_lag_tick"]).only_enforce_if([active[ident], active[pred]])
        if not op["predecessor_ids"] and op["state"] == "planned":
            party = parties[op["party_id"]]
            received = [f["event_tick"] for f in p["facts"] if f["party_id"] == party["id"] and f["event_kind"] == "terminal_received" and f["location_id"] == party["current"].get("location_id")]
            m.add(start[ident] == max([0, party["ready_tick"]] + received)).only_enforce_if(active[ident])
    for fact in p["facts"]:
        if fact["event_kind"] in {"operation_started", "operation_completed"}:
            ident = fact["operation_id"]
            m.add(active[ident] == 1)
            m.add((start if fact["event_kind"] == "operation_started" else end)[ident] == fact["event_tick"])
    receipt_vars = {}
    receipt_constraints = index(p["receipt_constraints"], "party_id")
    for ident, party in parties.items():
        service_id = party["allowed_service_ids"][0]  # Singleton guaranteed by the exact graph profile.
        service = services[service_id]
        party_ops = {i: o for i, o in ops.items() if o["party_id"] == ident}
        load_op = next(i for i, o in party_ops.items() if o["kind"] == "load")
        m.add(start[load_op] >= service["load_window"]["start_tick"])
        m.add(end[load_op] <= service["load_window"]["end_tick"])
        m.add(end[load_op] <= service["departure_tick"])
        for i, op in party_ops.items():
            if op["kind"] == "move" and op["to_location_id"] == "M":
                m.add(start[load_op] >= end[i] + party["min_preload_preparation_tick"]).only_enforce_if(active[i])
        for f in p["facts"]:
            if f["party_id"] == ident and f["event_kind"] == "terminal_received" and f["location_id"] == "M":
                m.add(start[load_op] >= f["event_tick"] + party["min_preload_preparation_tick"])
        offset = 0
        for edge_id in service["edge_ids"]:
            i = next(i for i, o in party_ops.items() if o["kind"] == "move" and o["edge_id"] == edge_id and o["service_id"] == service_id)
            m.add(start[i] == service["departure_tick"] + offset)
            offset += estimates[edges[edge_id]["estimate_id"]]["value_tick"]
        receipt = end[party["recipient_receipt_operation_id"]]
        receipt_vars[ident] = receipt
        m.add(receipt <= party["due_tick"])
        window_bools = []
        for w in receipt_constraints[ident]["windows"]:
            b = m.new_bool_var("window:" + w["id"])
            window_bools.append(b)
            m.add(receipt >= w["start_tick"]).only_enforce_if(b)
            m.add(receipt < w["end_tick"]).only_enforce_if(b)
        m.add_exactly_one(window_bools)
    for service_id, service in services.items():
        m.add(sum(q["allowed_service_ids"] == [service_id] for q in parties.values()) <= service["capacity"])
    for r in p["restrictions"]:
        for ident, op in ops.items():
            if op["edge_id"] == r["edge_id"]:
                before = m.new_bool_var("before:" + r["id"] + ":" + ident)
                m.add(end[ident] <= r["start_tick"]).only_enforce_if([active[ident], before])
                m.add(start[ident] >= r["end"]["end_tick"]).only_enforce_if([active[ident], before.Not()])
    for c in p["commitments"]:
        if c["kind"] == "operation_start_window":
            i, w = c["operation_id"], c["window"]
            m.add(active[i] == 1)
            m.add(start[i] >= w["start_tick"])
            m.add(start[i] < w["end_tick"])

    def resolve(ref):
        if ref["kind"] == "fixed_tick":
            return ref["tick"]
        return (start if ref["kind"] == "operation_start" else end)[ref["operation_id"]]

    reservation_vars = []
    for reservation in p["reservations"]:
        pres = m.new_bool_var("reservation:" + reservation["id"])
        m.add(pres == sum(selected[r] for r in reservation["route_candidate_ids"]))
        a, b = resolve(reservation["start"]), resolve(reservation["end"]) + reservation["release_lag_tick"]
        m.add(a <= b).only_enforce_if(pres)
        m.add(b <= 30).only_enforce_if(pres)
        reservation_vars.append((reservation, pres, a, b))
    for resource in p["resources"]:
        reservations = [v for v in reservation_vars if v[0]["resource_id"] == resource["id"]]
        for tick in range(-2, 30):
            demands = []
            for reservation, pres, a, b in reservations:
                left, right, occupied = (m.new_bool_var(f"{label}:{reservation['id']}:{tick}") for label in ("left", "right", "occupied"))
                m.add(a <= tick).only_enforce_if(left)
                m.add(a > tick).only_enforce_if(left.Not())
                m.add(b > tick).only_enforce_if(right)
                m.add(b <= tick).only_enforce_if(right.Not())
                m.add_bool_and([pres, left, right]).only_enforce_if(occupied)
                m.add_bool_or([pres.Not(), left.Not(), right.Not()]).only_enforce_if(occupied.Not())
                demands.append(reservation["amount"] * occupied)
            capacity = next((c["capacity"] for c in resource["calendar"] if c["start_tick"] <= tick < c["end_tick"]), 0)
            m.add(sum(demands) <= capacity)
    group_vars, changed_vars = {}, {}
    assignments = index(p["baseline"]["assignments"], "group_id") if p["mode"] == "replan" else {}
    for group in p.get("controllable_groups", []):
        ident = group["group_id"]
        dep = m.new_int_var(0, 30, "departure:" + ident)
        for op in group["departure_operation_ids"]:
            m.add(dep == start[op]).only_enforce_if(active[op])
        group_vars[ident] = dep
        if p["mode"] == "replan":
            baseline = assignments[ident]
            equal_dep = m.new_bool_var("same-departure:" + ident)
            m.add(dep == baseline["departure_tick"]).only_enforce_if(equal_dep)
            m.add(dep != baseline["departure_tick"]).only_enforce_if(equal_dep.Not())
            equal = {"departure_tick": equal_dep, "route_id": selected[baseline["route_id"]],
                     "service_id": m.new_constant(int(parties[group["party_id"]]["allowed_service_ids"] == [baseline["service_id"]]))}
            changed = m.new_bool_var("changed:" + ident)
            tests = [equal[k] for k in group["compared_fields"]]
            m.add_bool_and(tests).only_enforce_if(changed.Not())
            m.add_bool_or([t.Not() for t in tests]).only_enforce_if(changed)
            changed_vars[ident] = changed
    if p["mode"] == "initial":
        last = m.new_int_var(0, 30, "last-receipt")
        m.add_max_equality(last, list(receipt_vars.values()))
        objectives = [sum(receipt_vars.values()), last]
    else:
        base = index(p["baseline"]["plan"]["party_plans"], "party_id")
        shifts = []
        for ident, receipt in receipt_vars.items():
            shift = m.new_int_var(0, 30, "shift:" + ident)
            m.add_max_equality(shift, [0, receipt - base[ident]["recipient_receipt_tick"]])
            shifts.append(shift)
        largest = m.new_int_var(0, 30, "max-shift")
        m.add_max_equality(largest, shifts)
        objectives = [sum(shifts), largest, sum(changed_vars.values())]

    names = p["policy"]["objective_order"]
    result = {"engine_profile": engine_profile, "execution": "executed", "synthetic": True,
              "mode": p["mode"], **{k: p[k] for k in ("snapshot_id", "calculation_id", "attempt_id")},
              "research_run_id": str(uuid.uuid4()), "started_at_utc": started_at, "ortools_version": ortools.__version__, "python_version": platform.python_version(),
              "input_hash_format": "sha256-canonical-json-sort-keys-utf8", "input_sha256": canonical_hash(simulation if simulation is not None else p),
              "solver_parameters": {"total_budget_seconds": budget, "num_search_workers": 1, "random_seed": 0, "max_tiers": tier_limit,
                                    "absolute_gap_limit": 0.0, "relative_gap_limit": 0.0},
              "solver_status": "UNKNOWN", "proven_prefix": 0, "optimization_stages": [], "candidate": None,
              "objective": None, "group_decisions": [], "changed_group_ids": [], "resolved_reservations": []}
    for tier, expr in enumerate(objectives):
        remaining = max(0.0, budget - (time.perf_counter() - started))
        if tier and remaining == 0:
            break
        m.minimize(expr)
        solver = cp_model.CpSolver()
        solver.parameters.max_time_in_seconds = remaining
        solver.parameters.num_search_workers = 1
        solver.parameters.random_seed = 0
        solver.parameters.absolute_gap_limit = 0.0
        solver.parameters.relative_gap_limit = 0.0
        status = solver.solve(m)
        status_name = solver.status_name(status)
        feasible = status in (cp_model.OPTIMAL, cp_model.FEASIBLE)
        value = int(solver.value(expr)) if feasible else None
        result["optimization_stages"].append({"objective_name": names[tier], "status": status_name,
                                                "value": value, "best_bound": float(solver.best_objective_bound),
                                                "wall_time_seconds": solver.wall_time})
        if feasible:
            chosen = {r["party_id"]: rid for rid, r in routes.items() if solver.value(selected[rid])}
            scheduled = [{"operation_id": i, "start_tick": solver.value(start[i]), "end_tick": solver.value(end[i])} for i in ops if solver.value(active[i])]
            plans = [{"party_id": ident, "route_id": chosen[ident], "service_id": party["allowed_service_ids"][0],
                      "port_receipt_tick": solver.value(end[party["port_receipt_operation_id"]]),
                      "recipient_receipt_tick": solver.value(receipt_vars[ident])} for ident, party in parties.items()]
            result["candidate"] = {"party_plans": plans, "scheduled_operations": scheduled, "fixed_fact_ids": [f["id"] for f in p["facts"]]}
            result["objective"] = dict(zip(names, (int(solver.value(o)) for o in objectives)))
            result["changed_group_ids"] = sorted(g for g, b in changed_vars.items() if solver.value(b))
            result["group_decisions"] = [{"group_id": g["group_id"], "party_id": g["party_id"], "route_id": chosen[g["party_id"]],
                                          "service_id": parties[g["party_id"]]["allowed_service_ids"][0],
                                          "departure_tick": solver.value(group_vars[g["group_id"]])} for g in p.get("controllable_groups", [])]
            result["resolved_reservations"] = [{"reservation_id": r["id"], "resource_id": r["resource_id"], "party_id": r["party_id"],
                                                 "start_tick": solver.value(a), "end_tick": solver.value(b), "amount": r["amount"]}
                                                for r, pres, a, b in reservation_vars if solver.value(pres)]
            result["solver_status"] = "FEASIBLE"
        elif result["candidate"] is None:
            result["solver_status"] = status_name
        if status == cp_model.OPTIMAL:
            result["proven_prefix"] += 1
            m.add(expr == value)
            if tier + 1 == tier_limit:
                break
        else:
            break
    if result["proven_prefix"] == len(objectives):
        result["solver_status"] = "OPTIMAL"
    result["termination_reason"] = ("all_tiers_optimal" if result["solver_status"] == "OPTIMAL" else
                                    "requested_tier_limit" if result["candidate"] is not None and result["proven_prefix"] == tier_limit else
                                    "search_or_budget_stop")
    result["wall_time_seconds"] = time.perf_counter() - started
    result["solver_invocations"] = len(result["optimization_stages"])
    result["code_hash_format"] = "sha256-after-crlf-to-lf"
    result["solver_code_sha256"] = hashlib.sha256(Path(__file__).read_bytes().replace(b"\r\n", b"\n")).hexdigest()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--request", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--deps", type=Path)
    parser.add_argument("--time-limit", type=float)
    parser.add_argument("--max-tiers", type=int)
    args = parser.parse_args()
    if args.deps:
        sys.path.insert(0, str(args.deps.resolve()))
    try:
        result = solve(load(args.request), args.time_limit, args.max_tiers)
    except UnsupportedInput as exc:
        result = {"execution": "not_executed", "engine_profile": PROFILE, "error": "unsupported_or_invalid_input", "message": str(exc)}
    raw = (json.dumps(result, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    if args.output:
        args.output.write_bytes(raw)
    else:
        print(raw.decode("utf-8"), end="")
    return 1 if result.get("error") else 0


if __name__ == "__main__":
    raise SystemExit(main())
