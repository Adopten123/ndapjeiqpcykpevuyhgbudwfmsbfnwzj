"""Независимая проверка кандидата research-demo.v1, без OR-Tools и сети.

Проверяет расписание, а не доказательство отсутствия/оптимальности решения.
Не является универсальным валидатором всех будущих форматов контракта.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]


def _payload(value: dict) -> dict:
    return value.get("payload", value)


def _index(rows: list[dict], key: str, errors: list[str], label: str) -> dict:
    result = {}
    for row in rows:
        ident = row[key]
        if ident in result:
            errors.append(f"duplicate {label}: {ident}")
        result[ident] = row
    return result


def _integer(value: Any) -> bool:
    return type(value) is int


def _resolve(ref: dict, times: dict[str, dict]) -> int:
    kind = ref["kind"]
    if kind == "fixed_tick":
        return ref["tick"]
    if kind in ("operation_start", "operation_end"):
        return times[ref["operation_id"]]["start_tick" if kind == "operation_start" else "end_tick"]
    raise ValueError(f"unsupported reservation reference: {kind}")


def _reservations(payload: dict, routes: dict[str, str], times: dict[str, dict]) -> list[dict]:
    rows = []
    for item in payload["reservations"]:
        if routes[item["party_id"]] not in item["route_candidate_ids"]:
            continue
        rows.append({
            "reservation_id": item["id"], "resource_id": item["resource_id"],
            "party_id": item["party_id"], "start_tick": _resolve(item["start"], times),
            "end_tick": _resolve(item["end"], times) + item["release_lag_tick"],
            "amount": item["amount"],
        })
    return rows


def _group_decisions(payload: dict, plans: dict, times: dict) -> list[dict]:
    rows = []
    for group in payload.get("controllable_groups", []):
        plan = plans[group["party_id"]]
        departures = [op for op in group["departure_operation_ids"] if op in times]
        if len(departures) != 1:
            raise ValueError(f"group must have one active departure: {group['group_id']}")
        rows.append({"group_id": group["group_id"], "party_id": group["party_id"],
                     "route_id": plan["route_id"], "service_id": plan["service_id"],
                     "departure_tick": times[departures[0]]["start_tick"]})
    return rows


def validate(payload: dict, result: dict, *, allow_fixture: bool = False, engine_profile: str = "research-demo.v1", simulation: dict | None = None) -> list[str]:
    """Вернуть нарушения; [] означает проверенного кандидата или согласованный отказ.

    Для candidate=None отсутствие решения/оптимальность независимо не доказаны.
    allow_fixture используется только локальными испытаниями этого валидатора.
    """
    try:
        return _validate(_payload(payload), result, allow_fixture=allow_fixture, engine_profile=engine_profile, simulation=simulation)
    except (KeyError, TypeError, ValueError, IndexError) as exc:
        return [f"malformed or unsupported validation input: {type(exc).__name__}: {exc}"]


def _validate(p: dict, r: dict, *, allow_fixture: bool, engine_profile: str, simulation: dict | None) -> list[str]:
    errors: list[str] = []
    if engine_profile not in {"research-demo.v1", "workflow-demo.v1", "simulation-demo.v1"} or r.get("engine_profile") != engine_profile:
        errors.append("unsupported engine_profile")
    if (engine_profile == "simulation-demo.v1") != (simulation is not None):
        errors.append("simulation profile requires a simulation snapshot")
    executions = {"executed", "test_fixture"} if allow_fixture else {"executed"}
    if r.get("execution") not in executions or r.get("synthetic") is not True:
        errors.append("result must be an explicitly synthetic executed research result")
    if p["data_mode"] != "demo" or p["time_basis"]["kind"] != "scenario_steps":
        errors.append("unsupported input: only demo scenario_steps profile")
    mode = p["mode"]
    names = (["sum_remaining_tick", "last_remaining_tick"] if mode == "initial" else
             ["sum_positive_shift", "max_positive_shift", "changed_groups"])
    if mode not in {"initial", "replan"} or r.get("mode") != mode:
        errors.append("mode mismatch or unsupported mode")
    if p["policy"]["objective_order"] != names:
        errors.append("unsupported objective policy")
    now, horizon = p["time_basis"]["now_tick"], p["time_basis"]["horizon_end_tick"]
    if not all(_integer(v) for v in (now, horizon)) or not now < horizon:
        errors.append("invalid integer time basis")
    for field in ("snapshot_id", "calculation_id", "attempt_id"):
        if (not allow_fixture or field in r) and r.get(field) != p[field]:
            errors.append(f"context mismatch: {field}")
    if not allow_fixture:
        bound_input = simulation if simulation is not None else p
        digest = hashlib.sha256(json.dumps(bound_input,sort_keys=True,ensure_ascii=False,separators=(",",":")).encode("utf-8")).hexdigest()
        if r.get("input_hash_format") != "sha256-canonical-json-sort-keys-utf8" or r.get("input_sha256") != digest:
            errors.append("result is not bound to the supplied input content")
    prefix = r.get("proven_prefix")
    if not _integer(prefix) or not 0 <= prefix <= len(names):
        errors.append("invalid proven_prefix")
    stages = r.get("optimization_stages", [])
    if stages:
        leading = 0
        for n, stage in enumerate(stages):
            if n >= len(names) or stage["objective_name"] != names[n]:
                errors.append("optimization stage order mismatch")
                break
            if n == leading and stage["status"] == "OPTIMAL":
                leading += 1
        if prefix != leading:
            errors.append("proven_prefix differs from leading OPTIMAL stages")
    status = r.get("solver_status")
    candidate = r.get("candidate")
    if candidate is None:
        if status not in {"INFEASIBLE", "UNKNOWN", "MODEL_INVALID"}:
            errors.append("missing candidate with candidate-bearing status")
        if r.get("objective") not in (None, {}) or r.get("group_decisions") or r.get("resolved_reservations"):
            errors.append("missing candidate must not carry invented objective/assignments")
        return errors
    if status not in {"OPTIMAL", "FEASIBLE", "UNKNOWN", "NOT_EXECUTED_FIXTURE"}:
        errors.append("candidate incompatible with solver_status")
    if status == "NOT_EXECUTED_FIXTURE" and not allow_fixture:
        errors.append("hypothetical fixture is not an executed result")
    if status == "OPTIMAL" and prefix != len(names):
        errors.append("OPTIMAL without complete lexicographic proof prefix")

    parties = _index(p["parties"], "id", errors, "party")
    routes = _index(p["route_candidates"], "id", errors, "route")
    ops = _index(p["operations"], "id", errors, "input operation")
    estimates = _index(p["duration_estimates"], "id", errors, "estimate")
    edges = _index(p["edges"], "id", errors, "edge")
    resources = _index(p["resources"], "id", errors, "resource")
    services = _index(p["services"], "id", errors, "service")
    _index(p["reservations"], "id", errors, "reservation")
    plans = _index(candidate["party_plans"], "party_id", errors, "party plan")
    times = _index(candidate["scheduled_operations"], "operation_id", errors, "scheduled operation")
    if set(candidate) != {"party_plans", "scheduled_operations", "fixed_fact_ids"}:
        errors.append("unexpected/missing candidate fields")
    if set(plans) != set(parties):
        errors.append("party plan set differs from input")
        return errors
    selected = {}
    for ident, plan in plans.items():
        if set(plan) != {"party_id", "route_id", "service_id", "port_receipt_tick", "recipient_receipt_tick"}:
            errors.append(f"unexpected party plan fields: {ident}")
        route = routes[plan["route_id"]]
        if route["party_id"] != ident:
            errors.append(f"route belongs to another party: {ident}")
        selected[ident] = route["id"]
        if plan["service_id"] not in parties[ident]["allowed_service_ids"]:
            errors.append(f"service not allowed for party: {ident}")
    expected_ops = {i for i, op in ops.items() if selected[op["party_id"]] in op["route_candidate_ids"]}
    if set(times) != expected_ops:
        errors.append(f"scheduled operation set mismatch: missing={sorted(expected_ops-set(times))}, extra={sorted(set(times)-expected_ops)}")
        return errors
    if set(candidate["fixed_fact_ids"]) != {f["id"] for f in p["facts"]} or len(candidate["fixed_fact_ids"]) != len(p["facts"]):
        errors.append("fixed_fact_ids mismatch")

    fixed = _index(simulation["fixed_operations"], "operation_id", errors, "simulation fact") if simulation is not None else {}
    decision_tick = simulation["decision_tick"] if simulation is not None else now
    if not set(fixed).issubset(times):
        errors.append("fixed simulation operation missing")

    for ident, time in times.items():
        op = ops[ident]
        start, end = time["start_tick"], time["end_tick"]
        if set(time) != {"operation_id", "start_tick", "end_tick"}:
            errors.append(f"unexpected schedule fields: {ident}")
        if not all(_integer(v) for v in (start, end)):
            errors.append(f"noninteger time: {ident}")
            return errors
        if end < start or end > horizon or (op["state"] == "planned" and start < now):
            errors.append(f"time/horizon violation: {ident}")
        if ident in fixed:
            fact = fixed[ident]
            if start != fact["start_tick"]:
                errors.append(f"simulation fact start changed: {ident}")
            if fact["state"] == "in_progress" and op["kind"] == "wait":
                if end < decision_tick:
                    errors.append(f"simulation wait ends before decision: {ident}")
            elif end != fact["end_tick"]:
                errors.append(f"simulation fact end changed: {ident}")
        elif simulation is not None and start < decision_tick:
            errors.append(f"simulation operation starts in the past: {ident}")
        duration = op["duration"]
        if duration["kind"] == "estimate":
            if end-start != estimates[duration["estimate_id"]]["value_tick"]:
                errors.append(f"duration mismatch: {ident}")
        elif duration["kind"] == "decision":
            if not duration["min_tick"] <= end-start <= duration["max_tick"]:
                errors.append(f"decision duration bounds: {ident}")
        else:
            errors.append(f"unsupported duration: {ident}")
        if op["state"] in {"completed", "in_progress"} and start != op["fixed_start_tick"]:
            errors.append(f"fixed start changed: {ident}")
        if op["state"] == "completed" and end != op["fixed_end_tick"]:
            errors.append(f"completed end changed: {ident}")
        active_predecessors = [i for i in op["predecessor_ids"] if i in times]
        if op["predecessor_ids"] and len(active_predecessors) != 1:
            errors.append(f"expected one selected predecessor: {ident}")
        for pred in active_predecessors:
            if start != times[pred]["end_tick"] + op["min_predecessor_lag_tick"]:
                errors.append(f"unaccounted gap/overlap in strict profile: {pred}->{ident}")
            if ops[pred]["to_location_id"] != op["from_location_id"]:
                errors.append(f"location discontinuity: {pred}->{ident}")
        if op["kind"] == "move":
            edge = edges[op["edge_id"]]
            if (edge["from_location_id"],edge["to_location_id"]) != (op["from_location_id"],op["to_location_id"]):
                errors.append(f"edge geometry mismatch: {ident}")
            if duration["kind"] != "estimate" or duration["estimate_id"] != edge["estimate_id"]:
                errors.append(f"edge estimate mismatch: {ident}")

    # Facts are authoritative within the synthetic input, not real-world evidence.
    for fact in p["facts"]:
        if fact["event_kind"] == "operation_completed":
            if times[fact["operation_id"]]["end_tick"] != fact["event_tick"]:
                errors.append(f"completed fact changed: {fact['id']}")
        elif fact["event_kind"] == "operation_started":
            if times[fact["operation_id"]]["start_tick"] != fact["event_tick"]:
                errors.append(f"started fact changed: {fact['id']}")
        elif fact["event_kind"] != "terminal_received":
            errors.append(f"unsupported fact kind: {fact['id']}")

    receipts = _index(p["receipt_constraints"], "party_id", errors, "receipt constraint")
    if set(receipts) != set(parties):
        errors.append("receipt constraints must cover all parties")
        return errors
    for ident, party in parties.items():
        current = party["current"]
        roots = [i for i in times if ops[i]["party_id"] == ident and ops[i]["state"] != "completed" and not any(j in times for j in ops[i]["predecessor_ids"])]
        if len(roots) != 1:
            errors.append(f"expected one remaining-chain root: {ident}")
        else:
            root = roots[0]
            if current["kind"] == "at_location":
                if ops[root]["from_location_id"] != current["location_id"]:
                    errors.append(f"current location changed: {ident}")
                received = [f["event_tick"] for f in p["facts"] if f["party_id"] == ident and f["event_kind"] == "terminal_received" and f["location_id"] == current["location_id"]]
                required_start = max([now,party["ready_tick"]]+received)
                if times[root]["start_tick"] != required_start:
                    errors.append(f"readiness/root gap in strict profile: {ident}")
            elif current["kind"] == "on_edge":
                if root != current["operation_id"] or times[root]["start_tick"] != current["started_tick"] or ops[root]["edge_id"] != current["edge_id"]:
                    errors.append(f"in-progress position changed: {ident}")
            else:
                errors.append(f"unsupported current position: {ident}")
        route = routes[selected[ident]]
        route_edges = [ops[i]["edge_id"] for i in sorted(times,key=lambda k:(times[k]["start_tick"],times[k]["end_tick"],k)) if ops[i]["party_id"] == ident and ops[i]["kind"] == "move" and ops[i]["state"] != "completed"]
        if route_edges != route["edge_ids"]:
            errors.append(f"selected route edge sequence differs: {ident}")
        port = times[party["port_receipt_operation_id"]]["end_tick"]
        receipt = times[party["recipient_receipt_operation_id"]]["end_tick"]
        plan = plans[ident]
        if (plan["port_receipt_tick"],plan["recipient_receipt_tick"]) != (port,receipt):
            errors.append(f"receipt does not match scheduled event: {ident}")
        if receipt > party["due_tick"] or receipt < now:
            errors.append(f"due/receipt violation: {ident}")
        if receipts[ident]["event"] != "recipient_acceptance" or not any(w["start_tick"] <= receipt < w["end_tick"] for w in receipts[ident]["windows"]):
            errors.append(f"recipient event outside window: {ident}")
        loads = [i for i in times if ops[i]["party_id"] == ident and ops[i]["kind"] == "load"]
        if len(loads) != 1:
            errors.append(f"expected one load: {ident}")
            continue
        load = loads[0]
        location = ops[load]["from_location_id"]
        arrivals = [times[i]["end_tick"] for i in times if ops[i]["party_id"] == ident and ops[i]["kind"] == "move" and ops[i]["to_location_id"] == location]
        arrivals += [f["event_tick"] for f in p["facts"] if f["party_id"] == ident and f["event_kind"] == "terminal_received" and f["location_id"] == location]
        if not arrivals or times[load]["start_tick"] < max(arrivals) + party["min_preload_preparation_tick"]:
            errors.append(f"preload preparation violated: {ident}")
        service = services[plan["service_id"]]
        if ops[load]["service_id"] != service["id"] or not service["load_window"]["start_tick"] <= times[load]["start_tick"] or times[load]["end_tick"] > service["load_window"]["end_tick"] or times[load]["end_tick"] > service["departure_tick"]:
            errors.append(f"load outside service conditions: {ident}")
        offset = 0
        for edge_id in service["edge_ids"]:
            sea = [i for i in times if ops[i]["party_id"] == ident and ops[i]["edge_id"] == edge_id and ops[i]["service_id"] == service["id"]]
            if len(sea) != 1 or times[sea[0]]["start_tick"] != service["departure_tick"]+offset:
                errors.append(f"service movement mismatch: {ident}/{edge_id}")
            offset += estimates[edges[edge_id]["estimate_id"]]["value_tick"]
    for service in services.values():
        if sum(plan["service_id"] == service["id"] for plan in plans.values()) > service["capacity"]:
            errors.append(f"service capacity exceeded: {service['id']}")

    derived = _reservations(p,selected,times)
    supplied = _index(r["resolved_reservations"],"reservation_id",errors,"resolved reservation")
    if supplied != {row["reservation_id"]:row for row in derived}:
        errors.append("resolved reservations differ from canonical references/release_lag")
    for row in derived:
        if row["resource_id"] not in resources or row["end_tick"] < row["start_tick"] or row["end_tick"] > horizon:
            errors.append(f"invalid resolved reservation: {row['reservation_id']}")
    for resource in resources.values():
        if resource["capacity_basis"] != "residual" or resource["unit"] != "demo_party_slot":
            errors.append(f"unsupported resource basis/unit: {resource['id']}")
        rows = [row for row in derived if row["resource_id"] == resource["id"]]
        points = {row[k] for row in rows for k in ("start_tick","end_tick")} | {seg[k] for seg in resource["calendar"] for k in ("start_tick","end_tick")}
        for point in sorted(points):
            demand = sum(row["amount"] for row in rows if row["start_tick"] <= point < row["end_tick"])
            capacities = [seg["capacity"] for seg in resource["calendar"] if seg["start_tick"] <= point < seg["end_tick"]]
            if len(capacities) > 1:
                errors.append(f"overlapping resource calendar: {resource['id']}")
                break
            if demand > (capacities[0] if capacities else 0):
                errors.append(f"resource capacity exceeded: {resource['id']} at {point}")
                break
    for restriction in p["restrictions"]:
        if restriction["confirmation"] != "confirmed" or restriction["end"]["kind"] != "known":
            errors.append(f"unsupported restriction form: {restriction['id']}")
            continue
        for ident, time in times.items():
            if ops[ident]["edge_id"] == restriction["edge_id"] and time["start_tick"] < restriction["end"]["end_tick"] and restriction["start_tick"] < time["end_tick"]:
                errors.append(f"closed edge used: {ident}/{restriction['id']}")
    for commitment in p["commitments"]:
        if not commitment["validity"]["start_tick"] <= now < commitment["validity"]["end_tick"]:
            errors.append(f"commitment not current: {commitment['id']}")
        if commitment["kind"] == "service_conditions":
            service = services[commitment["service_id"]]
            if any(service[key] != commitment[key] for key in ("departure_tick","load_window")):
                errors.append(f"service commitment mismatch: {commitment['id']}")
        elif commitment["kind"] == "operation_start_window":
            time = times.get(commitment["operation_id"])
            if time is None or not commitment["window"]["start_tick"] <= time["start_tick"] < commitment["window"]["end_tick"]:
                errors.append(f"operation commitment violated: {commitment['id']}")
        else:
            errors.append(f"unsupported commitment: {commitment['id']}")

    groups = _group_decisions(p,plans,times)
    supplied_groups = _index(r["group_decisions"],"group_id",errors,"group decision")
    if supplied_groups != {g["group_id"]:g for g in groups}:
        errors.append("group decisions differ from selected route/service/departure")
    receipts = [plan["recipient_receipt_tick"] for plan in plans.values()]
    if mode == "initial":
        objective = {"sum_remaining_tick":sum(t-now for t in receipts),"last_remaining_tick":max(t-now for t in receipts)}
        changed = []
        if "baseline" in p:
            errors.append("initial input has a fabricated baseline")
    else:
        baseline = _index(p["baseline"]["plan"]["party_plans"],"party_id",errors,"baseline party")
        assignments = _index(p["baseline"]["assignments"],"group_id",errors,"baseline group")
        shifts = [max(0,plan["recipient_receipt_tick"]-baseline[party]["recipient_receipt_tick"]) for party,plan in plans.items()]
        compared = {g["group_id"]:g for g in p["controllable_groups"]}
        changed = sorted(g["group_id"] for g in groups if any(g[key] != assignments[g["group_id"]][key] for key in compared[g["group_id"]]["compared_fields"]))
        objective = {"sum_positive_shift":sum(shifts),"max_positive_shift":max(shifts),"changed_groups":len(changed)}
    if r["objective"] != objective:
        errors.append(f"objective differs from independently computed values: expected={objective}")
    if sorted(r.get("changed_group_ids",[])) != changed or len(r.get("changed_group_ids",[])) != len(changed):
        errors.append("changed_group_ids mismatch")
    return errors


def _fixture_result(payload: dict, response: dict) -> dict:
    """Локальный тестовый объект: гипотетический fixture не выдаётся за запуск."""
    p, response = _payload(payload), _payload(response)
    candidate = copy.deepcopy(response["candidate"])
    completed = [{"operation_id":o["id"],"start_tick":o["fixed_start_tick"],"end_tick":o["fixed_end_tick"]} for o in p["operations"] if o["state"] == "completed"]
    candidate["scheduled_operations"] += completed
    plans = {x["party_id"]:x for x in candidate["party_plans"]}
    times = {x["operation_id"]:x for x in candidate["scheduled_operations"]}
    selected = {party:plan["route_id"] for party,plan in plans.items()}
    return {"engine_profile":"research-demo.v1","execution":"test_fixture","synthetic":True,
            "mode":p["mode"],"solver_status":"NOT_EXECUTED_FIXTURE","proven_prefix":0,
            "optimization_stages":[],"candidate":candidate,"objective":copy.deepcopy(response["objective"]),
            "changed_group_ids":copy.deepcopy(response.get("changed_group_ids",[])),
            "resolved_reservations":_reservations(p,selected,times),"group_decisions":_group_decisions(p,plans,times)}


def self_test() -> dict:
    examples = ROOT / "contracts/planning-modes/examples"
    cases = []
    inputs = {}
    for mode in ("initial","replan"):
        p = json.loads((examples/f"{mode}-request.fixture.json").read_text(encoding="utf-8"))["payload"]
        response = json.loads((examples/f"{mode}-response.fixture.json").read_text(encoding="utf-8"))
        r = _fixture_result(p,response)
        errors = validate(p,r,allow_fixture=True)
        assert not errors,(mode,errors)
        cases.append({"id":f"fixture-{mode}","expected":"valid_candidate","passed":True})
        inputs[mode] = (p,r)
    p,r = inputs["replan"]
    def check(name,mutator):
        changed_p,changed_r = copy.deepcopy(p),copy.deepcopy(r)
        mutator(changed_p,changed_r)
        errors = validate(changed_p,changed_r,allow_fixture=True)
        assert errors,f"negative mutation accepted: {name}"
        cases.append({"id":name,"expected":"rejected","passed":True})
    def operation(result,ident):
        return next(t for t in result["candidate"]["scheduled_operations"] if t["operation_id"] == ident)
    check("missing-operation",lambda p,r:r["candidate"]["scheduled_operations"].pop())
    check("fixed-prefix-changed",lambda p,r:operation(r,"P2-BM").update(start_tick=1,end_tick=3))
    def overlap(p,r):
        operation(r,"P2-LOAD").update(start_tick=8,end_tick=9)
        operation(r,"P2-WAIT-M")["end_tick"] = 8
        operation(r,"P2-WAIT-SHIP")["start_tick"] = 9
        times = {x["operation_id"]:x for x in r["candidate"]["scheduled_operations"]}
        selected = {x["party_id"]:x["route_id"] for x in r["candidate"]["party_plans"]}
        r["resolved_reservations"] = _reservations(p,selected,times)
    check("crane-overflow-with-consistent-projection",overlap)
    check("release-lag-omitted",lambda p,r:next(x for x in r["resolved_reservations"] if x["resource_id"] == "M-YARD").update(end_tick=9))
    check("receipt-at-exclusive-end",lambda p,r:p["receipt_constraints"][0]["windows"][0].update(end_tick=21))
    check("false-objective",lambda p,r:r["objective"].update(sum_positive_shift=0))
    check("unaccounted-rail-gap",lambda p,r:operation(r,"P3-BM").update(start_tick=9,end_tick=11))
    check("closed-strait-used",lambda p,r:p["restrictions"].append({"id":"TEST-CLOSURE","edge_id":"UV","start_tick":13,"end":{"kind":"known","end_tick":14},"confirmation":"confirmed","evidence_ids":["DEMO-CASE"]}))
    check("false-service-commitment",lambda p,r:p["commitments"][0].update(departure_tick=11))
    check("missing-fixed-fact",lambda p,r:r["candidate"]["fixed_fact_ids"].pop())
    return {"scope":"independent candidate validator self-test on hypothetical fixtures and semantic mutations; no solver execution", "solver_executed":False,"cases":cases,"passed":len(cases)}


def main() -> int:
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--request",type=Path)
    parser.add_argument("--result",type=Path)
    parser.add_argument("--self-test",action="store_true")
    args=parser.parse_args()
    if args.self_test:
        print(json.dumps(self_test(),ensure_ascii=False,indent=2))
        return 0
    if not args.request or not args.result:
        parser.error("provide --request and --result, or --self-test")
    request=json.loads(args.request.read_text(encoding="utf-8"))
    result=json.loads(args.result.read_text(encoding="utf-8"))
    errors=validate(request,result)
    print(json.dumps({"candidate_checked":result.get("candidate") is not None,"errors":errors,"optimality_independently_proved":False,"infeasibility_independently_proved":False},ensure_ascii=False,indent=2))
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
