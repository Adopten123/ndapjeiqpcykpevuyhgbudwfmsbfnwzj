import { affects } from './simulation';
import type { Run } from './simulation';
import { eventTitle, eventWindow, operationLabel } from './simulation-presentation';

const sources: Record<string, string> = { 'carrier-mock': 'Морской перевозчик · имитатор', 'rail-mock': 'Железная дорога · имитатор',
  'ice-mock': 'Ледовый мониторинг · имитатор', manual: 'Введено вручную' };
const eventStatuses: Record<string, string> = { applied: 'Учтено в условиях', accepted: 'Учтено в условиях', rejected: 'Отклонено',
  conflict: 'Требуется разбор', expired: 'Срок действия истёк', ignored: 'В журнале', duplicate: 'Повтор доставки' };

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
