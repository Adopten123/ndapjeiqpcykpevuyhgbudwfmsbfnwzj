import { useEffect, useRef, useState } from 'react';
import {
  canApply, canAutoAdvance, canStep, cargoNames, cargoProfileDescription, createRun, defaultRouteId,
  modeNames, removeEvent, routeNames, runAction, runStatus, runStepValue, saveEvent, scenarioNames, simulationHorizon,
  transportWorkspaceTitle,
} from './simulation';
import type { Run } from './simulation';
import { SimulationEditor } from './SimulationEditor';
import { RunCreation, RunRegistry } from './SimulationRunChooser';
import { eventTitle } from './simulation-presentation';
import { SimulationComparison } from './SimulationComparison';
import { SimulationJournal } from './SimulationJournal';
import { SimulationParties } from './SimulationParties';
import { SimulationTimeline } from './SimulationTimeline';
import { loadingResult, openRun, useSimulationRun } from './useSimulationRun';
import './simulation.css';

const views = { overview: 'Обзор перевозки', parties: 'Партии', events: 'События', plans: 'Планы' } as const;
type View = keyof typeof views;

function focusSection(id: string) {
  const heading = document.getElementById(id);
  heading?.focus({ preventScroll: true });
  heading?.scrollIntoView({ block: 'start' });
}

function nextTick(run: Run) {
  const operations = run.active_plan.scheduled_operations.flatMap(operation => [operation.start_tick, operation.end_tick]);
  const manual = run.manual_events.map(event => event.available_tick);
  const restrictions = run.events.filter(event => event.status === 'applied' || event.status === 'accepted').flatMap(event =>
    [event.parameters?.from_tick, event.parameters?.until_tick, event.parameters?.load_from_tick, event.parameters?.load_until_tick, event.parameters?.departure_tick]);
  const possible = [run.source.next_tick, ...operations, ...manual, ...restrictions, simulationHorizon]
    .filter((value): value is number => typeof value === 'number' && value > run.tick && value <= simulationHorizon);
  return possible.length ? Math.min(...possible) : null;
}

