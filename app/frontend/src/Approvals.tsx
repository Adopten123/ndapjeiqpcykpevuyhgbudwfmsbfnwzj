import { useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ServiceError } from './api';
import type { Calculation } from './types';
import { getPacket, getPackets, mutateApproval } from './workflow';
import type { ApprovalContext, Decision, Packet, PacketEnvelope, Subject, User } from './workflow';
import PlanningPanel from './PlanningPanel';
import { canPreparePacket } from './planning';
import type { PlanningContext } from './planning';
import { replaceRoute } from './routing';

const dates = new Intl.DateTimeFormat('ru-RU', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'Europe/Moscow' });
const date = (value: string) => dates.format(new Date(value));
const message = (error: unknown) => error instanceof ServiceError ? error.message : 'Не удалось получить данные согласования. Повторите позже.';
const statusNames: Record<Packet['status'], string> = { pending: 'Сбор ответов', accepted: 'Учебная версия принята', superseded: 'Заменена новой версией', stale: 'Условия устарели', review_required: 'Нужна повторная проверка' };
const decisionNames: Record<Decision, string> = { agree: 'Согласовано без условий', rejected: 'Отклонено', conditional: 'Согласовано с условиями', revoked: 'Ответ отозван' };
type Operation = { key: string; path: string; body: Record<string, unknown>; label: string; kind: 'create' | 'response' | 'accept' };

function ResponseState({ subject, now }: { subject: Subject; now: number }) {
  const response = subject.latest_response;
  if (!response) return <span className="subject-state waiting">Ответа пока нет</span>;
  const expired = Date.parse(response.expires_at) <= now;
  return <span className={`subject-state ${expired || response.decision !== 'agree' ? 'attention' : 'confirmed'}`}>
    {decisionNames[response.decision]}{expired ? ' · срок истёк' : ''}<small>До {date(response.expires_at)} мск</small>
  </span>;
}

