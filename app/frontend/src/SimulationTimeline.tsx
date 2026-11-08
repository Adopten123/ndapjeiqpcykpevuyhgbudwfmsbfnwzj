import type { CSSProperties } from 'react';
import { affects, changedOperations, eventNames, partySchedule, runStepLabel, signedShift, simulationHorizon } from './simulation';
import type { Fact, Operation, Party, ReportedEvent, Run, Schedule } from './simulation';
import { edgeLabel, locationLabel, targetLabel } from './simulation-route';
import './simulation-tracking.css';

const sources: Record<string, string> = { 'carrier-mock': 'Морской перевозчик · имитатор', 'rail-mock': 'Железная дорога · имитатор',
  'ice-mock': 'Ледовый мониторинг · имитатор', manual: 'Введено вручную' };
const eventStatuses: Record<string, string> = { applied: 'Учтено в условиях', accepted: 'Учтено в условиях', rejected: 'Отклонено',
  conflict: 'Требуется разбор', expired: 'Срок действия истёк', ignored: 'В журнале', duplicate: 'Повтор доставки' };

function waitLocation(operation: Operation) {
  const name = locationLabel(operation.location_id || operation.from_location_id);
  return `${name[0].toLowerCase()}${name.slice(1)}`;
}

function operationLabel(operation: Operation | undefined, schedule: Schedule) {
  const id = schedule.operation_id;
  if (!operation) return id;
  switch (operation.kind) {
    case 'move': return operation.edge_id ? edgeLabel(operation.edge_id) : id;
    case 'wait': {
      const aboard = id.endsWith('WAIT-SHIP');
      if (schedule.start_tick === schedule.end_tick) {
        return aboard ? `Переход к выходу рейса ${operation.service_id ?? '—'}` : `Переход без ожидания · ${waitLocation(operation)}`;
      }
      return aboard ? `Ожидание на борту рейса ${operation.service_id ?? '—'}` : `Ожидание · ${waitLocation(operation)}`;
    }
    case 'load': return `Погрузка в Мурманске · рейс ${operation.service_id ?? '—'}`;
    case 'unload': return 'Выгрузка в порту Петропавловск-Камчатский';
    default: return id;
  }
}

function latestCompleted(facts: Fact[]) {
  return facts.filter(fact => fact.state === 'completed')
    .sort((a, b) => b.end_tick - a.end_tick || b.start_tick - a.start_tick || b.operation_id.localeCompare(a.operation_id))[0];
}

function startingPosition(party: Party, facts: Fact[], operations: Map<string, Operation>) {
  if (party.location_id) return locationLabel(party.location_id);
  const initialMovement = facts.find(fact => fact.start_tick <= 0 && fact.end_tick > 0 && operations.get(fact.operation_id)?.kind === 'move');
  if (initialMovement) return `В пути · ${operationLabel(operations.get(initialMovement.operation_id), initialMovement)}`;
  const prior = latestCompleted(facts.filter(fact => fact.end_tick <= 0));
  return locationLabel(prior && operations.get(prior.operation_id)?.to_location_id);
}

function position(party: Party, facts: Fact[], operations: Map<string, Operation>) {
  const progress = facts.find(fact => fact.state === 'in_progress');
  if (progress) {
    const operation = operations.get(progress.operation_id);
    const label = operationLabel(operation, progress);
    return operation?.kind === 'move' ? `В пути · ${label}` : label;
  }
  const last = latestCompleted(facts);
  const lastOperation = last && operations.get(last.operation_id);
  return locationLabel(lastOperation?.to_location_id || lastOperation?.location_id || party.location_id);
}

const timeStyle = (start: number, end = start): CSSProperties => ({
  left: `${Math.max(start, 0) * 100 / simulationHorizon}%`, width: `${Math.max(0, end - Math.max(start, 0)) * 100 / simulationHorizon}%`,
});

export function eventTitle(event: Pick<ReportedEvent, 'type' | 'target_id'>, run: Pick<Run, 'targets'>) {
  const target = run.targets.find(item => item.type === event.type && item.id === event.target_id);
  return `${eventNames[event.type] ?? 'Сообщение источника'} · ${target ? targetLabel(target) : event.target_id || 'объект неизвестен'}`;
}

