import { useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { getCalculation, getHistory, getScenarios, ServiceError } from './api';
import { executeResearchOperation, makeResearchOperation } from './researchOperation';
import type { ResearchOperation } from './researchOperation';
import { isPending, SCENARIOS } from './types';
import type { Calculation, CalculationStatus, ResearchResult, ScenarioID } from './types';

const dates = new Intl.DateTimeFormat('ru-RU', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'Europe/Moscow' });
const date = (value: string) => dates.format(new Date(value));
const message = (error: unknown) => error instanceof ServiceError ? error.message : 'Не удалось получить данные. Повторите позже.';
const statusNames: Record<CalculationStatus, string> = { queued: 'В очереди', running: 'Считаем', succeeded: 'Расчёт завершён', failed: 'Ошибка расчёта' };
const criterionNames: Record<string, string> = {
  sum_remaining_tick: 'Сумма времени до получения', last_remaining_tick: 'Время до последней партии',
  sum_positive_shift: 'Сумма положительных переносов', max_positive_shift: 'Максимальный перенос', changed_groups: 'Изменённые группы подачи',
};

function criterionUnit(value: number, group: boolean) {
  const n = Math.abs(value);
  const form = n % 100 >= 11 && n % 100 <= 14 ? 2 : n % 10 === 1 ? 0 : n % 10 >= 2 && n % 10 <= 4 ? 1 : 2;
  return (group ? ['группа', 'группы', 'групп'] : ['шаг', 'шага', 'шагов'])[form];
}

function mergeHistory(previous: Calculation[], incoming: Calculation[]): Calculation[] {
  const records = new Map(previous.map(row => [row.id, row]));
  for (const row of incoming) {
    const old = records.get(row.id);
    if (!old || Date.parse(row.updated_at) >= Date.parse(old.updated_at)) records.set(row.id, row);
  }
  return [...records.values()].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)).slice(0, 20);
}

export function StatusBadge({ status }: { status: CalculationStatus }) {
  return <span className={`status-badge status-${status}`}><span className={isPending(status) ? 'status-dot pending' : 'status-dot'} aria-hidden="true" />{statusNames[status]}</span>;
}

export function ResultContent({ calculation }: { calculation: Calculation }) {
  if (isPending(calculation.status)) return <div className="result-state pending-state">
    <div className="progress-mark" aria-hidden="true" />
    <h3>{calculation.status === 'queued' ? 'Расчёт поставлен в очередь' : 'Проверяем допустимые варианты'}</h3>
    <p>{calculation.status === 'queued' ? 'Исходный снимок сохранён. Результат появится здесь после выполнения.' : 'Проверяем ограничения и по очереди оптимизируем критерии. Страница обновится автоматически.'}</p>
  </div>;
  if (calculation.status === 'failed') return <div className="result-state failure-state">
    <span className="state-symbol" aria-hidden="true">!</span><h3>Расчёт не выполнен</h3>
    <p>Сервис не смог завершить этот запуск. Можно начать новый расчёт; запись об ошибке останется в истории.</p>
  </div>;
  const result = calculation.result;
  if (!result) return <div className="result-state failure-state"><h3>Результат не получен</h3><p>Обновите данные этого запуска.</p></div>;
  return <>
    <Outcome result={result} />
    {result.candidate && <>
      <div className="table-scroll" tabIndex={0} aria-label="Сроки четырёх партий, доступна горизонтальная прокрутка">
        <table className="party-table">
          <caption>Сроки в условных шагах от t0 = 0</caption>
          <thead><tr><th scope="col">Партия</th><th scope="col">Путь / рейс</th><th scope="col" className="number">Приём в порту<br />назначения</th><th scope="col" className="number">Получение<br />у получателя</th></tr></thead>
          <tbody>{[...result.candidate.party_plans].sort((a, b) => a.party_id.localeCompare(b.party_id)).map(party => <tr key={party.party_id}>
            <th scope="row">{party.party_id}</th>
            <td>{party.party_id === 'P1' ? 'Из порта Мурманска' : party.party_id === 'P2' ? 'Из текущего участка' : party.route_id.endsWith('-alt') ? 'Условный путь через C' : 'Условный путь через B'}<span className="subcell">Учебный рейс {party.service_id}</span></td>
            <td className="number">{party.port_receipt_tick}</td><td className="number">{party.recipient_receipt_tick}</td>
          </tr>)}</tbody>
        </table>
      </div>
      <p className="table-note"><span className="scroll-hint">Прокрутите таблицу вправо, чтобы увидеть сроки.</span>Приём в порту и получение у получателя — разные события. Шаги не являются часами или днями.</p>
      {result.objective && <section className="criteria" aria-labelledby="criteria-title">
        <div className="criteria-heading"><h3 id="criteria-title">Критерии выбора</h3><span>Порядок важен</span></div>
        <ol>{Object.entries(result.objective).map(([key, value], index) => <li key={key}>
          <span className="criterion-label"><span className="criterion-order" aria-hidden="true">{index + 1}</span>{criterionNames[key] ?? 'Критерий'}</span>
          <span className="criterion-value">{value}<small>{criterionUnit(value, key === 'changed_groups')}</small></span>
          <span className={index < result.proven_prefix ? 'criterion-proof proved' : 'criterion-proof'}>{index < result.proven_prefix ? 'Оптимум доказан' : 'Оптимум не доказан'}</span>
        </li>)}</ol>
        <p className="criteria-note">Подтверждён оптимум по {result.proven_prefix} из {Object.keys(result.objective).length} критериев. Вывод относится к заданной учебной модели.</p>
        {calculation.scenario_id === 'S1' && calculation.engine_profile === 'research-demo.v1' && <p className="criteria-note">Ожидание части груза переносится с порта на станцию. Это не измеренная экономия времени или денег.</p>}
      </section>}
    </>}
  </>;
}

