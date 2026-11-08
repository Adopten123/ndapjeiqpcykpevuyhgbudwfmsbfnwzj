"""ASGI и настоящие CP-SAT проверки пересчёта от принятой учебной базы."""
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
parser.add_argument('--deps', type=Path)
parser.add_argument('--planner-deps', type=Path)
parser.add_argument('--output', type=Path)
args = parser.parse_args()
for folder in (args.planner_deps, args.deps):
    if folder:
        sys.path.insert(0, str(folder.resolve()))

from fastapi.testclient import TestClient
import server
import workflow


class WorkflowAPITest(unittest.TestCase):
    native_results = {}

    @classmethod
    def setUpClass(cls):
        cls.app = server.create_app()
        with TestClient(cls.app) as client:
            cls.source = client.get('/v1/demo/scenarios/S1').json()['payload']
        cls.first = server.planner.solve(cls.source)
        assert cls.first['solver_status'] == 'OPTIMAL'
        assert server.checker.validate(cls.source, cls.first) == []
        cls.accepted = cls.baseline(cls.source, cls.first, 'ACCEPTED-PLAN-1')

    @staticmethod
    def baseline(source, result, ident):
        payload = copy.deepcopy(source)
        payload['baseline'] = {'plan_id': ident, 'plan': copy.deepcopy(result['candidate']),
                               'assignments': copy.deepcopy(result['group_decisions'])}
        return payload

    @staticmethod
    def departure(payload, tick):
        payload = copy.deepcopy(payload)
        service = next(row for row in payload['services'] if row['id'] == 'A')
        service['departure_tick'] = tick
        service['load_window'] = {'start_tick': tick - 2, 'end_tick': tick}
        commitment = next(row for row in payload['commitments'] if row['service_id'] == 'A')
        commitment['departure_tick'] = tick
        commitment['load_window'] = copy.deepcopy(service['load_window'])
        return payload

    def setUp(self):
        self.client = TestClient(self.app, raise_server_exceptions=False)

    def tearDown(self):
        self.client.close()

    def request(self, payload=None, profile=workflow.PROFILE):
        return {'request_id': str(uuid4()), 'engine_profile': profile,
                'payload': copy.deepcopy(self.accepted if payload is None else payload)}

    def calculate(self, payload, name):
        request = self.request(payload)
        response = self.client.post('/v1/research/solve', json=request)
        self.assertEqual(response.status_code, 200, response.text)
        body = response.json()
        self.assertEqual(body['request_id'], request['request_id'])
        self.assertEqual(body['engine_profile'], workflow.PROFILE)
        self.assertEqual(body['input_sha256'], server.planner.canonical_hash(payload))
        result = body['result']
        self.assertEqual(result['engine_profile'], workflow.PROFILE)
        self.assertEqual(workflow.validate(payload, result, checker=server.checker), [])
        self.native_results[name] = {'solver_status': result['solver_status'], 'objective': result['objective'],
                                     'proven_prefix': result['proven_prefix'], 'input_sha256': body['input_sha256'],
                                     'baseline_plan_id': payload['baseline']['plan_id']}
        return result

    def assert_rejected(self, payload):
        response = self.client.post('/v1/research/solve', json=self.request(payload))
        self.assertEqual(response.status_code, 422, response.text)
        self.assertNotIn('INFEASIBLE', response.text)

    def test_unchanged_source_against_actual_accepted_s1(self):
        before = copy.deepcopy(self.accepted)
        result = self.calculate(self.accepted, 'same-source-new-accepted-base')
        self.assertEqual(result['solver_status'], 'OPTIMAL')
        self.assertEqual(result['objective'], {'sum_positive_shift': 0, 'max_positive_shift': 0, 'changed_groups': 0})
        self.assertEqual(self.accepted, before)
        self.assertEqual(result['group_decisions'], self.first['group_decisions'])

    def test_actual_later_service_uses_accepted_receipts(self):
        payload = self.departure(self.accepted, 11)
        result = self.calculate(payload, 'A11-after-accepted-A10')
        self.assertEqual(result['solver_status'], 'OPTIMAL')
        self.assertEqual(result['objective']['sum_positive_shift'], 2)
        self.assertEqual(result['objective']['max_positive_shift'], 1)

    def test_new_base_in_a_third_calculation(self):
        first_new = self.departure(self.accepted, 9)
        result_a9 = self.calculate(first_new, 'A9-after-accepted-A10')
        self.assertEqual(result_a9['solver_status'], 'OPTIMAL')
        self.assertEqual(result_a9['objective']['sum_positive_shift'], 0)
        next_payload = self.baseline(self.departure(first_new, 10), result_a9, 'ACCEPTED-PLAN-2')
        result_a10 = self.calculate(next_payload, 'A10-after-accepted-A9')
        self.assertEqual(result_a10['solver_status'], 'OPTIMAL')
        self.assertEqual(result_a10['objective']['sum_positive_shift'], 2)
        self.assertEqual(result_a10['objective']['max_positive_shift'], 1)

    def test_real_infeasible_keeps_no_candidate(self):
        result = self.calculate(self.departure(self.accepted, 12), 'A12-infeasible-fixed-due')
        self.assertEqual(result['solver_status'], 'INFEASIBLE')
        self.assertIsNone(result['candidate'])
        self.assertIsNone(result['objective'])

    def test_assignment_must_come_from_baseline_schedule(self):
        payload = copy.deepcopy(self.accepted)
        payload['baseline']['assignments'][0]['departure_tick'] += 1
        self.assert_rejected(payload)

    def test_missing_or_foreign_baseline_party_route(self):
        for mutation in ('missing', 'foreign', 'duplicate'):
            with self.subTest(mutation=mutation):
                payload = copy.deepcopy(self.accepted)
                plans = payload['baseline']['plan']['party_plans']
                if mutation == 'missing':
                    plans.pop()
                elif mutation == 'foreign':
                    plans[0]['route_id'] = 'UNKNOWN-ROUTE'
                else:
                    plans[0] = copy.deepcopy(plans[1])
                self.assert_rejected(payload)

    def test_baseline_facts_schedule_and_receipts_are_bound(self):
        for mutation in ('fact', 'gap', 'receipt', 'missing-operation', 'unknown-field'):
            with self.subTest(mutation=mutation):
                payload = copy.deepcopy(self.accepted)
                plan = payload['baseline']['plan']
                if mutation == 'fact':
                    plan['fixed_fact_ids'].pop()
                elif mutation == 'gap':
                    row = next(x for x in plan['scheduled_operations'] if x['operation_id'] == 'P1-LOAD')
                    row['start_tick'] += 1
                    row['end_tick'] += 1
                elif mutation == 'receipt':
                    plan['party_plans'][0]['recipient_receipt_tick'] += 1
                elif mutation == 'missing-operation':
                    plan['scheduled_operations'].pop()
                else:
                    payload['baseline']['authorization'] = 'CLIENT-CLAIM'
                self.assert_rejected(payload)

    def test_reserved_initial_baseline_id_cannot_hide_different_content(self):
        payload = copy.deepcopy(self.accepted)
        payload['baseline']['plan_id'] = 'DEMO-v0'
        self.assert_rejected(payload)

    def test_legacy_research_still_rejects_dynamic_base(self):
        response = self.client.post('/v1/research/solve', json=self.request(profile=server.PROFILE))
        self.assertEqual(response.status_code, 422)
        with self.assertRaises(server.planner.UnsupportedInput):
            server.planner.solve(self.accepted)

    def test_profile_and_unknown_envelope_are_closed(self):
        for key, value in (('engine_profile', 'workflow-demo.v2'), ('source_context', {'revision': 2})):
            request = self.request()
            request[key] = value
            self.assertEqual(self.client.post('/v1/research/solve', json=request).status_code, 422)

    def test_integer_guard_applies_to_dynamic_baseline(self):
        payload = copy.deepcopy(self.accepted)
        payload['baseline']['assignments'][0]['departure_tick'] = 6.0
        with mock.patch.object(workflow, 'solve') as solve:
            self.assert_rejected(payload)
            solve.assert_not_called()

    def test_changed_graph_remains_unsupported(self):
        payload = copy.deepcopy(self.accepted)
        payload['operations'][0]['duration']['max_tick'] += 1
        self.assert_rejected(payload)

    def test_independent_validator_rejects_a_corrupt_new_candidate(self):
        result = workflow.solve(self.accepted, planner=server.planner, checker=server.checker)
        result['candidate']['party_plans'][0]['recipient_receipt_tick'] += 1
        self.assertTrue(workflow.validate(self.accepted, result, checker=server.checker))
        self.assertTrue(server.checker.validate(self.accepted, result))

    def test_failed_workflow_validation_is_sanitized(self):
        with mock.patch.object(workflow, 'solve', return_value={'secret': 'PRIVATE-SENTINEL'}), \
             mock.patch.object(workflow, 'validate', return_value=['PRIVATE-SENTINEL']):
            response = self.client.post('/v1/research/solve', json=self.request())
        self.assertEqual(response.status_code, 500)
        self.assertNotIn('PRIVATE-SENTINEL', response.text)

    def test_workflow_and_legacy_share_the_concurrency_limit(self):
        entered, release = threading.Event(), threading.Event()
        real_solve = server.planner.solve

        def blocked(payload):
            entered.set()
            if not release.wait(5):
                raise RuntimeError('test barrier timed out')
            return real_solve(payload)

        with mock.patch.object(server.planner, 'solve', side_effect=blocked):
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                pending = pool.submit(self.client.post, '/v1/research/solve', json=self.request(self.source, server.PROFILE))
                self.assertTrue(entered.wait(5))
                try:
                    response = self.client.post('/v1/research/solve', json=self.request())
                    self.assertEqual(response.status_code, 503)
                finally:
                    release.set()
                self.assertEqual(pending.result(timeout=10).status_code, 200)


if __name__ == '__main__':
    started = datetime.now(timezone.utc).isoformat()
    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(WorkflowAPITest))
    report = {'scope': 'in-process ASGI plus actual CP-SAT and independent candidate validation; baseline acceptance authority remains in Go',
              'started_at_utc': started, 'unittest_cases': result.testsRun, 'failures': len(result.failures),
              'errors': len(result.errors), 'skipped': len(result.skipped), 'native_results': WorkflowAPITest.native_results}
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_bytes((json.dumps(report, ensure_ascii=False, indent=2) + '\n').encode('utf-8'))
    raise SystemExit(0 if result.wasSuccessful() else 1)