export function eventWindow(event: Pick<ReportedEvent, 'type' | 'parameters'>) {
  const params = event.parameters ?? {};
  if (event.type === 'vessel_schedule_changed' && params.departure_tick != null && params.load_from_tick != null && params.load_until_tick != null) {
    return `Погрузка [${params.load_from_tick}, ${params.load_until_tick}), выход на шаге ${params.departure_tick}`;
  }
  if (params.from_tick != null && params.until_tick != null) return `Действует на шагах [${params.from_tick}, ${params.until_tick})`;
  return 'Интервал не указан';
}

function TimeTrack({ tick, children }: { tick: number; children: React.ReactNode }) {
  return <div className="sim-track" aria-hidden="true"><span className="sim-now" style={timeStyle(tick)} />{children}</div>;
}

function timeSpan({ start_tick, end_tick }: Schedule) {
  return start_tick === end_tick ? `шаг ${start_tick}` : `${start_tick}–${end_tick}`;
}

function operationTime(schedule: Schedule, fact: Fact | undefined) {
  if (fact?.state === 'completed') return `Факт · ${timeSpan(fact)}`;
  if (fact?.state === 'in_progress') return `Начато на шаге ${fact.start_tick} · окончание по плану ${schedule.end_tick}`;
  return `План · ${timeSpan(schedule)}`;
}

function PartyTrack({ label, schedule, run, facts, operations, draft = false }: {
  label: string; schedule: Schedule[]; run: Run; facts: Fact[]; operations: Map<string, Operation>; draft?: boolean;
}) {
  return <div className={`sim-track-row${draft ? ' sim-draft-row' : ''}`}>
    <span>{label}</span><TimeTrack tick={run.tick}>{schedule.filter(operation => operation.end_tick >= 0).map(operation => {
      const fact = facts.find(item => item.operation_id === operation.operation_id);
      const state = fact?.state ?? 'planned';
      return <span key={operation.operation_id} className={`sim-bar sim-${draft ? 'draft' : state}`} style={timeStyle(operation.start_tick, operation.end_tick)}
        title={`${operationLabel(operations.get(operation.operation_id), operation)} · ${operationTime(operation, fact)}`} />;
    })}</TimeTrack>
  </div>;
}

function OperationList({ title, schedule, operations, facts }: {
  title: string; schedule: Schedule[]; operations: Map<string, Operation>; facts: Fact[];
}) {
  return <div><h4>{title}</h4><ol className="sim-operation-list">{schedule.map(operation => {
    const fact = facts.find(item => item.operation_id === operation.operation_id);
    return <li key={operation.operation_id}><span>{operationLabel(operations.get(operation.operation_id), operation)}</span>
      <span className="muted">{operationTime(operation, fact)}</span></li>;
  })}</ol></div>;
}

const trackingPhases = [
  { id: 'rail', title: 'К порту отправления' },
  { id: 'departure', title: 'Порт отправления · Мурманск' },
  { id: 'sea', title: 'Морской переход по СМП' },
  { id: 'arrival', title: 'Порт назначения · Петропавловск-Камчатский' },
  { id: 'delivery', title: 'Доставка получателю' },
] as const;
type TrackingPhase = typeof trackingPhases[number]['id'];
type TrackingStep = TrackingPhase | 'start';
type TrackingState = 'done' | 'current' | 'pending' | 'future' | 'prior';
const trackingIcons: Record<TrackingStep, string> = {
  start: 'M3 7l7-4 7 4v7l-7 4-7-4z M3 7l7 4 7-4 M10 11v7',
  rail: 'M5 15V6a2 2 0 012-2h6a2 2 0 012 2v9z M5 9h10 M7 15l-2 3 M13 15l2 3 M7 12h.01 M13 12h.01',
  departure: 'M3 16V9l7-5 7 5v7z M7 16v-5h6v5 M9 13h2',
  sea: 'M3 11l7-3 7 3-2 5H5z M6 9V5h8v4 M10 3v2 M2 18h16',
  arrival: 'M3 16V9l7-5 7 5v7z M7 16v-5h6v5 M9 13h2',
  delivery: 'M10 3a5 5 0 015 5c0 4-5 9-5 9s-5-5-5-9a5 5 0 015-5z M10 8h.01',
};

