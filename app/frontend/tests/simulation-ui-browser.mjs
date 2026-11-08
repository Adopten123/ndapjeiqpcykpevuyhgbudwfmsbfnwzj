import assert from 'node:assert/strict';
import { readFile, mkdir } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

const [origin, credentialsPath] = process.argv.slice(2);
assert(/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(origin) && credentialsPath, 'Укажите loopback URL и файл учебных ключей.');
const credentials = JSON.parse(await readFile(credentialsPath, 'utf8'));
const key = credentials.users.find(user => user.id === 'coordinator.demo')?.access_key;
assert(key);
const loadBrowser = process.env.SIM_PLAYWRIGHT_ROOT
  ? createRequire(resolve(process.env.SIM_PLAYWRIGHT_ROOT, 'package.json')) : createRequire(import.meta.url);
const { chromium } = loadBrowser('playwright');
const browser = await chromium.launch({ headless: true, ...(process.env.SIM_BROWSER && { executablePath: process.env.SIM_BROWSER }) });
const checks = [];
const errors = [];
const check = (name, condition) => { assert(condition, name); checks.push(name); };
const button = (page, name) => page.getByRole('button', { name, exact: true });
const runsPath = '/api/v1/simulation/runs';
const currentPath = page => runsPath + '/' + new URL(page.url()).hash.split('/')[1];
const status = page => page.locator('.sim-status h3');
const output = process.env.SIM_SCREENSHOT_DIR;
if (output) await mkdir(output, { recursive: true });

async function read(page, path = currentPath(page)) {
  const response = await page.request.get(origin + path);
  assert(response.ok(), 'GET ' + path + ' HTTP ' + response.status());
  return response.json();
}
async function mutation(page, method, path, trigger) {
  const [response] = await Promise.all([
    page.waitForResponse(reply => new URL(reply.url()).pathname === path && reply.request().method() === method), trigger(),
  ]);
  assert(response.ok(), method + ' ' + path + ' HTTP ' + response.status());
  return response.json();
}
async function view(page, name) {
  await page.locator('.sim-view-nav').getByRole('button', { name, exact: true }).click();
}
async function create(page, mode, name, count = 4) {
  if (await button(page, 'Все прогоны').isVisible()) await button(page, 'Все прогоны').click();
  await button(page, 'Новый прогон').click();
  await page.getByRole('radio', { name: new RegExp(mode === 'training' ? '^Учебный режим' : '^Диспетчерский режим') }).check();
  await page.getByRole('spinbutton', { name: 'Количество партий' }).fill(String(count));
  await page.getByLabel('Название прогона').fill(name);
  const result = await mutation(page, 'POST', runsPath, () => button(page, 'Создать прогон').click());
  await page.getByRole('heading', { name, exact: true }).waitFor();
  return result;
}
async function legacy(page, scenario, name) {
  const response = await page.request.post(origin + runsPath, { headers: { Origin: origin }, data: { scenario, name } });
  assert(response.ok());
  const run = await response.json();
  await page.goto(origin + '/#simulation/' + run.id);
  await page.getByRole('heading', { name, exact: true }).waitFor();
  return run;
}
async function step(page) {
  await button(page, 'Следующий шаг').waitFor();
  return mutation(page, 'POST', currentPath(page) + '/step', () => button(page, 'Следующий шаг').click());
}
async function candidate(page) {
  await status(page).filter({ hasText: /Оптимальный план|Допустимый план$/ }).waitFor();
  check('Кандидат останавливает ручной шаг', await button(page, 'Следующий шаг').count() === 0);
  await view(page, 'Планы');
  check('Сравнение содержит карточки всех партий', await page.locator('.sim-comparison-card').count() === (await read(page)).parties.length);
}
async function apply(page) {
  const previous = await read(page);
  const applied = await mutation(page, 'POST', currentPath(page) + '/apply', () => button(page, 'Применить кандидат').click());
  check('Применение создаёт следующую версию', applied.plan_version === previous.plan_version + 1);
  await status(page).filter({ hasText: /Можно продолжать|Симуляция на паузе/ }).waitFor();
  return applied;
}