function ParameterTable({ subject }: { subject: Subject }) {
  const rows = (key: string) => Array.isArray(subject.details[key]) ? (subject.details[key] as unknown[]).filter((row): row is Record<string, unknown> => typeof row === 'object' && row !== null && !Array.isArray(row)) : [];
  const operations = rows('operations'); const reservations = rows('reservations'); const plans = rows('party_plans'); const services = rows('services'); const resources = rows('resources'); const receipts = rows('receipt_constraints');
  const cell = (value: unknown) => typeof value === 'string' || typeof value === 'number' ? value : '—';
  const kind: Record<string, string> = { move: 'Движение', load: 'Погрузка', unload: 'Разгрузка', rail: 'Железная дорога', sea: 'Море', wait: 'Ожидание', last_mile: 'Последняя миля', transfer: 'Перевалка' };
  const location: Record<string, string> = { S: 'Отправитель', O: 'Станция O', B: 'Участок B', C: 'Участок C', M: 'Мурманск', U: 'Участок U', V: 'Участок V', D: 'Порт назначения', R: 'Получатель' };
  const resource: Record<string, string> = { 'O-WAIT': 'Ожидание на станции', 'M-YARD': 'Площадка Мурманска', 'M-CRANE': 'Погрузка в Мурманске', 'D-UNLOAD': 'Разгрузка в порту назначения', 'LAST-MILE': 'Доставка получателю', ACCESS: 'Выпуск у отправителя' };
  const record = (value: unknown): Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {};
  return <details className="subject-details"><summary>Условия предмета</summary>
    <p className="muted">Время — условные шаги от t0 = 0. Это параметры сохранённого учебного маршрута.</p>
    {operations.length > 0 && <div className="table-scroll" tabIndex={0} aria-label="Операции предмета"><table className="condition-table"><caption>Операции</caption><thead><tr><th>Партия</th><th>Участок</th><th>Действие</th><th>Начало</th><th>Конец</th></tr></thead><tbody>{operations.map((row, i) => <tr key={i}><td>{cell(row.party_id)}</td><td>{location[String(row.from_location_id)] ?? cell(row.from_location_id)} → {location[String(row.to_location_id)] ?? cell(row.to_location_id)}</td><td>{kind[String(row.kind)] ?? 'Операция'}</td><td>{cell(row.start_tick)}</td><td>{cell(row.end_tick)}</td></tr>)}</tbody></table></div>}
    {reservations.length > 0 && <div className="table-scroll" tabIndex={0} aria-label="Ресурсы предмета"><table className="condition-table"><caption>Занятие ресурсов · места для партий</caption><thead><tr><th>Партия</th><th>Ресурс</th><th>С</th><th>До</th><th>Объём</th><th>Освобождение</th></tr></thead><tbody>{reservations.map((row, i) => <tr key={i}><td>{cell(row.party_id)}</td><td>{resource[String(row.resource_id)] ?? cell(row.resource_id)}</td><td>{cell(row.start_tick)}</td><td>{cell(row.end_tick)}{row.empty_interval === true ? ' · пустой интервал' : ''}</td><td>{cell(row.amount)}</td><td>{cell(row.release_lag_tick)} шаг.</td></tr>)}</tbody></table></div>}
    {services.map((row, i) => <p className="parameter-note" key={i}>Рейс {cell(row.id)}: погрузка с {cell(record(row.load_window).start_tick)} до {cell(record(row.load_window).end_tick)}, отправление {cell(row.departure_tick)}, вместимость {cell(row.capacity)} мест для партий.</p>)}
    {resources.map((row, i) => <p className="parameter-note" key={i}>{resource[String(row.id)] ?? cell(row.id)}: {(Array.isArray(row.calendar) ? row.calendar : []).map((slot: unknown) => { const value = record(slot); return `${cell(value.start_tick)}–${cell(value.end_tick)}: ${cell(value.capacity)} мест`; }).join('; ')}. Остаточная доступность учебной модели.</p>)}
    {plans.length > 0 && <div className="table-scroll" tabIndex={0} aria-label="Сроки предмета"><table className="condition-table"><caption>Сроки партий</caption><thead><tr><th>Партия</th><th>Рейс</th><th>Приём в порту</th><th>У получателя</th></tr></thead><tbody>{plans.map((row, i) => <tr key={i}><td>{cell(row.party_id)}</td><td>{cell(row.service_id)}</td><td>{cell(row.port_receipt_tick)}</td><td>{cell(row.recipient_receipt_tick)}</td></tr>)}</tbody></table></div>}
    {receipts.map((row, i) => <p className="parameter-note" key={i}>Окна приёма {cell(row.party_id)}: {(Array.isArray(row.windows) ? row.windows : []).map((window: unknown) => { const value = record(window); return `${cell(value.start_tick)}–${cell(value.end_tick)}`; }).join('; ')} условных шагов.</p>)}
    {operations.length === 0 && reservations.length === 0 && plans.length === 0 && <p>Предмет относится к условиям доступа или приёма груза, указанным в заголовке. Сервер проверяет его привязку к версии.</p>}
    <dl className="compact-meta"><dt>Представляющая сторона</dt><dd>{subject.organization_id}</dd><dt>Редакция ответа</dt><dd>{subject.revision}</dd></dl>
  </details>;
}