function trackingPhase(operation: Operation | undefined): TrackingPhase {
  if (operation?.to_location_id === 'R') return 'delivery';
  if (operation?.kind === 'unload') return 'arrival';
  if (operation?.kind === 'move' && operation.service_id) return 'sea';
  if (operation?.kind === 'load' || (operation?.kind === 'wait' && operation.from_location_id === 'M')) return 'departure';
  return 'rail';
}

function TrackingMarker({ step, state }: { step: TrackingStep; state: TrackingState }) {
  return <span className="sim-tracking-marker" aria-hidden="true">
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.55" strokeLinecap="round" strokeLinejoin="round">
      <path d={state === 'done' && step !== 'start' ? 'M4 10l4 4 8-8' : trackingIcons[step]} />
    </svg>
  </span>;
}

function TrackingCheckpoints({ schedule, operations, facts }: {
  schedule: Schedule[]; operations: Map<string, Operation>; facts: Map<string, Fact>;
}) {
  return <ol className="sim-tracking-checkpoints">{schedule.map(item => {
    const operation = operations.get(item.operation_id);
    const fact = facts.get(item.operation_id);
    const state = fact?.state ?? 'planned';
    const when = fact?.state === 'completed' ? `Факт · ${timeSpan(fact)}` :
      fact?.state === 'in_progress' ? `С шага ${fact.start_tick}` : `План · ${timeSpan(item)}`;
    const destination = operation?.kind === 'move' && state === 'completed' ? operation.to_location_id : undefined;
    const physicalLocation = destination && ['O', 'M', 'D', 'R'].includes(destination);
    return <li key={item.operation_id} data-state={state} data-operation={item.operation_id}>
      <div>{physicalLocation && <strong>{locationLabel(destination)}</strong>}
        <span>{operationLabel(operation, fact ?? item)}</span></div>
      <span className="sim-tracking-time">{when}</span>
    </li>;
  })}</ol>;
}

