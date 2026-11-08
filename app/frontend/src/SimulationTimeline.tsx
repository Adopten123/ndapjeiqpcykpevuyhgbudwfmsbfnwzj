import type { CSSProperties } from 'react';
import { partySchedule, runStepLabel, simulationHorizon } from './simulation';
import type { Fact, Operation, Run, Schedule } from './simulation';
import { eventTitle, eventWindow, operationLabel, operationTime } from './simulation-presentation';

const timeStyle = (start: number, end = start): CSSProperties => ({
  left: `${Math.max(start, 0) * 100 / simulationHorizon}%`, width: `${Math.max(0, end - Math.max(start, 0)) * 100 / simulationHorizon}%`,
});

function TimeTrack({ tick, children }: { tick: number; children: React.ReactNode }) {
  return <div className="sim-track" aria-hidden="true"><span className="sim-now" style={timeStyle(tick)} />{children}</div>;
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
