import type { Route } from './routing';
import type { User } from './workflow';
import { transportWorkspaceTitle } from './simulation';

const roleNames = { coordinator: 'Координатор', participant: 'Представитель стороны', auditor: 'Аудитор · только чтение' };

export function ApplicationShell({ user, signingOut, locked, page, onExit, onNavigate }: {
  user: User | null; signingOut: boolean; locked: boolean; page: Route['page'];
  onExit: () => void; onNavigate: (page: Route['page']) => void;
}) {
  return <>
    <header className="app-header"><div className="identity"><img src="/rzd-logo.svg" width="79" height="35" alt="РЖД" /><div>РЖД Арктика<small>{user ? roleNames[user.role] : 'Учебное рабочее место'}</small></div></div>
      {user ? <div className="session-controls"><span className="environment-label">Учебный контур</span><span>{user.name}<small>Полномочия до {new Date(user.valid_until).toLocaleString('ru-RU', { timeZone: 'Europe/Moscow' })} мск</small></span><button className="quiet-button" type="button" disabled={signingOut || locked} onClick={() => onExit()}>{signingOut ? 'Выходим…' : 'Выйти'}</button></div> : <span className="header-caption">Локальный учебный контур</span>}
    </header>
    {user && !signingOut && <nav className="topnav" aria-label="Разделы">
      {user.role === 'coordinator' && <button type="button" disabled={locked} aria-current={page === 'simulation' ? 'page' : undefined} onClick={() => { if (page !== 'simulation') onNavigate('simulation'); }}>{transportWorkspaceTitle}</button>}
      {user.role !== 'participant' && <button type="button" disabled={locked} aria-current={page === 'calculation' ? 'page' : undefined} onClick={() => onNavigate('calculation')}>Учебные примеры S0–S3</button>}
      <button type="button" disabled={locked} aria-current={page === 'approval' || user.role === 'participant' ? 'page' : undefined} onClick={() => onNavigate('approval')}>{user.role === 'participant' ? 'Мои предметы согласования' : 'Согласование версий'}</button>
    </nav>}
    <div className="demo-note"><strong>Учебные данные · реальный расчёт.</strong> Реальных подключений и обученной ETA-модели нет. Ответы и принятие версии действуют только в учебном контуре.</div>
  </>;
}