function DeliveryTracking({ party, run, active, operations, facts }: {
  party: Party; run: Run; active: Schedule[]; operations: Map<string, Operation>; facts: Fact[];
}) {
  const byFact = new Map(facts.map(fact => [fact.operation_id, fact]));
  const phases = trackingPhases.map(phase => {
    const schedule = active.filter(item => trackingPhase(operations.get(item.operation_id)) === phase.id);
    const observed = schedule.flatMap(item => byFact.get(item.operation_id) ?? []);
    const completed = observed.filter(fact => fact.state === 'completed');
    return { ...phase, schedule, completed, underway: observed.find(fact => fact.state === 'in_progress'), done: completed.length === schedule.length };
  }).filter(phase => phase.schedule.length > 0);
  const next = phases.some(phase => phase.underway) ? undefined : phases.find(phase => !phase.done);
  const railBeforeStart = party.location_id === 'M' && !phases.some(phase => phase.id === 'rail');
  const assignment = run.active_plan.party_plans.find(item => item.party_id === party.id);
  return <article className="sim-party sim-tracking-card">
    <header className="sim-tracking-party">
      <div><h3>Трек {party.id}</h3><p>{position(party, facts, operations)}</p></div>
      <span className="sim-tracking-service">План v{run.plan_version}{assignment && ` · рейс ${assignment.service_id}`}</span>
    </header>
    <ol className="sim-tracking-steps" aria-label={`Этапы партии ${party.id}`}>
      {railBeforeStart && <li className="sim-tracking-step" data-state="prior" data-phase="rail">
        <TrackingMarker step="rail" state="prior" />
        <div className="sim-tracking-milestone"><div className="sim-tracking-step-line">
          <span className="sim-tracking-title">К порту отправления</span><span className="sim-tracking-time">До начала прогона</span>
        </div><p className="sim-tracking-detail">На старте партия уже в порту Мурманск. Ж/д операции до старта не отслеживаются.</p></div>
      </li>}
      <li className="sim-tracking-step" data-state="done" data-phase="start">
        <TrackingMarker step="start" state="done" />
        <div className="sim-tracking-milestone"><div className="sim-tracking-step-line">
          <span className="sim-tracking-title">Старт прогона</span><span className="sim-tracking-time">Шаг 0</span>
        </div><p className="sim-tracking-detail">Исходная позиция · {startingPosition(party, facts, operations)}</p></div>
      </li>
      {phases.map(phase => {
        const state: TrackingState = phase.done ? 'done' : phase.underway ? 'current' : phase === next ? 'pending' : 'future';
        const currentOperation = phase.underway && operations.get(phase.underway.operation_id);
        const waiting = currentOperation?.kind === 'wait';
        const plannedTick = phase.id === 'sea' || phase.id === 'departure'
          ? Math.min(...phase.schedule.map(item => item.start_tick))
          : Math.max(...phase.schedule.map(item => item.end_tick));
        const when = phase.done ? `Факт · шаг ${Math.max(...phase.completed.map(fact => fact.end_tick))}`
          : phase.underway ? `С шага ${phase.underway.start_tick}`
          : `${phase.id === 'sea' ? 'Выход по плану' : 'План'} · шаг ${plannedTick}`;
        const title = phase.id === 'delivery' && phase.done ? 'Получен'
          : waiting && phase.underway ? operationLabel(currentOperation, phase.underway) : phase.title;
        const status = state === 'current' ? waiting ? 'Ожидание' : 'Выполняется' : state === 'pending'
          ? (phase.completed.length > 0 ? 'Ожидает продолжения' : 'Ожидает начала') : null;
        const row = <><span className="sim-tracking-step-line">
          <span className="sim-tracking-title">{title}</span><span className="sim-tracking-time">{when}</span>
        </span>{status && <span className="sim-tracking-detail">{status}</span>}</>;
        return <li className="sim-tracking-step" key={phase.id} data-state={state} data-phase={phase.id}
          aria-current={state === 'current' ? 'step' : undefined}>
          <TrackingMarker step={phase.id} state={state} />
          {state === 'current' || phase.schedule.length > 1
            ? <details className="sim-tracking-milestone">
              <summary>{row}</summary>
              <TrackingCheckpoints schedule={phase.schedule} operations={operations} facts={byFact} />
            </details>
            : <div className="sim-tracking-milestone">{row}</div>}
        </li>;
      })}
    </ol>
  </article>;
}

export function SimulationParties({ run, selectedId, onSelect, overview }: {
  run: Run; selectedId: string; onSelect: (id: string) => void; overview: boolean;
}) {
  const operations = new Map(run.operations.map(operation => [operation.id, operation]));
  const items = run.parties.map(party => {
    const active = partySchedule(run.active_plan, party.id, operations);
    const facts = run.facts.filter(fact => operations.get(fact.operation_id)?.party_id === party.id);
    const observed = new Set(facts.map(fact => fact.operation_id));
    const next = active.find(item => !observed.has(item.operation_id) && item.start_tick !== item.end_tick);
    const receipt = latestCompleted(facts.filter(fact => operations.get(fact.operation_id)?.to_location_id === 'R'));
    const assignment = run.active_plan.party_plans.find(item => item.party_id === party.id);
    return { party, active, facts, next, receipt, assignment, place: position(party, facts, operations) };
  });
  const selected = items.find(item => item.party.id === selectedId) ?? items[0];
  const receiptText = (item: typeof items[number]) => item.receipt ? `Факт · шаг ${item.receipt.end_tick}`
    : item.assignment ? `Шаг ${item.assignment.recipient_receipt_tick}` : '—';
  const nextText = (item: typeof items[number]) => item.next ? operationLabel(operations.get(item.next.operation_id), item.next)
    : item.receipt ? 'Доставка завершена' : '—';
  return <div className={`sim-party-workbench${overview ? ' sim-party-overview' : ''}`}>
    <section className="sim-panel sim-party-list">
      <div className="sim-section-heading"><h2>Партии груза</h2><span className="muted">Выдача по v{run.plan_version}</span></div>
      {overview ? <table className="sim-party-table"><thead><tr><th scope="col">Партия</th><th scope="col">Положение</th>
        <th scope="col">Следующее действие</th><th scope="col">Выдача</th></tr></thead>
        <tbody>{items.map(item => <tr key={item.party.id} data-selected={selected?.party.id === item.party.id}>
          <th scope="row"><button type="button" className="sim-text-button" aria-pressed={selected?.party.id === item.party.id}
            onClick={() => onSelect(item.party.id)}>{item.party.id}</button></th>
          <td>{item.place}</td><td>{nextText(item)}</td><td>{receiptText(item)}</td>
        </tr>)}</tbody></table> : <ul className="sim-party-options">{items.map(item => <li key={item.party.id}>
        <button type="button" className="sim-party-option" aria-pressed={selected?.party.id === item.party.id}
          onClick={() => onSelect(item.party.id)}>
          <span className="sim-party-id">{item.party.id}</span>
          <span><strong>{item.place}</strong><small>Дальше · {nextText(item)}</small></span>
          <span>{receiptText(item)}</span>
        </button>
      </li>)}</ul>}
    </section>
    {selected && <DeliveryTracking key={selected.party.id} party={selected.party} active={selected.active}
      facts={selected.facts} run={run} operations={operations} />}
  </div>;
}

