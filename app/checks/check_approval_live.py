"""Проверка настоящего локального workflow; создаёт учебные расчёты/пакеты/ответы.

Требует отдельную БД rzd_arctica_* из RZD_DATABASE_URL, индивидуальные ключи
из игнорируемого private-файла и psql. Ничего не удаляет. SQL-изменения грантов
и срока ответа ниже — управляемые условия испытания, не пользовательский API.
"""
from pathlib import Path
from datetime import datetime, timedelta, timezone
from http.cookiejar import CookieJar
import argparse, concurrent.futures, hashlib, json, os, subprocess, time
import urllib.request, urllib.error, urllib.parse, uuid

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--credentials', default='.local/approval-credentials.private.json')
parser.add_argument('--psql', default='psql')
parser.add_argument('--output', default='.local/approval-live.json')
parser.add_argument('--state-output', default='.local/approval-ui-state.private.json')
parser.add_argument('--base-url', default='http://127.0.0.1:8092')
parser.add_argument('--prepare-ui-only', action='store_true')
args = parser.parse_args()
users = json.loads(Path(args.credentials).read_text(encoding='utf-8'))['users']
keys = {u['id']: u['access_key'] for u in users}
db = urllib.parse.urlsplit(os.environ['RZD_DATABASE_URL'])
assert db.hostname in ('127.0.0.1', 'localhost', '::1')
assert db.path.lstrip('/').startswith('rzd_arctica_')
env = dict(os.environ, PGHOST=db.hostname, PGPORT=str(db.port or 5432),
           PGUSER=urllib.parse.unquote(db.username or ''),
           PGPASSWORD=urllib.parse.unquote(db.password or ''),
           PGDATABASE=db.path.lstrip('/'), PGCLIENTENCODING='UTF8')
env.pop('PGSERVICE', None)
PSQL = [args.psql, '-X', '-tA', '-v', 'ON_ERROR_STOP=1']
BASE = args.base_url.rstrip('/')
assert urllib.parse.urlsplit(BASE).hostname == '127.0.0.1'
if not args.prepare_ui_only:
    assert env['PGDATABASE'].startswith('rzd_arctica_approval_checks_'), 'Full checks require a separate test database'
    assert urllib.parse.urlsplit(BASE).port == 8092, 'Full checks require the separate test server on8092'
ORIGIN = 'http://127.0.0.1:5173'
checks = []


def sql(query):
    return subprocess.check_output(PSQL, input=query, text=True, encoding='utf-8', env=env).strip()


def check(name, condition):
    if not condition:
        raise AssertionError(name)
    checks.append({'id': name, 'passed': True})


