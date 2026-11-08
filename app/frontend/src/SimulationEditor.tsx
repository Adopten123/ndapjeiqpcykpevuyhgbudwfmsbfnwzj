import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ServiceError } from './api';
import { eventNames, simulationHorizon } from './simulation';
import type { EventInput, EventType, Run, SimulationEvent } from './simulation';
import { targetLabel } from './simulation-route';
import { eventTitle, eventWindow } from './SimulationTimeline';

type Save = (input: EventInput, id?: string) => Promise<void>;
type Field = 'available_tick' | 'target_id' | 'type' | 'reason' | 'from_tick' | 'until_tick' | 'load_from_tick' | 'load_until_tick' | 'departure_tick';
const numericFields = {
  rail_edge_closed: [['from_tick', 'Начало ремонта'], ['until_tick', 'Окончание ремонта']],
  sea_edge_closed: [['from_tick', 'Начало закрытия'], ['until_tick', 'Окончание закрытия']],
  vessel_schedule_changed: [['load_from_tick', 'Начало окна погрузки'], ['load_until_tick', 'Конец окна погрузки'], ['departure_tick', 'Шаг выхода судна']],
} as const;

function EventForm({ run, event, busy, onSave, onCancel }: { run: Run; event: SimulationEvent | null; busy: boolean; onSave: Save; onCancel: () => void }) {
  const [type, setType] = useState<EventType>(event?.type ?? 'rail_edge_closed');
  const [issue, setIssue] = useState<{ text: string; field?: string } | null>(null);
  const form = useRef<HTMLFormElement>(null);
  const fail = (text: string, field?: string) => setIssue({ text, field });
  useEffect(() => {
    if (!busy && issue?.field) (form.current?.elements.namedItem(issue.field) as HTMLElement | null)?.focus();
  }, [busy, issue]);
  const submit = async (submission: FormEvent<HTMLFormElement>) => {
    submission.preventDefault();
    const fields = new FormData(submission.currentTarget);
    const available = Number(fields.get('available_tick'));
    const parameters = Object.fromEntries(numericFields[type].map(([key]) => [key, Number(fields.get(key))]));
    if (available <= run.tick) return fail('Выберите шаг после текущего времени прогона.', 'available_tick');
    if (type === 'vessel_schedule_changed') {
      if (parameters.load_from_tick >= parameters.load_until_tick) return fail('Конец окна должен быть позже его начала.', 'load_until_tick');
      if (parameters.load_until_tick > parameters.departure_tick) return fail('Выход судна должен быть после окончания погрузки или на той же границе.', 'departure_tick');
    } else if (parameters.from_tick >= parameters.until_tick) return fail('Конец ограничения должен быть позже его начала.', 'until_tick');
    const reason = String(fields.get('reason')).trim();
    if (!reason) return fail('Укажите причину события.', 'reason');
    setIssue(null);
    try { await onSave({ type, target_id: String(fields.get('target_id')), available_tick: available, reason, parameters }, event?.event_id); }
    catch (error) {
      if (error instanceof ServiceError) {
        const field = error.field?.split('.').at(-1);
        fail(error.message, field && form.current?.elements.namedItem(field) ? field : undefined);
      } else fail('Не удалось сохранить событие.');
    }
  };
  const validation = (field: Field) => ({ 'aria-invalid': issue?.field === field || undefined,
    'aria-describedby': issue?.field === field ? `sim-error-${field}` : undefined });
  const feedback = (field: Field) => issue?.field === field && <span className="sim-field-error" id={`sim-error-${field}`} role="alert">{issue.text}</span>;
  const choices = run.targets.filter(target => target.type === type);
  return <form className="sim-event-form" ref={form} onSubmit={submission => void submit(submission)}>
    <h3>{event ? 'Редактирование события' : 'Добавить событие'}</h3>
    <div className="sim-field-grid">
      <label className="sim-field">Тип события<select name="type" disabled={busy} value={type} {...validation('type')} onChange={change => { setType(change.currentTarget.value as EventType); setIssue(null); }}>
        {Object.entries(eventNames).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
      </select>{feedback('type')}</label>
      <div className="sim-field"><label htmlFor="sim-target">{type === 'vessel_schedule_changed' ? 'Рейс' : 'Участок'}</label>
        <select id="sim-target" key={type} name="target_id" required disabled={busy} defaultValue={event?.type === type ? event.target_id : ''} {...validation('target_id')}>
          <option value="">Выберите объект</option>{choices.map(target => <option key={target.id} value={target.id}>{targetLabel(target)}</option>)}
        </select>{feedback('target_id')}</div>
      <label className="sim-field">Шаг поступления<input name="available_tick" type="number" min={run.tick + 1} max={simulationHorizon} step={1} required disabled={busy}
        defaultValue={event?.available_tick ?? run.tick + 1} {...validation('available_tick')} />{feedback('available_tick')}</label>
      {numericFields[type].map(([field, label]) => <label className="sim-field" key={field}>{label}
        <input name={field} type="number" min={0} max={simulationHorizon} step={1} required disabled={busy} defaultValue={event?.parameters?.[field]} {...validation(field)} />
        {feedback(field)}</label>)}
      <label className="sim-field sim-reason">Причина<input name="reason" required maxLength={240} disabled={busy} defaultValue={event?.reason ?? ''} {...validation('reason')} />{feedback('reason')}</label>
    </div>
    <p className="muted">Начало интервала включается, конец исключается. В журнале событие появится на шаге поступления.</p>
    {issue && !issue.field && <p className="sim-field-error" role="alert">{issue.text}</p>}
    <div className="sim-controls"><button className="primary" type="submit" disabled={busy}>{event ? 'Сохранить изменения' : 'Добавить будущее событие'}</button>
      {event && <button type="button" disabled={busy} onClick={onCancel}>Отменить редактирование</button>}</div>
  </form>;
}

export function SimulationEditor({ run, busy, onClose, onSave, onDelete }: {
  run: Run; busy: boolean; onClose: () => void; onSave: Save; onDelete: (id: string) => Promise<void>;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const [formVersion, setFormVersion] = useState(0);
  const [actionError, setActionError] = useState<string | null>(null);
  const future = run.manual_events.filter(event => event.available_tick > run.tick);
  const event = future.find(item => item.event_id === editing) ?? null;
  const dialog = useRef<HTMLDialogElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const formStart = useRef<HTMLDivElement>(null);
  const pendingTitle = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    if (dialog.current && !dialog.current.open) {
      dialog.current.showModal();
      title.current?.focus();
    }
  }, []);

  const edit = (id: string) => {
    setActionError(null);
    setEditing(id);
    formStart.current?.focus();
  };
  const save: Save = async (input, id) => {
    setActionError(null);
    await onSave(input, id);
    setEditing(null);
    setFormVersion(value => value + 1);
    formStart.current?.focus();
  };
  const remove = async (id: string) => {
    setActionError(null);
    try {
      await onDelete(id);
      if (editing === id) setEditing(null);
      pendingTitle.current?.focus();
    } catch (error) {
      setActionError(error instanceof ServiceError ? error.message : 'Не удалось отменить событие.');
    }
  };
  const operations = run.mode === 'operations';
  return <dialog ref={dialog} className="sim-incident-dialog" aria-labelledby="sim-editor-title" onClose={onClose}
    onCancel={event => { if (busy) event.preventDefault(); }}>
    <div className="sim-section-heading"><div><h2 id="sim-editor-title" ref={title} tabIndex={-1}>{operations ? 'Ручной ввод сбоев' : 'Настройка сценария'}</h2>
      <p>{operations ? 'Автоход на паузе. Ручные события учитываются вместе с сообщениями имитаторов.' : 'Будущие ручные события этого прогона'}</p></div>
      <button type="button" disabled={busy} onClick={() => dialog.current?.close()}>Закрыть</button></div>
    <div ref={formStart} className="sim-editor-form" tabIndex={-1}>{run.status === 'completed' || run.tick >= simulationHorizon
      ? <p className="muted">Прогон завершён. Для нового сценария создайте отдельный прогон.</p>
      : <EventForm key={`${event?.event_id ?? 'new'}/${formVersion}`} run={run} event={event} busy={busy} onSave={save} onCancel={() => setEditing(null)} />}</div>
    <section className="sim-incident-pending" aria-labelledby="sim-pending-events">
      <h3 id="sim-pending-events" ref={pendingTitle} tabIndex={-1}>Запланированные события <span className="muted" aria-live="polite">· {future.length}</span></h3>
      {actionError && <p className="sim-incident-error sim-field-error" role="alert">{actionError}</p>}
      {future.length ? <ul className="sim-scheduled-list">{future.map(item => <li key={item.event_id}>
        <div><h4>{eventTitle(item, run)}</h4><p>Поступит на шаге {item.available_tick} · {eventWindow(item)}</p>{item.reason && <p className="muted">{item.reason}</p>}</div>
        <div className="sim-controls"><button type="button" disabled={busy} onClick={() => edit(item.event_id)} aria-label={`Изменить ${eventTitle(item, run)}, шаг ${item.available_tick}`}>Изменить</button>
          <button type="button" disabled={busy} onClick={() => void remove(item.event_id)} aria-label={`Отменить событие ${eventTitle(item, run)}, шаг ${item.available_tick}`}>Отменить событие</button></div>
      </li>)}</ul> : <p className="sim-section-empty">Запланированных ручных событий пока нет.</p>}
    </section>
  </dialog>;
}