export function SimulationTimeline({ run }: { run: Run }) {
  const operations = new Map(run.operations.map(operation => [operation.id, operation]));
  const draft = run.status === 'candidate' ? run.candidate?.plan : null;
  const tracks = run.parties.map(party => ({
    party,
    active: partySchedule(run.active_plan, party.id, operations),
    nextPlan: draft ? partySchedule(draft, party.id, operations) : [],
    facts: run.facts.filter(fact => operations.get(fact.operation_id)?.party_id === party.id),
  }));
  const reported = run.events.flatMap(event => {
    const tick = event.received_tick ?? event.available_tick;
    return typeof tick === 'number' && Number.isInteger(tick) && tick >= 0 && tick <= simulationHorizon ? [{ event, tick }] : [];
  });
  return <details className="sim-track-dispatch"><summary>Временная шкала рейса и операции · {runStepLabel(run)}</summary>
    <ul className="sim-legend" aria-label="Обозначения">
      <li className="sim-key-completed">Выполнено</li><li className="sim-key-in-progress">Начато</li>
      <li className="sim-key-planned">Запланировано</li>{draft && <li className="sim-key-draft">Кандидат</li>}
    </ul>
    <div className="sim-axis sim-track-row"><span>Шаг модели (0–{simulationHorizon})</span>
      <div className="sim-ticks" aria-hidden="true">{[0, 5, 10, 15, 20, 25, simulationHorizon].map(tick => <span key={tick} style={timeStyle(tick)}>{tick}</span>)}</div>
    </div>
    <div className="sim-event-axis sim-track-row" aria-hidden="true"><span>События и действия</span><TimeTrack tick={run.tick}>
      {reported.map(({ event, tick }, index) => <span key={`${event.source}/${event.event_id}/${index}`} className="sim-marker" style={timeStyle(tick)} title={eventTitle(event, run)} />)}
      {run.timeline.map((entry, index) => entry.tick >= 0 && entry.tick <= simulationHorizon && <span className="sim-marker sim-action-marker" key={index} style={timeStyle(entry.tick)} title={entry.label} />)}
    </TimeTrack></div>
    {reported.filter(({ event }) => event.status === 'applied').map(({ event }) => {
      const params = event.parameters ?? {};
      const start = params.from_tick ?? params.load_from_tick;
      const end = params.until_tick ?? params.load_until_tick;
      return start === undefined || end === undefined ? null : <div key={`${event.source}/${event.event_id}`} className="sim-restriction sim-track-row" aria-hidden="true">
        <span>{event.type === 'vessel_schedule_changed' ? `Окно погрузки рейса ${event.target_id}` : `Закрытие ${event.target_id}`}</span>
        <TimeTrack tick={run.tick}>
          <span className="sim-bar sim-event-bar" style={timeStyle(start, end)} title={eventWindow(event)} />
          {params.departure_tick !== undefined && <span className="sim-marker" style={timeStyle(params.departure_tick)}
            title={`Выход рейса ${event.target_id} · шаг ${params.departure_tick}`} />}
        </TimeTrack>
      </div>;
    })}
    <div>{tracks.map(({ party, active, nextPlan, facts }) =>
      <div className="sim-dispatch-party" key={party.id}>
        <h3>Партия {party.id}</h3>
        <PartyTrack label={`Действующая версия ${run.plan_version}`} schedule={active} run={run} facts={facts} operations={operations} />
        {draft && <PartyTrack label="Кандидат" schedule={nextPlan} run={run} facts={facts} operations={operations} draft />}
        <details className="sim-operation-details"><summary>Операции партии {party.id}</summary><div className="sim-operations-grid">
          <OperationList title={`Действующая версия ${run.plan_version}`} schedule={active} operations={operations} facts={facts} />
          {draft && <OperationList title="Кандидат" schedule={nextPlan} operations={operations} facts={facts} />}
        </div></details>
      </div>)}</div>
  </details>;
}

