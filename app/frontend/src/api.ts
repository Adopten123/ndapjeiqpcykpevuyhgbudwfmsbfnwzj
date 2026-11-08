import { isScenario } from './types.js';
import type { Calculation, CalculationStatus, PartyPlan, ResearchResult, ScenarioID, SolverStatus } from './types.js';

export class ServiceError extends Error {
  readonly status: number;
  readonly field?: string;
  constructor(message: string, status = 0, field?: string) { super(message); this.status = status; this.field = field; }
}

const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const integer = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && Number.isFinite(value);
const text = (value: unknown): value is string => typeof value === 'string' && value.length > 0;
const invalid = (): never => { throw new ServiceError('Сервис вернул неподдерживаемый ответ. Обновите страницу или повторите позже.'); };

function partyPlan(value: unknown): PartyPlan {
  if (!object(value) || !text(value.party_id) || !text(value.route_id) || !text(value.service_id)
    || !integer(value.port_receipt_tick) || !integer(value.recipient_receipt_tick)) return invalid();
  return { party_id: value.party_id, route_id: value.route_id, service_id: value.service_id,
    port_receipt_tick: value.port_receipt_tick, recipient_receipt_tick: value.recipient_receipt_tick };
}

function researchResult(value: unknown): ResearchResult {
  if (!object(value) || !['research-demo.v1', 'workflow-demo.v1'].includes(String(value.engine_profile)) || value.execution !== 'executed'
    || value.synthetic !== true || !['initial', 'replan'].includes(String(value.mode))
    || !['OPTIMAL', 'FEASIBLE', 'INFEASIBLE', 'UNKNOWN', 'MODEL_INVALID'].includes(String(value.solver_status))
    || !integer(value.proven_prefix) || !text(value.snapshot_id)
    || !text(value.input_sha256) || !/^[a-f0-9]{64}$/i.test(value.input_sha256)) return invalid();
  const mode = value.mode as 'initial' | 'replan';
  const names = mode === 'initial' ? ['sum_remaining_tick', 'last_remaining_tick'] : ['sum_positive_shift', 'max_positive_shift', 'changed_groups'];
  if (value.proven_prefix < 0 || value.proven_prefix > names.length) return invalid();
  let candidate: ResearchResult['candidate'] = null;
  let objective: ResearchResult['objective'] = null;
  if (value.candidate !== null) {
    if (!object(value.candidate) || !Array.isArray(value.candidate.party_plans)) return invalid();
    const plans = value.candidate.party_plans.map(partyPlan);
    if (plans.length !== 4 || new Set(plans.map(p => p.party_id)).size !== 4
      || !plans.every(p => ['P1', 'P2', 'P3', 'P4'].includes(p.party_id))) return invalid();
    if (!object(value.objective) || !names.every(name => integer(value.objective && (value.objective as Record<string, unknown>)[name]))) return invalid();
    objective = Object.fromEntries(names.map(name => [name, (value.objective as Record<string, number>)[name]]));
    candidate = { party_plans: plans };
    if (!['OPTIMAL', 'FEASIBLE', 'UNKNOWN'].includes(String(value.solver_status))) return invalid();
  } else if (!['INFEASIBLE', 'UNKNOWN', 'MODEL_INVALID'].includes(String(value.solver_status)) || value.objective !== null) return invalid();
  if (value.solver_status === 'OPTIMAL' && value.proven_prefix !== names.length) return invalid();
  return { engine_profile: value.engine_profile as ResearchResult['engine_profile'], execution: 'executed', synthetic: true, mode,
    solver_status: value.solver_status as SolverStatus, proven_prefix: value.proven_prefix, candidate, objective,
    snapshot_id: value.snapshot_id, input_sha256: value.input_sha256 };
}

export function parseCalculation(value: unknown): Calculation {
  if (!object(value) || !text(value.id) || !isScenario(value.scenario_id)
    || !['queued', 'running', 'succeeded', 'failed'].includes(String(value.status))
    || !text(value.created_at) || !Number.isFinite(Date.parse(value.created_at))
    || !text(value.updated_at) || !Number.isFinite(Date.parse(value.updated_at))
    || !['research-demo.v1', 'workflow-demo.v1'].includes(String(value.engine_profile)) || !text(value.snapshot_id)
    || !text(value.input_sha256) || !/^[a-f0-9]{64}$/i.test(value.input_sha256)) return invalid();
  const result = value.result == null ? null : researchResult(value.result);
  if (value.status === 'succeeded' && result === null) return invalid();
  if (result && (result.mode === 'initial') !== (value.scenario_id === 'S0')) return invalid();
  if (result && (result.snapshot_id !== value.snapshot_id || result.input_sha256 !== value.input_sha256)) return invalid();
  if (result && result.engine_profile !== value.engine_profile) return invalid();
  const workflow = value.engine_profile === 'workflow-demo.v1';
  const binding = {
    source_revision: value.source_revision ?? 0, base_accepted_version: value.base_accepted_version ?? 0,
    base_accepted_packet_id: value.base_accepted_packet_id ?? null,
    source_sha256: value.source_sha256 ?? '', baseline_sha256: value.baseline_sha256 ?? '', context_sha256: value.context_sha256 ?? '',
  };
  if (workflow) {
    if (!['source_revision', 'base_accepted_version', 'base_accepted_packet_id', 'source_sha256', 'baseline_sha256', 'context_sha256'].every(key => Object.hasOwn(value, key))
      || !integer(value.base_accepted_version) || !(value.base_accepted_packet_id === null || text(value.base_accepted_packet_id))
      || value.scenario_id !== 'S1' || !integer(binding.source_revision) || binding.source_revision < 1
      || !integer(binding.base_accepted_version) || binding.base_accepted_version < 0
      || (binding.base_accepted_version === 0 ? binding.base_accepted_packet_id !== null : !text(binding.base_accepted_packet_id))
      || ![binding.source_sha256, binding.baseline_sha256, binding.context_sha256].every(hash => text(hash) && /^[a-f0-9]{64}$/i.test(hash))) return invalid();
  } else if (binding.source_revision !== 0 || binding.base_accepted_version !== 0 || binding.base_accepted_packet_id !== null
    || binding.source_sha256 !== '' || binding.baseline_sha256 !== '' || binding.context_sha256 !== '') return invalid();
  return { id: value.id, scenario_id: value.scenario_id, status: value.status as CalculationStatus,
    created_at: value.created_at, updated_at: value.updated_at, engine_profile: value.engine_profile as Calculation['engine_profile'],
    snapshot_id: value.snapshot_id, input_sha256: value.input_sha256, result,
    error: typeof value.error === 'string' ? value.error : null, ...binding } as Calculation;
}