function RunControls({ run, busy, playing, view, onView, onBack, onCopy, onPlayback, onStep, onRetry, onManualEvent }: {
  run: Run; busy: boolean; playing: boolean; view: View; onView: (view: View) => void; onBack: () => void; onCopy: () => void;
  onPlayback: () => void; onStep: () => void; onRetry: () => void;
  onManualEvent: (trigger: HTMLButtonElement) => void;
}) {
  const ready = canStep(run);
  const limitReached = run.status === 'ready' && !ready;
  const operations = run.mode === 'operations';
  const next = ready ? nextTick(run) : null;
  const lastDeliveryTick = Math.max(...run.active_plan.party_plans.map(party => party.recipient_receipt_tick));
  const showPlannedDelivery = run.status !== 'completed' || lastDeliveryTick !== run.tick;
  const applicable = canApply(run);
  const pending = loadingResult(run);
  const sourceError = run.status === 'source_error';
  const canRetry = run.status === 'unknown' || run.status === 'compute_error';
  const detail = run.message || run.candidate?.message;
  const playbackLabel = playing ? 'Пауза' : run.tick > 0 ? 'Продолжить симуляцию' : 'Запустить симуляцию';
  let status = runStatus(run);
  if (limitReached) status = 'Достигнут предел прогона';
  else if (operations && ready) status = playing ? 'Симуляция идёт' : run.tick > 0 ? 'Симуляция на паузе' : 'Готово к запуску';
  const cargoType = cargoNames[run.cargo.id];
  const lastEvent = run.events.at(-1);
  const affected = run.active_plan.party_plans.flatMap(plan => {
    const proposed = run.candidate?.plan?.party_plans.find(item => item.party_id === plan.party_id);
    return proposed && proposed.recipient_receipt_tick !== plan.recipient_receipt_tick ? [plan.party_id] : [];
  });
  let action;
  if (applicable || pending) {
    if (view !== 'plans') action = <button className="primary" type="button" onClick={() => onView('plans')}>
      {applicable ? 'Посмотреть изменения' : 'Посмотреть расчёт'}</button>;
  } else if (run.status === 'infeasible' || run.status === 'conflict') {
    if (view !== 'events') action = <button className="primary" type="button" onClick={() => onView('events')}>
      {run.status === 'infeasible' ? 'Посмотреть ограничения' : 'Посмотреть событие'}</button>;
  } else if (sourceError || canRetry) {
    action = <button className="primary" type="button" disabled={busy} onClick={sourceError ? onStep : onRetry}>
      {sourceError ? 'Повторить получение ленты' : 'Повторить расчёт'}</button>;
  } else if (limitReached || run.status === 'completed') {
    action = <button type="button" disabled={busy} onClick={onBack}>К прогонам</button>;
  } else if (ready) {
    action = <button className="primary" type="button" disabled={!playing && (busy || (operations && !canAutoAdvance(run)))}
      onClick={operations ? onPlayback : onStep}>{operations ? playbackLabel : 'Следующий шаг'}</button>;
  }
  return <>
    <nav className="sim-breadcrumb" aria-label="Путь"><button className="sim-text-button" type="button" disabled={busy} onClick={onBack}>Все прогоны</button>
      <span aria-hidden="true">/</span><span>Текущий прогон</span></nav>
    <header className="page-heading sim-run-heading">
      <div><p className="eyebrow">{transportWorkspaceTitle}</p><h1>{run.name}</h1><p className="route-line">{scenarioNames[run.scenario]} · {run.parties.length} {run.parties.length === 1 ? 'партия' : 'партии'}</p></div>
      <div className="sim-controls">
        <button type="button" disabled={busy} onClick={onCopy}>Копировать в учебный прогон</button>
        <button type="button" aria-haspopup="dialog" disabled={busy || run.status === 'completed' || run.tick >= simulationHorizon}
          onClick={event => onManualEvent(event.currentTarget)}>Задать сбой</button>
      </div>
    </header>
    <section className="sim-panel sim-controls-panel" aria-label="Управление прогоном">
      <div className="sim-cargo-summary"><strong>{routeNames[run.route.id]}</strong>
        <p>{run.cargo.name} · {run.cargo.name !== cargoType && `${cargoType} · `}{cargoProfileDescription}</p></div>
      <dl className="sim-run-facts">
        <div><dt>Текущий момент</dt><dd>{run.status === 'completed' ? runStepValue(run) : `Шаг ${run.tick}`}</dd></div>
        <div><dt>Режим</dt><dd>{modeNames[run.mode]}</dd></div>
        <div><dt>Действующий план</dt><dd>Версия {run.plan_version}</dd></div>
        {showPlannedDelivery && <div><dt>Последняя выдача по плану</dt><dd>Шаг {lastDeliveryTick}</dd></div>}
      </dl>
    </section>
    <div className={`sim-status sim-status-${limitReached ? 'unknown' : run.status}`} role="status" aria-live="polite">
      <div>
        <h3>{status}</h3>
        {detail && !applicable && <p>{detail}</p>}
        {(applicable || pending) && lastEvent && <p>{eventTitle(lastEvent, run)}</p>}
        {pending && <p>Редакция условий {run.condition_revision}. Новые операции ждут решения.</p>}
        {applicable && <p>{affected.length > 0 ? `Сдвинуты сроки выдачи · ${affected.join(', ')}.` : 'Сроки выдачи сохраняются.'} Прогон ждёт применения кандидата.</p>}
        {run.status === 'infeasible' && <p>Прогон остановлен на шаге {run.tick}. Действующий план сохранён для сравнения.</p>}
        {sourceError && <p>Проверьте учебный источник и повторите получение ленты.</p>}
        {next != null && <p>Ближайшее изменение · шаг {next}</p>}
        {limitReached && <p>Достигнута граница учебной модели на шаге {simulationHorizon}.</p>}
        {run.manual_events.length > 0 && <p>Ручные события в очереди · {run.manual_events.length}</p>}
      </div>
      {action}
    </div>
  </>;
}

