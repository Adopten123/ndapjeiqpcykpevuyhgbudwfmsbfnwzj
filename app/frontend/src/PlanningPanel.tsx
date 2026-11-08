import { useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { getCalculation, getHistory, ServiceError } from './api';
import { ResultContent, StatusBadge } from './CalculationWorkspace';
import { isPending } from './types';
import type { Calculation } from './types';
import { canPreparePacket, executePlanningOperation, getPlanningContext, makePlanningOperation, matchesPlanningContext, sameSavedCalculation, sourceUpdateNotice, validSource } from './planning';
import type { PlanningContext, PlanningOperation, SourceValues } from './planning';
import { replaceRoute } from './routing';

const message = (error: unknown) => error instanceof ServiceError ? error.message : 'Не удалось обновить условия планирования.';
const date = (value: string) => new Date(value).toLocaleString('ru-RU', { timeZone: 'Europe/Moscow' });
const rememberCalculation = (id: string) => { const url = new URL(window.location.href); url.searchParams.set('planning_calculation', id); replaceRoute(url); };

function SourceForm({ planning, disabled, onSave }: { planning: PlanningContext; disabled: boolean; onSave: (values: SourceValues) => void }) {
  const [error, setError] = useState<string | null>(null);
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    const values = { start: Number(data.get('start')), end: Number(data.get('end')), departure: Number(data.get('departure')) };
    if (!validSource(values)) { setError('Укажите целые шаги: 0 ≤ начало < конец ≤ отправление ≤ 30.'); return; }
    if (values.start === planning.source.service_a_load_window.start_tick && values.end === planning.source.service_a_load_window.end_tick && values.departure === planning.source.service_a_departure_tick) {
      setError('Значения не изменились. Для пересчёта используйте кнопку ниже.'); return;
    }
    setError(null); onSave(values);
  };
  return <form className="source-form" onSubmit={submit}>
    <label>Начало погрузки A<input type="number" name="start" min={0} max={29} step={1} required defaultValue={planning.source.service_a_load_window.start_tick} disabled={disabled} /></label>
    <label>Конец погрузки A<input type="number" name="end" min={1} max={30} step={1} required defaultValue={planning.source.service_a_load_window.end_tick} disabled={disabled} /></label>
    <label>Отправление рейса A<input type="number" name="departure" min={1} max={30} step={1} required defaultValue={planning.source.service_a_departure_tick} disabled={disabled} /></label>
    <p className="source-form-note">Целые условные шаги от t0 = 0. Сохранение меняет учебные входные условия и обязательство рейса A; прежний пакет потребует проверки.</p>
    {error && <p className="source-form-error" role="alert">{error}</p>}
    <button type="submit" disabled={disabled}>Сохранить учебные условия</button>
  </form>;
}