function formatChanges(schedule: Schedule[], operations: Map<string, Operation>) {
  return schedule.length ? schedule.map(item => `${operationLabel(operations.get(item.operation_id), item)} (${timeSpan(item)})`).join(' · ') : '—';
}

export function SimulationComparison({ run }: { run: Run }) {
  const operations = new Map(run.operations.map(operation => [operation.id, operation]));
  const draft = run.status === 'candidate' ? run.candidate?.plan : null;
  return <section className="sim-panel" aria-labelledby="sim-compare-title">
    <div className="sim-section-heading"><div><h2 id="sim-compare-title" tabIndex={-1}>Сравнение планов</h2>
      <p>Шаг приёма в порту назначения / шаг получения груза</p></div></div>
    <p className="sim-comparison-note">Исходный план сохранён при запуске. Действует версия {run.plan_version}.
      {draft ? ` Кандидат проверен для редакции условий ${run.condition_revision}.` : ' Кандидата для применения сейчас нет.'}</p>
    <div className="sim-comparison-grid">{run.parties.map(party => {
      const base = run.base_plan.party_plans.find(plan => plan.party_id === party.id);
      const active = run.active_plan.party_plans.find(plan => plan.party_id === party.id);
      const next = draft?.party_plans.find(plan => plan.party_id === party.id);
      const facts = run.facts.filter(fact => operations.get(fact.operation_id)?.party_id === party.id);
      const portActual = latestCompleted(facts.filter(fact => operations.get(fact.operation_id)?.kind === 'unload'))?.end_tick;
      const deliveryActual = latestCompleted(facts.filter(fact => operations.get(fact.operation_id)?.to_location_id === 'R'))?.end_tick;
      if (!active) return null;
      const before = draft ? active : base;
      const after = next ?? active;
      const changes = changedOperations(partySchedule(draft ? run.active_plan : run.base_plan, party.id, operations),
        partySchedule(draft ?? run.active_plan, party.id, operations));
      const related = run.events.filter(event => (event.status === 'applied' || event.status === 'accepted')
        && [...changes.before, ...changes.after].some(item => affects(event, item, operations.get(item.operation_id))));
      return <article className="sim-comparison-card" key={party.id}>
        <h3>Партия {party.id} · рейс {active.service_id}</h3><p>{position(party, facts, operations)}</p>
        <div className="sim-plan-pair">
          <div><span>{draft ? `Действующий план v${run.plan_version}` : 'Исходный план'}</span>
            <strong>{before ? `Выдача · шаг ${before.recipient_receipt_tick}` : 'Выдача · —'}</strong>
            {before && <small>Приём в порту · шаг {before.port_receipt_tick}</small>}</div>
          <div><span>{next ? 'Кандидат' : `Действующий план v${run.plan_version}`}</span>
            <strong>Выдача · шаг {after.recipient_receipt_tick}</strong>
            <small>Приём в порту · шаг {after.port_receipt_tick}</small></div>
        </div>
        <dl className="sim-metrics">
          {(portActual !== undefined || deliveryActual !== undefined) && <>
            <dt>Факт по прогону</dt><dd>{portActual ?? '—'} / {deliveryActual ?? '—'}</dd>
          </>}
          <dt>{draft ? 'Сдвиг к действующей версии' : 'Сдвиг к исходному плану'}</dt>
          <dd>{before ? after.recipient_receipt_tick === before.recipient_receipt_tick ? 'Без изменений срока'
            : `${signedShift(after.recipient_receipt_tick - before.recipient_receipt_tick)} шаг` : '—'}</dd>
          <dt>Контрольный срок</dt><dd>{party.due_tick}</dd>
          <dt>Запас до срока</dt><dd>{party.due_tick - after.recipient_receipt_tick}</dd>
        </dl>
        {changes.before.length || changes.after.length ? <details className="sim-operation-details">
          <summary>{before?.route_id !== after.route_id ? 'Изменились маршрут и операции' : 'Изменились окна или операции'}</summary>
          <p><strong>{draft ? 'Действующий план' : 'Исходный план'}</strong> {formatChanges(changes.before, operations)}</p>
          <p><strong>{draft ? 'Кандидат' : 'Действующий план'}</strong> {formatChanges(changes.after, operations)}</p>
          {related.length > 0 && <ul className="sim-related" aria-label="Связанные условия">{related.map(event =>
            <li key={`${event.source}/${event.event_id}`}>{eventTitle(event, run)} · {eventWindow(event)}</li>)}</ul>}
        </details> : <p className="sim-no-change">Маршрут и интервалы сохраняются</p>}
      </article>;
    })}</div>
  </section>;
}

