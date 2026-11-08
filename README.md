# РЖД Арктика

Учебное приложение управления железнодорожно-морской перевозкой Ховрино → Мурманск → СМП → Петропавловск-Камчатский.

React показывает положение партий, события, сравнение и применение плана. Go/River сохраняет состояние в PostgreSQL. FastAPI выполняет расчёт CP-SAT, отдельный имитатор подаёт учебные события.

Все транспортные данные и учётные записи учебные. Подключения к перевозчикам и обученная ETA-модель требуют отдельной реализации.

## Состав

- app/frontend — интерфейс React и TypeScript.
- app/backend — сервер Go, миграции PostgreSQL и генератор учебных ключей.
- app/compute — вычислитель FastAPI, ядро CP-SAT в planner и контракты в contracts.
- app/simulator — HTTP-имитатор событий.
- app/checks — сквозные HTTP-проверки.

## Подготовка

Нужны Node.js 22+, Go с поддержкой версии из go.mod, Python 3.12 и собственная PostgreSQL. Команды выполняются из корня клонированного репозитория.

~~~powershell
python -m pip install --only-binary=:all: --target .local/compute-deps -r app/compute/requirements-lock.txt
python -m pip install --only-binary=:all: --target .local/planner-deps -r app/compute/planner/requirements-lock.txt
npm.cmd --prefix app/frontend ci
go -C app/backend mod download
~~~

Создайте отдельную пустую учебную БД с владельцем rzd_app. Команды ниже выполняются в psql администратором локального сервера. Пароль задаётся интерактивно.

~~~sql
CREATE ROLE rzd_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
\password rzd_app
CREATE DATABASE rzd_arctica_demo OWNER rzd_app;
~~~

Создайте учебные ключи. Генератор сохраняет их под .local внутри клонированного репозитория.

~~~powershell
New-Item -ItemType Directory -Force .local | Out-Null
go -C app/backend build -o ../../.local/demo-auth.exe ./cmd/demo-auth
& .local/demo-auth.exe -output .local/auth.private.json -credentials .local/credentials.private.json -valid-for 168h
~~~

## Запуск

Каждый блок запускается в отдельном терминале из корня репозитория.

Вычислитель на порту 8091.

~~~powershell
python app/compute/main.py --deps .local/compute-deps --planner-deps .local/planner-deps
~~~

Имитатор на порту 8094.

~~~powershell
python app/simulator/main.py --deps .local/compute-deps
~~~

Go-сервер на порту 8090. Введите URL созданной БД вида postgres://rzd_app:YOUR_PASSWORD@127.0.0.1:5432/rzd_arctica_demo?sslmode=disable. Замените пароль и порт, закодируйте специальные символы пароля для URL.

~~~powershell
$databaseInput = Read-Host 'URL учебной PostgreSQL' -AsSecureString
$env:RZD_DATABASE_URL = [System.Net.NetworkCredential]::new('', $databaseInput).Password
$env:RZD_AUTH_FILE = (Resolve-Path .local/auth.private.json).Path
$env:RZD_HTTP_ADDR = '127.0.0.1:8090'
$env:RZD_COMPUTE_URL = 'http://127.0.0.1:8091'
$env:RZD_SIMULATOR_URL = 'http://127.0.0.1:8094'
go -C app/backend run ./cmd/server
~~~

Интерфейс на порту 5173.

~~~powershell
npm.cmd --prefix app/frontend run dev
~~~

Откройте http://127.0.0.1:5173/#simulation и введите ключ coordinator.demo из .local/credentials.private.json. После перезапуска Go сеанс нужно открыть заново. Срок полномочий проверяется сервером. Для нового независимого запуска с истёкшими назначениями используйте отдельную пустую учебную БД.

Сначала создайте учебный прогон с 1–4 партиями. Задайте ручное событие, выполните следующий шаг, сравните планы и примените проверенный кандидат. Сохранённый прогон восстанавливается после перезагрузки страницы.

## Проверки

~~~powershell
npm.cmd --prefix app/frontend test
npm.cmd --prefix app/frontend run build
go -C app/backend test ./...
go -C app/backend vet ./...
python app/compute/test_api.py --deps .local/compute-deps --planner-deps .local/planner-deps
python app/compute/test_workflow.py --deps .local/compute-deps --planner-deps .local/planner-deps
python app/compute/test_simulation.py --deps .local/compute-deps --planner-deps .local/planner-deps
~~~

Сквозные проверки из app/checks и app/frontend/tests/simulation-ui-browser.mjs запускаются на отдельной чистой учебной PostgreSQL. Браузерному сценарию нужны Playwright и установленный браузер. Каталог модулей можно задать через SIM_PLAYWRIGHT_ROOT, путь к браузеру — через SIM_BROWSER.

Пароли, ключи, локальные базы и установленные зависимости хранятся под .local и исключены из Git.
