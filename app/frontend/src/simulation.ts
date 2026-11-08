import { request, ServiceError } from './api.js';
import type { PartyPlan } from './types.js';

export const transportWorkspaceTitle = 'Управление перевозкой';
export const maxPartyCount = 4;
export const simulationHorizon = 30;
export const defaultCargoId = 'dry-supplies';
export const defaultRouteId = 'khovrino-murmansk-kamchatka';
export const cargoNames = {
  'steel-fasteners': 'Стальной крепёж',
  'metal-spares': 'Металлические запчасти',
  'dry-supplies': 'Сухой контейнерный груз',
} as const;
export const cargoProfileDescription = 'Сухой неопасный груз в стандартном контейнере';
export const routeNames = {
  [defaultRouteId]: 'Ховрино → Мурманск → СМП → Петропавловск-Камчатский',
} as const;

export const eventNames = {
  vessel_schedule_changed: 'Изменение расписания судна',
  rail_edge_closed: 'Ремонт железнодорожного пути',
  sea_edge_closed: 'Закрытие морского участка',
} as const;
export const scenarioNames = {
  standard: 'Задержка судна и ремонт пути',
  critical: 'Задержка, ремонт и закрытие пролива',
  'short-ice': 'Задержка, ремонт и короткое закрытие пролива',
  manual: 'Ручные события',
  operations: 'События от имитаторов и ручной ввод',
} as const;
export const modeNames = {
  training: 'Учебный режим',
  operations: 'Диспетчерский режим',
  legacy: 'Сценарный прогон',
} as const;
export const statusNames = {
  ready: 'Можно продолжать', queued: 'В очереди', running: 'Рассчитывается',
  candidate: 'Проверенный кандидат', infeasible: 'Допустимого плана нет',
  unknown: 'Расчёт не завершён', compute_error: 'Ошибка расчётного сервиса',
  source_error: 'Источник недоступен', conflict: 'Требуется разбор', completed: 'Прогон завершён',
} as const;

export type EventType = keyof typeof eventNames;
export type Scenario = keyof typeof scenarioNames;
export type RunMode = keyof typeof modeNames;
export type CargoId = keyof typeof cargoNames;
export type RouteId = keyof typeof routeNames;
export type CreateRunInput = {
  name: string;
  party_count: number;
  cargo_id?: CargoId;
  cargo_name?: string;
  route_id?: RouteId;
} & (
  { mode: 'training'; copy_from?: string } | { mode: 'operations' }
);
type RunStatus = keyof typeof statusNames;
type EventParameters = Partial<Record<'from_tick' | 'until_tick' | 'load_from_tick' | 'load_until_tick' | 'departure_tick', number>>;
export interface EventInput {
  type: EventType;
  target_id: string;
  available_tick: number;
  reason: string;
  parameters: EventParameters;
}
export interface SimulationEvent extends EventInput {
  event_id: string;
  source: string;
}
export interface ReportedEvent extends Omit<SimulationEvent, 'available_tick' | 'parameters'> {
  available_tick: number | null;
  parameters: EventParameters | null;
  status?: string;
  message?: string;
  received_tick?: number;
  affected_parties?: string[];
}
export interface Schedule { operation_id: string; start_tick: number; end_tick: number }
export interface Fact extends Schedule { state: 'completed' | 'in_progress' }
export interface Plan { party_plans: PartyPlan[]; scheduled_operations: Schedule[] }
export interface Operation {
  id: string;
  party_id: string;
  kind: string;
  location_id?: string;
  from_location_id?: string;
  to_location_id?: string;
  edge_id?: string;
  service_id?: string;
}
export interface Party { id: string; due_tick: number; location_id?: string }
export interface Target { id: string; type: EventType; label: string }
export interface RunHeader { id: string; name: string; mode: RunMode; scenario: Scenario; tick: number; status: RunStatus; plan_version: number }
export interface Run extends RunHeader {
  cargo: { id: CargoId; name: string };
  route: { id: RouteId };
  state_version: number;
  condition_revision: number;
  base_plan: Plan;
  active_plan: Plan;
  candidate: { id: string; status: string; plan: Plan | null; message?: string } | null;
  events: ReportedEvent[];
  manual_events: SimulationEvent[];
  operations: Operation[];
  facts: Fact[];
  parties: Party[];
  targets: Target[];
  timeline: { tick: number; kind: string; label: string }[];
  message?: string;
  source: { complete_through_tick: number; next_tick: number | null; cursor?: string };
}

const path = '/api/v1/simulation/runs';
const runPath = (id: string) => `${path}/${encodeURIComponent(id)}`;
const isRecord = (value: unknown): value is Record<string, unknown> => Boolean(value && typeof value === 'object' && !Array.isArray(value));
const integer = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value);
const invalid = (): never => { throw new ServiceError('Сервис вернул неполные данные прогона. Обновите страницу или повторите запрос.'); };

