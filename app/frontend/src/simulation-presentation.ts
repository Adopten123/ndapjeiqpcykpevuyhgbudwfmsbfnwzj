import { eventNames } from './simulation.js';
import type { Fact, Operation, Party, ReportedEvent, Run, Schedule } from './simulation.js';
import { edgeLabel, locationLabel, targetLabel } from './simulation-route.js';

function waitLocation(operation: Operation) {
  const name = locationLabel(operation.location_id || operation.from_location_id);
  return `${name[0].toLowerCase()}${name.slice(1)}`;
}

export function operationLabel(operation: Operation | undefined, schedule: Schedule) {
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

export function latestCompleted(facts: Fact[]) {
  return facts.filter(fact => fact.state === 'completed')
    .sort((a, b) => b.end_tick - a.end_tick || b.start_tick - a.start_tick || b.operation_id.localeCompare(a.operation_id))[0];
}

export function startingPosition(party: Party, facts: Fact[], operations: Map<string, Operation>) {
  if (party.location_id) return locationLabel(party.location_id);
  const initialMovement = facts.find(fact => fact.start_tick <= 0 && fact.end_tick > 0 && operations.get(fact.operation_id)?.kind === 'move');
  if (initialMovement) return `В пути · ${operationLabel(operations.get(initialMovement.operation_id), initialMovement)}`;
  const prior = latestCompleted(facts.filter(fact => fact.end_tick <= 0));
  return locationLabel(prior && operations.get(prior.operation_id)?.to_location_id);
}

export function position(party: Party, facts: Fact[], operations: Map<string, Operation>) {
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

export function timeSpan({ start_tick, end_tick }: Schedule) {
  return start_tick === end_tick ? `шаг ${start_tick}` : `${start_tick}–${end_tick}`;
}

export function operationTime(schedule: Schedule, fact: Fact | undefined) {
  if (fact?.state === 'completed') return `Факт · ${timeSpan(fact)}`;
  if (fact?.state === 'in_progress') return `Начато на шаге ${fact.start_tick} · окончание по плану ${schedule.end_tick}`;
  return `План · ${timeSpan(schedule)}`;
}