export function SimulationJournal({ run }: { run: Run }) {
  const operations = new Map(run.operations.map(operation => [operation.id, operation]));
  const schedules = [...run.base_plan.scheduled_operations, ...run.active_plan.scheduled_operations, ...(run.candidate?.plan?.scheduled_operations ?? [])];
  const observed = new Map(run.facts.map(fact => [fact.operation_id, fact]));
  const training = run.mode === 'training';
  return <section className="sim-panel" aria-labelledby="sim-journal-title">
    <div className="sim-section-heading"><div><h2 id="sim-journal-title">Журнал событий</h2><p>{training ? 'События, заданные координатором' : run.source.complete_through_tick < 0
      ? 'Полнота ленты пока не подтверждена' : `Лента проверена до шага ${run.source.complete_through_tick}`}</p></div></div>
    {run.events.length === 0 ? <p className="sim-section-empty">{training ? 'Ручные события ещё не поступили.' : 'События ещё не поступили.'}</p> : <ol className="sim-journal-list">{run.events.map((event, index) => {
      const affected = new Set(event.affected_parties);
      for (const schedule of event.affected_parties || event.status !== 'applied' ? [] : schedules) {
        const operation = operations.get(schedule.operation_id);
        if (operation && affects(event, schedule, operation)) affected.add(operation.party_id);
      }
      return <li key={`${event.source}/${event.event_id}/${index}`}>
        <p className="sim-journal-meta"><strong>Поступило на шаге {event.received_tick ?? event.available_tick ?? '—'}</strong><span>{sources[event.source] || event.source || 'Источник неизвестен'}</span>
          {event.status && <span>{eventStatuses[event.status] ?? event.status}</span>}</p>
        <h3>{eventTitle(event, run)}</h3><p>{eventWindow(event)}</p>
        {event.reason && <p className="muted">{event.reason}</p>}
        {affected.size > 0 && <p className="muted">Связанные партии · {[...affected].sort().join(', ')}</p>}
        {event.message && <p className="sim-event-message">{event.message}</p>}
      </li>;
    })}</ol>}
    {run.timeline.length > 0 && <details className="sim-history"><summary>История шагов, расчётов и решений</summary>
      <ol>{[...run.timeline].reverse().map((entry, index) => {
        let label = entry.label;
        if (['initial_fact', 'operation_started', 'operation_completed'].includes(entry.kind)) {
          const [action, id] = entry.label.split(' · ');
          const fact = observed.get(id);
          if (fact) label = `${action} · ${operationLabel(operations.get(id), fact)} (${id})`;
        }
        return <li key={index}><strong>Шаг {entry.tick}</strong> · {label}</li>;
      })}</ol>
    </details>}
  </section>;
}