function parseHeader(value: unknown): RunHeader {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.name !== 'string' || !integer(value.tick) || !integer(value.plan_version)
    || typeof value.scenario !== 'string' || !Object.hasOwn(scenarioNames, value.scenario)
    || typeof value.status !== 'string' || !Object.hasOwn(statusNames, value.status)) return invalid();
  const mode = value.scenario === 'manual' ? 'training' : value.scenario === 'operations' ? 'operations' : 'legacy';
  if (value.mode !== mode && !(mode === 'legacy' && value.mode === undefined)) return invalid();
  return { id: value.id, name: value.name, mode, scenario: value.scenario as Scenario, tick: value.tick,
    status: value.status as RunStatus, plan_version: value.plan_version };
}

const validRows = (value: unknown, valid: (row: unknown) => boolean) => value == null || Array.isArray(value) && value.every(valid);
const optionalString = (value: Record<string, unknown>, key: string) => value[key] == null || typeof value[key] === 'string';
const validSchedule = (value: unknown) => isRecord(value) && typeof value.operation_id === 'string'
  && integer(value.start_tick) && integer(value.end_tick) && value.start_tick <= value.end_tick;

function validOperation(value: unknown, partyIds: Set<string>) {
  return isRecord(value) && typeof value.id === 'string' && typeof value.party_id === 'string' && partyIds.has(value.party_id)
    && typeof value.kind === 'string' && ['location_id', 'from_location_id', 'to_location_id', 'edge_id', 'service_id']
      .every(key => optionalString(value, key));
}

function validEvent(value: unknown, manual: boolean) {
  if (!isRecord(value) || typeof value.event_id !== 'string' || typeof value.source !== 'string'
    || typeof value.type !== 'string' || typeof value.target_id !== 'string'
    || !['reason', 'message', 'status'].every(key => optionalString(value, key))) return false;
  if (manual ? !integer(value.available_tick) : value.available_tick != null && !integer(value.available_tick)) return false;
  if (value.received_tick != null && !integer(value.received_tick)) return false;
  if (value.affected_parties != null && (!Array.isArray(value.affected_parties)
    || !value.affected_parties.every(id => typeof id === 'string'))) return false;
  return value.parameters == null || isRecord(value.parameters) && Object.values(value.parameters).every(integer);
}

function validPlan(value: unknown, partyIds: Set<string>): value is Plan {
  return isRecord(value) && Array.isArray(value.party_plans) && value.party_plans.length === partyIds.size && Array.isArray(value.scheduled_operations)
    && new Set(value.party_plans.map(party => party?.party_id)).size === partyIds.size && value.party_plans.every(party => isRecord(party)
      && typeof party.party_id === 'string' && partyIds.has(party.party_id) && typeof party.route_id === 'string'
      && typeof party.service_id === 'string' && integer(party.port_receipt_tick) && integer(party.recipient_receipt_tick))
    && value.scheduled_operations.every(validSchedule);
}

export function parseRun(value: unknown): Run {
  if (!isRecord(value)) return invalid();
  const row = value;
  const header = parseHeader(row);
  if (!integer(row.state_version) || !integer(row.condition_revision)
    || !isRecord(row.source)
    || !integer(row.source.complete_through_tick) || (row.source.next_tick != null && !integer(row.source.next_tick))
    || !Array.isArray(row.operations) || !Array.isArray(row.parties) || !Array.isArray(row.targets)) return invalid();
  if (row.parties.length < 1 || row.parties.length > maxPartyCount || !row.parties.every(party => isRecord(party)
    && typeof party.id === 'string' && /^P[1-4]$/.test(party.id) && integer(party.due_tick)
    && optionalString(party, 'location_id'))) return invalid();
  const partyIds = new Set<string>(row.parties.map(party => party.id));
  if (partyIds.size !== row.parties.length || !validPlan(row.base_plan, partyIds) || !validPlan(row.active_plan, partyIds)) return invalid();
  if (!row.operations.every(operation => validOperation(operation, partyIds))
    || !row.targets.every(target => isRecord(target) && typeof target.id === 'string' && typeof target.type === 'string'
      && typeof target.label === 'string') || !optionalString(row, 'message')) return invalid();
  const candidate = row.candidate;
  if (candidate != null && (!isRecord(candidate) || typeof candidate.id !== 'string' || typeof candidate.status !== 'string'
    || !optionalString(candidate, 'message') || candidate.plan != null && !validPlan(candidate.plan, partyIds))) return invalid();
  if (header.status === 'candidate' && (!isRecord(candidate) || !['OPTIMAL', 'FEASIBLE'].includes(String(candidate.status)) || !candidate.plan)) return invalid();
  if (!validRows(row.facts, fact => validSchedule(fact) && isRecord(fact)
    && ['completed', 'in_progress'].includes(String(fact.state)))
    || !validRows(row.events, event => validEvent(event, false))
    || !validRows(row.manual_events, event => validEvent(event, true))
    || !validRows(row.timeline, entry => isRecord(entry) && integer(entry.tick)
      && typeof entry.kind === 'string' && typeof entry.label === 'string')) return invalid();
  const cargo = row.cargo === undefined ? { id: defaultCargoId, name: cargoNames[defaultCargoId] } : row.cargo;
  const route = row.route === undefined ? { id: defaultRouteId } : row.route;
  if (!isRecord(cargo) || typeof cargo.id !== 'string' || !Object.hasOwn(cargoNames, cargo.id)
    || typeof cargo.name !== 'string' || !cargo.name.trim() || [...cargo.name].length > 100
    || !isRecord(route) || typeof route.id !== 'string' || !Object.hasOwn(routeNames, route.id)) return invalid();
  return { ...row, ...header, cargo, route, candidate: candidate ?? null, events: row.events ?? [], manual_events: row.manual_events ?? [],
    facts: row.facts ?? [], timeline: row.timeline ?? [] } as unknown as Run;
}

