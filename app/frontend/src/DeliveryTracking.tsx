import type { Fact, Operation, Party, Run, Schedule } from './simulation';
import { locationLabel } from './simulation-route';
import { operationLabel, position, startingPosition, timeSpan } from './simulation-presentation';
import './simulation-tracking.css';

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

export function DeliveryTracking({ party, run, active, operations, facts }: {
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
