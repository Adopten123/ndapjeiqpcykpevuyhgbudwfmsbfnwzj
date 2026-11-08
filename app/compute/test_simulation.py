"""Проверки узкого профиля симуляции с настоящими CP-SAT и FastAPI."""
from __future__ import annotations

import argparse
import copy
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from uuid import uuid4

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--deps", type=Path)
parser.add_argument("--planner-deps", type=Path)
args = parser.parse_args()
for folder in (args.planner_deps, args.deps):
    if folder:
        sys.path.insert(0, str(folder.resolve()))

from fastapi.testclient import TestClient
import server
import simulation


def candidate_times(result):
    return {row["operation_id"]: row for row in result["candidate"]["scheduled_operations"]}


class SimulationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.app = server.create_app()
        cls.source = server.demo.scenarios()["S1"]
        cls.seed = server.planner.solve(cls.source)
        assert cls.seed["solver_status"] == "OPTIMAL"
        assert server.checker.validate(cls.source, cls.seed) == []
        cls.operations = server.planner.index(cls.source["operations"])

    def setUp(self):
        self.client = TestClient(self.app, raise_server_exceptions=False)
        self.addCleanup(self.client.close)
        self.run_id = str(uuid4())
        self.delay = self.event("delay-a", "carrier-mock", "vessel_schedule_changed", 1, "A",
                                load_from_tick=9, load_until_tick=11, departure_tick=11)
        self.repair = self.event("repair-oc", "rail-mock", "rail_edge_closed", 2, "OC",
                                 from_tick=2, until_tick=12)
        self.ice = self.event("ice-uv", "ice-mock", "sea_edge_closed", 3, "UV",
                              from_tick=8, until_tick=20)

    def event(self, ident, source, kind, available, target, **parameters):
        return {"run_id": self.run_id, "event_id": ident, "source": source, "type": kind, "target_id": target,
                "available_tick": available, "reason": "Учебная проверка", "parameters": parameters}

    def payload(self, base, tick, events, *, revision=1, version=1):
        fixed = [
            {**row, "state": "completed" if row["end_tick"] <= tick else "in_progress"}
            for ident, row in candidate_times(base).items()
            if row["start_tick"] < tick or self.operations[ident]["state"] != "planned"
        ]
        return copy.deepcopy({
            "run_id": self.run_id, "snapshot_id": str(uuid4()), "calculation_id": str(uuid4()), "attempt_id": str(uuid4()),
            "decision_tick": tick, "condition_revision": revision, "state_version": tick + 1, "plan_version": version,
            "baseline": {"plan_id": f"SIM-PLAN-{version}", "plan": base["candidate"], "assignments": base["group_decisions"]},
            "events": events, "fixed_operations": fixed,
        })

    def post(self, payload):
        return self.client.post("/v1/research/solve", json={
            "request_id": str(uuid4()), "engine_profile": simulation.PROFILE, "payload": payload,
        })

    def validate(self, payload, result):
        return simulation.validate(payload, result, source=self.source, planner=server.planner, checker=server.checker)

    def solve(self, payload, status="OPTIMAL"):
        response = self.post(payload)
        self.assertEqual(response.status_code, 200, response.text)
        body = response.json()
        self.assertEqual(body["engine_profile"], simulation.PROFILE)
        self.assertEqual(body["input_sha256"], server.planner.canonical_hash(payload))
        result = body["result"]
        self.assertEqual(result["solver_status"], status)
        self.assertEqual(body["validation_errors"], [])
        self.assertEqual(self.validate(payload, result), [])
        return result

    def test_sequential_incidents_preserve_facts_and_change_plan(self):
        self.assertEqual({row["route_id"] for row in self.seed["candidate"]["party_plans"] if row["service_id"] == "B"},
                         {"P3-alt", "P4-alt"})
        first = self.solve(self.payload(self.seed, 1, [self.delay]))
        times_before, times_first = candidate_times(self.seed), candidate_times(first)
        self.assertEqual(times_first["P2-BM"], times_before["P2-BM"])
        self.assertEqual(times_first["P1-WAIT-M"]["start_tick"], times_before["P1-WAIT-M"]["start_tick"])
        self.assertGreater(times_first["P1-WAIT-M"]["end_tick"], times_before["P1-WAIT-M"]["end_tick"])
        second_input = self.payload(first, 2, [self.delay, self.repair], revision=2, version=2)
        self.assertEqual(next(row["state"] for row in second_input["fixed_operations"] if row["operation_id"] == "P2-BM"),
                         "completed")
        self.assertNotIn("P3-SO", {row["operation_id"] for row in second_input["fixed_operations"]})
        second = self.solve(second_input)
        times_second = candidate_times(second)
        self.assertEqual(times_second["P2-BM"], times_before["P2-BM"])
        self.assertEqual(times_second["P2-WAIT-M"]["start_tick"], 2)
        self.assertEqual(times_second["P1-MU"]["start_tick"], 11)
        self.assertEqual(times_second["P2-MU"]["start_tick"], 11)
        self.assertIn("P3-OB", times_second)
        self.assertIn("P4-OB", times_second)
        self.assertNotIn("P3-OC", times_second)
        self.assertNotIn("P4-OC", times_second)
        self.assertEqual(second["objective"], {"sum_positive_shift": 0, "max_positive_shift": 0, "changed_groups": 2})

        third_input = self.payload(second, 3, [self.delay, self.repair, self.ice], revision=3, version=3)
        third = self.solve(third_input, "INFEASIBLE")
        self.assertIsNone(third["candidate"])
        # UV начинается в 14 у A11. Проверяем короткий вариант из ТЗ и границу интервала.
        for until in (13, 14):
            with self.subTest(until=until):
                third_input["events"][-1]["parameters"]["until_tick"] = until
                reopened = self.solve(third_input)
                self.assertEqual(candidate_times(reopened)["P1-UV"]["start_tick"], 14)

    def test_compare_simultaneous_constraints_with_same_base_and_facts(self):
        payload = self.payload(self.seed, 2, [self.delay, self.repair], revision=2)
        combined = self.solve(payload)
        payload["events"] = [self.repair]
        rail_only = self.solve(payload)
        payload["events"] = [self.delay]
        vessel_only = self.solve(payload)
        self.assertEqual(candidate_times(combined)["P1-MU"]["start_tick"], 11)
        self.assertEqual(candidate_times(rail_only)["P1-MU"]["start_tick"], 10)
        self.assertIn("P3-OB", candidate_times(combined))
        self.assertIn("P3-OC", candidate_times(vessel_only))
        self.assertEqual(combined["objective"], {"sum_positive_shift": 2, "max_positive_shift": 1, "changed_groups": 2})

    def test_common_completed_move_allows_a_future_branch_change(self):
        first = self.solve(self.payload(self.seed, 1, [self.delay]))
        repair = {**self.repair, "available_tick": 3}
        second = self.solve(self.payload(first, 3, [self.delay, repair], revision=2, version=2))
        self.assertEqual(candidate_times(second)["P3-SO"], candidate_times(first)["P3-SO"])
        self.assertIn("P3-OB", candidate_times(second))

    def test_bad_snapshot_or_mutated_candidate_is_rejected(self):
        payload = self.payload(self.seed, 0, [])
        self.assertEqual({row["operation_id"]: row["state"] for row in payload["fixed_operations"]},
                         {"P2-OB-PAST": "completed", "P2-BM": "in_progress"})
        self.solve(payload)
        payload = self.payload(self.seed, 2, [self.delay, self.repair], revision=2)
        result = self.solve(payload)
        incorrect = copy.deepcopy(result)
        candidate_times(incorrect)["P2-BM"]["end_tick"] += 1
        self.assertIn("simulation fact end changed: P2-BM", self.validate(payload, incorrect))
        incorrect = copy.deepcopy(result)
        candidate_times(incorrect)["P3-SO"]["start_tick"] = 1
        self.assertIn("simulation operation starts in the past: P3-SO", self.validate(payload, incorrect))
        payload["fixed_operations"].pop()
        self.assertEqual(self.post(payload).status_code, 422)

    def test_late_restriction_does_not_rewrite_completed_movement(self):
        closure = self.event("late", "manual", "rail_edge_closed", 1, "BM", from_tick=0, until_tick=2)
        active = self.solve(self.payload(self.seed, 1, [closure]), "CONFLICT")
        self.assertEqual(active["conflicts"][0]["operation_id"], "P2-BM")
        closure["received_tick"] = 2
        result = self.solve(self.payload(self.seed, 2, [closure]))
        self.assertEqual(candidate_times(result)["P2-BM"], candidate_times(self.seed)["P2-BM"])
        closure["parameters"]["until_tick"] = 3
        result = self.solve(self.payload(self.seed, 2, [closure]))
        self.assertEqual(candidate_times(result)["P2-BM"], candidate_times(self.seed)["P2-BM"])

    def test_conflicting_schedules_and_change_after_loading(self):
        manual = self.event("move-a-again", "manual", "vessel_schedule_changed", 1, "A",
                            load_from_tick=8, load_until_tick=10, departure_tick=10)
        conflict = self.solve(self.payload(self.seed, 1, [manual, self.delay]), "CONFLICT")
        self.assertEqual(conflict["conflicts"][0]["reason"], "conflicting_service_schedule")
        manual["available_tick"] = 9
        conflict = self.solve(self.payload(self.seed, 9, [manual]), "CONFLICT")
        self.assertIn("loading_already_started", {row["reason"] for row in conflict["conflicts"]})

    def test_service_error_and_unfinished_search_are_distinct(self):
        payload = self.payload(self.seed, 1, [self.delay])
        result = simulation.solve(payload, source=self.source, planner=server.planner, checker=server.checker,
                                  time_limit_seconds=0)
        self.assertEqual(result["solver_status"], "UNKNOWN")
        self.assertIsNone(result["candidate"])
        self.assertEqual(self.validate(payload, result), [])
        with patch.object(simulation, "solve", side_effect=RuntimeError):
            response = self.post(payload)
        self.assertEqual(response.status_code, 500)
        self.assertEqual(response.json()["error"]["code"], "compute_failed")

    def test_independent_validation_checks_loading_capacity_and_elapsed_wait(self):
        payload = self.payload(self.seed, 1, [self.delay])
        result = self.solve(payload)
        times = candidate_times(result)
        first, second = sorted((times["P1-LOAD"], times["P2-LOAD"]), key=lambda row: row["start_tick"])
        party = second["operation_id"].split("-")[0]
        second.update(start_tick=first["start_tick"], end_tick=first["end_tick"])
        times[f"{party}-WAIT-M"]["end_tick"] = first["start_tick"]
        times[f"{party}-WAIT-SHIP"]["start_tick"] = first["end_tick"]
        errors = self.validate(payload, result)
        self.assertTrue(any("resource capacity exceeded:" in message for message in errors), errors)
        times["P1-WAIT-M"]["end_tick"] = 0
        self.assertIn("simulation wait ends before decision: P1-WAIT-M", self.validate(payload, result))

    def test_invalid_event_parameters_are_not_infeasibility(self):
        for target, parameters in (("OC", {"from_tick": 2, "until_tick": 2}),
                                   ("UV", {"from_tick": 2, "until_tick": 12}),
                                   ("OC", {"from_tick": False, "until_tick": 12})):
            with self.subTest(target=target, parameters=parameters):
                event = {**self.repair, "target_id": target, "parameters": parameters}
                response = self.post(self.payload(self.seed, 2, [event]))
                self.assertEqual(response.status_code, 422)
                self.assertNotIn("INFEASIBLE", response.text)

    def test_selected_parties_keep_their_facts_and_replan_without_omitted_parties(self):
        for ids in (["P2"], ["P2", "P3"], ["P1", "P2", "P3"]):
            with self.subTest(ids=ids):
                chosen = set(ids)
                fixed_ids = [fact["id"] for fact in self.source["facts"] if fact["party_id"] in chosen]
                plan = self.seed["candidate"]
                projected = {
                    "candidate": {
                        "party_plans": [party for party in plan["party_plans"] if party["party_id"] in chosen],
                        "scheduled_operations": [operation for operation in plan["scheduled_operations"]
                                                 if self.operations[operation["operation_id"]]["party_id"] in chosen],
                        "fixed_fact_ids": fixed_ids,
                    },
                    "group_decisions": [group for group in self.seed["group_decisions"] if group["party_id"] in chosen],
                }
                first = self.solve({**self.payload(projected, 1, [self.delay]), "party_ids": ids})
                second = self.solve({**self.payload(first, 2, [self.delay, self.repair], revision=2, version=2),
                                     "party_ids": ids})
                self.assertEqual({party["party_id"] for party in second["candidate"]["party_plans"]}, chosen)
                self.assertEqual(second["candidate"]["fixed_fact_ids"], fixed_ids)
                times = candidate_times(second)
                self.assertEqual(times["P2-BM"], candidate_times(self.seed)["P2-BM"])
                self.assertEqual(times["P2-MU"]["start_tick"], 11)
                self.assertEqual({self.operations[ident]["party_id"] for ident in times}, chosen)
                self.assertEqual({row["party_id"] for row in second["resolved_reservations"]}, chosen)
                self.assertEqual([row["party_id"] for row in second["group_decisions"]], ["P3"] if "P3" in chosen else [])
                if "P3" in chosen:
                    self.assertIn("P3-OB", times)
                    self.assertNotIn("P3-OC", times)

    def test_rejects_invalid_selection_and_unprojected_baseline(self):
        payload = self.payload(self.seed, 1, [self.delay])
        for ids in ([], ["P2", "P2"], ["P5"], [None], ["P2"]):
            with self.subTest(ids=ids):
                response = self.post({**payload, "party_ids": ids})
                self.assertEqual(response.status_code, 422, response.text)


if __name__ == "__main__":
    unittest.main(argv=[sys.argv[0]], verbosity=2)
