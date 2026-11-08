# Запуск

Нужны Docker с работающим движком Linux-контейнеров и Docker Compose. Из корня репозитория выполните

~~~sh
make up
~~~

Если GNU Make в Windows называется `mingw32-make`, выполните `mingw32-make up`. Если Make не установлен, выполните

~~~sh
docker compose up --build --detach --wait --wait-timeout 180
~~~

Откройте [http://localhost:8080/#simulation](http://localhost:8080/#simulation).

# Ключи входа

`make up` выводит ключ координатора. Чтобы вывести его повторно, выполните `make key` или `mingw32-make key` в Windows.

Ключи всех ролей, включая координатора `coordinator.demo`, хранятся в файле `credentials.private.json` в томе `auth-data`. После запуска выведите список ролей и ключей этой командой

~~~sh
docker compose run --rm --no-deps --entrypoint python bootstrap -m json.tool /workspace/.local/credentials.private.json
~~~

Выберите нужную запись в списке `users` и введите её `access_key` на экране входа.
