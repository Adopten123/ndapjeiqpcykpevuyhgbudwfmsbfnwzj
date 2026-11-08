import { parseCalculation, request, ServiceError } from './api.js';
import type { Calculation, ScenarioID } from './types.js';

export interface ResearchOperation {
  readonly scenario: ScenarioID;
  readonly key: string;
  readonly body: string;
}

export function makeResearchOperation(scenario: ScenarioID, pending: ResearchOperation | null = null): ResearchOperation {
  // An unresolved request owns its original input even if another selection is attempted.
  return pending ?? Object.freeze({ scenario, key: crypto.randomUUID(), body: JSON.stringify({ scenario_id: scenario }) });
}

export async function executeResearchOperation(operation: ResearchOperation, signal?: AbortSignal): Promise<Calculation> {
  const calculation = parseCalculation(await request('/api/v1/calculations', {
    method: 'POST', signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': operation.key }, body: operation.body,
  }, 15000));
  if (calculation.scenario_id !== operation.scenario || calculation.engine_profile !== 'research-demo.v1') {
    throw new ServiceError('Ответ не соответствует отправленному учебному сценарию. Повторите тот же запрос или обновите историю.');
  }
  return calculation;
}
