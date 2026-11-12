# Краш-репорты

Игра отправляет отчёты о вылетах на сервер с протоколом Sentry. Мы используем
[Bugsink](https://www.bugsink.com): это один контейнер на SQLite. Подойдут и
GlitchTip, и Sentry.

## Что приходит

| Случай | Что в отчёте |
|---|---|
| Вылет (access violation, переполнение стека, pure virtual, `abort()`) | minidump всех потоков; при следующем запуске — событие «Console output before the last crash» с консолью |
| Порча кучи, `__fastfail` (Windows) | minidump через WER (`crashpad_wer.dll`) |
| Процесс исчез без вылета (EAC, диспетчер задач, зависание) | событие «ended without a clean shutdown» с консолью прошлой сессии |
| `Error()` движка | текст ошибки и консоль |

Консоль (последние ~64 КБ) лежит в разделе **Extra** события, ключ
`console`. Вложением она тоже уходит, но Bugsink хранит из вложений только
minidump.

У каждого отчёта есть release `frontress@<PatchVersion из steam.inf>`, теги
`side`, `eac_build`, `crash_backend` и SteamID игрока (`user.id`).

## Как это работает

- **Везде crashpad из sentry-native.** Его хендлер — отдельный процесс. Он
  открывает игру только после вылета, пишет minidump и сразу отправляет его.
  Неотправленное (нет сети) уйдёт при следующем запуске. База лежит в
  `crashreports/.sentry` рядом с exe, вне `bin/x64`: тот каталог целиком входит
  в каталог целостности EAC.
- **Windows и EAC.** Хендлер называется `CrashReportHandler.exe`, а не
  `crashpad_handler.exe`. По [правилам Epic](https://dev.epicgames.com/docs/epic-online-services/trust-and-safety/anti-cheat-interfaces/using-anti-cheat)
  (раздел «External Crash Dumpers») внешний дампер получает доступ к игре
  после вылета, если до вылета он не открывает её с доступом к памяти
  (crashpad так делает начиная с 0.7.4, у нас 0.12.2) и имя его exe
  начинается с `CrashReport`. После вылета EAC приостанавливает потоки игры,
  даёт дамперу прочитать память и через короткое время завершает процесс.
  Дампер, открывающий игру сразу при старте, вызвал бы зависание и закрытие
  игры. `crashpad_wer.dll` рядом с хендлером ловит через WER вылеты, которые
  Windows завершает в обход игры.
- **Вылеты, которые ловит движок (Windows).** `tier0` движка запускает код
  под своим `__try/__except` (`CatchAndWriteMiniDump*`): такой вылет не
  доходит до необработанного исключения, `tier0` пишет свой дамп и
  завершает процесс. Мы подменяем его функцию дампа (`SetMiniDumpFunction`)
  и отдаём настоящие вылеты crashpad. Без этого игроки присылали только
  «ended without a clean shutdown».
- **Запасной вариант (Windows).** Если хендлера нет или задан
  `-crashinprocess`, minidump пишет сама игра
  ([crashreport_native.cpp](../../src/game/shared/crashreport/crashreport_native.cpp))
  в `crashreports/`. При следующем запуске игра отправляет их, ждёт до 8 секунд
  и удаляет. Хранится не больше 5.
- **Ключи запуска.** `-nosentry` выключает всё, `-sentrydsn <dsn>` отправляет
  отчёты этого запуска на другой DSN, `-sentrydebug` включает подробный лог
  Sentry в консоли, `-crashinprocess` включает запасной вариант.

## Подключение

### 1. Bugsink на Coolify

Ресурс Docker Image `bugsink/bugsink:latest`, порт 8000, домен, например
`crash.team-frontress.org`. **Постоянный том на `/data`** обязателен, иначе
база пропадёт при пересоздании контейнера.

```
SECRET_KEY=<openssl rand -base64 50>
CREATE_SUPERUSER=admin@team-frontress.org:<пароль>
BASE_URL=https://crash.team-frontress.org
BEHIND_HTTPS_PROXY=True
FEATURE_MINIDUMPS=True
```

После первого входа уберите `CREATE_SUPERUSER`. Создайте команду и проект
(например `game`) и скопируйте DSN со страницы проекта.

### 2. DSN в игру

Одна строка в
[crashreport.cpp](../../src/game/shared/crashreport/crashreport.cpp):

```cpp
#define FRONTRESS_SENTRY_DSN "https://<ключ>@crash.team-frontress.org/<id>"
```

Без DSN игра ничего не отправляет. Чтобы проверить сервер до сборки,
запустите игру с `-sentrydsn <dsn> -sentrydebug`.

### 3. Символы (имена функций в стеке)

Без символов в отчёте будут только модуль и смещение. CI загружает PDB/.dbg
и сами DLL после каждой сборки (job `crash-symbols`), если в настройках
репозитория заданы:

- variable `CRASH_SERVER_URL`: `https://crash.team-frontress.org`
- variable `CRASH_SERVER_PROJECT`: slug или id проекта
- secret `CRASH_SERVER_TOKEN`: токен Bugsink с правом `debug-files:upload`

Символы TF2-движка (`engine.dll`, `materialsystem.dll` и т. д.) Valve не
публикует. Для них в стеке будут только смещения, но имя модуля уже говорит,
где упало.

### 4. Проверка

1. Соберите клиент с DSN и запустите на Windows через Steam (с EAC).
2. В консоли должна появиться строка
   `Crash reporting on (frontress@..., crashpad).` и в диспетчере задач
   процесс `CrashReportHandler.exe`. Игра не должна зависать на старте.
3. Уроните игру. Выход через `quit` не годится: это чистое завершение.
4. В Bugsink появится событие с minidump, а после следующего запуска —
   «Console output before the last crash» с консолью в разделе Extra.

## Ограничения

- Minidump в Bugsink помечен как экспериментальный. Если символизация
  Windows-стека не заработает, скачайте `.dmp` из события и откройте его в
  WinDbg/Visual Studio с PDB из артефакта CI `debug-client-windows`.
- «ended without a clean shutdown» не отправляется, если в базе crashpad
  есть дамп новее файла консоли прошлой сессии: так вылет узнаётся и на
  macOS, и через WER, где отметки sentry о вылете нет.
- Пути считаются от папки exe (Linux: `/proc/self/exe`, macOS:
  `_NSGetExecutablePath`), а не от рабочей папки.
- В запасном варианте порча кучи и `__fastfail` проходят мимо обработчиков:
  придёт только событие о завершении без дампа.
