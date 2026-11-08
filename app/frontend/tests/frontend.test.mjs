import test from 'node:test';
import assert from 'node:assert/strict';
import { parseCalculation, getHistory, request, ServiceError } from '../.test-build/api.js';
import { readRoute } from '../.test-build/routing.js';
import { canPreparePacket, executePlanningOperation, makePlanningOperation, matchesPlanningContext, parsePlanningContext, sameSavedCalculation, sourceUpdateNotice, validSource } from '../.test-build/planning.js';
import { executeResearchOperation, makeResearchOperation } from '../.test-build/researchOperation.js';
import { affects, canApply, canAutoAdvance, canStep, changedOperations, createRun, getRun, listRuns, parseRun, runStatus, runStepLabel, signedShift } from '../.test-build/simulation.js';

const hash = char => char.repeat(64);
const clone = value => structuredClone(value);
const stamp = '2026-11-03T00:00:00Z';
function calculation(profile = 'workflow-demo.v1') {
  const result = { engine_profile: profile, execution: 'executed', synthetic: true, mode: 'replan', solver_status: 'OPTIMAL', proven_prefix: 3,
    objective: { sum_positive_shift: 0, max_positive_shift: 0, changed_groups: 0 }, snapshot_id: 'saved-snapshot', input_sha256: hash('a'),
    candidate: { party_plans: [1, 2, 3, 4].map(i => ({ party_id: `P${i}`, route_id: `route-${i}`, service_id: i < 3 ? 'A' : 'B', port_receipt_tick: 18, recipient_receipt_tick: 20 })) } };
  return { id: 'calculation-1', scenario_id: 'S1', status: 'succeeded', created_at: stamp, updated_at: stamp,
    engine_profile: profile, snapshot_id: result.snapshot_id, input_sha256: result.input_sha256, result, error: null,
    ...(profile === 'workflow-demo.v1' ? { source_revision: 2, base_accepted_version: 1, base_accepted_packet_id: 'packet-1', source_sha256: hash('b'), baseline_sha256: hash('c'), context_sha256: hash('d') } : {}),
  };
}
function planning() {
  return { context: { source_revision: 2, approval_revision: 15, active_packet_id: 'packet-1', accepted_packet_id: 'packet-1', accepted_version: 1 },
    source: { revision: 2, input_sha256: hash('b'), service_a_departure_tick: 12, service_a_load_window: { start_tick: 10, end_tick: 12 } },
    baseline: { accepted_version: 1, packet_id: 'packet-1', plan_id: 'accepted-plan-1', candidate_sha256: hash('e'), baseline_sha256: hash('c') }, context_sha256: hash('d'), can_calculate: true };
}
const withFetch = async (fetcher, action) => {
  const previousFetch = globalThis.fetch; const previousWindow = globalThis.window;
  globalThis.window = { setTimeout, clearTimeout, dispatchEvent() {} }; globalThis.fetch = fetcher;
  try { await action(); } finally { globalThis.fetch = previousFetch; if (previousWindow === undefined) delete globalThis.window; else globalThis.window = previousWindow; }
};
const response = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });

test('глубокие ссылки открывают заданный профиль и отвергают повреждённые идентификаторы', () => {
  const base = 'http://127.0.0.1:5183/';
  assert.deepEqual(readRoute(base + '?planning_calculation=run-1'), { page: 'approval', packetId: null, calculationId: 'run-1', error: null });
  assert.equal(readRoute(base + '#approvals/packet-2').packetId, 'packet-2');
  assert.equal(readRoute(base + '#workspace').page, 'calculation');
  for (const suffix of ['#approvals/%', '#approvals/', '#approvals/a%2Fb', '?planning_calculation=', '?planning_calculation=a&planning_calculation=b']) assert.ok(readRoute(base + suffix).error, suffix);
});

