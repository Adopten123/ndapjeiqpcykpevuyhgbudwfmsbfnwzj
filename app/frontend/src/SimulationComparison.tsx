import { affects, changedOperations, partySchedule, signedShift } from './simulation';
import type { Operation, Run, Schedule } from './simulation';
import { eventTitle, eventWindow, latestCompleted, operationLabel, position, timeSpan } from './simulation-presentation';

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