function json(method: string, body?: object): RequestInit {
  return { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) };
}

export async function listRuns(signal?: AbortSignal): Promise<RunHeader[]> {
  const value = await request(path, { signal });
  if (!isRecord(value) || value.runs != null && !Array.isArray(value.runs)) return invalid();
  return (value.runs ?? []).map(parseHeader);
}

export async function getRun(id: string, signal?: AbortSignal): Promise<Run> {
  const run = parseRun(await request(runPath(id), { signal }));
  return run.id === id ? run : invalid();
}

export const createRun = async (input: CreateRunInput) =>
  parseRun(await request(path, json('POST', input), 25000));

export const runAction = async (id: string, action: 'step' | 'apply' | 'retry', input: { state_version: number } | { candidate_id: string }) =>
  parseRun(await request(`${runPath(id)}/${action}`, json('POST', input), 25000));

export const saveEvent = async (id: string, input: EventInput, eventId?: string) =>
  parseRun(await request(`${runPath(id)}/events${eventId ? `/${encodeURIComponent(eventId)}` : ''}`, json(eventId ? 'PUT' : 'POST', input)));

export const removeEvent = async (id: string, eventId: string) =>
  parseRun(await request(`${runPath(id)}/events/${encodeURIComponent(eventId)}`, json('DELETE')));

export function runStatus(run: Pick<Run, 'status' | 'candidate'>) {
  if (run.status === 'candidate') return run.candidate?.status === 'OPTIMAL' ? 'Оптимальный план для модели' : 'Допустимый план';
  return statusNames[run.status];
}

export function canApply(run: Pick<Run, 'status' | 'candidate'>): boolean {
  return run.status === 'candidate' && Boolean(run.candidate?.plan && ['OPTIMAL', 'FEASIBLE'].includes(run.candidate.status));
}

export function runStepValue(run: Pick<Run, 'status' | 'tick'>): string {
  return run.status === 'completed' ? `${run.tick} из ${run.tick}` : String(run.tick);
}

export function runStepLabel(run: Pick<Run, 'status' | 'tick'>): string {
  return `Шаг ${runStepValue(run)}`;
}

export function canStep(run: Pick<Run, 'status' | 'tick'>): boolean {
  return run.status === 'ready' && run.tick < simulationHorizon;
}

export function canAutoAdvance(run: Pick<Run, 'mode' | 'status' | 'tick'>): boolean {
  return run.mode === 'operations' && canStep(run);
}

export function signedShift(value: number) { return value > 0 ? `+${value}` : value < 0 ? `−${-value}` : '0'; }

export function partySchedule(plan: Plan, partyId: string, operations: Map<string, Operation>): Schedule[] {
  return plan.scheduled_operations.filter(operation => operations.get(operation.operation_id)?.party_id === partyId)
    .sort((a, b) => a.start_tick - b.start_tick || a.end_tick - b.end_tick || a.operation_id.localeCompare(b.operation_id));
}

export function changedOperations(active: Schedule[], candidate: Schedule[]) {
  const changed = (from: Schedule[], to: Schedule[]) => from.filter(a => !to.some(b =>
    a.operation_id === b.operation_id && a.start_tick === b.start_tick && a.end_tick === b.end_tick));
  return { before: changed(active, candidate), after: changed(candidate, active) };
}

export function affects(event: ReportedEvent, operation: Schedule, metadata: Operation | undefined) {
  if (event.type === 'vessel_schedule_changed') return metadata?.service_id === event.target_id;
  const { from_tick, until_tick } = event.parameters ?? {};
  return metadata?.edge_id === event.target_id && from_tick !== undefined && until_tick !== undefined
    && operation.start_tick < until_tick && from_tick < operation.end_tick;
}