export default function PlanningPanel({ readOnly, blocked, refreshToken, routeCalculationId, onContext, onPrepare, onLock, onChanged }: {
  readOnly: boolean; blocked: boolean; refreshToken: number; onContext: (context: PlanningContext) => void;
  routeCalculationId: string | null;
  onPrepare: (calculation: Calculation) => void; onLock: (locked: boolean) => void; onChanged: () => void;
}) {
  const [planning, setPlanning] = useState<PlanningContext | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [history, setHistory] = useState<Calculation[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(routeCalculationId);
  const [calculation, setCalculation] = useState<Calculation | null>(null);
  const [resultError, setResultError] = useState<string | null>(null);
  const [detailRevision, setDetailRevision] = useState(0);
  const [busy, setBusy] = useState(false);
  const [resolving, setResolving] = useState(false);
  const [pending, setPending] = useState<PlanningOperation | null>(null);
  const [operationError, setOperationError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [sourceFormRevision, setSourceFormRevision] = useState(0);
  const mounted = useRef(false); const posting = useRef(false); const pendingRef = useRef<PlanningOperation | null>(null);
  const resolvingRef = useRef(false); const selectedIdRef = useRef(routeCalculationId); const routeRef = useRef(routeCalculationId);
  const refreshController = useRef<AbortController | null>(null); const postController = useRef<AbortController | null>(null);
  const detailController = useRef<AbortController | null>(null); const epoch = useRef(0); const seed = useRef<Calculation | null>(null);
  useEffect(() => { onLock(busy || resolving || Boolean(pending)); }, [busy, resolving, pending, onLock]);
  useEffect(() => () => onLock(false), [onLock]);
  const selectCalculationId = useCallback((id: string | null) => { selectedIdRef.current = id; setSelectedId(id); }, []);
  const applyContext = useCallback((value: PlanningContext) => { setPlanning(value); onContext(value); }, [onContext]);
  const refresh = useCallback(async () => {
    if (posting.current) return false;
    refreshController.current?.abort(); const controller = new AbortController(); refreshController.current = controller; setLoading(true);
    const result = await Promise.allSettled([getPlanningContext(controller.signal), getHistory(controller.signal)]);
    if (!mounted.current || controller.signal.aborted) return false;
    if (result[0].status === 'fulfilled') { applyContext(result[0].value); setError(null); } else setError(message(result[0].reason));
    if (result[1].status === 'fulfilled') {
      const rows = result[1].value.filter(item => item.engine_profile === 'workflow-demo.v1');
      setHistory(rows); setHistoryError(null);
      if (!pendingRef.current && !selectedIdRef.current) selectCalculationId(rows[0]?.id ?? null);
    } else setHistoryError(message(result[1].reason));
    setLoading(false);
    return result.every(item => item.status === 'fulfilled');
  }, [applyContext, selectCalculationId]);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; epoch.current += 1; refreshController.current?.abort(); postController.current?.abort(); detailController.current?.abort(); };
  }, []);
  useEffect(() => { if (blocked) refreshController.current?.abort(); else if (!pending) void refresh(); }, [refresh, refreshToken, blocked, pending]);
  useEffect(() => {
    if (routeRef.current === routeCalculationId) return;
    routeRef.current = routeCalculationId;
    // Internal selection already installed its saved record; keep its notice and polling seed.
    if (selectedIdRef.current === routeCalculationId) return;
    epoch.current += 1; detailController.current?.abort(); seed.current = null;
    selectCalculationId(routeCalculationId); setCalculation(null); setResultError(null); setNotice(null);
    setDetailRevision(current => current + 1);
  }, [routeCalculationId, selectCalculationId]);
  const open = (value: Calculation) => {
    epoch.current += 1; detailController.current?.abort(); seed.current = value;
    selectCalculationId(value.id); setCalculation(value); setResultError(null); setDetailRevision(current => current + 1); rememberCalculation(value.id);
  };
  useEffect(() => {
    if (!selectedId || busy) return;
    const current = ++epoch.current; const controller = new AbortController(); detailController.current = controller;
    let timer: number | undefined; let polling = seed.current?.id === selectedId && isPending(seed.current.status);
    const update = async () => {
      try {
        const value = await getCalculation(selectedId, controller.signal);
        if (value.engine_profile !== 'workflow-demo.v1') throw new ServiceError('Этот запуск относится к отдельному учебному примеру. Выберите расчёт текущего планирования.');
        if (!mounted.current || controller.signal.aborted || current !== epoch.current) return;
        if (seed.current?.id === value.id && !sameSavedCalculation(seed.current, value)) throw new ServiceError('Привязка сохранённого расчёта изменилась. Результат не подставлен; обновите историю.');
        seed.current = value;
        setCalculation(value); setResultError(null); polling = isPending(value.status);
        setHistory(previous => [value, ...previous.filter(item => item.id !== value.id)].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)).slice(0, 20));
        if (polling) timer = window.setTimeout(update, 1000);
      } catch (e) {
        if (!mounted.current || controller.signal.aborted || current !== epoch.current) return;
        setResultError(message(e)); if (polling) timer = window.setTimeout(update, 2500);
      }
    };
    void update();
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [selectedId, detailRevision, busy]);
  const perform = async (operation: PlanningOperation) => {
    if (readOnly || posting.current || resolvingRef.current || blocked) return;
    const repeating = Boolean(pendingRef.current);
    operation = pendingRef.current ?? operation;
    posting.current = true; pendingRef.current = operation; setPending(operation); setBusy(true); onLock(true); setOperationError(null); setNotice(null);
    refreshController.current?.abort(); const controller = new AbortController(); postController.current = controller;
    if (operation.kind === 'calculate') { epoch.current += 1; detailController.current?.abort(); seed.current = null; setCalculation(null); selectCalculationId(null); }
    try {
      const value = await executePlanningOperation(operation, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      if (operation.kind === 'calculate') { open(value as Calculation); setNotice('Расчёт поставлен в очередь с сохранённой привязкой к условиям и базе.'); }
      else { const current = value as PlanningContext; applyContext(current); setSourceFormRevision(revision => revision + 1); setNotice(sourceUpdateNotice(current)); }
      pendingRef.current = null; setPending(null); onChanged();
    } catch (e) {
      if (!mounted.current || controller.signal.aborted) return;
      setOperationError(message(e));
      if (!repeating && e instanceof ServiceError && [400, 403, 404, 409, 422].includes(e.status)) { pendingRef.current = null; setPending(null); onChanged(); }
    } finally {
      posting.current = false;
      if (mounted.current && !controller.signal.aborted) { setBusy(false); if (!pendingRef.current) void refresh(); }
    }
  };
  const resolvePending = async () => {
    const operation = pendingRef.current;
    if (!operation || blocked || posting.current || resolvingRef.current) return;
    resolvingRef.current = true; setResolving(true);
    try {
      if (await refresh() && mounted.current && pendingRef.current === operation) {
        pendingRef.current = null; setPending(null); setOperationError(null);
        setNotice('Условия, база и история обновлены, повтор закрыт. Это не отменяет прежнюю операцию и не подтверждает её исход. Проверьте актуальные данные перед новым действием.');
        onChanged();
      }
    } finally { resolvingRef.current = false; if (mounted.current) setResolving(false); }
  };
  const start = (kind: PlanningOperation['kind'], values?: SourceValues) => {
    if (readOnly || !planning || loading || error || blocked || posting.current || resolvingRef.current || pendingRef.current) return;
    try { void perform(makePlanningOperation(kind, planning, values)); } catch (e) { setOperationError(message(e)); }
  };
  const active = calculation?.id === selectedId && (routeCalculationId === routeRef.current || routeCalculationId === selectedId) ? calculation : null;
  const activePending = Boolean(active && isPending(active.status));
  const disabled = blocked || busy || resolving || Boolean(pending) || loading;
  const currentResult = Boolean(active && planning && matchesPlanningContext(active, planning));
  return <section className="planning-panel" aria-labelledby="planning-title">
    <div className="section-heading"><h2 id="planning-title">Текущие условия и принятая база</h2><button type="button" className="quiet-button" disabled={busy || resolving || Boolean(pending) || blocked} onClick={() => void refresh()}>Обновить</button></div>
    {error && <div className="inline-error" role="alert">{error}</div>}
    {!planning ? <p className="packet-summary">{loading ? 'Загружаем учебные условия…' : 'Условия недоступны. Обновите данные.'}</p> : <>
      <dl className="planning-facts"><dt>Условия</dt><dd>Редакция {planning.source.revision} · рейс A: погрузка {planning.source.service_a_load_window.start_tick}–{planning.source.service_a_load_window.end_tick}, отправление {planning.source.service_a_departure_tick}</dd><dt>База сравнения</dt><dd>{planning.baseline.accepted_version === 0 ? 'Начальная учебная база DEMO-v0' : `Принятая учебная версия ${planning.baseline.accepted_version}`}</dd></dl>
      {!readOnly && <SourceForm key={`${planning.source.revision}:${sourceFormRevision}`} planning={planning} disabled={disabled} onSave={values => start('source', values)} />}
      <div className="planning-calculate"><p>{planning.baseline.accepted_version === 0 ? 'Расчёт использует начальную базу и сохранённые учебные условия.' : `Новый кандидат сравнивается с принятой версией ${planning.baseline.accepted_version}. Выполненные факты сохраняются.`} Сроки в условных шагах; результат не принимается автоматически.</p>
        {!readOnly && <button type="button" className="primary" disabled={disabled || Boolean(error) || !planning.can_calculate || activePending} onClick={() => start('calculate')}>{busy && pending?.kind === 'calculate' ? 'Отправляем…' : activePending ? 'Расчёт выполняется' : planning.baseline.accepted_version > 0 ? 'Пересчитать от принятой версии' : 'Рассчитать от начальной базы'}</button>}
      </div>
      <details className="source-details"><summary>Контроль привязки текущих условий</summary><dl><dt>План базы</dt><dd>{planning.baseline.plan_id}</dd><dt>Пакет базы</dt><dd>{planning.baseline.packet_id ?? 'Начальная база, без пакета'}</dd><dt>Контроль источника</dt><dd>{planning.source.input_sha256}</dd><dt>Контроль базы</dt><dd>{planning.baseline.baseline_sha256}</dd><dt>Контроль контекста</dt><dd>{planning.context_sha256}</dd></dl></details>
    </>}
    {operationError && <div className="inline-error" role="alert"><p>{operationError}</p>{pending && <><p className="fine">Исход операции неизвестен. Повтор сохраняет прежний ключ и точные условия, даже если серверный контекст уже изменился.</p><button type="button" disabled={busy || resolving || blocked} onClick={() => void perform(pending)}>Повторить тот же запрос</button><button type="button" disabled={busy || resolving || blocked} onClick={() => void resolvePending()}>{resolving ? 'Обновляем условия и историю…' : 'Закрыть повтор и сверить историю'}</button><p className="fine">Повтор закроется только после успешного обновления условий, базы и истории. Прежняя операция не отменяется.</p></>}</div>}
    {notice && <p className="approval-notice" role="status">{notice}</p>}
    <div className="planning-results">
      <div className="section-heading"><h3>Расчёт для следующей версии</h3>{active && <StatusBadge status={active.status} />}</div>
      {resultError && <div className="inline-error" role="alert"><p>{resultError}</p><button type="button" disabled={busy || resolving || Boolean(pending)} onClick={() => setDetailRevision(value => value + 1)}>Обновить результат</button></div>}
      {active ? <>
        <p className="packet-summary">Запуск {date(active.created_at)} мск · редакция условий {active.source_revision} · база {active.base_accepted_version === 0 ? 'DEMO-v0' : `принятая версия ${active.base_accepted_version}`}</p>
        {!currentResult && planning && <div className="outcome outcome-warning"><div><h3>Расчёт относится к прежнему контексту</h3><p>Условия или принятая база изменились. Этот результат остаётся в истории; для нового пакета нужен пересчёт от текущего контекста.</p></div></div>}
        <ResultContent calculation={active} />
        {!readOnly && planning && <div className="prepare-action"><button type="button" disabled={disabled || Boolean(error) || !canPreparePacket(active, planning)} onClick={() => onPrepare(active)}>Подготовить пакет из этого расчёта</button></div>}
      </> : <p className="packet-summary">Запустите пересчёт или откройте сохранённый расчёт. Результат из другого запуска не подставляется.</p>}
      <div className="planning-history"><h3>История расчётов версий</h3>{historyError && <p role="alert">{historyError}</p>}{history.length === 0 && <p className="muted">Расчётов нового профиля пока нет.</p>}<ul>{history.map(item => <li key={item.id}><button type="button" disabled={busy || resolving || Boolean(pending) || blocked} aria-pressed={selectedId === item.id} onClick={() => { if (!posting.current && !resolvingRef.current && !pendingRef.current && !blocked) open(item); }}><span>{date(item.created_at)} мск · условия {item.source_revision} · база {item.base_accepted_version}</span><StatusBadge status={item.status} /></button></li>)}</ul></div>
    </div>
  </section>;
}