export default function SimulationWorkspace({ runId, onInteractionLock }: { runId?: string; onInteractionLock: (locked: boolean) => void }) {
  const { current, runs, registryState, registryError, error, setError, busy, playing, setPlaying,
    pause, change, step, refresh } = useSimulationRun(runId, onInteractionLock);
  const [creation, setCreation] = useState<Run | 'new' | null>(null);
  const [view, setView] = useState<View>('overview');
  const [partyId, setPartyId] = useState('');
  const [editorRunId, setEditorRunId] = useState<string | null>(null);
  const editorTrigger = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    setEditorRunId(null); setCreation(null); setView('overview'); setPartyId('');
  }, [runId]);

  const goToRun = (id: string) => { pause(); setError(null); openRun(id); };
  const showView = (next: View) => {
    setView(next);
    requestAnimationFrame(() => {
      if (next === 'plans') focusSection('sim-compare-title');
      else document.getElementById(`sim-view-${next}`)?.focus({ preventScroll: true });
    });
  };
  const lastEvent = current?.events.at(-1);
  const visibleError = error ?? (!runId && !creation ? registryError : null);
  return <div className="sim-workspace">
    {visibleError && <div className="inline-error sim-workspace-error" role="alert"><p>{visibleError}</p>
      <button type="button" disabled={busy} onClick={() => { setError(null); refresh(); }}>Обновить данные</button></div>}
    <div hidden={Boolean(runId || creation)}>
      <header className="page-heading"><div><p className="eyebrow">Рабочее место координатора</p><h1>{transportWorkspaceTitle}</h1>
        <p className="route-line">{routeNames[defaultRouteId]}</p></div></header>
      <RunRegistry runs={runs} busy={busy} loading={registryState === 'loading'} failed={registryState === 'failed'}
        onOpen={goToRun} onNew={() => { setError(null); setCreation('new'); }} />
    </div>
    {creation && <RunCreation current={creation === 'new' ? null : creation} busy={busy}
      onBack={() => { setError(null); setCreation(null); }} onCreate={input => change(() => createRun(input))} />}
    {!current && runId && !error && <p className="sim-panel sim-section-empty" role="status">Загружается сохранённый прогон…</p>}
    {!current && runId && error && <button type="button" onClick={() => goToRun('')}>К прогонам</button>}
    {current && !creation && <>
      <RunControls run={current} busy={busy} playing={playing} view={view} onView={showView} onStep={step}
        onBack={() => goToRun('')} onCopy={() => { pause(); setCreation(current); }}
        onPlayback={() => { if (playing) pause(); else { setError(null); setPlaying(true); } }}
        onRetry={() => void change(() => runAction(current.id, 'retry', { state_version: current.state_version }))}
        onManualEvent={trigger => { pause(); editorTrigger.current = trigger; setEditorRunId(current.id); }} />
      <nav className="sim-view-nav" aria-label="Виды прогона">{(Object.entries(views) as [View, string][]).map(([id, label]) =>
        <button key={id} id={`sim-view-${id}`} type="button" aria-current={view === id ? 'page' : undefined}
          onClick={() => showView(id)}>{label}</button>)}</nav>
      {(view === 'overview' || view === 'parties') && <>
        <SimulationParties run={current} selectedId={partyId} onSelect={setPartyId} overview={view === 'overview'} />
        <div className="sim-last-event">
          {lastEvent ? <><strong>Последнее событие</strong> · {eventTitle(lastEvent, current)} · шаг {lastEvent.received_tick ?? lastEvent.available_tick ?? '—'} · {lastEvent.source === 'manual' ? 'Введено вручную' : 'Сообщение имитатора'}</>
            : 'События ещё не поступили.'}
          {lastEvent && <button className="sim-text-button" type="button" onClick={() => showView('events')}>Посмотреть событие</button>}
        </div>
      </>}
      {view === 'events' && <SimulationJournal run={current} />}
      {view === 'plans' && <>
        <SimulationComparison run={current} />
        {canApply(current) && current.candidate && <div className="sim-apply-row">
          <button className="primary" type="button" disabled={busy} onClick={() => {
            const candidateId = current.candidate?.id;
            if (candidateId) void change(() => runAction(current.id, 'apply', { candidate_id: candidateId }));
          }}>Применить кандидат</button>
        </div>}
        <SimulationTimeline run={current} />
      </>}
      {editorRunId === current.id && <SimulationEditor key={current.id} run={current} busy={busy}
        onClose={() => { setEditorRunId(null); editorTrigger.current?.focus({ preventScroll: true }); }}
        onSave={(input, id) => { pause(); return change(() => saveEvent(current.id, input, id), true); }}
        onDelete={id => { pause(); return change(() => removeEvent(current.id, id), true); }} />}
    </>}
  </div>;
}
