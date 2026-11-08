"""Проверки внутреннего HTTP-контракта; не сетевой/эксплуатационный аудит."""
from __future__ import annotations

import argparse
import concurrent.futures
import copy
from datetime import datetime, timezone
import json
from pathlib import Path
import sys
import threading
import unittest
from unittest import mock
from uuid import uuid4

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--deps", type=Path)
parser.add_argument("--planner-deps", type=Path)
parser.add_argument("--output", type=Path)
args = parser.parse_args()
if args.planner_deps:
    sys.path.insert(0, str(args.planner_deps.resolve()))
if args.deps:
    sys.path.insert(0, str(args.deps.resolve()))

from fastapi.testclient import TestClient
import server

app = server.create_app()


class ComputeAPITest(unittest.TestCase):
    scenario_results = {}

    def setUp(self):
        self.client = TestClient(app, raise_server_exceptions=False)
        self.detail = self.client.get("/v1/demo/scenarios/S1").json()

    def tearDown(self):
        self.client.close()

    def request(self, payload=None):
        return {"request_id": str(uuid4()), "engine_profile": server.PROFILE,
                "payload": copy.deepcopy(self.detail["payload"] if payload is None else payload)}

    def test_health_and_catalog(self):
        health = self.client.get("/health")
        self.assertEqual(health.status_code, 200)
        self.assertEqual(health.json()["status"], "ok")
        catalog = self.client.get("/v1/demo/scenarios").json()
        self.assertEqual([r["scenario_id"] for r in catalog], ["S0", "S1", "S2", "S3"])
        for item in catalog:
            self.assertIn(item["mode"], {"initial", "replan"})
            self.assertEqual(item["engine_profile"], server.PROFILE)
        self.assertEqual(self.client.get("/docs").status_code, 404)
        self.assertEqual(self.client.get("/v1/demo/scenarios/../../not-a-scenario").status_code, 404)
        self.assertEqual(self.client.get("/v1/demo/scenarios/S4").status_code, 404)
        wrong_method = self.client.get("/v1/research/solve")
        self.assertEqual(wrong_method.status_code, 405)
        self.assertEqual(wrong_method.json()["error"]["code"], "method_not_allowed")

    def test_real_four_scenarios_and_binding(self):
        expected = {"S0": ("OPTIMAL", [84, 25]), "S1": ("OPTIMAL", [8, 4, 2]),
                    "S2": ("INFEASIBLE", None), "S3": ("OPTIMAL", [0, 0, 2])}
        for ident, (status, values) in expected.items():
            with self.subTest(scenario=ident):
                detail = self.client.get("/v1/demo/scenarios/" + ident).json()
                request = self.request(detail["payload"])
                response = self.client.post("/v1/research/solve", json=request)
                self.assertEqual(response.status_code, 200, response.text)
                body = response.json()
                result = body["result"]
                self.assertEqual(body["request_id"], request["request_id"])
                self.assertEqual(body["input_sha256"], detail["input_sha256"])
                self.assertEqual(result["input_sha256"], detail["input_sha256"])
                self.assertEqual(result["snapshot_id"], detail["snapshot_id"])
                self.assertEqual(result["execution"], "executed")
                self.assertTrue(result["synthetic"])
                self.assertEqual(result["solver_status"], status)
                self.assertEqual(list(result["objective"].values()) if values is not None else result["objective"], values)
                self.assertEqual(body["validation_errors"], [])
                self.scenario_results[ident] = {"solver_status": status, "objective": result["objective"],
                                               "proven_prefix": result["proven_prefix"]}

    def test_catalog_snapshot_is_not_mutated_by_client(self):
        self.detail["payload"]["snapshot_id"] = "CLIENT-MUTATION"
        fresh = self.client.get("/v1/demo/scenarios/S1").json()
        self.assertNotEqual(fresh["snapshot_id"], "CLIENT-MUTATION")
        self.assertEqual(fresh["input_sha256"], server.planner.canonical_hash(fresh["payload"]))

    def test_outer_fields_profile_and_uuid_are_strict(self):
        bad = [self.request() for _ in range(4)]
        bad[0]["sensitive_unknown"] = "PRIVATE-SENTINEL"
        bad[1]["engine_profile"] = "future-unapproved.v99"
        bad[2]["request_id"] = 42
        bad[3]["request_id"] = "not-a-uuid"
        with mock.patch.object(server.planner, "solve") as solve:
            for request in bad:
                response = self.client.post("/v1/research/solve", json=request)
                self.assertEqual(response.status_code, 422)
                self.assertNotIn("PRIVATE-SENTINEL", response.text)
            solve.assert_not_called()

    def test_inner_profile_rejected_without_false_infeasibility(self):
        request = self.request()
        request["payload"]["unknown_business_condition"] = "PRIVATE-SENTINEL"
        response = self.client.post("/v1/research/solve", json=request)
        self.assertEqual(response.status_code, 422)
        self.assertEqual(response.json()["error"]["code"], "unsupported_or_invalid_input")
        self.assertNotIn("INFEASIBLE", response.text)
        self.assertNotIn("PRIVATE-SENTINEL", response.text)

    def test_noninteger_ticks_do_not_reach_native_model(self):
        request = self.request()
        request["payload"]["duration_estimates"][0]["value_tick"] = 1.0
        with mock.patch.object(server.planner, "solve") as solve:
            response = self.client.post("/v1/research/solve", json=request)
            self.assertEqual(response.status_code, 422)
            solve.assert_not_called()

    def test_ambiguous_json_is_rejected_before_planner(self):
        bodies = ['{"payload":{},"payload":{}}', '{"payload":{"x":NaN}}',
                  '{"payload":{"x":1e9999}}', '{"payload":{"x":"\\ud800"}}', '{bad', '[]']
        with mock.patch.object(server.planner, "solve") as solve:
            for body in bodies:
                response = self.client.post("/v1/research/solve", content=body, headers={"content-type": "application/json"})
                self.assertIn(response.status_code, {400, 422})
            solve.assert_not_called()

    def test_content_type_and_size_limits(self):
        with mock.patch.object(server.planner, "solve") as solve:
            self.assertEqual(self.client.post("/v1/research/solve", content="{}").status_code, 415)
            self.assertEqual(self.client.post("/v1/research/solve", content="{}", headers={"content-type":"application/json", "content-length":str(server.MAX_REQUEST_BYTES+1)}).status_code, 413)
            chunks = iter([b'{"payload":"', b'x' * server.MAX_REQUEST_BYTES, b'"}'])
            self.assertEqual(self.client.post("/v1/research/solve", content=chunks, headers={"content-type":"application/json"}).status_code, 413)
            solve.assert_not_called()

    def test_failed_candidate_validation_is_not_200(self):
        with mock.patch.object(server.planner, "solve", return_value={"secret": "PRIVATE-SENTINEL"}), \
             mock.patch.object(server.checker, "validate", return_value=["PRIVATE-SENTINEL"]):
            response = self.client.post("/v1/research/solve", json=self.request())
        self.assertEqual(response.status_code, 500)
        self.assertEqual(response.json()["error"]["code"], "invalid_solver_result")
        self.assertNotIn("PRIVATE-SENTINEL", response.text)

    def test_internal_exception_is_sanitized_and_gate_released(self):
        with mock.patch.object(server.planner, "solve", side_effect=RuntimeError("PRIVATE-SENTINEL")):
            response = self.client.post("/v1/research/solve", json=self.request())
        self.assertEqual(response.status_code, 500)
        self.assertEqual(response.json()["error"]["code"], "compute_failed")
        self.assertNotIn("PRIVATE-SENTINEL", response.text)
        # Последующая ошибка входа должна быть422, а не занятый навсегда процесс503.
        request = self.request()
        request["payload"]["unknown"] = True
        self.assertEqual(self.client.post("/v1/research/solve", json=request).status_code, 422)

    def test_concurrent_native_work_is_bounded(self):
        entered, released = threading.Event(), threading.Event()
        real_solve = server.planner.solve

        def blocked(payload):
            entered.set()
            if not released.wait(5):
                raise RuntimeError("test gate timeout")
            return real_solve(payload)

        with mock.patch.object(server.planner, "solve", side_effect=blocked):
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                first = pool.submit(self.client.post, "/v1/research/solve", json=self.request())
                self.assertTrue(entered.wait(5))
                try:
                    second = self.client.post("/v1/research/solve", json=self.request())
                    self.assertEqual(second.status_code, 503)
                    self.assertEqual(second.json()["error"]["code"], "compute_busy")
                finally:
                    released.set()
                self.assertEqual(first.result(timeout=10).status_code, 200)


if __name__ == "__main__":
    started = datetime.now(timezone.utc).isoformat()
    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(ComputeAPITest))
    report = {"scope": "in-process ASGI TestClient; independent native planner validation; not TCP/Go/PostgreSQL or production security",
              "started_at_utc": started, "unittest_cases": result.testsRun, "failures": len(result.failures),
              "errors": len(result.errors), "skipped": len(result.skipped), "scenarios": ComputeAPITest.scenario_results}
    if args.output:
        args.output.write_bytes((json.dumps(report, ensure_ascii=False, indent=2) + "\n").encode("utf-8"))
    raise SystemExit(0 if result.wasSuccessful() else 1)
