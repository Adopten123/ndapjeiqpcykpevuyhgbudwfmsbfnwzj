"""Настоящие циклы принятия/пересчёта и гонки в отдельной учебной БД.

Создаёт данные; не удаляет историю. Полный набор требует нового пустого
контура8092 и БД rzd_arctica_replan_checks_*. Ключи/DSN читаются вне Git.
"""
from pathlib import Path
from datetime import datetime, timedelta, timezone
import argparse, concurrent.futures, hashlib, json, os, subprocess, time
import urllib.error, urllib.parse, urllib.request, uuid

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--base-url', default='http://127.0.0.1:8092')
parser.add_argument('--credentials', default='.local/approval-credentials.private.json')
parser.add_argument('--psql', default='psql')
parser.add_argument('--output', default='.local/replanning-live.json')
parser.add_argument('--prepare-ui', action='store_true')
parser.add_argument('--ui-departure-tick', type=int)
parser.add_argument('--state-output', default='.local/replanning-ui.private.json')
args = parser.parse_args()
BASE = args.base_url.rstrip('/')
assert urllib.parse.urlsplit(BASE).hostname == '127.0.0.1'
db = urllib.parse.urlsplit(os.environ['RZD_DATABASE_URL'])
assert db.hostname in ('127.0.0.1', 'localhost', '::1')
assert db.path.lstrip('/').startswith('rzd_arctica_')
if not args.prepare_ui:
    assert db.path.lstrip('/').startswith('rzd_arctica_replan_checks_')
    assert urllib.parse.urlsplit(BASE).port == 8092
keys = {u['id']: u['access_key'] for u in json.loads(Path(args.credentials).read_text(encoding='utf-8'))['users']}
env = dict(os.environ, PGHOST=db.hostname, PGPORT=str(db.port or 5432),
           PGDATABASE=db.path.lstrip('/'), PGUSER=urllib.parse.unquote(db.username or ''),
           PGPASSWORD=urllib.parse.unquote(db.password or ''), PGCLIENTENCODING='UTF8')
env.pop('PGSERVICE', None)
PSQL = [args.psql, '-X', '-tA', '-v', 'ON_ERROR_STOP=1']
checks = []
cycles = []


def check(name, value):
    assert value, name
    checks.append({'id': name, 'passed': True})


def sql(query):
    return subprocess.check_output(PSQL, input=query, text=True, encoding='utf-8', env=env).strip()


def jsql(query):
    return json.loads(sql(query))


def key():
    return 'replanning-' + uuid.uuid4().hex