function Outcome({ result }: { result: ResearchResult }) {
  if (result.solver_status === 'INFEASIBLE') return <div className="outcome outcome-warning">
    <span className="state-symbol" aria-hidden="true">!</span><div><h3>При заданных условиях нет допустимого варианта</h3>
      <p>{result.engine_profile === 'research-demo.v1' ? 'Фиксированные рейсы A и B конфликтуют с закрытием пролива.' : 'Заданные окна рейсов и ограничения учебной модели несовместимы.'} Новые сроки четырёх партий не определены. Вывод относится к этому учебному набору, а не ко всем возможным перевозкам.</p></div>
  </div>;
  if (result.solver_status === 'MODEL_INVALID') return <div className="outcome outcome-error"><span className="state-symbol" aria-hidden="true">!</span><div><h3>Ошибка расчётной модели</h3><p>Достоверный вариант не получен. Этот результат нельзя использовать как план.</p></div></div>;
  if (!result.candidate) return <div className="outcome outcome-warning"><span className="state-symbol" aria-hidden="true">…</span><div><h3>Расчёт остановлен без найденного варианта</h3><p>Отсутствие решения не доказано. Это не означает невозможность перевозки.</p></div></div>;
  if (result.solver_status === 'OPTIMAL') return <div className="outcome outcome-success"><span className="state-symbol" aria-hidden="true">✓</span><div><h3>Найден оптимум в учебной модели</h3><p>Вариант прошёл отдельную проверку ограничений. Согласование учебной версии выполняется отдельно после расчёта.</p></div></div>;
  return <div className="outcome outcome-warning"><span className="state-symbol" aria-hidden="true">!</span><div><h3>Найден допустимый вариант</h3><p>Полный оптимум не доказан. Ниже отмечены только проверенные уровни критериев.</p></div></div>;
}