export async function request(path: string, options: RequestInit = {}, timeoutMs = 10000): Promise<unknown> {
  const controller = new AbortController();
  const external = options.signal;
  const abort = () => controller.abort();
  if (external?.aborted) throw new DOMException('Aborted', 'AbortError');
  external?.addEventListener('abort', abort, { once: true });
  const timer = window.setTimeout(abort, timeoutMs);
  try {
    const response = await fetch(path, { ...options, credentials: 'same-origin', signal: controller.signal, cache: 'no-store' });
    if (external?.aborted) throw new DOMException('Aborted', 'AbortError');
    if (!response.ok) {
      if (response.status === 401 && path !== '/api/v1/session') window.dispatchEvent(new Event('session-expired'));
      const approval = path.includes('approval-') || path.includes('planning-');
      const simulation = path.startsWith('/api/v1/simulation/');
      if (simulation && [400, 404, 409, 422].includes(response.status)) {
        const issue: unknown = await response.json().catch(() => null);
        if (external?.aborted) throw new DOMException('Aborted', 'AbortError');
        if (object(issue) && text(issue.error) && issue.error.length <= 300) {
          throw new ServiceError(issue.error, response.status, text(issue.field) ? issue.field : undefined);
        }
        throw new ServiceError('Действие недоступно. Проверьте актуальное состояние прогона.', response.status);
      }
      const message = response.status === 401 ? 'Сеанс не действует. Войдите с индивидуальным учебным ключом.'
        : response.status === 403 ? 'Для этой операции нет полномочий. Обновите данные или войдите с выданным вам ключом.'
        : response.status === 409 ? approval ? 'Условия или ответы изменились. Данные обновлены; проверьте версию перед новым действием.' : 'Этот запрос уже связан с другим сценарием. Начните новый расчёт.'
        : response.status === 404 ? approval ? 'Версия не найдена или недоступна. Обновите список пакетов.' : 'Расчёт не найден. Обновите историю.'
        : response.status === 400 || response.status === 422 ? approval ? 'Сервис не принял ответ или переход. Проверьте срок, выбранную версию и состояние предметов.' : 'Сервис не принял исходные условия. Обновите список сценариев.'
        : 'Сервис приложения временно недоступен. Повторите запрос позже.';
      throw new ServiceError(message, response.status);
    }
    if (response.status === 204) return null;
    try { return await response.json(); } catch { return invalid(); }
  } catch (error) {
    if (external?.aborted) throw new DOMException('Aborted', 'AbortError');
    if (error instanceof ServiceError) throw error;
    throw new ServiceError(controller.signal.aborted ? 'Сервис не ответил вовремя. Повторите запрос.' : 'Нет связи с сервисом приложения. Повторите запрос.');
  } finally {
    window.clearTimeout(timer);
    external?.removeEventListener('abort', abort);
  }
}

export async function getScenarios(signal?: AbortSignal): Promise<ScenarioID[]> {
  const value = await request('/api/v1/scenarios', { signal });
  if (!Array.isArray(value)) return invalid();
  const ids = value.map(row => object(row) && isScenario(row.scenario_id) ? row.scenario_id : invalid());
  if (ids.length !== 4 || new Set(ids).size !== 4) return invalid();
  return ids;
}
export async function getHistory(signal?: AbortSignal): Promise<Calculation[]> {
  const value = await request('/api/v1/calculations', { signal });
  if (!Array.isArray(value)) return invalid();
  return value.map(parseCalculation);
}
export async function getCalculation(id: string, signal?: AbortSignal): Promise<Calculation> {
  const value = parseCalculation(await request(`/api/v1/calculations/${encodeURIComponent(id)}`, { signal }));
  if (value.id !== id) return invalid();
  return value;
}
export async function createCalculation(scenario: ScenarioID, key: string, signal?: AbortSignal): Promise<Calculation> {
  const value = parseCalculation(await request('/api/v1/calculations', { method: 'POST', signal,
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    body: JSON.stringify({ scenario_id: scenario }) }, 15000));
  if (value.scenario_id !== scenario || value.engine_profile !== 'research-demo.v1') return invalid();
  return value;
}
