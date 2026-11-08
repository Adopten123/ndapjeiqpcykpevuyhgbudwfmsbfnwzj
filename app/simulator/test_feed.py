"""Проверки конвертов, порядка, курсора и гарантии полноты учебной ленты."""
import unittest

from fastapi.testclient import TestClient
from server import app, page_events, scenario_events


class FeedTest(unittest.TestCase):
    def setUp(self):
        self.client = TestClient(app)
        self.addCleanup(self.client.close)

    def feed(self, tick, after="0", scenario="standard", run_id="run-one", **params):
        response = self.client.get(f"/v1/runs/{run_id}/events", params={
            "through_tick": tick, "after": after, "scenario": scenario, **params,
        })
        self.assertEqual(response.status_code, 200, response.text)
        return response.json()

    def test_future_events_stay_hidden_and_replays_are_stable(self):
        self.assertEqual(self.feed(0), {
            "events": [], "next_cursor": "0", "complete_through_tick": 0, "next_tick": 1, "has_more": False,
        })
        first = self.feed(1, after="")
        self.assertEqual(first, self.feed(1))
        self.assertEqual(first["events"], [{
            "run_id": "run-one", "source": "carrier-mock", "event_id": "delay-a",
            "type": "vessel_schedule_changed", "target_id": "A", "available_tick": 1,
            "reason": "Задержка судна A",
            "parameters": {"load_from_tick": 9, "load_until_tick": 11, "departure_tick": 11},
        }])
        self.assertEqual(first["next_tick"], 2)
        second = self.feed(2, first["next_cursor"])
        self.assertEqual(second["events"][0]["source"], "rail-mock")
        self.assertEqual(second["events"][0]["parameters"], {"from_tick": 2, "until_tick": 12})
        self.assertEqual(second["next_tick"], None)
        self.assertEqual(self.feed(30, second["next_cursor"]), {
            "events": [], "next_cursor": "2", "complete_through_tick": 30, "next_tick": None, "has_more": False,
        })

    def test_scenarios_keep_ice_scope_and_run_identity(self):
        critical = self.feed(3, scenario="critical")
        short = self.feed(3, scenario="short-ice", run_id="run-two")
        self.assertEqual([event["target_id"] for event in critical["events"]], ["A", "OC", "UV"])
        self.assertEqual(critical["events"][2]["source"], "ice-mock")
        self.assertEqual(critical["events"][2]["parameters"], {"from_tick": 8, "until_tick": 20})
        self.assertEqual(short["events"][2]["parameters"], {"from_tick": 8, "until_tick": 13})
        self.assertTrue(all(event["run_id"] == "run-two" for event in short["events"]))

    def test_operations_variants_are_stable_and_keep_future_events_hidden(self):
        variants = set()
        for run_id in (f"operations-{index}" for index in range(30)):
            with self.subTest(run_id=run_id):
                args = {"scenario": "operations", "run_id": run_id}
                zero = self.feed(0, **args)
                self.assertEqual((zero["events"], zero["next_tick"]), ([], 1))
                first = self.feed(1, **args)
                self.assertEqual([event["source"] for event in first["events"]], ["carrier-mock"])
                self.assertEqual(first["next_tick"], 2)
                second = self.feed(2, first["next_cursor"], **args)
                self.assertEqual([event["source"] for event in second["events"]], ["rail-mock"])
                last = self.feed(3, second["next_cursor"], **args)
                self.assertIn(len(last["events"]), (0, 1))
                if last["events"]:
                    self.assertEqual(last["events"][0]["source"], "ice-mock")
                self.assertEqual((last["next_tick"], last["complete_through_tick"]), (None, 3))
                full = self.feed(30, **args)
                self.assertEqual(full, self.feed(30, **args))
                self.assertEqual(full["events"], first["events"] + second["events"] + last["events"])
                variants.add(tuple(event["parameters"]["until_tick"] for event in full["events"][1:]))
        self.assertEqual(variants, {(11,), (12,), (11, 12), (11, 13), (12, 12), (12, 13)})

    def test_partial_response_guarantees_only_complete_ticks(self):
        first = self.feed(30, scenario="critical", limit=1)
        self.assertTrue(first["has_more"])
        self.assertEqual(first["complete_through_tick"], 1)
        self.assertEqual(first["next_tick"], 2)
        last = self.feed(30, first["next_cursor"], scenario="critical")
        self.assertFalse(last["has_more"])
        self.assertEqual(last["complete_through_tick"], 30)
        self.assertEqual(first["events"] + last["events"], self.feed(30, scenario="critical")["events"])

    def test_same_tick_across_pages_uses_stable_order(self):
        sample = scenario_events("standard")[0]
        rows = [{**sample, "source": source, "event_id": event_id} for source, event_id in (
            ("rail-mock", "a"), ("carrier-mock", "b"), ("carrier-mock", "a"),
        )]
        first = page_events(rows, "test", 1, 0, 2)
        self.assertEqual([row["event_id"] for row in first["events"]], ["a", "b"])
        self.assertEqual(first["complete_through_tick"], 0)
        self.assertEqual(first["next_tick"], 1)
        self.assertEqual(first, page_events(reversed(rows), "test", 1, 0, 2))
        last = page_events(rows, "test", 1, int(first["next_cursor"]), 2)
        self.assertEqual(last["events"][0]["source"], "rail-mock")
        self.assertEqual(last["complete_through_tick"], 1)

    def test_validation_rejects_bad_parameters_and_cursor(self):
        for params in ({}, {"through_tick": -1}, {"through_tick": 31}, {"through_tick": 1, "after": "2"},
                       {"through_tick": 2, "after": "3"}, {"through_tick": 2, "after": "bad"},
                       {"through_tick": 2, "scenario": "unknown"}, {"through_tick": 2, "limit": 0}):
            with self.subTest(params=params):
                self.assertEqual(self.client.get("/v1/runs/test/events", params=params).status_code, 422)


if __name__ == "__main__":
    unittest.main()
