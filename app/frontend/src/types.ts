export const SCENARIOS = {
  S0: { name: 'Первый план', description: 'Построить начальный план без общей принятой базы. Сравниваются время до получения всех партий и срок последней партии.' },
  S1: { name: 'Задержка судна', description: 'Рейс A задержан. Пересчёт проверяет будущую железнодорожную подачу и вместимость порта; выполненное движение сохраняется.' },
  S2: { name: 'Закрытие пролива', description: 'Проход через UV закрыт в заданный период. Проверяются фиксированные рейсы A и B; новый рейс автоматически не добавляется.' },
  S3: { name: 'Ремонт пути', description: 'Участок OB закрыт на ремонт. Для будущей подачи доступен учебный путь через C; уже начатое движение P2 сохраняется.' },
} as const;

export type ScenarioID = keyof typeof SCENARIOS;
export type CalculationStatus = 'queued' | 'running' | 'succeeded' | 'failed';
export type SolverStatus = 'OPTIMAL' | 'FEASIBLE' | 'INFEASIBLE' | 'UNKNOWN' | 'MODEL_INVALID';
export type EngineProfile = 'research-demo.v1' | 'workflow-demo.v1';
export interface PartyPlan {
  party_id: string;
  route_id: string;
  service_id: string;
  port_receipt_tick: number;
  recipient_receipt_tick: number;
}
export interface ResearchResult {
  engine_profile: EngineProfile;
  execution: 'executed';
  synthetic: true;
  mode: 'initial' | 'replan';
  solver_status: SolverStatus;
  proven_prefix: number;
  objective: Record<string, number> | null;
  candidate: { party_plans: PartyPlan[] } | null;
  snapshot_id: string;
  input_sha256: string;
}
export interface Calculation {
  id: string;
  scenario_id: ScenarioID;
  status: CalculationStatus;
  created_at: string;
  updated_at: string;
  engine_profile: EngineProfile;
  snapshot_id: string;
  input_sha256: string;
  result: ResearchResult | null;
  error: string | null;
  source_revision: number;
  base_accepted_version: number;
  base_accepted_packet_id: string | null;
  source_sha256: string;
  baseline_sha256: string;
  context_sha256: string;
}

export function isScenario(value: unknown): value is ScenarioID {
  return typeof value === 'string' && Object.hasOwn(SCENARIOS, value);
}

export function isPending(status: CalculationStatus): boolean {
  return status === 'queued' || status === 'running';
}
