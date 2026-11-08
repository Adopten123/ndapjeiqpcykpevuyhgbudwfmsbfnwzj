"""Живая приёмка симуляции на отдельном локальном Go/PostgreSQL стенде.

Для проверок повтора и отказа источника настройте RZD_SIMULATOR_URL своего
Go-процесса на свободный локальный порт и передайте его как --feed-proxy-port.
Проверка поднимет на этом порту прокси к настоящему FastAPI-имитатору.
Для проверки отказа вычислителя передайте --compute-proxy-port и
--backend-exe. Скрипт сам поднимет отдельный Go после запуска прокси.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
from urllib.error import HTTPError
from urllib.parse import parse_qs, unquote, urlencode, urlsplit
from urllib.request import Request, urlopen
from uuid import UUID


RUNS = '/api/v1/simulation/runs'
CONTEXT = '/api/v1/planning-context'


def loopback(url):
    parsed = urlsplit(url)
    return parsed.scheme == 'http' and parsed.hostname == '127.0.0.1' and parsed.port and not (
        parsed.username or parsed.password or parsed.path or parsed.query or parsed.fragment
    )


@contextmanager
def serve_local(port, handler):
    with ThreadingHTTPServer(('127.0.0.1', port), handler) as server:
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            yield
        finally:
            server.shutdown()
            worker.join()


@contextmanager
def feed_proxy(port, upstream):
    modes = {}
    if port is None:
        yield None
        return

    class FeedProxy(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            address = urlsplit(self.path)
            parts = address.path.split('/')
            mode = modes.get(parts[3], modes.get('*')) if len(parts) == 5 else None
            if mode == 'offline':
                self.send_error(503)
                return
            query = parse_qs(address.query)
            upstream_path = self.path
            duplicate_page = mode == 'duplicate' and query.get('after') == ['qa-page-1']
            if duplicate_page:
                upstream_path = address.path + '?' + urlencode(dict(query, after=['0']), doseq=True)
            try:
                with urlopen(upstream + upstream_path, timeout=1) as response:
                    payload = json.load(response)
                if mode == 'duplicate' and payload.get('events') and not duplicate_page:
                    tick = payload['events'][0]['available_tick']
                    payload.update(next_cursor='qa-page-1', has_more=True,
                                   complete_through_tick=tick - 1, next_tick=tick)
                if mode == 'invalid' and payload.get('events'):
                    original = payload['events'][0]
                    payload['events'] = [dict(original, target_id='INVALID'), dict(
                        original, event_id='bad-window', parameters={'load_from_tick': 11, 'load_until_tick': 9,
                                                                     'departure_tick': 11})]
                if mode == 'conflicting-replay' and query.get('through_tick') == ['2']:
                    previous_query = urlencode({'through_tick': 1, 'after': 0, 'scenario': query['scenario'][0]})
                    with urlopen(upstream + address.path + '?' + previous_query, timeout=1) as response:
                        previous = json.load(response)['events'][0]
                    payload['events'].insert(0, dict(previous, reason='Изменённый повтор сообщения'))
            except (OSError, ValueError):
                self.send_error(502)
                return
            data = json.dumps(payload).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    with serve_local(port, FeedProxy):
        yield modes


@contextmanager
def compute_proxy(port, upstream):
    if port is None:
        yield None
        return
    modes = {}

    class ComputeProxy(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def forward(self, data=None):
            request = Request(upstream + self.path, data=data, method=self.command,
                              headers={'Content-Type': 'application/json'})
            try:
                try:
                    response = urlopen(request, timeout=10)
                except HTTPError as error:
                    response = error
                with response:
                    status, body = response.status, response.read()
            except OSError:
                self.send_error(502)
                return
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            self.forward()

        def do_POST(self):
            data = self.rfile.read(int(self.headers['Content-Length']))
            document = json.loads(data)
            if self.path == '/v1/research/solve' and document.get('engine_profile') == 'simulation-demo.v1':
                mode = modes.get(document['payload']['run_id'])
                if mode == 'offline':
                    self.send_error(503)
                    return
                if mode is not None:
                    arrived, release = mode
                    arrived.set()
                    if not release.wait(8):
                        self.send_error(504)
                        return
            self.forward(data)

    with serve_local(port, ComputeProxy):
        yield modes


@contextmanager
def launch_backend(executable, base, source_port, compute_port):
    if executable is None:
        yield
        return
    address = urlsplit(base)
    with socket.socket() as probe:
        probe.settimeout(.3)
        assert probe.connect_ex((address.hostname, address.port)) != 0, 'Порт Go уже занят'
    env = dict(os.environ, RZD_HTTP_ADDR=address.netloc)
    if source_port is not None:
        env['RZD_SIMULATOR_URL'] = f'http://127.0.0.1:{source_port}'
    if compute_port is not None:
        env['RZD_COMPUTE_URL'] = f'http://127.0.0.1:{compute_port}'
    flags = subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0
    with subprocess.Popen([str(executable.resolve())], env=env, creationflags=flags) as server:
        try:
            for _ in range(100):
                assert server.poll() is None, 'Go завершился при запуске'
                try:
                    with urlopen(base + '/api/health', timeout=.3) as response:
                        if response.status == 200:
                            break
                except OSError:
                    time.sleep(.15)
            else:
                raise AssertionError('Go не стал доступен')
            yield
        finally:
            if server.poll() is None:
                server.terminate()


class Acceptance:
    def __init__(self, base, credentials, modes, compute_modes, psql, database):
        self.base = base
        self.key = next(row['access_key'] for row in credentials['users'] if row['id'] == 'coordinator.demo')
        self.modes = modes
        self.compute_modes = compute_modes
        self.psql = psql
        self.database = database
        self.checks = []
        self.snapshots = []
        self.scope = 'standard'

    def attempts(self, run):
        database = self.database
        env = dict(os.environ, PGHOST=database.hostname, PGPORT=str(database.port or 5432),
                   PGUSER=unquote(database.username or ''), PGPASSWORD=unquote(database.password or ''),
                   PGDATABASE=database.path.lstrip('/'), PGCLIENTENCODING='UTF8')
        env.pop('PGSERVICE', None)
        sql = f"""SELECT coalesce(json_agg(row_to_json(a) ORDER BY condition_revision, created_at), '[]')
            FROM rzd_sim_attempts a WHERE run_id = '{UUID(run['id'])}';"""
        return json.loads(subprocess.check_output([self.psql, '-X', '-tA', '-v', 'ON_ERROR_STOP=1'],
                                                   input=sql, text=True, encoding='utf-8', env=env))

    def call(self, method, path, body=None, expected=(200, 201, 202)):
        headers = {'Authorization': 'Bearer ' + self.key, 'Content-Type': 'application/json'}
        request = Request(self.base + path, method=method, headers=headers,
                          data=json.dumps(body).encode() if body is not None else None)
        try:
            response = urlopen(request, timeout=20)
        except HTTPError as error:
            response = error
        with response:
            assert response.status in expected, (method, path, response.status, expected)
            data = response.read()
        return json.loads(data) if data else None

    def check(self, name, condition):
        identifier = f'{self.scope}/{name}'
        assert condition, identifier
        self.checks.append(identifier)

    def record_snapshots(self, attempts):
        for row in attempts:
            payload = row['payload']
            self.snapshots.append({
                'scenario': self.scope, 'decision_tick': payload['decision_tick'],
                'condition_revision': row['condition_revision'], 'state_version': row['state_version'],
                'plan_version': row['plan_version'], 'input_sha256': row['input_sha256'],
                'solver_status': row['result']['solver_status'],
            })

    def get(self, run):
        return self.call('GET', f'{RUNS}/{run["id"]}')

    def action(self, run, action, body, expected=(200, 202)):
        return self.call('POST', f'{RUNS}/{run["id"]}/{action}', body, expected)

    def create(self, scenario, **extra):
        run = self.call('POST', RUNS, {'name': f'Приёмка {scenario}', 'scenario': scenario, **extra})
        assert run['tick'] == 0 and len(run['parties']) == 4 and run['status'] == 'ready', 'T01-fresh-run'
        return run

    def wait(self, run):
        until = time.monotonic() + 30
        while run['status'] in ('queued', 'running'):
            assert time.monotonic() < until, ('solver timeout', run['tick'])
            time.sleep(.12)
            run = self.get(run)
        return run

    def next(self, run, tick):
        for _ in range(31):
            if run['tick'] == tick:
                return run
            assert run['tick'] < tick and run['status'] == 'ready', (run['tick'], tick, run['status'])
            run = self.wait(self.action(run, 'step', {'state_version': run['state_version']}))
        raise AssertionError(('tick not reached', tick))

    def apply(self, run, replay=False):
        assert run['status'] == 'candidate', (run['tick'], run['status'], run['message'])
        candidate = run['candidate']
        self.check(f'T01-verified-result-at-{run["tick"]}', candidate['status'].upper() in ('OPTIMAL', 'FEASIBLE') and candidate['plan'])
        self.action(run, 'step', {'state_version': run['state_version']}, expected=(409,))
        applied = self.action(run, 'apply', {'candidate_id': candidate['id']})
        self.check(f'T05-single-plan-version-at-{run["tick"]}', applied['plan_version'] == run['plan_version'] + 1 and
                   applied['active_plan'] == candidate['plan'])
        if replay:
            self.action(run, 'apply', {'candidate_id': candidate['id']}, expected=(200, 409))
            self.check('T05-apply-replay', self.get(run)['plan_version'] == applied['plan_version'])
        return applied

    def through_repair(self, run, manual_id=None, race=False):
        start = run
        if race:
            body = {'state_version': run['state_version']}
            with ThreadPoolExecutor(2) as pool:
                list(pool.map(lambda _: self.action(run, 'step', body, expected=(200, 202, 409)), range(2)))
            first = self.wait(self.get(run))
        else:
            first = self.next(run, 1)
        self.check('T01-carrier-change', first['tick'] == 1 and first['condition_revision'] == start['condition_revision'] + 1
                   and [(row['source'], row['status']) for row in first['events']] == [('carrier-mock', 'applied')])
        initial_move = next(fact for fact in start['facts'] if fact['operation_id'] == 'P2-BM')
        self.check('T03-initial-movement-unchanged', initial_move['state'] == 'in_progress' and initial_move in first['facts'])
        self.action(start, 'step', {'state_version': start['state_version']}, expected=(409,))
        first_applied = self.apply(first, replay=race)
        second = self.next(first_applied, 2)
        expected_sources = {'carrier-mock', 'rail-mock'} | ({'manual'} if manual_id else set())
        self.check('T01-combined-batch', second['condition_revision'] == first['condition_revision'] + 1 and
                   len(second['events']) == len(expected_sources) and
                   {event['source'] for event in second['events'] if event['status'] == 'applied'} == expected_sources)
        if manual_id:
            self.check('T06-manual-event-released', any(event['event_id'] == manual_id for event in second['events']) and
                       all(event['event_id'] != manual_id for event in second['manual_events']))
            self.call('PUT', f'{RUNS}/{run["id"]}/events/{manual_id}', self.manual(), expected=(400, 404, 409, 422))
            self.check('T06-released-event-immutable', any(event['event_id'] == manual_id for event in self.get(run)['events']))
        self.check('T03-completed-movement-preserved', dict(initial_move, state='completed') in second['facts'])
        self.check('T01-second-solver-candidate', second['status'] == 'candidate')
        active = second['active_plan']
        candidate = second['candidate']['plan']
        times = {row['operation_id']: row for row in candidate['scheduled_operations']}
        self.check('T01-second-plan-retains-delay', all(times[key]['start_tick'] == 11 for key in ('P1-MU', 'P2-MU')))
        self.check('T01-second-plan-obeys-repair', all(key in times for key in ('P3-OB', 'P4-OB')) and
                   all(key not in times for key in ('P3-OC', 'P4-OC')))
        self.check('T01-different-route-in-candidate', [row['route_id'] for row in candidate['party_plans']] !=
                   [row['route_id'] for row in active['party_plans']])
        self.check('T05-readback-restores-state', self.get(second) == second)
        applied = self.apply(second)
        if self.psql:
            attempts = self.attempts(run)
            self.check('T01-one-persisted-attempt-per-revision', len(attempts) == 2 and
                       [row['condition_revision'] for row in attempts] ==
                       [first['condition_revision'], second['condition_revision']])
            self.check('T01-persisted-snapshots-and-results',
                       {event['source'] for event in attempts[1]['payload']['events']} == expected_sources and
                       [row['payload']['decision_tick'] for row in attempts] == [1, 2] and
                       all(row['input_sha256'] == row['result']['input_sha256'] and
                           row['result']['solver_status'] in ('OPTIMAL', 'FEASIBLE') for row in attempts))
            self.record_snapshots(attempts)
        return applied

    @staticmethod
    def manual(tick=2, target='CM', until=12):
        return {'type': 'rail_edge_closed', 'target_id': target, 'available_tick': tick,
                'reason': 'Учебный ручной ремонт', 'parameters': {'from_tick': 2, 'until_tick': until}}

    def compute_faults(self):
        self.scope = 'compute-faults'
        run = self.apply(self.next(self.create('standard'), 1))
        self.compute_modes[run['id']] = 'offline'
        failed = self.next(run, 2)
        self.check('T04-compute-error-retains-plan-and-conditions', failed['status'] == 'compute_error' and
                   failed['tick'] == 2 and failed['condition_revision'] == run['condition_revision'] + 1 and
                   failed['plan_version'] == run['plan_version'] and failed['active_plan'] == run['active_plan'] and
                   failed['candidate'] is None and {row['source'] for row in failed['events']} == {'carrier-mock', 'rail-mock'})
        due = {row['operation_id'] for row in failed['active_plan']['scheduled_operations'] if row['start_tick'] == 2}
        self.check('T04-failure-prevents-due-starts', bool(due) and
                   not due.intersection(row['operation_id'] for row in failed['facts']) and
                   any(row['operation_id'] == 'P2-BM' and row['state'] == 'completed' for row in failed['facts']))
        self.action(failed, 'step', {'state_version': failed['state_version']}, expected=(409,))

        arrived, release = threading.Event(), threading.Event()
        self.compute_modes[run['id']] = arrived, release
        try:
            self.action(failed, 'retry', {'state_version': failed['state_version']})
            self.check('T04-retry-reaches-compute', arrived.wait(6))
            running = self.get(run)
            self.check('T04-running-retains-facts-without-new-starts', running['status'] == 'running' and
                       running['tick'] == failed['tick'] and running['active_plan'] == failed['active_plan'] and
                       running['facts'] == failed['facts'] and running['candidate'] is None)
            self.action(running, 'step', {'state_version': running['state_version']}, expected=(409,))
        finally:
            release.set()
        recovered = self.wait(self.get(run))
        self.check('T04-retry-retains-step-revision-and-old-plan', recovered['status'] == 'candidate' and
                   recovered['tick'] == failed['tick'] and recovered['condition_revision'] == failed['condition_revision'] and
                   recovered['plan_version'] == failed['plan_version'] and recovered['active_plan'] == failed['active_plan'] and
                   recovered['facts'] == failed['facts'])
        applied = self.apply(recovered)
        if self.psql:
            attempts = [row for row in self.attempts(applied) if row['condition_revision'] == recovered['condition_revision']]
            self.check('T04-stores-failed-and-successful-attempts-for-same-revision',
                       [row['status'] for row in attempts] == ['compute_error', 'candidate'] and
                       len({row['id'] for row in attempts}) == 2 and attempts[0]['result'] is None and
                       attempts[0]['payload']['events'] == attempts[1]['payload']['events'] and
                       attempts[0]['payload']['fixed_operations'] == attempts[1]['payload']['fixed_operations'] and
                       attempts[1]['input_sha256'] == attempts[1]['result']['input_sha256'])
            self.record_snapshots(attempts[1:])

    def delivery_modes(self):
        self.scope = 'modes'
        if self.modes is not None:
            self.modes['*'] = 'offline'
        try:
            training = self.call('POST', RUNS, {'name': 'Ручная тренировка', 'mode': 'training'})
            self.check('training-starts-without-mock-feed', training['mode'] == 'training' and
                       training['status'] == 'ready' and training['tick'] == 0 and not training['events'])
            event = {
                'type': 'vessel_schedule_changed', 'target_id': 'A', 'available_tick': 1,
                'reason': 'Тренировка с ручным переносом рейса',
                'parameters': {'load_from_tick': 9, 'load_until_tick': 11, 'departure_tick': 11},
            }
            training = self.call('POST', f'{RUNS}/{training["id"]}/events', event)
            self.check('training-event-is-pending', [e['source'] for e in training['manual_events']] == ['manual'])
            first = self.next(training, 1)
            self.check('training-calculates-with-only-manual-event', first['status'] == 'candidate' and
                       [e['source'] for e in first['events']] == ['manual'] and not first['manual_events'])
            training = self.apply(first)
            second = self.next(training, 2)
            self.check('training-never-injects-mock-repair', second['status'] == 'ready' and
                       second['condition_revision'] == training['condition_revision'] and len(second['events']) == 1)
            copy = self.call('POST', RUNS, {'name': 'Копия тренировки', 'mode': 'training', 'copy_from': training['id']})
            self.check('training-copy-uses-independent-clock', copy['id'] != training['id'] and copy['tick'] == 0 and
                       copy['mode'] == 'training' and [e['available_tick'] for e in copy['manual_events']] == [1])
        finally:
            if self.modes is not None:
                self.modes.pop('*', None)

        self.scope = 'operations'
        live = self.call('POST', RUNS, {'name': 'Рабочая смена', 'mode': 'operations', 'party_count': 2})
        self.check('operations-starts-without-disclosing-future-incidents', live['mode'] == 'operations' and
                   live['status'] == 'ready' and live['tick'] == 0 and not live['events'] and not live['manual_events'])
        path = f'{RUNS}/{live["id"]}/events'
        self.call('POST', path, self.manual(target='INVALID'), expected=(400, 422))
        self.check('operations-invalid-manual-event-does-not-persist', not self.get(live)['manual_events'])
        added = self.call('POST', path, self.manual(until=13))
        event_id = added['manual_events'][0]['event_id']
        self.call('PUT', f'{path}/{event_id}', self.manual())
        added = self.call('POST', path, self.manual(tick=5))
        to_delete = next(e['event_id'] for e in added['manual_events'] if e['available_tick'] == 5)
        live = self.call('DELETE', f'{path}/{to_delete}')
        self.check('operations-adds-edits-and-cancels-future-events', len(live['manual_events']) == 1 and
                   live['manual_events'][0]['event_id'] == event_id and
                   live['manual_events'][0]['parameters']['until_tick'] == 12)
        if self.modes is not None:
            self.modes[live['id']] = 'offline'
            paused = self.wait(self.action(live, 'step', {'state_version': live['state_version']}))
            self.check('operations-waits-when-feed-fails', paused['status'] == 'source_error' and paused['tick'] == 0)
            self.modes.pop(live['id'])
            live = self.wait(self.action(paused, 'step', {'state_version': paused['state_version']}))
        first = self.next(live, 1)
        self.check('operations-receives-carrier-mock', first['status'] == 'candidate' and
                   [e['source'] for e in first['events']] == ['carrier-mock'])
        second = self.next(self.apply(first), 2)
        self.check('operations-recalculates-manual-and-mock-in-one-revision', second['status'] == 'candidate' and
                   second['condition_revision'] == first['condition_revision'] + 1 and not second['manual_events'] and
                   sorted(e['source'] for e in second['events']) == ['carrier-mock', 'manual', 'rail-mock'] and
                   any(e['event_id'] == event_id and e['status'] == 'applied' for e in second['events']))
        self.call('PUT', f'{path}/{event_id}', self.manual(until=13), expected=(400, 404, 409, 422))
        self.check('operations-mode-survives-readback', self.get(second) == second and
                   any(row['id'] == second['id'] and row['mode'] == 'operations' for row in self.call('GET', RUNS)['runs']))
        self.check('operations-candidate-keeps-selected-parties', len(live['parties']) == 2 and
                   self.plan_matches_parties(second, second['candidate']['plan']))

    @staticmethod
    def plan_matches_parties(run, plan):
        return sorted(row['party_id'] for row in plan['party_plans']) == sorted(p['id'] for p in run['parties'])

    def party_counts(self):
        self.scope = 'party-count'
        for count in (0, -1, 2.5, 5, 10, 14, '2', None, True):
            error = self.call('POST', RUNS,
                              {'name': 'Недопустимое число партий', 'mode': 'training', 'party_count': count},
                              expected=(400,))
            assert error['field'] == 'party_count', count
        self.check('invalid-counts-do-not-create-runs',
                   all(row['name'] != 'Недопустимое число партий' for row in self.call('GET', RUNS)['runs']))
        for count in (1, 2, 3):
            run = self.call('POST', RUNS, {'name': f'Учебный прогон · {count}', 'mode': 'training', 'party_count': count})
            self.check(f'{count}-parties-in-initial-and-active-plan', len(run['parties']) == count and
                       all(self.plan_matches_parties(run, run[plan]) for plan in ('base_plan', 'active_plan')))
            event = {
                'type': 'vessel_schedule_changed', 'target_id': 'A', 'available_tick': 1,
                'reason': 'Перенос рейса для проверки состава',
                'parameters': {'load_from_tick': 9, 'load_until_tick': 11, 'departure_tick': 11},
            }
            run = self.call('POST', f'{RUNS}/{run["id"]}/events', event)
            result = self.next(run, 1)
            self.check(f'{count}-parties-after-recalculation', result['status'] == 'candidate' and
                       self.plan_matches_parties(run, result['candidate']['plan']))
            applied = self.action(result, 'apply', {'candidate_id': result['candidate']['id']})
            self.check(f'{count}-parties-survive-readback', self.get(run) == applied and
                       self.plan_matches_parties(run, applied['active_plan']))

    def cargo_and_route(self):
        self.scope = 'cargo-route'
        payload = {'name': 'Выбор груза', 'mode': 'training', 'party_count': 2,
                   'cargo_id': 'steel-fasteners', 'cargo_name': '  Стальные болты  ',
                   'route_id': 'khovrino-murmansk-kamchatka'}
        for field, value in [('cargo_id', 'dangerous'), ('cargo_name', 'Строка\nСтрока'),
                             ('cargo_name', 'а' * 101), ('route_id', 'unknown')]:
            error = self.call('POST', RUNS, dict(payload, **{field: value}), expected=(400,))
            assert error['field'] == field
        self.check('unknown-options-and-invalid-name-are-rejected',
                   all(row['name'] != payload['name'] for row in self.call('GET', RUNS)['runs']))
        run = self.call('POST', RUNS, payload)
        self.check('cargo-and-route-persist', self.get(run) == run and
                   run['cargo'] == {'id': 'steel-fasteners', 'name': 'Стальные болты'} and
                   run['route'] == {'id': payload['route_id']})
        copied = self.call('POST', RUNS, {'name': 'Копия выбранного груза', 'mode': 'training', 'copy_from': run['id']})
        self.check('copy-inherits-cargo-route-and-count', copied['cargo'] == run['cargo'] and
                   copied['route'] == run['route'] and len(copied['parties']) == 2)

    def run(self):
        initial_context = self.call('GET', CONTEXT)
        standard = self.through_repair(self.create('standard'), race=True)
        self.scope = 'critical'
        critical = self.through_repair(self.create('critical'))
        stopped = self.next(critical, 3)
        self.check('T01-critical-no-plan', stopped['status'] == 'infeasible' and
                   stopped['events'][-1]['source'] == 'ice-mock' and stopped['active_plan'] == critical['active_plan'] and
                   (stopped['candidate'] is None or stopped['candidate']['plan'] is None))
        self.action(stopped, 'apply', {'candidate_id': '00000000-0000-4000-8000-000000000000'}, expected=(400, 409, 422))
        self.action(stopped, 'step', {'state_version': stopped['state_version']}, expected=(409,))
        if self.psql:
            attempt = self.attempts(stopped)[-1]
            self.check('T01-persisted-critical-attempt', attempt['result']['solver_status'] == 'INFEASIBLE' and
                       attempt['payload']['decision_tick'] == 3 and len(attempt['payload']['events']) == 3)
            self.record_snapshots([attempt])
        self.check('T05-other-run-unmodified', self.get(standard) == standard)

        self.scope = 'manual'
        manual = self.create('standard')
        path = f'{RUNS}/{manual["id"]}/events'
        self.call('POST', path, self.manual(target='INVALID'), expected=(400, 422))
        self.call('POST', path, self.manual(until=1), expected=(400, 422))
        self.check('T06-invalid-manual-inputs-do-not-persist', not self.get(manual)['manual_events'])
        self.call('POST', path, self.manual())
        two = self.call('POST', path, self.manual(tick=5))
        by_tick = {row['available_tick']: row['event_id'] for row in two['manual_events']}
        self.check('T06-multiple-pending-events', set(by_tick) == {2, 5})
        edited = self.call('PUT', f'{path}/{by_tick[2]}', self.manual(until=13))
        self.check('T06-edit-pending-event', any(row['event_id'] == by_tick[2] and row['parameters']['until_tick'] == 13
                                               for row in edited['manual_events']))
        self.call('DELETE', f'{path}/{by_tick[5]}')
        copied = self.create('standard', copy_from=manual['id'])
        self.check('T06-copy-keeps-pending-events', copied['id'] != manual['id'] and len(copied['manual_events']) == 1 and
                   copied['manual_events'][0]['parameters']['until_tick'] == 13)
        self.through_repair(self.get(manual), manual_id=by_tick[2])
        self.check('T05-copy-isolated', self.get(copied) == copied)

        if self.modes is not None:
            self.scope = 'source-faults'
            run = self.create('standard')
            self.modes[run['id']] = 'offline'
            self.action(run, 'step', {'state_version': run['state_version']}, expected=(200, 202, 409, 503))
            paused = self.get(run)
            self.check('T06-source-failure-keeps-cursor-and-tick', paused['status'] == 'source_error' and
                       paused['tick'] == run['tick'] and paused['source'] == run['source'])
            self.modes[run['id']] = 'duplicate'
            self.action(paused, 'step', {'state_version': paused['state_version']})
            recovered = self.wait(self.get(run))
            self.check('T02-exact-replay-across-pages-on-recovery', recovered['tick'] == 1 and recovered['status'] == 'candidate' and
                       recovered['condition_revision'] == run['condition_revision'] + 1 and len(recovered['events']) == 1 and
                       recovered['source']['complete_through_tick'] == 1 and recovered['source']['cursor'] == '1')
            applied = self.apply(recovered)
            self.modes[run['id']] = 'conflicting-replay'
            conflict = self.next(applied, 2)
            self.check('T02-changed-replay-conflicts', conflict['status'] == 'conflict' and
                       any(row['status'] == 'conflict' and row['event_id'] == 'delay-a' for row in conflict['events']))
            if self.psql:
                self.check('T02-no-additional-attempt-on-replays', len(self.attempts(run)) == 1)
            invalid = self.create('standard')
            self.modes[invalid['id']] = 'invalid'
            rejected = self.next(invalid, 1)
            self.check('T06-invalid-http-events-are-logged-without-recalculation', rejected['status'] == 'ready' and
                       rejected['condition_revision'] == invalid['condition_revision'] and
                       len(rejected['events']) == 2 and all(row['status'] == 'rejected' and row['message']
                                                           for row in rejected['events']))

        if self.compute_modes is not None:
            self.compute_faults()

        self.scope = 'standard'
        completed = self.next(standard, max(row['end_tick'] for row in standard['active_plan']['scheduled_operations']))
        self.check('T01-all-parties-completed', completed['status'] == 'completed' and
                   all(fact['state'] == 'completed' for fact in completed['facts']))

        listing = self.call('GET', RUNS)['runs']
        self.check('T05-list-restores-runs', {standard['id'], stopped['id'], manual['id'], copied['id']} <=
                   {row['id'] for row in listing})
        self.delivery_modes()
        self.party_counts()
        self.cargo_and_route()
        self.scope = 'standard'
        self.check('T05-global-approval-base-unchanged', self.call('GET', CONTEXT) == initial_context)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', required=True, help='Отдельный Go стенд, например http://127.0.0.1:8190')
    parser.add_argument('--credentials', required=True, type=Path)
    parser.add_argument('--feed-proxy-port', type=int)
    parser.add_argument('--upstream', default='http://127.0.0.1:8094')
    parser.add_argument('--compute-proxy-port', type=int)
    parser.add_argument('--compute-upstream', default='http://127.0.0.1:8091')
    parser.add_argument('--backend-exe', type=Path, help='Запустить отдельный Go после прокси с RZD_AUTH_FILE из окружения')
    parser.add_argument('--psql', help='Путь к psql для сверки сохранённых снимков и числа расчётов')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    database = urlsplit(os.environ.get('RZD_DATABASE_URL', ''))
    if database.hostname != '127.0.0.1' or not database.path.startswith('/rzd_arctica_sim'):
        parser.error('Требуется отдельная локальная БД rzd_arctica_sim* в RZD_DATABASE_URL')
    if not all(map(loopback, (args.base_url, args.upstream, args.compute_upstream))):
        parser.error('Разрешены только локальные HTTP-адреса на 127.0.0.1 с указанным портом')
    services = {urlsplit(url).port for url in (args.base_url, args.upstream, args.compute_upstream)}
    proxy_ports = [port for port in (args.feed_proxy_port, args.compute_proxy_port) if port is not None]
    if len(set(proxy_ports)) != len(proxy_ports) or services.intersection(proxy_ports) or any(
            not 1 <= port <= 65535 for port in proxy_ports):
        parser.error('Прокси должны использовать отдельные свободные порты')
    if args.backend_exe and not os.environ.get('RZD_AUTH_FILE'):
        parser.error('Для запуска Go укажите RZD_AUTH_FILE')
    credentials = json.loads(args.credentials.read_text(encoding='utf-8-sig'))
    with (feed_proxy(args.feed_proxy_port, args.upstream) as modes,
          compute_proxy(args.compute_proxy_port, args.compute_upstream) as compute_modes,
          launch_backend(args.backend_exe, args.base_url, args.feed_proxy_port, args.compute_proxy_port)):
        acceptance = Acceptance(args.base_url, credentials, modes, compute_modes, args.psql, database)
        acceptance.run()
    report = {'checks': acceptance.checks, 'passed': len(acceptance.checks), 'snapshots': acceptance.snapshots,
              'feed_faults_checked': args.feed_proxy_port is not None,
              'compute_faults_checked': args.compute_proxy_port is not None, 'persisted_snapshots_checked': bool(args.psql)}
    text = json.dumps(report, ensure_ascii=False, indent=2) + '\n'
    if args.output:
        args.output.write_text(text, encoding='utf-8')
    print(text, end='')


if __name__ == '__main__':
    main()