try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.setDefaultTimeout(15000);
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(origin + '/#simulation');
  await page.getByLabel('Индивидуальный ключ').fill(key);
  await button(page, 'Войти').click();
  await page.getByRole('heading', { name: 'Прогоны перевозки' }).waitFor();
  check('Реестр отделён от создания', !await page.getByLabel('Название прогона').isVisible());
  const existing = await read(page, runsPath);
  check('Новая база или явно разрешённая повторная проверка', existing.runs.length === 0 || process.env.SIM_ALLOW_NONEMPTY === '1');
  await button(page, 'Новый прогон').click();
  const input = page.getByRole('spinbutton', { name: 'Количество партий' });
  for (const value of ['', '0', '-1', '2.5', '5']) {
    await input.fill(value);
    await button(page, 'Создать прогон').click();
    await page.getByText('Введите целое число от 1 до 4.', { exact: true }).waitFor();
  }
  check('Невалидная форма не создала записи', (await read(page, runsPath)).runs.length === existing.runs.length);
  await button(page, 'Назад').click();

  for (const count of [1, 2, 3, 4]) {
    const run = await create(page, 'training', 'UI · ' + count + ' партии', count);
    check('Создан состав ' + count, run.parties.length === count);
    await view(page, 'Партии');
    check('Список содержит только партии прогона', await page.locator('.sim-party-option').count() === count);
    check('Открыт один вертикальный трек', await page.locator('.sim-tracking-card').count() === 1);
    const lastId = run.parties.at(-1).id;
    await page.locator('.sim-party-option').filter({ hasText: lastId }).click();
    await view(page, 'События'); await view(page, 'Партии');
    check('Смена вида сохраняет выбранную партию', await page.getByRole('heading', { name: 'Трек ' + lastId, exact: true }).isVisible());
  }

  const original = await legacy(page, 'standard', 'UI · последовательные сбои');
  await view(page, 'Партии');
  await page.locator('.sim-party-option').filter({ hasText: 'P1' }).click();
  check('P1 имеет исходную позицию до отслеживаемой ж/д', await page.getByText('На старте партия уже в порту Мурманск. Ж/д операции до старта не отслеживаются.', { exact: true }).isVisible());
  for (const width of [1440, 768, 390]) {
    await page.setViewportSize({ width, height: 900 });
    check('Партии помещаются на ' + width, await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    if (width === 1440) {
      await page.evaluate(() => scrollTo(0, 0));
      check('Четыре партии видны до прокрутки на 1440 × 900',
        await page.locator('.sim-party-options').evaluate(node => node.getBoundingClientRect().bottom <= 900));
    }
    if (output) await page.screenshot({ path: resolve(output, 'parties-' + width + '.png'), fullPage: true });
  }
  await button(page, 'Задать сбой').click();
  const editor = page.getByRole('dialog');
  await editor.getByLabel('Тип события').selectOption('rail_edge_closed');
  await editor.getByLabel('Участок', { exact: true }).selectOption('CM');
  await editor.getByLabel('Шаг поступления').fill('2');
  await editor.getByLabel('Начало ремонта').fill('2');
  await editor.getByLabel('Окончание ремонта').fill('12');
  await editor.getByLabel('Причина').fill('Ручной ремонт для проверки UI');
  await mutation(page, 'POST', currentPath(page) + '/events', () => button(page, 'Добавить будущее событие').click());
  await page.keyboard.press('Escape');
  await editor.waitFor({ state: 'hidden' });
  check('Escape возвращает фокус', await button(page, 'Задать сбой').evaluate(node => document.activeElement === node));

  await step(page); await candidate(page);
  await view(page, 'Партии');
  await page.locator('.sim-party-option').filter({ hasText: 'P2' }).click();
  check('Порт Мурманск показывает прибытие, морской этап — выход',
    await page.locator('[data-phase="departure"]').getByText('План · шаг 2', { exact: true }).isVisible() &&
    await page.locator('[data-phase="sea"]').getByText('Выход по плану · шаг 10', { exact: true }).isVisible());
  await view(page, 'Планы');
  check('Кандидат сравнивается с действующим планом', await page.locator('.sim-plan-pair').getByText('Действующий план v1', { exact: true }).count() === 4 &&
    await page.locator('.sim-plan-pair').getByText('Кандидат', { exact: true }).count() === 4);
  if (output) await page.screenshot({ path: resolve(output, 'candidate-390.png'), fullPage: true });
  await apply(page); await step(page); await candidate(page);
  const second = await read(page);
  check('Второй пересчёт сохраняет факт и накопленные ограничения',
    second.facts.some(fact => fact.operation_id === 'P2-BM' && fact.state === 'completed' && fact.end_tick === 2) &&
    second.events.some(event => event.source === 'manual') && second.events.some(event => event.source === 'carrier-mock'));
  await apply(page);
  await page.reload();
  await page.getByRole('heading', { name: original.name, exact: true }).waitFor();
  check('Перезагрузка восстанавливает серверную версию', (await read(page)).plan_version === 3);
  let final = await read(page);
  for (let i = 0; i < 40 && final.status === 'ready'; i++) final = await step(page);
  await status(page).filter({ hasText: 'Прогон завершён' }).waitFor();
  check('Все партии выданы на шаге 25', final.tick === 25 && final.status === 'completed' &&
    final.facts.filter(fact => /P[1-4]-DR$/.test(fact.operation_id) && fact.state === 'completed').length === 4);
  await button(page, 'Все прогоны').click();
  await page.locator('.sim-registry-filters select').nth(1).selectOption('completed');
  check('Фильтр реестра находит завершённый прогон', await page.getByRole('button', { name: 'Открыть прогон «' + original.name + '»' }).first().isVisible());

  await legacy(page, 'critical', 'UI · закрытие пролива');
  await step(page); await candidate(page); await apply(page);
  await step(page); await candidate(page); await apply(page); await step(page);
  await status(page).filter({ hasText: 'Допустимого плана нет' }).waitFor();
  check('Закрытие пролива блокирует применение и шаг', await button(page, 'Применить кандидат').count() === 0 && await button(page, 'Следующий шаг').count() === 0);
  await button(page, 'Посмотреть ограничения').click();
  check('Ограничение открывает события', await page.getByRole('heading', { name: 'Журнал событий' }).isVisible());

  const ops = await create(page, 'operations', 'UI · диспетчерский', 2);
  await button(page, 'Запустить симуляцию').click(); await button(page, 'Пауза').click();
  await page.waitForTimeout(1700);
  check('Пауза останавливает время', (await read(page)).tick === 0);
  await button(page, 'Запустить симуляцию').click(); await candidate(page); await apply(page);
  await page.waitForTimeout(1700);
  check('Применение ждёт явного продолжения', (await read(page)).tick === 1);
  await button(page, 'Продолжить симуляцию').click();
  await button(page, 'Задать сбой').click(); await page.keyboard.press('Escape');
  await page.waitForTimeout(1700);
  check('Редактор ставит автоход на паузу', (await read(page)).tick === 1);
  await button(page, 'Копировать в учебный прогон').click();
  check('Копирование предзаполняет состав', await input.inputValue() === '2');
  await page.getByLabel('Название прогона').fill('UI · копия');
  const copied = await mutation(page, 'POST', runsPath, () => button(page, 'Создать прогон').click());
  check('Копия создаёт отдельный учебный прогон', copied.id !== ops.id && copied.mode === 'training' && copied.tick === 0);
  check('JavaScript без ошибок', errors.length === 0);
  console.log(JSON.stringify({ checks, errors, widths: [1440, 768, 390], stack: 'React/Go/PostgreSQL/FastAPI/CP-SAT', standard: { tick: final.tick, version: final.plan_version } }, null, 2));
} finally {
  await browser.close();
}
