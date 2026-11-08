import { parseCalculation, request, ServiceError } from './api.js';
import { parseContext } from './workflow.js';
import type { Calculation } from './types.js';
import type { ApprovalContext } from './workflow.js';

export interface PlanningContext {
  context: ApprovalContext;
  source: { revision: number; input_sha256: string; service_a_departure_tick: number; service_a_load_window: { start_tick: number; end_tick: number } };
  baseline: { accepted_version: number; packet_id: string | null; plan_id: string; candidate_sha256: string; baseline_sha256: string };
  context_sha256: string;
  can_calculate: boolean;
  applied_source_revision?: number;
}
export interface SourceValues { departure: number; start: number; end: number }
type CalculationBinding = Pick<Calculation, 'source_revision' | 'base_accepted_version' | 'base_accepted_packet_id' | 'source_sha256' | 'baseline_sha256' | 'context_sha256'>;
export interface PlanningOperation { readonly kind: 'source' | 'calculate'; readonly key: string; readonly path: string; readonly body: string; readonly expected: Readonly<CalculationBinding> }
const object = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x);
const integer = (x: unknown): x is number => typeof x === 'number' && Number.isSafeInteger(x);
const hash = (x: unknown): x is string => typeof x === 'string' && /^[a-f0-9]{64}$/i.test(x);
const invalid = (): never => { throw new ServiceError('Сервис вернул неподдерживаемую привязку условий и принятой базы. Обновите данные.'); };

export function validSource(values: SourceValues): boolean {
  return [values.departure, values.start, values.end].every(integer) && values.start >= 0 && values.start < values.end && values.end <= values.departure && values.departure <= 30;
}
export function parsePlanningContext(value: unknown): PlanningContext {
  if (!object(value) || !object(value.source) || !object(value.baseline) || typeof value.can_calculate !== 'boolean' || !hash(value.context_sha256)) return invalid();
  const context = parseContext(value.context); const s = value.source; const b = value.baseline;
  if (!integer(s.revision) || s.revision < 1 || s.revision !== context.source_revision || !hash(s.input_sha256) || !object(s.service_a_load_window)
    || !validSource({ departure: s.service_a_departure_tick as number, start: s.service_a_load_window.start_tick as number, end: s.service_a_load_window.end_tick as number })
    || !integer(b.accepted_version) || b.accepted_version !== context.accepted_version || b.packet_id !== context.accepted_packet_id
    || (b.accepted_version === 0 ? b.packet_id !== null : typeof b.packet_id !== 'string' || b.packet_id.length === 0)
    || typeof b.plan_id !== 'string' || b.plan_id.length === 0 || (b.accepted_version === 0 && b.plan_id !== 'DEMO-v0') || !hash(b.candidate_sha256) || !hash(b.baseline_sha256)) return invalid();
  if (value.applied_source_revision !== undefined && (!integer(value.applied_source_revision) || value.applied_source_revision < 1 || value.applied_source_revision > s.revision)) return invalid();
  return { context, source: s as unknown as PlanningContext['source'], baseline: b as unknown as PlanningContext['baseline'], context_sha256: value.context_sha256, can_calculate: value.can_calculate,
    ...(value.applied_source_revision === undefined ? {} : { applied_source_revision: value.applied_source_revision }) };
}
export function sourceUpdateNotice(planning: PlanningContext): string {
  const applied = planning.applied_source_revision;
  if (applied === undefined) return invalid();
  return applied < planning.source.revision
    ? `Изменение условий применено в редакции ${applied}. Сейчас актуальна редакция ${planning.source.revision}; в форме показаны её параметры. Повтор не перезаписал более новые условия.`
    : `Учебные условия сохранены в редакции ${applied}. В форме показаны параметры этой редакции. Прежние пакеты и ответы не подтверждают новые условия.`;
}
export function matchesPlanningContext(calculation: Calculation, planning: PlanningContext): boolean {
  return calculation.engine_profile === 'workflow-demo.v1' && calculation.scenario_id === 'S1'
    && calculation.source_revision === planning.source.revision && calculation.source_sha256 === planning.source.input_sha256
    && calculation.base_accepted_version === planning.baseline.accepted_version
    && calculation.base_accepted_packet_id === planning.baseline.packet_id
    && calculation.baseline_sha256 === planning.baseline.baseline_sha256 && calculation.context_sha256 === planning.context_sha256;
}
export function canPreparePacket(calculation: Calculation, planning: PlanningContext): boolean {
  return calculation.status === 'succeeded' && calculation.result?.solver_status === 'OPTIMAL' && Boolean(calculation.result.candidate)
    && matchesPlanningContext(calculation, planning);
}
export function sameSavedCalculation(previous: Calculation, incoming: Calculation): boolean {
  return previous.id === incoming.id && previous.engine_profile === incoming.engine_profile && previous.input_sha256 === incoming.input_sha256
    && previous.snapshot_id === incoming.snapshot_id && ['source_revision', 'base_accepted_version', 'base_accepted_packet_id', 'source_sha256', 'baseline_sha256', 'context_sha256']
      .every(field => previous[field as keyof Calculation] === incoming[field as keyof Calculation]);
}
export function makePlanningOperation(kind: PlanningOperation['kind'], context: PlanningContext, values?: SourceValues): PlanningOperation {
  if (kind === 'source' && (!values || !validSource(values))) throw new ServiceError('Укажите целые шаги: 0 ≤ начало < конец ≤ отправление ≤ 30.');
  return Object.freeze({ kind, key: crypto.randomUUID(), path: kind === 'source' ? '/api/v1/planning-context/source' : '/api/v1/planning-calculations', expected: Object.freeze({
    source_revision: context.source.revision, base_accepted_version: context.baseline.accepted_version, base_accepted_packet_id: context.baseline.packet_id,
    source_sha256: context.source.input_sha256, baseline_sha256: context.baseline.baseline_sha256, context_sha256: context.context_sha256,
  }), body: JSON.stringify({
    expected_source_revision: context.source.revision, expected_accepted_version: context.baseline.accepted_version,
    ...(kind === 'source' && values ? { service_a_departure_tick: values.departure, service_a_load_window: { start_tick: values.start, end_tick: values.end } } : {}),
  }) });
}
export async function executePlanningOperation(operation: PlanningOperation, signal?: AbortSignal): Promise<PlanningContext | Calculation> {
  const value = await request(operation.path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': operation.key }, body: operation.body }, 15000);
  if (operation.kind === 'source') {
    const planning = parsePlanningContext(value);
    if (planning.applied_source_revision !== operation.expected.source_revision + 1) return invalid();
    return planning;
  }
  const calculation = parseCalculation(value);
  if (calculation.engine_profile !== 'workflow-demo.v1') return invalid();
  if (!Object.entries(operation.expected).every(([field, value]) => calculation[field as keyof Calculation] === value)) return invalid();
  return calculation;
}
export async function getPlanningContext(signal?: AbortSignal): Promise<PlanningContext> {
  return parsePlanningContext(await request('/api/v1/planning-context', { signal }));
}