function ResponseForm({ subject, user, disabled, onSubmit }: { subject: Subject; user: User; disabled: boolean; onSubmit: (subject: Subject, decision: Decision, expires: string, comment: string) => void }) {
  const [decision, setDecision] = useState<Decision | ''>('');
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    const time = new Date(String(data.get('expires')));
    if (!Number.isFinite(time.getTime()) || time.getTime() <= Date.now() || time.getTime() > Date.parse(user.valid_until)) {
      const field = event.currentTarget.elements.namedItem('expires') as HTMLInputElement;
      field.setCustomValidity('Выберите будущий срок в пределах ваших полномочий.'); field.reportValidity(); return;
    }
    if (!decision) return;
    onSubmit(subject, decision, time.toISOString(), decision === 'agree' ? '' : String(data.get('comment') ?? '').trim());
  };
  const defaultDate = new Date(Math.min(Date.now() + 3600000, Date.parse(user.valid_until)));
  const local = (date: Date) => new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  return <form className="response-form" onSubmit={submit}>
    <label>Ваш учебный ответ<select name="decision" value={decision} required disabled={disabled} onChange={event => setDecision(event.target.value as Decision)}><option value="" disabled>Выберите решение</option>{Object.entries(decisionNames).map(([value, title]) => <option key={value} value={value}>{title}</option>)}</select></label>
    <label>Действует до (время устройства)<input type="datetime-local" name="expires" required defaultValue={local(defaultDate)} max={local(new Date(user.valid_until))} disabled={disabled} onInput={event => event.currentTarget.setCustomValidity('')} /></label>
    <label className="comment-label">Комментарий<input name="comment" type="text" maxLength={1000} placeholder={decision === 'agree' ? 'Безусловный ответ не содержит встречных условий' : 'Условия, причина или пояснение'} disabled={disabled || !decision || decision === 'agree'} /></label>
    <p className="response-note">Для встречных условий выберите «Согласовано с условиями». Ответ относится только к этому предмету и версии; срок ограничен вашими полномочиями.</p>
    <button type="submit" disabled={disabled}>Сохранить мой ответ</button>
  </form>;
}

