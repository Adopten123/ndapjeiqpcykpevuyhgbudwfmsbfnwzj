import type { Target } from './simulation';

const locations: Record<string, string> = {
  S: 'У отправителя',
  O: 'На станции Ховрино',
  B: 'В контрольной точке учебной ж/д ветки B',
  C: 'В контрольной точке учебной ж/д ветки C',
  M: 'В порту Мурманск',
  U: 'На границе участка СМП у входа в учебный пролив',
  V: 'На границе участка СМП у выхода из учебного пролива',
  D: 'В порту Петропавловск-Камчатский',
  R: 'У получателя',
};

const edges: Record<string, string> = {
  SO: 'Подача груза на станцию Ховрино',
  OB: 'Станция Ховрино → контрольная точка учебной ж/д ветки B',
  BM: 'Учебная ж/д ветка B → порт Мурманск',
  OC: 'Станция Ховрино → контрольная точка учебной ж/д ветки C',
  CM: 'Учебная ж/д ветка C → порт Мурманск',
  MU: 'Порт Мурманск → вход в учебный пролив',
  UV: 'Проход учебного пролива',
  VD: 'Выход из учебного пролива → порт Петропавловск-Камчатский',
  DR: 'Порт Петропавловск-Камчатский → получатель',
};

export function locationLabel(id: string | undefined) {
  return id ? locations[id] ?? `В точке маршрута ${id}` : 'Положение уточняется';
}

export function edgeLabel(id: string) {
  return edges[id] ?? `Участок ${id}`;
}

export function targetLabel(target: Target) {
  return Object.hasOwn(edges, target.id) ? `${edges[target.id]} · ${target.id}` : target.label;
}