export default function CalculationWorkspace({ readOnly, onInteractionLock }: { readOnly: boolean; onInteractionLock?: (locked: boolean) => void }) {
  const [scenario, setScenario] = useState<ScenarioID>('S1');
  const [available, setAvailable] = useState<ScenarioID[]>([]);
  const [catalogLoading, setCatalogLoading] = useState(true);
  const [catalogError, setCatalogError] = useState<string | null>(null);
  const [history, setHistory] = useState<Calculation[]>([]);
  const [historyLoading, setHistoryLoading] = useState(true);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [calculation, setCalculation] = useState<Calculation | null>(null);
  const [recordLoading, setRecordLoading] = useState(false);
  const [viewError, setViewError] = useState<string | null>(null);
  const [detailRevision, setDetailRevision] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [pendingRequest, setPendingRequest] = useState<ResearchOperation | null>(null);
  const [resolving, setResolving] = useState(false);
  const [submitNotice, setSubmitNotice] = useState<string | null>(null);
  const mounted = useRef(true);
  const context = useRef(0);
  const seed = useRef<Calculation | null>(null);
  const posting = useRef(false);
  const pendingRef = useRef<ResearchOperation | null>(null);
  const resolvingRef = useRef(false);
  const catalogController = useRef<AbortController | null>(null);
  const historyController = useRef<AbortController | null>(null);
  const postController = useRef<AbortController | null>(null);
  const resultHeading = useRef<HTMLHeadingElement | null>(null);
  const upsert = useCallback((record: Calculation) => setHistory(previous => mergeHistory(previous, [record])), []);
  const interactionLocked = submitting || resolving || Boolean(pendingRequest);
  useEffect(() => { onInteractionLock?.(interactionLocked); }, [interactionLocked, onInteractionLock]);
  useEffect(() => () => onInteractionLock?.(false), [onInteractionLock]);
  const retainOperation = (operation: ResearchOperation | null) => { pendingRef.current = operation; setPendingRequest(operation); };

  const refreshCatalog = useCallback(async () => {
    catalogController.current?.abort();
    const controller = new AbortController(); catalogController.current = controller;
    setCatalogLoading(true); setCatalogError(null);
    try {
      const ids = await getScenarios(controller.signal);
      if (!controller.signal.aborted && mounted.current) setAvailable(ids);
    } catch (error) {
      if (!controller.signal.aborted && mounted.current) { setAvailable([]); setCatalogError(message(error)); }
    } finally { if (!controller.signal.aborted && mounted.current) setCatalogLoading(false); }
  }, []);
  const refreshHistory = useCallback(async () => {
    historyController.current?.abort();
    const controller = new AbortController(); historyController.current = controller;
    setHistoryLoading(true); setHistoryError(null);
    try {
      const rows = (await getHistory(controller.signal)).filter(row => row.engine_profile === 'research-demo.v1');
      if (!controller.signal.aborted && mounted.current) { setHistory(previous => mergeHistory(previous, rows)); return true; }
    } catch (error) { if (!controller.signal.aborted && mounted.current) setHistoryError(message(error)); }
    finally { if (!controller.signal.aborted && mounted.current) setHistoryLoading(false); }
    return false;
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refreshCatalog(); void refreshHistory();
    return () => { mounted.current = false; catalogController.current?.abort(); historyController.current?.abort(); postController.current?.abort(); };
  }, [refreshCatalog, refreshHistory]);

  const hasPendingHistory = history.some(record => isPending(record.status));
  useEffect(() => {
    if (!hasPendingHistory || pendingRequest) return;
    let cancelled = false;
    let timer: number | undefined;
    const update = async () => {
      await refreshHistory();
      if (!cancelled) timer = window.setTimeout(update, 3500);
    };
    timer = window.setTimeout(update, 3500);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [hasPendingHistory, pendingRequest, refreshHistory]);

  const clearSelection = () => {
    context.current += 1; seed.current = null; setActiveId(null); setCalculation(null); setViewError(null); setRecordLoading(false);
  };
  const showRecord = (record: Calculation) => {
    context.current += 1; seed.current = record;
    setScenario(record.scenario_id); setActiveId(record.id); setCalculation(record); setRecordLoading(false); setViewError(null);
    setSubmitError(null); setSubmitNotice(null); setDetailRevision(value => value + 1); upsert(record);
    requestAnimationFrame(() => resultHeading.current?.focus({ preventScroll: false }));
  };

  useEffect(() => {
    if (!activeId) return;
    const current = context.current;
    const controller = new AbortController();
    let cancelled = false;
    let timer: number | undefined;
    let keepPolling = seed.current?.id === activeId && isPending(seed.current.status);
    const update = async () => {
      try {
        const record = await getCalculation(activeId, controller.signal);
        if (cancelled || current !== context.current) return;
        setCalculation(record); setRecordLoading(false); setViewError(null); upsert(record);
        keepPolling = isPending(record.status);
        if (keepPolling) timer = window.setTimeout(update, 900);
      } catch (error) {
        if (cancelled || controller.signal.aborted || current !== context.current) return;
        setViewError(message(error)); setRecordLoading(false);
        if (keepPolling) timer = window.setTimeout(update, 2500);
      }
    };
    void update();
    return () => { cancelled = true; controller.abort(); window.clearTimeout(timer); };
  }, [activeId, detailRevision, upsert]);

  const active = calculation?.id === activeId ? calculation : null;
  const activePending = active ? isPending(active.status) : false;
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (readOnly || posting.current || resolvingRef.current || activePending || (!pendingRef.current && (catalogLoading || catalogError || !available.includes(scenario)))) return;
    const repeating = Boolean(pendingRef.current);
    const operation = makeResearchOperation(scenario, pendingRef.current);
    posting.current = true; const controller = new AbortController(); postController.current = controller;
    setSubmitting(true); setSubmitError(null); setSubmitNotice(null); retainOperation(operation); onInteractionLock?.(true); clearSelection();
    try {
      const created = await executeResearchOperation(operation, controller.signal);
      if (mounted.current && !controller.signal.aborted) { retainOperation(null); showRecord(created); void refreshHistory(); }
    } catch (error) {
      if (mounted.current && !controller.signal.aborted) {
        setSubmitError(message(error));
        if (!repeating && error instanceof ServiceError && [400, 403, 404, 409, 422].includes(error.status)) retainOperation(null);
      }
    } finally {
      posting.current = false;
      if (mounted.current) setSubmitting(false);
    }
  };
  const resolvePending = async () => {
    const operation = pendingRef.current;
    if (!operation || posting.current || resolvingRef.current) return;
    resolvingRef.current = true; setResolving(true);
    try {
      if (await refreshHistory() && mounted.current && pendingRef.current === operation) {
        retainOperation(null); setSubmitError(null);
        setSubmitNotice('История обновлена, повтор закрыт. Это не отменяет прежний запрос и не подтверждает его исход. Проверьте последние запуски перед новым расчётом.');
      }
    } finally { resolvingRef.current = false; if (mounted.current) setResolving(false); }
  };
  const liveText = submitting ? 'Отправляем запрос расчёта.' : active ? `${SCENARIOS[active.scenario_id].name}. ${statusNames[active.status]}.` : '';
  const buttonLabel = submitting ? 'Отправляем…' : activePending ? active?.status === 'queued' ? 'Расчёт в очереди' : 'Расчёт выполняется' : pendingRequest ? 'Повторить отправку' : 'Рассчитать';

  return <>
    <section id="workspace">
      <div className="page-heading"><div><p className="eyebrow">Мультимодальная перевозка</p><h1>Учебные расчётные примеры S0–S3</h1><p className="route-line">Московский узел → Мурманск → СМП → Петропавловск-Камчатский → получатель</p></div><div className="shipment-summary"><b>4 партии</b><span>Сухой неопасный контейнерный груз</span></div></div>
      <section className="scenario-panel" aria-labelledby="scenario-title">
        <h2 id="scenario-title" className="sr-only">Исходные условия</h2>
        <form onSubmit={submit}>
          <div className="scenario-input"><label htmlFor="scenario">Учебный сценарий</label><select id="scenario" value={scenario} disabled={interactionLocked || catalogLoading || available.length === 0} onChange={event => {
            if (posting.current || pendingRef.current || resolvingRef.current) return;
            clearSelection(); setScenario(event.target.value as ScenarioID); setSubmitError(null); setSubmitNotice(null);
          }}>{(Object.keys(SCENARIOS) as ScenarioID[]).map(id => <option key={id} value={id}>{id} · {SCENARIOS[id].name}</option>)}</select></div>
          <p className="scenario-description">{SCENARIOS[scenario].description}</p>
          <button className="primary calculate-button" type="submit" disabled={readOnly || submitting || resolving || (!pendingRequest && (catalogLoading || Boolean(catalogError) || !available.includes(scenario))) || activePending}>{readOnly ? 'Только просмотр' : buttonLabel}</button>
        </form>
        {catalogLoading && <p className="control-note" role="status">Проверяем доступность сценариев…</p>}
        {catalogError && <div className="inline-error" role="alert"><p>{catalogError}</p><button type="button" onClick={() => void refreshCatalog()}>Повторить подключение</button></div>}
        {submitError && <div className="inline-error" role="alert"><p>{submitError}</p>{pendingRequest && <><p className="fine">Исход операции неизвестен: сервер мог сохранить запуск. Повторная отправка использует прежние условия и ключ запроса. Выбор сценария, истории и переход в другой раздел временно недоступны.</p><button type="button" disabled={submitting || resolving} onClick={() => void resolvePending()}>{resolving ? 'Обновляем историю…' : 'Закрыть повтор и обновить'}</button><p className="fine">Повтор закроется только после успешного обновления истории. Прежний запуск при этом не отменяется.</p></>}</div>}
        {submitNotice && <p className="control-note" role="status">{submitNotice}</p>}
        {!catalogLoading && !catalogError && <p className="control-note">Эти примеры используют фиксированную базу. Рабочий пересчёт от принятой версии находится в разделе «Согласование версий».</p>}
      </section>
      <p className="sr-only" role="status" aria-live="polite" aria-atomic="true">{liveText}</p>
      <div className="workspace-grid">
        <section className="result-panel" aria-labelledby="result-title" aria-busy={recordLoading || activePending}>
          <div className="section-heading"><h2 id="result-title" tabIndex={-1} ref={resultHeading}>Результат расчёта</h2>{active && <StatusBadge status={active.status} />}</div>
          {viewError && <div className="inline-error result-error" role="alert"><p>{viewError}</p><button type="button" onClick={() => { context.current += 1; seed.current = active; setViewError(null); setRecordLoading(!active); setDetailRevision(value => value + 1); }}>Обновить результат</button></div>}
          {!active && <div className="result-state empty-state"><span className="empty-rule" aria-hidden="true" /><h3>{submitting ? 'Отправляем исходные условия' : recordLoading ? 'Загружаем сохранённый расчёт' : 'Выберите сценарий и запустите расчёт'}</h3><p>Здесь появятся сроки четырёх партий и основания выбора. Старый результат не подставляется в новый запуск.</p></div>}
          {active && <>
            <div className="calculation-context"><span><b>{active.scenario_id} · {SCENARIOS[active.scenario_id].name}</b><small>{active.scenario_id === 'S0' ? 'Первый план без принятой базы' : 'Пересчёт относительно фиксированной учебной базы'}</small></span><span className="timestamp">Запуск · {date(active.created_at)} мск</span></div>
            <ResultContent calculation={active} />
            <details className="source-details"><summary>Основание расчёта</summary><dl><dt>Источник</dt><dd>Учебные условия {active.scenario_id}, сохранённые с этим запуском</dd><dt>Исходный снимок</dt><dd>{active.snapshot_id}</dd><dt>Единицы времени</dt><dd>Целые условные шаги, начало отсчёта t0 = 0</dd><dt>Область вывода</dt><dd>Одна учебная цепочка и фиксированные рейсы A/B. Пример не привязан к текущей принятой версии и не создаёт новый цикл согласования.</dd></dl></details>
          </>}
        </section>
        <aside className="history-panel" id="history" aria-labelledby="history-title">
          <div className="section-heading"><h2 id="history-title">История запусков</h2><button className="quiet-button" type="button" onClick={() => void refreshHistory()} disabled={historyLoading || interactionLocked} aria-label="Обновить историю">{historyLoading ? 'Загрузка…' : 'Обновить'}</button></div>
          <p className="history-description">Последние 20 запусков. Результаты сохраняются после обновления страницы.</p>
          {historyError && <p className="history-error" role="alert">{historyError}</p>}
          {history.length === 0 && <div className="history-empty"><p>{historyLoading ? 'Загружаем историю…' : historyError ? 'История пока недоступна.' : 'Запусков пока нет.'}</p><span>Начните с выбранного учебного сценария.</span></div>}
          {history.length > 0 && <ul className="history-list">{history.map(record => <li key={record.id}><button type="button" className={record.id === activeId ? 'history-item selected' : 'history-item'} disabled={interactionLocked} aria-pressed={record.id === activeId} onClick={() => { if (!posting.current && !pendingRef.current && !resolvingRef.current) showRecord(record); }}>
            <span className="history-scenario"><b>{record.scenario_id}</b>{SCENARIOS[record.scenario_id].name}</span><span className="history-date">{date(record.created_at)} мск</span><StatusBadge status={record.status} />
          </button></li>)}</ul>}
        </aside>
      </div>
      <footer>Расчёт рекомендации не является распоряжением на перевозку. Фактическое движение не выполняется. Учебные ответы сторон не заменяют реальные полномочия и согласия.</footer>
    </section>
  </>;
}