test('ссылка симуляции сохраняет выбранный прогон при перезагрузке', () => {
  const base = 'http://127.0.0.1:5183/';
  assert.equal(readRoute(base + '#simulation').page, 'simulation');
  assert.equal(readRoute(base + '#simulation/run-1').simulationRunId, 'run-1');
  assert.ok(readRoute(base + '#simulation/a%2Fb').error);
});

function simulation(status = 'candidate', count = 4) {
  const candidate = calculation().result.candidate;
  const plan = { ...candidate, party_plans: candidate.party_plans.slice(0, count), scheduled_operations: [{ operation_id: 'P2-BM', start_tick: 0, end_tick: 2 }] };
  return { id: 'sim-1', name: 'Учебный прогон', scenario: 'standard', status, tick: 2, plan_version: 2, state_version: 4,
    condition_revision: 2, active_plan: plan, base_plan: plan, candidate: { id: 'sim-candidate', status: 'OPTIMAL', plan },
    operations: [], parties: plan.party_plans.map(({ party_id: id }) => ({ id, due_tick: 30 })), targets: [],
    source: { complete_through_tick: 2, next_tick: null } };
}

test('режим прогона проверяется по источнику событий, прежняя история открывается', async () => {
  const manual = { ...simulation('ready'), scenario: 'manual', mode: 'training' };
  const live = { ...simulation('ready'), scenario: 'operations', mode: 'operations' };
  assert.equal(parseRun(manual).mode, 'training');
  assert.equal(parseRun(live).mode, 'operations');
  assert.equal(parseRun(simulation()).mode, 'legacy');
  assert.throws(() => parseRun({ ...live, mode: 'training' }), ServiceError);
  assert.throws(() => parseRun({ ...live, mode: undefined }), ServiceError);
  await withFetch(async () => response({ runs: [manual, live, simulation()] }), async () => {
    assert.deepEqual((await listRuns()).map(run => run.mode), ['training', 'operations', 'legacy']);
  });
});

test('создание двух режимов отправляет выбор оператора без шаблона', async () => {
  const requests = [];
  await withFetch(async (url, options) => {
    assert.equal(url, '/api/v1/simulation/runs');
    assert.equal(options.method, 'POST');
    const input = JSON.parse(options.body);
    requests.push(input);
    return response({ ...simulation('ready', input.party_count), mode: input.mode, scenario: input.mode === 'training' ? 'manual' : 'operations' });
  }, async () => {
    assert.equal((await createRun({ name: 'Ремонт', mode: 'training', party_count: 1, copy_from: 'sim-old' })).mode, 'training');
    const live = await createRun({ name: 'Диспетчерская смена', mode: 'operations', party_count: 2 });
    assert.equal(live.mode, 'operations');
    assert.equal(live.parties.length, 2);
  });
  assert.deepEqual(requests, [{ name: 'Ремонт', mode: 'training', party_count: 1, copy_from: 'sim-old' },
    { name: 'Диспетчерская смена', mode: 'operations', party_count: 2 }]);
});

test('кандидат должен содержать те же партии, что и прогон', () => {
  const run = simulation('candidate', 2);
  assert.ok(canApply(parseRun(run)));
  run.candidate.plan = simulation().active_plan;
  assert.throws(() => parseRun(run), ServiceError);
});

