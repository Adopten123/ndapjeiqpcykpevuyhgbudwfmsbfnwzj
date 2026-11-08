import { useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { cargoNames, defaultCargoId, defaultRouteId, maxPartyCount, modeNames, routeNames, statusNames } from './simulation';
import type { CargoId, CreateRunInput, RouteId, Run, RunHeader } from './simulation';
import './simulation-setup.css';

export function RunRegistry({ runs, busy, loading, failed, onOpen, onNew }: {
  runs: RunHeader[]; busy: boolean; loading: boolean; failed: boolean; onOpen: (id: string) => void; onNew: () => void;
}) {
  const [mode, setMode] = useState('');
  const [status, setStatus] = useState('');
  const visible = runs.filter(run => (!mode || run.mode === mode) && (!status || run.status === status));
  return <section className="sim-panel sim-registry" aria-label="Прогоны перевозки">
    <div className="sim-section-heading"><h2>Прогоны перевозки</h2>
      <button className="primary" type="button" disabled={busy} onClick={onNew}>Новый прогон</button></div>
    {loading && <p className="sim-section-empty" role="status">Загружаются прогоны…</p>}
    {!loading && !failed && !runs.length && <p className="sim-section-empty">Сохранённых прогонов пока нет.</p>}
    {runs.length > 0 && <>
      <div className="sim-registry-filters">
        <label className="sim-field">Режим<select value={mode} onChange={event => setMode(event.currentTarget.value)}>
          <option value="">Все режимы</option>{Object.entries(modeNames).map(([id, name]) => <option key={id} value={id}>{name}</option>)}
        </select></label>
        <label className="sim-field">Состояние<select value={status} onChange={event => setStatus(event.currentTarget.value)}>
          <option value="">Все состояния</option>{Object.entries(statusNames).map(([id, name]) => <option key={id} value={id}>{name}</option>)}
        </select></label>
      </div>
      {visible.length ? <ul className="sim-registry-list">{visible.map(run => <li key={run.id}>
        <div><strong>{run.name}</strong><p className="muted">{modeNames[run.mode]}</p></div>
        <span>{statusNames[run.status]}</span><span>Шаг {run.tick} · версия {run.plan_version}</span>
        <button type="button" disabled={busy} aria-label={`Открыть прогон «${run.name}»`} onClick={() => onOpen(run.id)}>Открыть</button>
      </li>)}</ul> : <div className="sim-section-empty">По фильтрам ничего не найдено.{' '}
        <button type="button" onClick={() => { setMode(''); setStatus(''); }}>Сбросить фильтры</button></div>}
    </>}
  </section>;
}

export function RunCreation({ current, busy, onBack, onCreate }: {
  current: Run | null; busy: boolean; onBack: () => void; onCreate: (input: CreateRunInput) => Promise<void>;
}) {
  const [mode, setMode] = useState<CreateRunInput['mode']>('training');
  const [name, setName] = useState('');
  const [partyCount, setPartyCount] = useState(String(current?.parties.length ?? maxPartyCount));
  const [countTouched, setCountTouched] = useState(false);
  const countField = useRef<HTMLInputElement>(null);
  const [routeId, setRouteId] = useState<RouteId>(current?.route.id ?? defaultRouteId);
  const [cargoId, setCargoId] = useState<CargoId>(current?.cargo.id ?? defaultCargoId);
  const [cargoName, setCargoName] = useState(current && current.cargo.name !== cargoNames[current.cargo.id] ? current.cargo.name : '');
  const [copy, setCopy] = useState(Boolean(current));
  const canCopy = mode === 'training' && current;
  const count = Number(partyCount);
  const countValid = Number.isInteger(count) && count >= 1 && count <= maxPartyCount;
  const showCountError = countTouched && !countValid;

  const create = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!countValid) {
      setCountTouched(true);
      countField.current?.focus();
      return;
    }
    const cargoDesignation = cargoName.trim();
    const configuration: Omit<CreateRunInput, 'mode'> = {
      name: name.trim() || (mode === 'training' ? 'Учебный прогон' : 'Диспетчерский прогон'),
      party_count: count,
      route_id: routeId,
      cargo_id: cargoId,
      ...(cargoDesignation && { cargo_name: cargoDesignation }),
    };
    if (canCopy && copy) {
      void onCreate({ ...configuration, mode: 'training', copy_from: current.id });
    } else {
      void onCreate({ ...configuration, mode });
    }
  };

  return <section className="sim-create" aria-label="Создание прогона">
    <div className="sim-section-heading"><h2>Новый прогон</h2><button type="button" disabled={busy} onClick={onBack}>Назад</button></div>
      <form className="sim-create-form" noValidate onSubmit={create}>
        <fieldset className="sim-mode-picker" disabled={busy}>
          <legend>Режим прогона</legend>
          <div className="sim-mode-options">
            <label className="sim-mode-option">
              <input type="radio" name="run-mode" value="training" checked={mode === 'training'} aria-description="Пошаговый ход и ручные события." onChange={() => setMode('training')} />
              <strong>{modeNames.training}</strong><span>Пошаговый ход и ручные события.</span>
            </label>
            <label className="sim-mode-option">
              <input type="radio" name="run-mode" value="operations" checked={mode === 'operations'} aria-description="Автоход, события имитаторов и ручной ввод." onChange={() => { setMode('operations'); setCopy(false); }} />
              <strong>{modeNames.operations}</strong><span>Автоход, события имитаторов и ручной ввод.</span>
            </label>
          </div>
        </fieldset>
        <div className="sim-create-fields">
          <label className="sim-field sim-create-route">Пилотный маршрут
            <select value={routeId} disabled={busy} onChange={event => setRouteId(event.currentTarget.value as RouteId)}>
              {Object.entries(routeNames).map(([id, title]) => <option key={id} value={id}>{title}</option>)}
            </select>
          </label>
          <label className="sim-field">Тип груза
            <select value={cargoId} disabled={busy} onChange={event => { setCargoId(event.currentTarget.value as CargoId); setCargoName(''); }}>
              {Object.entries(cargoNames).map(([id, title]) => <option key={id} value={id}>{title}</option>)}
            </select>
          </label>
          <label className="sim-field">Наименование груза
            <input value={cargoName} maxLength={100} disabled={busy} onChange={event => setCargoName(event.currentTarget.value)} placeholder={cargoNames[cargoId]} />
          </label>
          <div className="sim-create-count">
            <label className="sim-field">
              <span>Количество партий <span aria-hidden="true">(1–{maxPartyCount})</span></span>
              <input ref={countField} type="number" inputMode="numeric" min={1} max={maxPartyCount} step={1} required value={partyCount}
                disabled={busy} onChange={event => setPartyCount(event.currentTarget.value)} onBlur={() => setCountTouched(true)}
                aria-invalid={showCountError || undefined} aria-describedby={showCountError ? 'sim-count-error' : undefined} />
            </label>
            {showCountError && <p id="sim-count-error" className="sim-field-error" role="alert">Введите целое число от 1 до {maxPartyCount}.</p>}
          </div>
          <label className="sim-field">Название прогона
            <input value={name} maxLength={100} disabled={busy} onChange={event => setName(event.currentTarget.value)} placeholder="Необязательно" />
          </label>
        </div>
        {canCopy && <label className="sim-check"><input type="checkbox" checked={copy} disabled={busy}
          onChange={event => setCopy(event.currentTarget.checked)} />Скопировать ручные события из прогона «{current.name}»</label>}
        <button className="primary" type="submit" disabled={busy}>Создать прогон</button>
      </form>
  </section>;
}