export default function Approvals({ user, routePacketId, routeCalculationId, onInteractionLock }: { user: User; routePacketId: string | null; routeCalculationId: string | null; onInteractionLock: (locked: boolean) => void }) {
  const [source, setSource] = useState<Calculation | null>(null);
  const [planning, setPlanning] = useState<PlanningContext | null>(null);
  const [planningLocked, setPlanningLocked] = useState(false);
  const [packets, setPackets] = useState<Packet[]>([]);
  const [context, setContext] = useState<ApprovalContext | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(routePacketId);
  const [packet, setPacket] = useState<Packet | null>(null);
  const [listLoading, setListLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [resolving, setResolving] = useState(false);
  const [pending, setPending] = useState<Operation | null>(null);
  const [revision, setRevision] = useState(0);
  const [now, setNow] = useState(Date.now());
  const mounted = useRef(false); const posting = useRef(false); const epoch = useRef(0);
  const listController = useRef<AbortController | null>(null); const detailController = useRef<AbortController | null>(null); const postController = useRef<AbortController | null>(null);
  const pendingRef = useRef<Operation | null>(null);
  const resolvingRef = useRef(false);
  const routePacket = useRef(routePacketId); const previousRoutePacket = useRef(routePacketId);
  routePacket.current = routePacketId;
  const coordinator = user.role === 'coordinator';
  useEffect(() => { onInteractionLock(busy || resolving || Boolean(pending) || planningLocked); return () => onInteractionLock(false); }, [busy, resolving, pending, planningLocked, onInteractionLock]);
  const syncContext = useCallback((incoming: ApprovalContext) => setContext(previous => {
    if (!previous || incoming.source_revision > previous.source_revision || (incoming.source_revision === previous.source_revision && incoming.approval_revision >= previous.approval_revision)) return incoming;
    return previous;
  }), []);
  const refresh = useCallback(async () => {
    if (posting.current) return false;
    listController.current?.abort(); const controller = new AbortController(); listController.current = controller;
    setListLoading(true);
    try {
      const value = await getPackets(controller.signal);
      if (!mounted.current || controller.signal.aborted) return false;
      syncContext(value.context); setPackets(value.packets); setListError(null);
      if (!pendingRef.current) setSelectedId(previous => routePacket.current ?? previous ?? value.context.active_packet_id ?? value.packets[0]?.id ?? null);
      return true;
    } catch (e) {
      if (mounted.current && !controller.signal.aborted) {
        setListError(message(e));
        if (e instanceof ServiceError && e.status === 403) { setPackets([]); setPacket(null); }
      }
    }
    finally { if (mounted.current && !controller.signal.aborted) setListLoading(false); }
    return false;
  }, [syncContext]);
  const planningContext = useCallback((value: PlanningContext) => { setPlanning(value); syncContext(value.context); }, [syncContext]);
  const planningChanged = useCallback(() => { void refresh(); setRevision(value => value + 1); }, [refresh]);
  useEffect(() => {
    mounted.current = true; void refresh();
    const clock = window.setInterval(() => setNow(Date.now()), 1000);
    return () => { mounted.current = false; epoch.current += 1; listController.current?.abort(); detailController.current?.abort(); postController.current?.abort(); window.clearInterval(clock); };
  }, [refresh]);
  useEffect(() => {
    if (previousRoutePacket.current === routePacketId) return;
    previousRoutePacket.current = routePacketId;
    if (selectedId === routePacketId) return;
    epoch.current += 1; detailController.current?.abort();
    setSelectedId(routePacketId); setPacket(null); setDetailError(null); setActionError(null); setNotice(null); setDetailLoading(Boolean(routePacketId));
    if (!routePacketId) void refresh();
  }, [routePacketId, selectedId, refresh]);
  useEffect(() => {
    if (!selectedId || busy) return;
    const current = ++epoch.current;
    detailController.current?.abort(); const controller = new AbortController(); detailController.current = controller;
    setDetailLoading(true);
    const update = async () => {
      try {
        const value = await getPacket(selectedId, controller.signal);
        if (!mounted.current || controller.signal.aborted || current !== epoch.current || posting.current) return;
        syncContext(value.context); setPacket(value.packet); setDetailError(null);
      } catch (e) {
        if (mounted.current && !controller.signal.aborted && current === epoch.current) {
          setDetailError(message(e));
          if (e instanceof ServiceError && [403, 404].includes(e.status)) setPacket(null);
        }
      }
      finally { if (mounted.current && !controller.signal.aborted && current === epoch.current) setDetailLoading(false); }
    };
    void update();
    return () => { controller.abort(); };
  }, [selectedId, revision, busy, syncContext]);
  useEffect(() => {
    if (busy || pending) return;
    const timer = window.setTimeout(() => { void refresh(); setRevision(value => value + 1); }, 6000);
    return () => window.clearTimeout(timer);
  }, [packet, packets, busy, pending, refresh]);
  const select = (id: string) => {
    if (posting.current || resolvingRef.current || pendingRef.current || planningLocked) return;
    epoch.current += 1; detailController.current?.abort(); setSelectedId(id); setPacket(null); setDetailError(null); setActionError(null); setNotice(null); setRevision(value => value + 1);
    replaceRoute(`#approvals/${encodeURIComponent(id)}`);
  };
  const apply = (value: PacketEnvelope) => {
    syncContext(value.context);
    if (value.packet) {
      setPacket(value.packet); setSelectedId(value.packet.id);
      setPackets(previous => [value.packet!, ...previous.filter(item => item.id !== value.packet!.id)].sort((a, b) => b.version - a.version));
      replaceRoute(`#approvals/${encodeURIComponent(value.packet.id)}`);
    }
  };
  const perform = async (operation: Operation) => {
    if (posting.current || resolvingRef.current) return;
    const repeating = Boolean(pendingRef.current);
    operation = pendingRef.current ?? operation;
    posting.current = true; setBusy(true); onInteractionLock(true); setActionError(null); setNotice(null); setPending(operation); pendingRef.current = operation;
    epoch.current += 1; detailController.current?.abort(); listController.current?.abort();
    const controller = new AbortController(); postController.current = controller;
    try {
      const value = await mutateApproval(operation.path, operation.body, operation.key, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      apply(value); setPending(null); pendingRef.current = null;
      if (operation.kind === 'create') setSource(null);
      setNotice(operation.kind === 'accept' ? 'Учебная версия принята сервером. Это не распоряжение на перевозку и не реальное согласие организаций.' : operation.kind === 'response' ? 'Ваш учебный ответ сохранён сервером.' : 'Версия создана из сохранённого расчёта. Предметы определены сервером.');
    } catch (e) {
      if (!mounted.current || controller.signal.aborted) return;
      setActionError(message(e));
      if (e instanceof ServiceError && e.status === 403) setPacket(null);
      if (!repeating && e instanceof ServiceError && [400, 403, 404, 409, 422].includes(e.status)) { setPending(null); pendingRef.current = null; }
    } finally {
      posting.current = false;
      if (mounted.current && !controller.signal.aborted) { setBusy(false); setListLoading(false); setDetailLoading(false); void refresh(); setRevision(value => value + 1); }
    }
  };
  const resolvePending = async () => {
    const operation = pendingRef.current;
    if (!operation || posting.current || resolvingRef.current) return;
    resolvingRef.current = true; setResolving(true); onInteractionLock(true);
    epoch.current += 1; detailController.current?.abort();
    const controller = new AbortController(); detailController.current = controller;
    try {
      if (!await refresh()) return;
      const value = selectedId ? await getPacket(selectedId, controller.signal) : null;
      if (!mounted.current || controller.signal.aborted || pendingRef.current !== operation) return;
      if (value) { syncContext(value.context); setPacket(value.packet); }
      pendingRef.current = null; setPending(null); setActionError(null); setDetailError(null); setDetailLoading(false);
      setNotice('Данные версий обновлены. Проверьте сохранённые ответы и акт; прежняя операция не отменяется.');
    } catch (e) { if (mounted.current && !controller.signal.aborted) setDetailError(message(e)); }
    finally { resolvingRef.current = false; if (mounted.current) setResolving(false); }
  };
  const start = (kind: Operation['kind'], path: string, body: Record<string, unknown>, label: string) => {
    if (posting.current || resolvingRef.current || pendingRef.current || planningLocked) return;
    if (kind === 'create' && (!source || !planning || !canPreparePacket(source, planning))) return;
    void perform({ kind, path, body, label, key: crypto.randomUUID() });
  };
  const active = packet?.id === selectedId ? packet : null;
  const disabled = busy || resolving || Boolean(pending) || planningLocked || detailLoading || Boolean(listError || detailError);
  const stale = Boolean(active && context && active.source_revision !== context.source_revision);
  const visibleSubjects = active?.subjects.filter(subject => user.role !== 'participant' || user.subjects.includes(subject.id)) ?? [];
  const expired = visibleSubjects.some(subject => subject.latest_response && Date.parse(subject.latest_response.expires_at) <= now);
  const canCreate = Boolean(source && planning && context && context.source_revision === planning.source.revision && context.accepted_version === planning.baseline.accepted_version && canPreparePacket(source, planning));
  const canAccept = coordinator && active?.can_accept && !active.acceptance && !stale && !expired && !disabled;
  return <section id="workspace" className="approvals-workspace">
    <div className="page-heading"><div><p className="eyebrow">Серверное согласование · учебный контур</p><h1>{user.role === 'participant' ? 'Мои предметы согласования' : 'Согласование версий плана'}</h1><p className="route-line">Один ответ — один предмет, конкретная версия и ограниченный срок.</p></div></div>
    <div className="approval-intro">{coordinator ? 'Координатор создаёт пакет из сохранённого оптимума S1 и принимает версию после проверки сервером. Ответы за стороны даются их представителями.' : user.role === 'participant' ? 'Доступны только назначенные вам предметы. Каждый ответ сохраняется от вашей учебной учётной записи.' : 'Режим аудита: просмотр расчётов, предметов, ответов и принятой версии. Изменения недоступны.'}</div>
    {user.role !== 'participant' && <PlanningPanel readOnly={!coordinator} routeCalculationId={routeCalculationId} blocked={busy || resolving || Boolean(pending)} refreshToken={revision} onContext={planningContext} onPrepare={setSource} onLock={setPlanningLocked} onChanged={planningChanged} />}
    {coordinator && source && <section className="prepare-panel"><h2>Новая версия из пересчёта S1</h2><p>{canCreate ? `Сохранённый запуск от ${date(source.created_at)} мск · условия ${source.source_revision} · база ${source.base_accepted_version}. Предметы сервер построит по маршруту этого результата.` : 'Условия или принятая база изменились. Выполните актуальный пересчёт и подготовьте пакет из него.'}</p><button className="primary" type="button" disabled={disabled || !canCreate} onClick={() => context && canCreate && start('create', '/api/v1/approval-packets', { calculation_id: source.id, expected_source_revision: context.source_revision, expected_accepted_version: context.accepted_version }, 'Создание версии')}>Создать учебную версию</button></section>}
    {actionError && <div className="inline-error" role="alert"><p>{actionError}</p>{pending && <><p className="fine">Исход операции неизвестен. Повтор отправит прежние условия с тем же ключом операции.</p><button type="button" disabled={busy || resolving} onClick={() => void perform(pending)}>Повторить: {pending.label.toLowerCase()}</button><button type="button" disabled={busy || resolving} onClick={() => void resolvePending()}>{resolving ? 'Обновляем версии и ответы…' : 'Закрыть повтор и обновить'}</button><p className="fine">Повтор закроется только после успешного обновления списка и выбранного пакета. Прежняя операция не отменяется.</p></>}</div>}
    {notice && <p className="approval-notice" role="status">{notice}</p>}
    {(listError || detailError) && <div className="inline-error" role="alert">{[...new Set([listError, detailError].filter(Boolean))].map(text => <p key={text}>{text}</p>)}<button type="button" disabled={busy || resolving} onClick={() => { void refresh(); setRevision(value => value + 1); }}>Обновить данные</button></div>}
    <div className="approval-grid">
      <aside className="history-panel" aria-labelledby="packet-history-title"><div className="section-heading"><h2 id="packet-history-title">Версии плана</h2><button type="button" className="quiet-button" disabled={listLoading || busy || resolving} onClick={() => { void refresh(); setRevision(value => value + 1); }}>Обновить</button></div>
        <p className="history-description">Сохранённые пакеты доступны после обновления страницы.</p>
        {packets.length === 0 && <div className="history-empty"><p>{listLoading ? 'Загружаем версии…' : 'Версий пока нет.'}</p><span>{coordinator ? 'Выполните расчёт от текущих условий и базы, затем подготовьте пакет.' : 'Пакет появится после его создания координатором.'}</span></div>}
        <ul className="history-list packet-list">{packets.map(item => <li key={item.id}><button type="button" className={item.id === selectedId ? 'history-item selected' : 'history-item'} disabled={busy || Boolean(pending) || planningLocked} aria-pressed={item.id === selectedId} onClick={() => select(item.id)}><b>Версия {item.version}</b><span className="history-date">{date(item.created_at)} мск</span><span className={`packet-status packet-${item.status}`}>{statusNames[item.status]}</span></button></li>)}</ul>
      </aside>
      <section className="result-panel packet-panel" aria-labelledby="packet-title" aria-busy={detailLoading || busy || resolving}>
        <div className="section-heading"><h2 id="packet-title">{active ? `Версия ${active.version}` : 'Пакет согласования'}</h2>{active && <span className={`packet-status packet-${active.status}`}>{statusNames[active.status]}</span>}</div>
        {!active ? <div className="result-state"><h3>{detailLoading ? 'Загружаем выбранную версию' : 'Выберите сохранённую версию'}</h3><p>Здесь появятся предметы, ответы сторон и результат серверной проверки.</p></div> : <>
          <div className="packet-summary"><p>Создана {date(active.created_at)} мск · ответы проверяются для этой версии.</p>{user.role !== 'participant' && <p>{visibleSubjects.length} предметов сформировано по сохранённому маршруту. Готовность к принятию определяет сервер.</p>}</div>
          {(stale || active.status === 'stale') && <div className="outcome outcome-warning"><div><h3>Исходные условия изменились</h3><p>Ответы прежней версии не подтверждают новые условия. Для следующей версии координатор должен пересчитать кандидат от текущих условий и принятой базы.</p></div></div>}
          {active.acceptance && <div className={active.status === 'accepted' ? 'outcome outcome-success' : 'outcome outcome-warning'}><div><h3>{active.status === 'review_required' || stale ? 'Требуется пересмотр' : 'Учебная версия уже принята'}</h3><p>Акт принятия учебной версии {active.acceptance.accepted_version} от {date(active.acceptance.accepted_at)} мск сохранён. Это учебный переход, а не исполнение перевозки или юридически значимое согласие сторон.</p></div></div>}
          {active.status === 'review_required' && !active.acceptance && <div className="outcome outcome-warning"><div><h3>Требуется пересмотр</h3><p>Проверьте причины и ответы сторон. Новый пакет создаётся только из актуального пересчёта.</p></div></div>}
          {active.issues.length > 0 && <div className="packet-issues"><h3>Что требует внимания</h3><ul>{active.issues.map((issue, i) => <li key={i}>{issue}</li>)}</ul></div>}
          {visibleSubjects.length === 0 && <p className="packet-summary">В этой версии нет доступных вам предметов.</p>}
          <div className="subjects-list">{visibleSubjects.map(subject => <article className="subject-card" key={`${active.id}:${subject.id}`}><div className="subject-heading"><h3>{subject.title}</h3><ResponseState subject={subject} now={now} /></div><ParameterTable subject={subject} />
            {subject.latest_response?.comment && <p className="subject-comment">Комментарий представителя: {subject.latest_response.comment}</p>}
            {user.role === 'participant' && subject.can_respond && !stale && !['superseded', 'stale'].includes(active.status) && <ResponseForm key={`${active.id}:${subject.id}:${subject.revision}`} subject={subject} user={user} disabled={disabled} onSubmit={(item, decision, expires, comment) => start('response', `/api/v1/approval-packets/${encodeURIComponent(active.id)}/responses`, { packet_version: active.version, subject_id: item.id, subject_sha256: item.sha256, expected_revision: item.revision, decision, expires_at: expires, ...(comment ? { comment } : {}) }, 'Сохранение ответа')} />}
          </article>)}</div>
          {coordinator && <div className="accept-panel"><h3>Принятие учебной версии</h3><p>{active.acceptance ? active.status === 'review_required' || stale ? 'Требуется пересмотр. Акт прежнего учебного принятия сохранён; дальнейший пересчёт выполняется отдельным запуском.' : 'Учебная версия уже принята. Акт сохранён; повторное принятие этого пакета не требуется.' : active.can_accept && !expired && !stale ? 'Сервер разрешает запрос на принятие. При сохранении условия и ответы будут проверены повторно.' : 'Принятие недоступно: нужны действующие безусловные ответы всех требуемых сторон и актуальные условия.'}</p><button type="button" className="primary" disabled={!canAccept} onClick={() => context && start('accept', `/api/v1/approval-packets/${encodeURIComponent(active.id)}/accept`, { packet_version: active.version, expected_approval_revision: active.approval_revision, expected_source_revision: context.source_revision, expected_accepted_version: context.accepted_version }, 'Принятие версии')}>Принять учебную версию</button></div>}
          <details className="source-details"><summary>Привязка и контроль версии</summary><dl><dt>Исходный снимок</dt><dd>{active.snapshot_id}</dd><dt>Расчёт</dt><dd>{active.calculation_id}</dd><dt>Редакция условий</dt><dd>{active.source_revision}</dd><dt>Редакция ответов</dt><dd>{active.approval_revision}</dd><dt>Контроль входа</dt><dd>{active.input_sha256}</dd><dt>Контроль кандидата</dt><dd>{active.candidate_sha256}</dd><dt>Правило проверки</dt><dd>{active.policy_id}</dd></dl></details>
        </>}
      </section>
    </div>
    <footer>Все организации и полномочия этого контура учебные. Система не бронирует ресурсы и не передаёт распоряжения реальным участникам перевозки.</footer>
  </section>;
}