test('повреждённые вложенные данные прогона дают ServiceError до отрисовки', () => {
  const mutations = [
    run => { run.parties[0].location_id = {}; },
    run => { run.operations = [null]; },
    run => { run.operations = [{ id: 'bad', party_id: 'P1', kind: 'move', edge_id: {} }]; },
    run => { run.facts = [null]; },
    run => { run.facts = [{ operation_id: 'P1-LOAD', start_tick: 2, end_tick: 1, state: 'completed' }]; },
    run => { run.events = [null]; },
    run => { run.events = [{ event_id: 'event', source: 'manual', type: 'rail_edge_closed', target_id: 'OC', available_tick: 2, parameters: { from_tick: {} } }]; },
    run => { run.manual_events = [{ event_id: 'event', source: 'manual', type: 'rail_edge_closed', target_id: 'OC', available_tick: null, parameters: {} }]; },
    run => { run.targets = [null]; },
    run => { run.timeline = [{ tick: 1, kind: 'operation_started', label: {} }]; },
    run => { run.message = {}; },
    run => { run.candidate.message = {}; },
  ];
  for (const mutate of mutations) {
    const run = simulation();
    mutate(run);
    assert.throws(() => parseRun(run), ServiceError);
  }
  const supported = simulation();
  supported.operations = [{ id: 'P1-WAIT-M', party_id: 'P1', kind: 'wait', edge_id: null, service_id: null, from_location_id: 'M', to_location_id: 'M' }];
  supported.events = [{ event_id: 'event', source: 'manual', type: 'rail_edge_closed', target_id: 'OC', available_tick: null, parameters: null }];
  assert.equal(parseRun(supported).events[0].parameters, null);
});

test('автоматический ход доступен только до границы решения в диспетчерском режиме', () => {
  const ready = { mode: 'operations', status: 'ready', tick: 12 };
  assert.equal(canAutoAdvance(ready), true);
  for (const status of ['queued', 'running', 'candidate', 'infeasible', 'unknown', 'compute_error', 'source_error', 'conflict', 'completed']) {
    assert.equal(canAutoAdvance({ ...ready, status }), false, status);
  }
  for (const mode of ['training', 'legacy']) assert.equal(canAutoAdvance({ ...ready, mode }), false, mode);
  assert.equal(canAutoAdvance({ ...ready, tick: 30 }), false);
});

test('индикатор отличает текущий шаг, завершение и предел модели', () => {
  assert.equal(runStepLabel({ status: 'ready', tick: 12 }), 'Шаг 12');
  assert.equal(runStepLabel({ status: 'completed', tick: 25 }), 'Шаг 25 из 25');
  assert.equal(runStepLabel({ status: 'infeasible', tick: 3 }), 'Шаг 3');
  assert.equal(canStep({ status: 'ready', tick: 29 }), true);
  assert.equal(canStep({ status: 'ready', tick: 30 }), false);
  assert.equal(canStep({ status: 'completed', tick: 25 }), false);
});

test('к применению допускается только сохранённый и проверенный кандидат текущего состояния', () => {
  assert.ok(canApply(parseRun(simulation())));
  for (const status of ['ready', 'queued', 'running', 'infeasible', 'unknown', 'compute_error', 'source_error', 'conflict', 'completed']) {
    const run = parseRun(simulation(status));
    assert.equal(canApply(run), false, status);
  }
  for (const status of ['INFEASIBLE', 'UNKNOWN', 'MODEL_INVALID']) {
    const value = simulation(); value.candidate.status = status;
    assert.throws(() => parseRun(value), ServiceError);
  }
  assert.notEqual(runStatus(simulation('infeasible')), runStatus(simulation('unknown')));
  assert.notEqual(runStatus(simulation('unknown')), runStatus(simulation('compute_error')));
  assert.notEqual(runStatus(simulation('compute_error')), runStatus(simulation('source_error')));
});

test('сравнение замечает смену интервала и использует полуоткрытые границы', () => {
  const active = { operation_id: 'P3-OC', start_tick: 2, end_tick: 3 };
  const candidate = { ...active, start_tick: 3, end_tick: 4 };
  const event = { type: 'rail_edge_closed', target_id: 'OC', parameters: { from_tick: 2, until_tick: 3 } };
  assert.deepEqual(changedOperations([active], [candidate]), { before: [active], after: [candidate] });
  assert.equal(affects(event, active, { edge_id: 'OC' }), true);
  assert.equal(affects(event, candidate, { edge_id: 'OC' }), false);
  assert.deepEqual([-1, 0, 1].map(signedShift), ['−1', '0', '+1']);
});

