"""Четыре синтетических сценария и смысловые проверки настоящего CP-SAT."""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]


def scenarios():
    folder = ROOT / "contracts"
    def read(relative):
        return json.loads((folder / relative).read_text(encoding="utf-8"))["payload"]
    initial = read("planning-modes/examples/initial-request.fixture.json")
    delayed = read("planning-modes/examples/replan-request.fixture.json")
    result = {"S0": initial, "S1": delayed}
    for name in ("S2", "S3"):
        p = read(f"replan-v1/examples/{name.lower()}-request.fixture.json")
        p["contract_version"], p["mode"] = "planning-replan.v2", "replan"
        p["receipt_constraints"] = copy.deepcopy(initial["receipt_constraints"])
        p["commitments"] = copy.deepcopy(initial["commitments"])
        result[name] = p
    return result


def run():
    from importlib.metadata import version
    from solver import solve, UnsupportedInput, canonical_hash
    from check_demo import validate, self_test
    payloads = scenarios()
    results, tests = {}, []
    for name, p in payloads.items():
        result = solve(p)
        errors = validate(p, result)
        assert not errors, (name, errors)
        assert result["input_sha256"] == canonical_hash(p)
        if name == "S2":
            assert result["solver_status"] == "INFEASIBLE" and result["candidate"] is None
            assert result["proven_prefix"] == 0 and result["objective"] is None
        else:
            expected = {"S0": [84,25], "S1": [8,4,2], "S3": [0,0,2]}[name]
            assert list(result["objective"].values()) == expected, (name, result["objective"])
            assert result["solver_status"] == "OPTIMAL" and result["proven_prefix"] == len(expected)
        result["candidate_independently_validated"] = result["candidate"] is not None
        result["optimality_independently_proved"] = False
        result["infeasibility_independently_proved"] = False
        if name in ("S2", "S3"):
            result["projection_notice"] = "Derived from unchanged replan-v1 request; v2 mode/version plus receipt windows and A6/B14 commitments from initial request. Not an original v2 fixture."
        results[name] = result
        tests.append({"id": name, "passed": True, "solver_status": result["solver_status"], "objective": result["objective"]})

    def test_solve(name, p, status, limit=None):
        result = solve(p, limit)
        assert result["solver_status"] == status, (name, result["solver_status"])
        assert not validate(p, result), (name, validate(p, result))
        if status in {"INFEASIBLE", "UNKNOWN"}:
            assert result["candidate"] is None and result["objective"] is None
        tests.append({"id": name, "passed": True, "solver_status": status})
    p = copy.deepcopy(payloads["S1"])
    p["receipt_constraints"][0]["windows"][0]["end_tick"] = 22
    test_solve("receipt-before-exclusive-end", p, "OPTIMAL")
    p["receipt_constraints"][0]["windows"][0]["end_tick"] = 21
    test_solve("receipt-at-exclusive-end", p, "INFEASIBLE")
    p = copy.deepcopy(payloads["S1"])
    next(r for r in p["resources"] if r["id"] == "M-CRANE")["calendar"][0]["capacity"] = 0
    test_solve("no-crane-capacity", p, "INFEASIBLE")
    p = copy.deepcopy(payloads["S1"])
    p["services"][0]["capacity"] = 1
    test_solve("service-capacity-below-fixed-parties", p, "INFEASIBLE")
    p = copy.deepcopy(payloads["S1"])
    p["commitments"].append({"id":"TEST-RAIL","kind":"operation_start_window","validity":{"start_tick":0,"end_tick":30},
                            "evidence_ids":["DEMO-CASE"],"status":"demo_precondition","operation_id":"P3-OB",
                            "window":{"start_tick":3,"end_tick":4}})
    test_solve("future-departure-causes-yard-overflow", p, "INFEASIBLE")
    test_solve("zero-search-budget", payloads["S1"], "UNKNOWN", 0)
    partial = solve(payloads["S1"], max_tiers=1)
    assert partial["solver_status"] == "FEASIBLE" and partial["proven_prefix"] == 1
    assert len(partial["optimization_stages"]) == 1 and not validate(payloads["S1"], partial)
    tests.append({"id":"stop-after-first-tier","passed":True,"solver_status":"FEASIBLE","proven_prefix":1})

    def rejects(name, mutate):
        p = copy.deepcopy(payloads["S1"])
        mutate(p)
        try:
            solve(p)
        except UnsupportedInput:
            tests.append({"id":name,"passed":True,"outcome":"unsupported_or_invalid_input; solver not invoked"})
        else:
            raise AssertionError(f"unsupported input accepted: {name}")
    rejects("unknown-field", lambda p: p.update(unrecognized_condition=True))
    rejects("reversed-objective-policy", lambda p: p["policy"]["objective_order"].reverse())
    rejects("uncertain-closure-not-infeasible", lambda p: p["restrictions"].append({"id":"TEST-UNKNOWN","edge_id":"UV","start_tick":8,"end":{"kind":"unknown"},"confirmation":"confirmed","evidence_ids":["DEMO-CASE"]}))
    rejects("removed-release-lag", lambda p: next(r for r in p["reservations"] if r["resource_id"] == "M-YARD").update(release_lag_tick=0))
    negative = self_test()
    tests += [{**case, "id":"validator:"+case["id"]} for case in negative["cases"]]
    source_paths = ["contracts/planning-modes/examples/initial-request.fixture.json", "contracts/planning-modes/examples/replan-request.fixture.json",
                    "contracts/replan-v1/examples/s2-request.fixture.json", "contracts/replan-v1/examples/s3-request.fixture.json"]
    source_paths += sorted(str(f.relative_to(ROOT)).replace("\\", "/") for folder in (ROOT/"contracts/replan-v1", ROOT/"contracts/planning-modes") for f in folder.glob("*.schema.json"))
    checksums = {name: hashlib.sha256((ROOT/name).read_bytes().replace(b"\r\n",b"\n")).hexdigest() for name in source_paths}
    code_hashes = {name: hashlib.sha256(Path(__file__).with_name(name).read_bytes().replace(b"\r\n",b"\n")).hexdigest()
                   for name in ("solver.py","check_demo.py","run_demo.py")}
    return {"engine_profile":"research-demo.v1","scope":"synthetic fixed graph; actual CP-SAT executions plus independent candidate validation; no API/ML/authorization/real shipment",
            "direct_dependency_versions":{name:version(name) for name in ("ortools","jsonschema")},
            "dependency_lock_sha256":hashlib.sha256(Path(__file__).with_name("requirements-lock.txt").read_bytes().replace(b"\r\n",b"\n")).hexdigest(),
            "hash_format":"sha256-after-crlf-to-lf","source_hashes":checksums,"code_hashes":code_hashes,
            "scenarios":results,"checks":tests,"passed":len(tests),"failed":0,
            "validator_proves_optimality":False,"s2_infeasible_only_within_this_fixed_profile":True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deps",type=Path)
    parser.add_argument("--output",type=Path)
    args = parser.parse_args()
    if args.deps:
        sys.path.insert(0,str(args.deps.resolve()))
    result = run()
    if args.output:
        args.output.write_bytes((json.dumps(result,ensure_ascii=False,indent=2)+"\n").encode("utf-8"))
    print(json.dumps({"passed":result["passed"],"failed":0,"scenarios":{k:{"solver_status":v["solver_status"],"objective":v["objective"],"proven_prefix":v["proven_prefix"]} for k,v in result["scenarios"].items()}},ensure_ascii=False,indent=2))


if __name__ == "__main__":
    main()