def request(actor, method, path, body=None, key=None, opener=None, origin=ORIGIN):
    headers = {'Content-Type': 'application/json'}
    if actor:
        headers['Authorization'] = 'Bearer ' + keys[actor]
    if origin:
        headers['Origin'] = origin
    if key:
        headers['Idempotency-Key'] = key
    data = json.dumps(body).encode() if isinstance(body, dict) else body
    req = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        response = (opener.open if opener else urllib.request.urlopen)(req, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    raw = response.read()
    return response.code, json.loads(raw) if raw else None, response.headers


def call(actor, method, path, body=None, key=None):
    status, value, _ = request(actor, method, path, body, key)
    return status, value


def fresh():
    return 'approval-live-' + uuid.uuid4().hex


def listing():
    status, value = call('coordinator.demo', 'GET', '/api/v1/approval-packets')
    assert status == 200, ('listing', status)
    return value


def get(packet_id):
    status, value = call('coordinator.demo', 'GET', '/api/v1/approval-packets/' + packet_id)
    assert status == 200, ('get packet', status)
    return value


def calculation():
    status, value = call('coordinator.demo', 'POST', '/api/v1/calculations', {'scenario_id': 'S1'}, fresh())
    assert status == 202
    for _ in range(150):
        status, value = call('coordinator.demo', 'GET', '/api/v1/calculations/' + value['id'])
        if value['status'] in ('succeeded', 'failed'):
            break
        time.sleep(.1)
    assert value['status'] == 'succeeded' and value['result']['solver_status'] == 'OPTIMAL'
    return value


def create(calc):
    ctx = listing()['context']
    body = {'calculation_id': calc['id'], 'expected_source_revision': ctx['source_revision'],
            'expected_accepted_version': ctx['accepted_version']}
    status, value = call('coordinator.demo', 'POST', '/api/v1/approval-packets', body, fresh())
    assert status in (200, 201), ('create', status, value)
    return value


OWNERS = {'SERVICE-A': 'marine.demo', 'SERVICE-B': 'marine.demo', 'WAIT-O': 'railway.demo',
          'RAIL-P3': 'railway.demo', 'RAIL-P4': 'railway.demo', 'PORT-M': 'port-m.demo',
          'PORT-D': 'port-d.demo', 'LAST-MILE': 'last-mile.demo', 'ACCESS': 'shipper.demo',
          **{f'RECIPIENT-P{i}': f'recipient-p{i}.demo' for i in range(1, 5)}}


def response_body(doc, subject_id, decision='agree', expires=None):
    packet = doc['packet']
    subject = next(s for s in packet['subjects'] if s['id'] == subject_id)
    return {'packet_version': packet['version'], 'subject_id': subject_id,
            'subject_sha256': subject['sha256'], 'expected_revision': subject['revision'],
            'decision': decision,
            'expires_at': expires or (datetime.now(timezone.utc) + timedelta(hours=2)).isoformat()}


def respond(doc, subject_id, decision='agree', key=None):
    return call(OWNERS[subject_id], 'POST', '/api/v1/approval-packets/' + doc['packet']['id'] + '/responses',
                response_body(doc, subject_id, decision), key or fresh())


def fill(doc, skip=()):
    for subject_id in OWNERS:
        if subject_id in skip:
            continue
        packet_id = doc['packet']['id']
        status, _ = respond(doc, subject_id)
        assert status == 200, ('response', subject_id, status)
        doc = get(packet_id)
    return doc


def accept_body(doc):
    ctx, packet = doc['context'], doc['packet']
    return {'packet_version': packet['version'], 'expected_approval_revision': packet['approval_revision'],
            'expected_source_revision': ctx['source_revision'], 'expected_accepted_version': ctx['accepted_version']}


def accept(doc, key=None):
    return call('coordinator.demo', 'POST', '/api/v1/approval-packets/' + doc['packet']['id'] + '/accept',
                accept_body(doc), key or fresh())


class HeldWorkflow:
    def __enter__(self):
        self.p = subprocess.Popen(PSQL, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  text=True, encoding='utf-8', bufsize=1, env=env)
        self.send("BEGIN;\nSELECT workflow_id FROM rzd_approval_context WHERE workflow_id='demo-chain' FOR UPDATE;")
        while True:
            line = self.p.stdout.readline()
            if line.strip() == 'demo-chain':
                break
            if line == '':
                raise AssertionError('workflow lock unavailable')
        return self

    def send(self, query):
        self.p.stdin.write(query + '\n')
        self.p.stdin.flush()

    def __exit__(self, kind, value, traceback):
        self.send('ROLLBACK;' if kind else 'COMMIT;')
        self.p.stdin.close()
        assert self.p.wait(timeout=8) == 0


def await_blocked():
    for _ in range(50):
        if int(sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock';")):
            return
        time.sleep(.05)
    raise AssertionError('HTTP accept did not wait for the workflow lock')


if args.prepare_ui_only:
    ui_doc = fill(create(calculation()), skip=('SERVICE-A',))
    Path(args.state_output).parent.mkdir(parents=True, exist_ok=True)
    Path(args.state_output).write_text(json.dumps({'packet_id': ui_doc['packet']['id'], 'calculation_id': ui_doc['packet']['calculation_id']}), encoding='utf-8')
    print('Prepared one live UI packet with SERVICE-A awaiting its own representative.')
    raise SystemExit(0)

check('isolated-nonsuperuser-database', sql("SELECT current_database() LIKE 'rzd_arctica_%' AND NOT rolsuper FROM pg_roles WHERE rolname=current_user;") == 't')
check('health-remains-public', call(None, 'GET', '/api/health')[0] == 200)
check('anonymous-history-denied', call(None, 'GET', '/api/v1/calculations')[0] == 401)
check('wrong-key-denied', request(None, 'POST', '/api/v1/session', {'access_key': 'invalid'}, origin=ORIGIN)[0] == 401)
jar = CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
status, identity, headers = request(None, 'POST', '/api/v1/session', {'access_key': keys['coordinator.demo']}, opener=opener)
cookie = headers.get('Set-Cookie', '')
check('session-login-hides-key', status == 200 and identity['user']['id'] == 'coordinator.demo' and keys['coordinator.demo'] not in json.dumps(identity))
check('session-cookie-flags', 'HttpOnly' in cookie and 'SameSite=Strict' in cookie)
check('session-read-restored', request(None, 'GET', '/api/v1/session', opener=opener)[0] == 200)
check('cookie-mutation-missing-origin-denied', request(None, 'POST', '/api/v1/calculations', {'scenario_id': 'S1'}, fresh(), opener, None)[0] == 403)
check('cookie-foreign-origin-denied', request(None, 'POST', '/api/v1/calculations', {'scenario_id': 'S1'}, fresh(), opener, 'https://example.com')[0] == 403)
check('session-logout', request(None, 'DELETE', '/api/v1/session', opener=opener)[0] == 204)
check('logged-out-session-denied', request(None, 'GET', '/api/v1/session', opener=opener)[0] == 401)
check('participant-history-denied', call('marine.demo', 'GET', '/api/v1/calculations')[0] == 403)
check('participant-calculation-create-denied', call('marine.demo', 'POST', '/api/v1/calculations', {'scenario_id': 'S1'}, fresh())[0] == 403)
check('auditor-can-read-history', call('auditor.demo', 'GET', '/api/v1/calculations')[0] == 200)
check('auditor-cannot-create-calculation', call('auditor.demo', 'POST', '/api/v1/calculations', {'scenario_id': 'S1'}, fresh())[0] == 403)

calc = calculation()
doc = create(calc)
packet_id = doc['packet']['id']
check('thirteen-server-subjects', {s['id'] for s in doc['packet']['subjects']} == set(OWNERS))
check('packet-binds-actual-calculation', doc['packet']['calculation_id'] == calc['id'] and doc['packet']['input_sha256'] == calc['input_sha256'])
for party in ('P3', 'P4'):
    plan = next(p for p in calc['result']['candidate']['party_plans'] if p['party_id'] == party)
    subject = next(s for s in doc['packet']['subjects'] if s['id'] == 'RAIL-' + party)
    details = json.dumps(subject['details'])
    expected = ('-OC', '-CM') if plan['route_id'].endswith('-alt') else ('-OB', '-BM')
    check('dynamic-route-' + party, all(party + suffix in details for suffix in expected))
check('coordinator-not-resource-representative', all(not s['can_respond'] for s in doc['packet']['subjects']))
status, limited = call('railway.demo', 'GET', '/api/v1/approval-packets/' + packet_id)
check('participant-subject-isolation', status == 200 and {s['id'] for s in limited['packet']['subjects']} == {'WAIT-O', 'RAIL-P3', 'RAIL-P4'} and not limited['packet'].get('fixed_context'))
check('cannot-accept-without-responses', accept(doc)[0] == 409)
body = response_body(doc, 'SERVICE-A')
check('coordinator-cannot-confirm-ship', call('coordinator.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', body, fresh())[0] == 403)
check('foreign-subject-denied', call('railway.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', body, fresh())[0] == 403)
bad = dict(body, subject_sha256='0' * 64)
check('foreign-subject-hash-denied', call('marine.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', bad, fresh())[0] in (400, 409))
check('client-actor-injection-denied', call('marine.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', dict(body, actor_id='coordinator.demo'), fresh())[0] == 400)
race_key = fresh()
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    values = list(pool.map(lambda _: call('marine.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', body, race_key), range(8)))
check('eight-response-retries-one-event', all(s == 200 for s, _ in values) and sql(f"SELECT count(*) FROM rzd_approval_responses WHERE packet_id='{packet_id}' AND subject_id='SERVICE-A';") == '1')
check('response-key-other-body-conflict', call('marine.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', dict(body, decision='rejected'), race_key)[0] == 409)
doc = get(packet_id)
body = response_body(doc, 'SERVICE-B')
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
    values = list(pool.map(lambda _: call('marine.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/responses', body, fresh()), range(2)))
check('response-cas-one-winner', sorted(s for s, _ in values) == [200, 409])
doc = fill(get(packet_id))
status, declined = respond(doc, 'SERVICE-A', 'conditional')
check('conditional-is-not-agreement', status == 200 and accept(declined)[0] == 409)
status, declined = respond(declined, 'SERVICE-A', 'rejected')
check('rejection-blocks-acceptance', status == 200 and accept(declined)[0] == 409)
doc = fill(declined)
check('all-agreements-ready', doc['packet']['can_accept'] is True)
with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
    with HeldWorkflow() as lock:
        lock.send(f"UPDATE rzd_approval_responses SET expires_at=clock_timestamp()+interval '1 second' WHERE packet_id='{packet_id}' AND subject_id='SERVICE-A';")
        future = pool.submit(accept, doc)
        await_blocked()
        time.sleep(1.3)
    status, _ = future.result()
check('expires-while-accept-waits-for-lock', status == 409 and sql(f"SELECT count(*) FROM rzd_approval_acts WHERE packet_id='{packet_id}';") == '0')
doc = fill(get(packet_id))
with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
    with HeldWorkflow() as lock:
        lock.send("UPDATE rzd_approval_grants SET revoked=true,version=version+1 WHERE user_id='railway.demo';")
        future = pool.submit(accept, doc)
        await_blocked()
    status, _ = future.result()
check('grant-revocation-wins-before-accept', status == 409)
check('revoked-grant-read-denied', call('railway.demo', 'GET', '/api/v1/approval-packets/' + packet_id)[0] == 403)
sql("BEGIN; SELECT workflow_id FROM rzd_approval_context WHERE workflow_id='demo-chain' FOR UPDATE; UPDATE rzd_approval_grants SET revoked=false,version=version+1 WHERE user_id='railway.demo'; COMMIT;")
check('regrant-does-not-reuse-old-response', accept(get(packet_id))[0] == 409)
doc = fill(get(packet_id))
old = create(calc)
new = create(calc)
check('new-version-does-not-inherit-answers', all(s['latest_response'] is None for s in new['packet']['subjects']))
check('superseded-version-cannot-be-accepted', accept(old)[0] == 409)
ctx = new['context']
status, _ = call('coordinator.demo', 'POST', '/api/v1/approval-context/invalidate', {'expected_source_revision': ctx['source_revision']}, fresh())
check('source-revision-only-change-denied', status == 409)
status, invalidated = call('coordinator.demo', 'POST', '/api/v1/planning-context/source', {
    'expected_source_revision': ctx['source_revision'], 'expected_accepted_version': ctx['accepted_version'],
    'service_a_departure_tick': 11, 'service_a_load_window': {'start_tick': 9, 'end_tick': 11},
}, fresh())
check('source-change-invalidates-pending', status == 200 and invalidated['context']['source_revision'] == ctx['source_revision'] + 1 and accept(new)[0] == 409)
ctx = listing()['context']
stale_body = {'calculation_id': calc['id'], 'expected_source_revision': ctx['source_revision'], 'expected_accepted_version': ctx['accepted_version']}
check('old-calculation-cannot-rebind-after-source-change', call('coordinator.demo', 'POST', '/api/v1/approval-packets', stale_body, fresh())[0] == 409)
new_calc = calculation()
stale_body['calculation_id'] = new_calc['id']
check('fresh-fixed-profile-cannot-pretend-new-inputs', call('coordinator.demo', 'POST', '/api/v1/approval-packets', stale_body, fresh())[0] == 409)
# Только подготовка независимого состояния в отдельной тестовой БД, не API
# восстановления источника. История пакетов не удаляется и не переписывается.
assert env['PGDATABASE'].startswith('rzd_arctica_approval_checks_')
sql("BEGIN; SELECT workflow_id FROM rzd_approval_context WHERE workflow_id='demo-chain' FOR UPDATE; UPDATE rzd_approval_context SET source_revision=1,source_changed_at=NULL,active_packet_id=NULL WHERE workflow_id='demo-chain' AND accepted_version=0; COMMIT;")
doc = fill(create(new_calc))
packet_id = doc['packet']['id']
body = accept_body(doc)
accept_keys = [fresh(), fresh()]
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
    values = list(pool.map(lambda k: call('coordinator.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/accept', body, k), accept_keys))
check('two-accepts-one-head-transition', sorted(s for s, _ in values) == [200, 409] and sql(f"SELECT count(*) FROM rzd_approval_acts WHERE packet_id='{packet_id}';") == '1')
winner = next(i for i, (status, _) in enumerate(values) if status == 200)
accept_key = accept_keys[winner]
accepted = values[winner][1]
check('atomic-accepted-head', accepted['packet']['status'] == 'accepted' and accepted['context']['accepted_packet_id'] == packet_id)
status, replay = call('coordinator.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/accept', body, accept_key)
check('acceptance-replay-same-act', status == 200 and replay['packet']['acceptance']['id'] == accepted['packet']['acceptance']['id'])
check('acceptance-key-other-body-conflict', call('coordinator.demo', 'POST', f'/api/v1/approval-packets/{packet_id}/accept', dict(body, expected_accepted_version=99999), accept_key)[0] == 409)
status, review = respond(get(packet_id), 'SERVICE-A', 'revoked')
check('post-accept-revocation-preserves-history', status == 200 and review['packet']['status'] == 'review_required' and review['packet']['acceptance']['id'] == accepted['packet']['acceptance']['id'])
ctx = listing()['context']
body = {'calculation_id': new_calc['id'], 'expected_source_revision': ctx['source_revision'], 'expected_accepted_version': ctx['accepted_version']}
check('new-packet-cannot-replace-calculation-baseline', call('coordinator.demo', 'POST', '/api/v1/approval-packets', body, fresh())[0] == 409)
report = {'checked_at_utc': datetime.now(timezone.utc).isoformat(),
          'scope': 'real localhost HTTP, individual local keys, separate PostgreSQL test database, row locks and CP-SAT; synthetic fixed-base S1 only; not legal authorization or production security certification',
          'checks': checks, 'passed': len(checks), 'failed': 0,
          'race_wait_verified_with_pg_stat_activity': True,
          'administrative_test_mutations': ['response expiry', 'grant revocation/regrant under workflow lock', 'source context restored in separate test database to prepare independent acceptance state'],
          'live_ui_checked_separately': True}
out = Path(args.output)
out.parent.mkdir(parents=True, exist_ok=True)
out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'passed': len(checks), 'failed': 0, 'report': str(out)}, ensure_ascii=False))
