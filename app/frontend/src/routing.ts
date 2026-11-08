export interface Route {
  page: 'calculation' | 'approval' | 'simulation';
  packetId: string | null;
  calculationId: string | null;
  error: string | null;
  simulationRunId?: string;
}

export function readRoute(href: string): Route {
  const url = new URL(href);
  const route: Route = { page: 'calculation', packetId: null, calculationId: null, error: null };
  const validId = (value: string) => /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(value);
  try {
    if (url.searchParams.has('planning_calculation')) {
      const ids = url.searchParams.getAll('planning_calculation');
      if (ids.length !== 1 || !validId(ids[0])) throw new Error('invalid calculation');
      route.calculationId = ids[0];
      route.page = 'approval';
    }
    if (url.hash === '#approvals') route.page = 'approval';
    else if (url.hash.startsWith('#approvals/')) {
      const id = decodeURIComponent(url.hash.slice('#approvals/'.length));
      if (!validId(id)) throw new Error('invalid packet');
      route.packetId = id;
      route.page = 'approval';
    } else if (url.hash === '#simulation') route.page = 'simulation';
    else if (url.hash.startsWith('#simulation/')) {
      const id = decodeURIComponent(url.hash.slice('#simulation/'.length));
      if (!validId(id)) throw new Error('invalid run');
      route.simulationRunId = id;
      route.page = 'simulation';
    } else if (url.hash && url.hash !== '#workspace') throw new Error('invalid route');
  } catch {
    route.error = 'Некорректная ссылка. Выберите раздел и откройте сохранённую запись из истории.';
  }
  return route;
}

export function replaceRoute(url: URL | string) {
  window.history.replaceState(null, '', url);
  window.dispatchEvent(new Event('app-route-changed'));
}