test('чужой прогон отвергается, а серверная ошибка поля возвращается в форму', async () => {
  await withFetch(async () => response(simulation()), async () => {
    await assert.rejects(getRun('foreign'), ServiceError);
  });
  await withFetch(async () => response({ error: 'Выберите объект в учебном графе.', field: 'target_id' }, 400), async () => {
    await assert.rejects(request('/api/v1/simulation/runs/sim-1/events'), { status: 400, field: 'target_id', message: 'Выберите объект в учебном графе.' });
  });
});

test('поздний 401 отменённого запроса не завершает новый сеанс', async () => {
  const controller = new AbortController(); let expired = 0;
  await withFetch(async () => { controller.abort(); return response({}, 401); }, async () => {
    window.dispatchEvent = () => { expired++; };
    await assert.rejects(request('/api/v1/calculations', { signal: controller.signal }), { name: 'AbortError' });
    assert.equal(expired, 0);
  });
});

test('смешанная история сохраняет профиль и не приписывает привязку старому примеру', async () => {
  await withFetch(async () => response([calculation('research-demo.v1'), calculation()]), async () => {
    const result = await getHistory();
    assert.equal(result[0].source_revision, 0); assert.equal(result[0].source_sha256, '');
    assert.equal(result[1].engine_profile, 'workflow-demo.v1'); assert.equal(result[1].base_accepted_packet_id, 'packet-1');
  });
});
test('новый профиль принимает нулевые цели и точную принятую базу', () => {
  const result = parseCalculation(calculation());
  assert.deepEqual(Object.values(result.result.objective), [0, 0, 0]);
  assert.equal(canPreparePacket(result, parsePlanningContext(planning())), true);
});
test('отсутствие любой обязательной привязки workflow отвергается', () => {
  for (const field of ['source_revision', 'base_accepted_version', 'base_accepted_packet_id', 'source_sha256', 'baseline_sha256', 'context_sha256']) {
    const value = calculation(); delete value[field]; assert.throws(() => parseCalculation(value), ServiceError, field);
  }
});
test('чужие nested SHA, профиль и снимок не становятся сохранённым результатом', () => {
  for (const [field, value] of [['input_sha256', hash('f')], ['engine_profile', 'research-demo.v1'], ['snapshot_id', 'foreign-snapshot']]) {
    const record = calculation(); record.result[field] = value; assert.throws(() => parseCalculation(record), ServiceError, field);
  }
});
test('некорректные профили и неполная база блокируются parser', () => {
  for (const patch of [{ scenario_id: 'S0' }, { source_revision: 0 }, { base_accepted_version: -1 }, { base_accepted_version: null, base_accepted_packet_id: null }, { base_accepted_packet_id: null }, { context_sha256: 'short' }, { engine_profile: 'unknown.v1' }]) {
    assert.throws(() => parseCalculation({ ...calculation(), ...patch }), ServiceError);
  }
  assert.throws(() => parseCalculation({ ...calculation('research-demo.v1'), source_revision: 2 }), ServiceError);
});
test('INFEASIBLE и UNKNOWN без кандидата различаются, подготовка запрещена', () => {
  for (const status of ['INFEASIBLE', 'UNKNOWN']) {
    const value = calculation(); Object.assign(value.result, { solver_status: status, objective: null, candidate: null, proven_prefix: 0 });
    const result = parseCalculation(value); assert.equal(result.result.solver_status, status); assert.equal(canPreparePacket(result, planning()), false);
  }
});
test('контекст проверяет согласованность редакции, головы и хешей', () => {
  for (const mutate of [p => { p.source.revision++; }, p => { p.baseline.accepted_version++; }, p => { p.baseline.packet_id = null; }, p => { p.baseline.baseline_sha256 = ''; }, p => { p.context_sha256 = ''; }]) {
    const value = planning(); mutate(value); assert.throws(() => parsePlanningContext(value), ServiceError);
  }
  const initial = planning(); Object.assign(initial.context, { accepted_version: 0, accepted_packet_id: null }); Object.assign(initial.baseline, { accepted_version: 0, packet_id: null, plan_id: 'DEMO-v0' });
  assert.equal(parsePlanningContext(initial).baseline.packet_id, null);
  initial.baseline.plan_id = 'foreign-initial'; assert.throws(() => parsePlanningContext(initial), ServiceError);
});
test('каждая изменённая привязка делает сохранённый кандидат устаревшим', () => {
  const current = parsePlanningContext(planning());
  for (const patch of [{ source_revision: 1 }, { source_sha256: hash('f') }, { base_accepted_version: 0 }, { base_accepted_packet_id: 'other-packet' }, { baseline_sha256: hash('f') }, { context_sha256: hash('f') }, { engine_profile: 'research-demo.v1' }]) {
    assert.equal(matchesPlanningContext({ ...parseCalculation(calculation()), ...patch }, current), false);
    assert.equal(canPreparePacket({ ...parseCalculation(calculation()), ...patch }, current), false);
  }
  const feasible = calculation(); feasible.result.solver_status = 'FEASIBLE'; assert.equal(canPreparePacket(parseCalculation(feasible), current), false);
});
test('окна проверяются в целых шагах, граница 30 допустима', () => {
  assert.equal(validSource({ start: 0, end: 1, departure: 30 }), true);
  for (const values of [{ start: 10, end: 10, departure: 12 }, { start: -1, end: 10, departure: 12 }, { start: 10, end: 13, departure: 12 }, { start: 1, end: 30, departure: 31 }, { start: 1.5, end: 10, departure: 12 }, { start: NaN, end: 10, departure: 12 }]) {
    assert.equal(validSource(values), false); assert.throws(() => makePlanningOperation('source', planning(), values), ServiceError);
  }
});
test('операция сохраняет неизменяемый body и ключ при последующем изменении контекста', () => {
  const context = planning(); const operation = makePlanningOperation('source', context, { start: 10, end: 12, departure: 13 });
  context.context.source_revision = 99; context.source.revision = 99; context.baseline.accepted_version = 99;
  assert.equal(Object.isFrozen(operation), true);
  assert.deepEqual(JSON.parse(operation.body), { expected_source_revision: 2, expected_accepted_version: 1, service_a_departure_tick: 13, service_a_load_window: { start_tick: 10, end_tick: 12 } });
});
test('потеря ответа и повтор отправляют идентичные body/key и используют cookie', async () => {
  const operation = makePlanningOperation('calculate', planning()); const calls = [];
  const queued = calculation(); queued.status = 'queued'; queued.result = null;
  await withFetch(async (path, init) => {
    calls.push({ path, body: init.body, key: init.headers['Idempotency-Key'], credentials: init.credentials });
    if (calls.length === 1) throw new TypeError('network lost');
    return response(queued, 202);
  }, async () => {
    await assert.rejects(executePlanningOperation(operation), error => error instanceof ServiceError && error.status === 0);
    const value = await executePlanningOperation(operation); assert.equal(value.id, queued.id);
  });
  assert.deepEqual(calls[0], calls[1]); assert.equal(calls[0].credentials, 'same-origin');
});
test('409 не создаёт автоматический новый запрос или новую редакцию под старым ключом', async () => {
  const operation = makePlanningOperation('source', planning(), { start: 11, end: 13, departure: 13 }); const calls = [];
  await withFetch(async (path, init) => { calls.push({ path, body: init.body }); return response({}, 409); }, async () => {
    await assert.rejects(executePlanningOperation(operation), error => error instanceof ServiceError && error.status === 409);
  });
  assert.equal(calls.length, 1); assert.equal(calls[0].body, operation.body);
  const newer = planning(); newer.source.revision++; newer.context.source_revision++;
  const newOperation = makePlanningOperation('source', newer, { start: 11, end: 13, departure: 13 });
  assert.notEqual(newOperation.key, operation.key); assert.equal(JSON.parse(newOperation.body).expected_source_revision, 3);
});
test('сохранение окна принимает новую серверную редакцию, а не подставляет локальные условия', async () => {
  const operation = makePlanningOperation('source', planning(), { start: 11, end: 13, departure: 13 });
  const updated = planning(); updated.context.source_revision = 3; updated.context.approval_revision++;
  Object.assign(updated.source, { revision: 3, input_sha256: hash('f'), service_a_departure_tick: 13, service_a_load_window: { start_tick: 11, end_tick: 13 } });
  updated.context_sha256 = hash('e');
  updated.applied_source_revision = 3;
  await withFetch(async (path, init) => {
    assert.equal(path, '/api/v1/planning-context/source'); assert.equal(init.body, operation.body);
    assert.equal(init.headers['Idempotency-Key'], operation.key); return response(updated);
  }, async () => {
    const result = await executePlanningOperation(operation);
    assert.equal(result.source.revision, 3); assert.equal(result.source.input_sha256, hash('f'));
    assert.equal(result.context_sha256, hash('e')); assert.equal(matchesPlanningContext(parseCalculation(calculation()), result), false);
    assert.match(sourceUpdateNotice(result), /сохранены в редакции 3/);
  });
});
test('replay изменения после более новой редакции показывает applied отдельно и сохраняет текущие поля сервера', async () => {
  const operation = makePlanningOperation('source', planning(), { start: 11, end: 13, departure: 13 });
  const latest = planning(); latest.context.source_revision = 4; latest.context.approval_revision += 2;
  Object.assign(latest.source, { revision: 4, input_sha256: hash('f'), service_a_departure_tick: 15, service_a_load_window: { start_tick: 12, end_tick: 14 } });
  latest.context_sha256 = hash('e'); latest.applied_source_revision = 3;
  const calls = [];
  await withFetch(async (path, init) => {
    calls.push({ path, body: init.body, key: init.headers['Idempotency-Key'] });
    if (calls.length === 1) throw new TypeError('lost response after server apply');
    return response(latest);
  }, async () => {
    await assert.rejects(executePlanningOperation(operation), ServiceError);
    const result = await executePlanningOperation(operation);
    assert.equal(result.applied_source_revision, 3); assert.equal(result.source.revision, 4);
    assert.deepEqual(result.source.service_a_load_window, { start_tick: 12, end_tick: 14 }); assert.equal(result.source.service_a_departure_tick, 15);
    assert.notEqual(result.source.service_a_departure_tick, JSON.parse(operation.body).service_a_departure_tick);
    const notice = sourceUpdateNotice(result);
    assert.match(notice, /применено в редакции 3/); assert.match(notice, /актуальна редакция 4/); assert.match(notice, /не перезаписал более новые условия/);
  });
  assert.deepEqual(calls[0], calls[1]);
});
test('source POST требует точную applied-редакцию операции и отвергает пропущенную или чужую', async () => {
  const operation = makePlanningOperation('source', planning(), { start: 11, end: 13, departure: 13 });
  for (const applied of [undefined, null, 0, 2, 4, '3']) {
    const result = planning(); result.source.revision = 4; result.context.source_revision = 4;
    if (applied !== undefined) result.applied_source_revision = applied;
    await withFetch(async () => response(result), async () => assert.rejects(executePlanningOperation(operation), ServiceError));
  }
});
test('каждая чужая привязка в ответе POST блокирует подмену отправленного контекста', async () => {
  const operation = makePlanningOperation('calculate', planning());
  for (const patch of [{ source_revision: 3 }, { base_accepted_version: 2 }, { base_accepted_packet_id: 'other' }, { source_sha256: hash('f') }, { baseline_sha256: hash('f') }, { context_sha256: hash('f') }]) {
    const foreign = { ...clone(calculation()), ...patch };
    await withFetch(async () => response(foreign, 202), async () => assert.rejects(executePlanningOperation(operation), ServiceError));
  }
});
test('опрос сохранённого запуска допускает смену статуса, но сохраняет всю неизменную привязку', () => {
  const queued = parseCalculation(calculation()); queued.status = 'queued'; queued.result = null;
  const completed = parseCalculation(calculation());
  assert.equal(sameSavedCalculation(queued, completed), true);
  for (const patch of [{ id: 'other' }, { input_sha256: hash('f') }, { snapshot_id: 'other' }, { source_revision: 3 }, { source_sha256: hash('f') }, { baseline_sha256: hash('f') }, { context_sha256: hash('f') }, { base_accepted_packet_id: 'other' }, { base_accepted_version: 2 }]) {
    assert.equal(sameSavedCalculation(queued, { ...completed, ...patch }), false);
  }
});

