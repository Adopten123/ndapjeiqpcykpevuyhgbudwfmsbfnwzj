import { partySchedule } from './simulation';
import type { Run } from './simulation';
import { latestCompleted, operationLabel, position } from './simulation-presentation';
import { DeliveryTracking } from './DeliveryTracking';

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
