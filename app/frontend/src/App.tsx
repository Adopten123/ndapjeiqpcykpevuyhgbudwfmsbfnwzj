import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import CalculationWorkspace from './CalculationWorkspace';
import Approvals from './Approvals';
import SimulationWorkspace from './SimulationWorkspace';
import { ApplicationShell } from './ApplicationShell';
import { ServiceError } from './api';
import { getSession, login, logout } from './workflow';
import type { User } from './workflow';
import { readRoute, replaceRoute } from './routing';
import type { Route } from './routing';


const message = (error: unknown) => error instanceof ServiceError ? error.message : 'Не удалось связаться с сервером. Повторите позже.';

function Login({ onLogin, initialError }: { onLogin: (user: User) => void; initialError: string | null }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(initialError);
  const posting = useRef(false);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (posting.current) return;
    const field = event.currentTarget.elements.namedItem('access_key') as HTMLInputElement;
    let accessKey = field.value.trim();
    if (!accessKey) return;
    field.value = '';
    posting.current = true; setBusy(true); setError(null);
    const current = new AbortController(); controller.current = current;
    try { const user = await login(accessKey, current.signal); if (!current.signal.aborted) onLogin(user); }
    catch (e) { if (!current.signal.aborted) setError(e instanceof ServiceError && e.status === 401 ? 'Ключ не принят: проверьте выданный ключ и срок его действия.' : message(e)); }
    finally { accessKey = ''; posting.current = false; if (!current.signal.aborted) setBusy(false); }
  };
  return <main id="app-content" tabIndex={-1} className="login-main"><section className="login-panel" aria-labelledby="login-title">
    <p className="eyebrow">Локальный учебный контур</p><h1 id="login-title">Вход в рабочее место</h1>
    <p>Введите индивидуальный учебный ключ. Его выдаёт организатор локального запуска из файла, который не включается в репозиторий.</p>
    <p className="muted">Это не учётная запись РЖД. Роль, организация, назначенные предметы и срок полномочий проверяются сервером.</p>
    <form onSubmit={submit}><label htmlFor="access-key">Индивидуальный ключ</label><input id="access-key" name="access_key" type="password" autoComplete="off" autoCapitalize="none" spellCheck={false} required disabled={busy} aria-describedby="key-note" />
      <small id="key-note">Ключ не сохраняется в приложении. После выхода потребуется ввести его снова.</small>
      <button className="primary" type="submit" disabled={busy}>{busy ? 'Проверяем ключ…' : 'Войти'}</button>
    </form>{error && <div className="login-error" role="alert">{error}</div>}
  </section></main>;
}

export default function App() {
  const [user, setUser] = useState<User | null>(null);
  const [checking, setChecking] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [signingOut, setSigningOut] = useState(false);
  const [route, setRoute] = useState(() => readRoute(window.location.href));
  const page = route.page;
  const lastHref = useRef(window.location.href);
  const exiting = useRef(false);
  const locked = useRef(false);

  const [workflowLocked, setWorkflowLocked] = useState(false);
  locked.current = workflowLocked;
  useEffect(() => {
    const sync = (event: Event) => {
      if (event.type !== 'app-route-changed' && (locked.current || exiting.current)) {
        window.history.replaceState(null, '', lastHref.current);
        return;
      }
      lastHref.current = window.location.href;
      setRoute(readRoute(lastHref.current));
    };
    for (const name of ['hashchange', 'popstate', 'app-route-changed']) window.addEventListener(name, sync);
    return () => { for (const name of ['hashchange', 'popstate', 'app-route-changed']) window.removeEventListener(name, sync); };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void getSession(controller.signal).then(value => { if (!controller.signal.aborted) setUser(value); }).catch(e => {
      if (!controller.signal.aborted && !(e instanceof ServiceError && e.status === 401)) setError(message(e));
    }).finally(() => { if (!controller.signal.aborted) setChecking(false); });
    const expire = () => { setUser(null); setError('Сеанс завершён или полномочия истекли. Войдите повторно.'); };
    window.addEventListener('session-expired', expire);
    return () => { controller.abort(); window.removeEventListener('session-expired', expire); };
  }, []);
  useEffect(() => {
    if (!user) return;
    if (user.role === 'coordinator' && !window.location.hash && !new URL(window.location.href).searchParams.has('planning_calculation')) {
      const url = new URL(window.location.href);
      url.hash = '#simulation';
      replaceRoute(url);
    }
    const remaining = Date.parse(user.valid_until) - Date.now();
    const timer = window.setTimeout(() => window.dispatchEvent(new Event('session-expired')), Math.max(0, Math.min(remaining, 2147483647)));
    return () => window.clearTimeout(timer);
  }, [user]);
  const exit = async () => {
    if (exiting.current || locked.current) return;
    exiting.current = true;
    setSigningOut(true); setError(null);
    try { await logout(); setUser(null); changePage('calculation'); }
    catch (e) {
      // A lost DELETE reply does not establish whether the server removed the session.
      try { setUser(await getSession()); setError(message(e)); }
      catch (checkError) { setUser(null); setError(checkError instanceof ServiceError && checkError.status === 401 ? null : message(checkError)); }
    }
    finally { exiting.current = false; setSigningOut(false); }
  };
  const changePage = (value: Route['page']) => {
    const url = new URL(window.location.href); url.searchParams.delete('planning_calculation');
    url.hash = value === 'approval' ? '#approvals' : value === 'simulation' ? '#simulation' : '#workspace'; replaceRoute(url);
  };
  return <>
    <a className="skip-link" href="#app-content" onClick={event => { event.preventDefault(); const content = document.querySelector<HTMLElement>('main'); content?.focus(); content?.scrollIntoView(); }}>Перейти к содержимому</a>
    <ApplicationShell user={user} signingOut={signingOut} locked={workflowLocked} page={page}
      onExit={() => void exit()} onNavigate={changePage} />
    {checking || signingOut ? <main id="app-content" tabIndex={-1}><div className="result-state" role="status">{signingOut ? 'Завершаем сеанс…' : 'Проверяем сеанс…'}</div></main> : !user ? <Login onLogin={value => { setUser(value); setError(null); }} initialError={error} /> : <main id="app-content" tabIndex={-1}>
      {error && <div className="inline-error" role="alert">{error}</div>}
      {route.error ? <div className="inline-error" role="alert">{route.error}</div>
        : user.role === 'participant' || page === 'approval' ? <Approvals key={user.id} user={user} routePacketId={route.packetId} routeCalculationId={route.calculationId} onInteractionLock={setWorkflowLocked} />
        : user.role === 'coordinator' && page === 'simulation' ? <SimulationWorkspace key={user.id} runId={route.simulationRunId} onInteractionLock={setWorkflowLocked} />
        : <CalculationWorkspace key={user.id} readOnly={user.role === 'auditor'} onInteractionLock={setWorkflowLocked} />}
    </main>}
  </>;
}