test('UI-ASTRA-003: попытка S1→S0→S1 сохраняет незавершённую операцию и точный body', () => {
  const pending = makeResearchOperation('S1');
  assert.equal(Object.isFrozen(pending), true);
  assert.equal(pending.body, JSON.stringify({ scenario_id: 'S1' }));
  for (const attemptedScenario of ['S0', 'S1', 'S2', 'S3']) {
    const retry = makeResearchOperation(attemptedScenario, pending);
    assert.equal(retry, pending); assert.equal(retry.scenario, 'S1');
    assert.equal(retry.key, pending.key); assert.equal(retry.body, pending.body);
  }
  assert.throws(() => { pending.body = JSON.stringify({ scenario_id: 'S0' }); }, TypeError);
  assert.notEqual(makeResearchOperation('S1').key, pending.key);
});

test('UI-ASTRA-003: применённый POST с потерянным ответом повторяется без второго запуска', async () => {
  const pending = makeResearchOperation('S1'); const calls = []; const saved = new Map();
  const queued = calculation('research-demo.v1'); queued.status = 'queued'; queued.result = null;
  await withFetch(async (path, init) => {
    const call = { path, body: init.body, key: init.headers['Idempotency-Key'], credentials: init.credentials };
    calls.push(call);
    if (!saved.has(call.key)) saved.set(call.key, { body: call.body, calculation: queued });
    assert.equal(saved.get(call.key).body, call.body);
    if (calls.length === 1) throw new TypeError('response dropped after apply');
    return response(saved.get(call.key).calculation, 202);
  }, async () => {
    await assert.rejects(executeResearchOperation(pending), error => error instanceof ServiceError && error.status === 0);
    const retry = makeResearchOperation('S0', pending);
    assert.equal((await executeResearchOperation(retry)).id, queued.id);
  });
  assert.equal(saved.size, 1); assert.deepEqual(calls[0], calls[1]);
  assert.equal(calls[0].credentials, 'same-origin'); assert.equal(calls[0].path, '/api/v1/calculations');
});

test('ответ чужого профиля или сценария не завершает исходную учебную операцию успехом', async () => {
  const pending = makeResearchOperation('S1');
  for (const value of [calculation(), { ...calculation('research-demo.v1'), scenario_id: 'S2', status: 'queued', result: null }]) {
    await withFetch(async () => response(value, 202), async () => assert.rejects(executeResearchOperation(pending), ServiceError));
    assert.equal(makeResearchOperation('S0', pending), pending);
  }
});

test('409 учебного POST не повторяется автоматически с новым ключом или новым сценарием', async () => {
  const pending = makeResearchOperation('S1'); const calls = [];
  await withFetch(async (path, init) => {
    calls.push({ path, body: init.body, key: init.headers['Idempotency-Key'] });
    return response({}, 409);
  }, async () => {
    await assert.rejects(executeResearchOperation(pending), error => error instanceof ServiceError && error.status === 409);
  });
  assert.equal(calls.length, 1); assert.equal(calls[0].body, pending.body); assert.equal(calls[0].key, pending.key);
});