def call(actor, method, path, body=None, idem=None):
    headers = {'Content-Type': 'application/json', 'Origin': 'http://127.0.0.1:5173'}
    if actor:
        headers['Authorization'] = 'Bearer ' + keys[actor]
    if idem:
        headers['Idempotency-Key'] = idem
    req = urllib.request.Request(BASE + path, method=method, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        response = urllib.request.urlopen(req, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    raw = response.read()
    return response.code, json.loads(raw) if raw else None


def planning():
    status, value = call('coordinator.demo', 'GET', '/api/v1/planning-context')
    assert status == 200, ('planning', status, value)
    return value


def versions(doc):
    return {'expected_source_revision': doc['context']['source_revision'],
            'expected_accepted_version': doc['context']['accepted_version']}


def capture(doc=None, idem=None):
    return call('coordinator.demo', 'POST', '/api/v1/planning-calculations',
                versions(doc or planning()), idem or key())


def completed(row):
    for _ in range(200):
        status, value = call('coordinator.demo', 'GET', '/api/v1/calculations/' + row['id'])
        assert status == 200
        if value['status'] in ('succeeded', 'failed'):
            assert value['status'] == 'succeeded', ('computation failed', value.get('error'))
            return value
        time.sleep(.1)
    raise AssertionError('calculation timeout')


def calculate(doc=None):
    status, row = capture(doc)
    assert status == 202, ('capture', status, row)
    return completed(row)


def stored(row):
    ident = str(uuid.UUID(row['id']))
    return jsql(f"SELECT json_build_object('payload',input_payload,'input_sha256',input_sha256,'result',result,'source_revision',source_revision,'base_accepted_version',base_accepted_version,'base_accepted_packet_id',base_accepted_packet_id,'source_sha256',source_sha256,'baseline_sha256',baseline_sha256,'context_sha256',context_sha256,'jobs',(SELECT count(*) FROM river_job WHERE kind='rzd_demo_calculation' AND args->>'calculation_id'=c.id::text)) FROM rzd_calculations c WHERE id='{ident}';")


def get_packet(ident):
    status, doc = call('coordinator.demo', 'GET', '/api/v1/approval-packets/' + ident)
    assert status == 200
    return doc


def create(row):
    body = dict(versions(planning()), calculation_id=row['id'])
    return call('coordinator.demo', 'POST', '/api/v1/approval-packets', body, key())


OWNERS = {'SERVICE-A': 'marine.demo', 'SERVICE-B': 'marine.demo', 'WAIT-O': 'railway.demo',
          'RAIL-P3': 'railway.demo', 'RAIL-P4': 'railway.demo', 'PORT-M': 'port-m.demo',
          'PORT-D': 'port-d.demo', 'LAST-MILE': 'last-mile.demo', 'ACCESS': 'shipper.demo',
          **{f'RECIPIENT-P{i}': f'recipient-p{i}.demo' for i in range(1, 5)}}


def fill(doc, skip=()):
    ident = doc['packet']['id']
    for subject in doc['packet']['subjects']:
        if subject['id'] in skip:
            continue
        body = {'packet_version': doc['packet']['version'], 'subject_id': subject['id'],
                'subject_sha256': subject['sha256'], 'expected_revision': subject['revision'],
                'decision': 'agree', 'expires_at': (datetime.now(timezone.utc) + timedelta(hours=3)).isoformat()}
        status, _ = call(OWNERS[subject['id']], 'POST', f'/api/v1/approval-packets/{ident}/responses', body, key())
        assert status == 200, ('response', subject['id'], status)
    return get_packet(ident)


def ready(row):
    status, doc = create(row)
    assert status == 201, ('packet', status, doc)
    assert all(s['latest_response'] is None for s in doc['packet']['subjects'])
    return fill(doc)


def accept(doc, idem=None):
    body = dict(versions(doc), packet_version=doc['packet']['version'],
                expected_approval_revision=doc['packet']['approval_revision'])
    return call('coordinator.demo', 'POST', f"/api/v1/approval-packets/{doc['packet']['id']}/accept", body, idem or key())


def save_source(departure, doc=None, idem=None):
    body = dict(versions(doc or planning()), service_a_departure_tick=departure,
                service_a_load_window={'start_tick': departure - 2, 'end_tick': departure})
    return call('coordinator.demo', 'POST', '/api/v1/planning-context/source', body, idem or key())


def objective(row):
    return [row['result']['objective'][x] for x in ('sum_positive_shift', 'max_positive_shift', 'changed_groups')]


def hash_json(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()


def record_cycle(row, doc):
    cycles.append({'source_revision': row['source_revision'], 'base_accepted_version': row['base_accepted_version'],
                   'accepted_version': doc['packet']['acceptance']['accepted_version'], 'objective': objective(row),
                   'input_sha256': row['input_sha256'], 'baseline_sha256': row['baseline_sha256']})


class Lock:
    def __enter__(self):
        self.p = subprocess.Popen(PSQL, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
                                  encoding='utf-8', bufsize=1, env=env)
        self.p.stdin.write("BEGIN;\nSELECT workflow_id FROM rzd_approval_context WHERE workflow_id='demo-chain' FOR UPDATE;\n")
        self.p.stdin.flush()
        while True:
            line = self.p.stdout.readline()
            if line.strip() == 'demo-chain':
                return self
            assert line != '', 'workflow lock unavailable'

    def __exit__(self, kind, value, tb):
        self.p.stdin.write('ROLLBACK;\n' if kind else 'COMMIT;\n')
        self.p.stdin.close()
        assert self.p.wait(timeout=8) == 0


def blocked(count):
    for _ in range(100):
        if int(sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock';")) >= count:
            return
        time.sleep(.03)
    raise AssertionError('concurrent HTTP operations did not reach database lock')


if args.prepare_ui:
    if args.ui_departure_tick is not None:
        status, _ = save_source(args.ui_departure_tick)
        assert status == 200
    row = calculate()
    assert row['result']['solver_status'] == 'OPTIMAL'
    status, doc = create(row)
    assert status == 201, (status, doc)
    doc = fill(doc, skip=('SERVICE-A',))
    Path(args.state_output).write_text(json.dumps({'packet_id': doc['packet']['id'], 'calculation_id': row['id'],
                                                 'objective': objective(row), 'source_revision': row['source_revision'],
                                                 'base_accepted_version': row['base_accepted_version']}), encoding='utf-8')
    print(json.dumps({'ui_packet_prepared': True, 'objective': objective(row), 'source_revision': row['source_revision'],
                      'base_accepted_version': row['base_accepted_version']}))
    raise SystemExit(0)

check('isolated-database-and-role', sql("SELECT current_database() LIKE 'rzd_arctica_replan_checks_%' AND NOT rolsuper FROM pg_roles WHERE rolname=current_user;") == 't')
initial = planning()
check('bootstrap-real-source-v1', initial['source']['revision'] == 1 and initial['source']['service_a_departure_tick'] == 10 and initial['baseline']['accepted_version'] == 0)
check('anonymous-planning-denied', call(None, 'GET', '/api/v1/planning-context')[0] == 401)
check('participant-planning-denied', call('marine.demo', 'GET', '/api/v1/planning-context')[0] == 403)
check('auditor-read-only-context', call('auditor.demo', 'GET', '/api/v1/planning-context')[0] == 200)
check('auditor-capture-denied', call('auditor.demo', 'POST', '/api/v1/planning-calculations', versions(initial), key())[0] == 403)
invalid = dict(versions(initial), service_a_departure_tick=9, service_a_load_window={'start_tick': 8, 'end_tick': 10})
check('invalid-window-rejected', call('coordinator.demo', 'POST', '/api/v1/planning-context/source', invalid, key())[0] == 400)
check('invalid-source-leaves-revision', planning()['source'] == initial['source'])
check('client-snapshot-injection-rejected', call('coordinator.demo', 'POST', '/api/v1/planning-calculations', dict(versions(initial), payload={}), key())[0] == 400)
check('wrong-source-cas-rejected', call('coordinator.demo', 'POST', '/api/v1/planning-calculations', dict(versions(initial), expected_source_revision=999), key())[0] == 409)
first_key = key()
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    replies = list(pool.map(lambda _: capture(initial, first_key), range(8)))
check('eight-captures-one-operation', all(s == 202 for s, _ in replies) and len({v['id'] for _, v in replies}) == 1)
first = completed(replies[0][1])
saved = stored(first)
check('one-river-job', saved['jobs'] == 1)
check('workflow-profile-and-initial-objective', first['engine_profile'] == 'workflow-demo.v1' and objective(first) == [8, 4, 2])
check('stored-snapshot-hash', hash_json(saved['payload']) == first['input_sha256'] == saved['input_sha256'])
check('snapshot-bound-to-source-head', first['source_revision'] == 1 and first['base_accepted_version'] == 0 and first['source_sha256'] == initial['source']['input_sha256'] and first['context_sha256'] == initial['context_sha256'])
# Inject a failure at River insertion: the already inserted calculation must roll back.
before = int(sql('SELECT count(*) FROM rzd_calculations;'))
sql("CREATE FUNCTION rzd_test_fail_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected test failure'; END $$; CREATE TRIGGER rzd_test_fail_job BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION rzd_test_fail_job();")
retry_key = key()
try:
    status, _ = capture(initial, retry_key)
    check('river-failure-closes-request', status == 500)
finally:
    sql('DROP TRIGGER rzd_test_fail_job ON river_job; DROP FUNCTION rzd_test_fail_job();')
check('rollback-no-half-calculation', int(sql('SELECT count(*) FROM rzd_calculations;')) == before)
status, retry = capture(initial, retry_key)
check('retry-after-rollback-creates-operation', status == 202)
retry = completed(retry)
check('retry-after-rollback-one-job', stored(retry)['jobs'] == 1)
doc1 = ready(first)
status, accepted1 = accept(doc1)
check('first-accepted-version', status == 200 and accepted1['context']['accepted_version'] == 1)
record_cycle(first, accepted1)
check('old-capture-cannot-create-after-head-change', create(retry)[0] == 409)
second_context = planning()
second = calculate(second_context)
second_saved = stored(second)
check('same-conditions-new-accepted-base-zero', objective(second) == [0, 0, 0] and second['base_accepted_version'] == 1)
check('exact-accepted-candidate-baseline', second_saved['payload']['baseline']['plan'] == first['result']['candidate'] and second_saved['payload']['baseline']['assignments'] == first['result']['group_decisions'])
check('accepted-plan-id-not-demo-v0', second_saved['payload']['baseline']['plan_id'] == second_context['baseline']['plan_id'] != 'DEMO-v0')
doc2 = ready(second)
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
    with Lock():
        fa = pool.submit(accept, doc2)
        fc = pool.submit(capture, second_context)
        blocked(2)
    accept_result, capture_result = fa.result(), fc.result()
check('capture-versus-accept-linearizes', accept_result[0] == 200 and capture_result[0] in (202, 409))
accepted2 = accept_result[1]
record_cycle(second, accepted2)
if capture_result[0] == 202:
    old = completed(capture_result[1])
    check('capture-first-keeps-old-baseline', old['base_accepted_version'] == 1 and create(old)[0] == 409)
third_context = planning()
after_head = calculate(third_context)
check('accept-first-capture-new-head', after_head['base_accepted_version'] == 2 and stored(after_head)['payload']['baseline']['plan'] == second['result']['candidate'])
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
    with Lock():
        fs = pool.submit(save_source, 9, third_context)
        fc = pool.submit(capture, third_context)
        blocked(2)
    source_result, capture_result = fs.result(), fc.result()
check('capture-versus-source-linearizes', source_result[0] == 200 and capture_result[0] in (202, 409))
if capture_result[0] == 202:
    old = completed(capture_result[1])
    check('capture-first-keeps-old-source', old['source_revision'] == 1 and stored(old)['payload']['services'][0]['departure_tick'] == 10 and create(old)[0] == 409)
updated = planning()
check('source-update-real-revision-and-sha', updated['source']['revision'] == 2 and updated['source']['input_sha256'] != initial['source']['input_sha256'])
source_payload = jsql("SELECT payload FROM rzd_planning_sources WHERE revision=2 AND workflow_id='demo-chain';")
old_source = jsql("SELECT payload FROM rzd_planning_sources WHERE revision=1 AND workflow_id='demo-chain';")
check('source-history-immutable', next(s for s in old_source['services'] if s['id'] == 'A')['departure_tick'] == 10 and next(s for s in source_payload['services'] if s['id'] == 'A')['departure_tick'] == 9)
check('service-commitment-updated-together', next(c for c in source_payload['commitments'] if c['id'] == 'COMMIT-A')['departure_tick'] == 9)
sql(f"UPDATE rzd_calculations SET created_at=clock_timestamp()+interval '10 years' WHERE id='{after_head['id']}';")
check('future-timestamp-cannot-bypass-revision', create(after_head)[0] == 409)
status, replay = capture(initial, first_key)
check('old-idempotency-replay-original-binding', status == 202 and replay['id'] == first['id'] and replay['input_sha256'] == first['input_sha256'] and replay['source_revision'] == 1 and replay['base_accepted_version'] == 0)
check('old-input-unchanged-after-source-update', stored(first)['payload'] == saved['payload'])
third = calculate(updated)
check('new-source-old-baseline-can-be-replanned', third['source_revision'] == 2 and third['base_accepted_version'] == 2 and objective(third) == [0, 0, 0])
third_stored = stored(third)
check('captured-source-matches-new-fields', next(s for s in third_stored['payload']['services'] if s['id'] == 'A')['departure_tick'] == 9)
status, doc3 = create(third)
check('new-source-requires-new-responses', status == 201 and all(s['latest_response'] is None for s in doc3['packet']['subjects']))
check('no-automatic-acceptance', accept(doc3)[0] == 409)
status, accepted3 = accept(fill(doc3))
check('third-accepted-base', status == 200 and accepted3['context']['accepted_version'] == 3)
record_cycle(third, accepted3)
source_key = key()
source_before = planning()
status, back_to_ten = save_source(10, source_before, source_key)
check('source-can-change-again', status == 200 and back_to_ten['source']['revision'] == 3)
fourth = calculate()
check('positive-shift-measured-from-latest-base', fourth['base_accepted_version'] == 3 and objective(fourth) == [2, 1, 0])
check('fourth-base-exactly-third-candidate', stored(fourth)['payload']['baseline']['plan'] == third['result']['candidate'])
status, accepted4 = accept(ready(fourth))
check('fourth-accepted-version', status == 200 and accepted4['context']['accepted_version'] == 4)
record_cycle(fourth, accepted4)
check('four-immutable-acts', sql('SELECT count(*) FROM rzd_approval_acts;') == '4')
for old_doc in (accepted1, accepted2, accepted3):
    old_act = get_packet(old_doc['packet']['id'])['packet']['acceptance']
    check('historic-act-preserved-' + str(old_act['accepted_version']), old_act == old_doc['packet']['acceptance'])
before_replay = sql('SELECT count(*) FROM rzd_planning_sources;')
status, source_replay = save_source(10, source_before, source_key)
check('source-replay-does-not-add-revision', status == 200 and sql('SELECT count(*) FROM rzd_planning_sources;') == before_replay)
check('source-key-other-body-conflict', save_source(11, source_before, source_key)[0] == 409)
report = {'checked_at_utc': datetime.now(timezone.utc).isoformat(),
          'scope': 'real isolated HTTP/PostgreSQL/River/CP-SAT, versioned source and actual accepted baselines; fixed synthetic topology/time, not live transport',
          'checks': checks, 'passed': len(checks), 'failed': 0, 'accepted_cycles': cycles,
          'race_wait_observed_in_pg_stat_activity': True,
          'test_only_mutations': ['temporary failing River INSERT trigger, removed in finally', 'future created_at on one obsolete calculation; revisions still block it']}
out = Path(args.output)
out.parent.mkdir(parents=True, exist_ok=True)
out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'passed': len(checks), 'accepted_versions': len(cycles), 'report': str(out)}, ensure_ascii=False))
