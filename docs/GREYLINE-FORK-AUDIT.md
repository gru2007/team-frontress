# Аудит форка GREYLINE относительно upstream

Дата аудита: 2026-08-20.

База сравнения: `upstream/tc2-mod` (`05d2324d`) против `gf-mod` (`921bae9e` до
исправлений этого прохода). Форк был на 16 коммитов впереди upstream: 116
измененных файлов, около 32 474 добавленных строк и 40 удаленных.

Аудит покрывает только изменения форка и их точки интеграции с TC2. Это не
обещание, что во всем исходном коде Source/TC2 нет старых ошибок.

## Главная причина наблюдавшихся симптомов

У матча нет одного владельца жизненного цикла. Сейчас путь разделен между:

1. C++ сервера: roster, briefing, muster, score convars.
2. HTML главного меню: запуск P2P, console commands, game-over polling, HTTP result.
3. `greyline-agent`: RCON, UDP Source log, HTTP result для dedicated server.
4. Coordinator: применение результата к войне и формирование следующего боя.

До этого прохода любой разрыв оставлял coordinator в `live`, а штатный TF уже
запускал intermission и обычный mapcycle. Дополнительно SteamID64 преобразовывался
в JavaScript `Number` и округлялся, поэтому roster не узнавал реального игрока.

## Исправлено в этом проходе

| Приоритет | Проблема | Исправление |
| --- | --- | --- |
| P0 | SteamID64 округлялся в JS | ID идет decimal string через HTTP, roster и player views; добавлен round-trip test на `76561198317961869` |
| P0 | Игра использовала ручной или случайный ID | `greyline_identity` получает ID из `ISteamUser`; RANDOM ID удален |
| P0 | `auth.mode=webapi` не получал ticket | C++ создает Steam auth session ticket, отдает его активному menu client и отменяет при shutdown |
| P0 | P2P-результат вечно ждал скрытый quorum | quorum, endpoint, event и UI подтверждения удалены; result завершает бой сразу |
| P0 | Одна HTTP-ошибка навсегда теряла dedicated result | agent хранит pending result и повторяет POST с backoff до ACK |
| P0 | P2P result помечался отправленным до HTTP ACK | `resultReported` выставляется только после успешного POST |
| P0 | Map cfg перезаписывал win conditions | agent повторно применяет полный battle contract после `KindMapStarted` и только затем сообщает `ready` |
| P0 | Режим наследовал convars предыдущей карты | каждый mode profile сначала обнуляет `mp_winlimit`, `mp_maxrounds`, `mp_timelimit`, `mp_windifference` и arena queue |
| P0 | Stock votes могли kick/nextlevel/extend/change crits | `sv_allow_votes 0` задается в agent/P2P и поддерживается server-side watchdog |
| P0 | P2P бой рекламировался master server | advertisement выключается до карты, после карты и server-side watchdog |
| P0 | Штатный mapcycle забирал сервер | battle задает `nextlevel`, увеличивает `mp_chattime`; dedicated уходит на idle map, P2P удерживает назначенную карту до закрытия |
| P0 | Console RPC молча принимал неизвестные команды | RPC возвращает `{ok,error}`, запрещает CR/LF/`;`; UI останавливает host boot при ошибке |
| P0 | Late roster failure все равно отправлял игрока | seating откатывается, игрок остается в очереди; добавлен regression test |
| P0 | Команды late roster читались слишком поздно | P2P дренирует mailbox до `ready`, затем запускает постоянный poll |
| P1 | Параллельные бои одного front применялись в разном порядке | до появления front revision/aggregation разрешен один unresolved battle на front |
| P1 | P2P host управлял strategic weight через `players` | coordinator вычисляет вес из собственного roster, а не HTTP-поля host |
| P1 | Muster считал strangers/spectators как roster | учитываются только SteamID активного roster |
| P1 | Arena отвергала waiting-for-players | stock arena guard пропускает активный `greyline_battle_id` |
| P1 | Gate пропускал неизвестный ID навсегда | после появления ненулевого SteamID periodic enforcement удаляет verified stranger |
| P1 | `protocol_version` можно было не отправлять | hello требует точного совпадения версии |
| P1 | Testbed unit использовал несуществующий `-world` | аргумент и мертвый env удалены, ошибка systemd больше не подавляется |
| P1 | Testbed публиковался с известным pool key | setup создает отдельный случайный `GREYLINE_POOL_KEY` |
| P1 | Build job сохранял repository credential для тестируемого кода | добавлены read-only permissions и `persist-credentials: false`; Greyline job больше не получает PAT |
| UX | В Greyline menu не было основных действий | добавлены Resume, Loadout, Armory, TC2 Settings, Legacy Settings, Servers, Disconnect, Quit через typed C++ RPC |
| UX | Игрок не видел состояние систем | добавлены Steam/auth/queue/battle/game/P2P diagnostics и queue position/needed counts |
| UX | Join принимал background/любую карту за успех | success требует non-background connection на назначенную map |

## Открытые P0 проблемы

### 1. Нет authoritative terminal lifecycle в server C++

`src/game/server/greyline/greyline_hoststate.cpp` публикует score, но не создает
атомарную terminal запись `{battle_id, map, winner, score, participants}` и не
владеет переходом `round/game over -> result accepted -> idle/next assignment`.
P2P все еще зависит от client event counter в
`src/game/client/greyline/greyline_menu_rpc.cpp`, а dedicated от UDP log.

Риск: потерянное событие, reset score при level transition или закрытое меню
оставят бой в неправильном состоянии. `nextlevel` и retries уменьшают риск, но
не заменяют state machine.

### 2. P2P result сейчас доверенный

Quorum удален намеренно: без полноценного UI, reconnect recovery, отрицательных
голосов и immutable witness set он только замораживал завершенные матчи. Теперь
владелец P2P-сервера может подделать outcome/score.

Нужное решение: server-generated result digest, Steam-authenticated participant
evidence или trusted relay/plugin. Нельзя возвращать старый скрытый quorum.

### 3. Server command mailbox остается at-most-once

`internal/pool.Poll` удаляет command до явного ACK клиента. Потеря HTTP response
после чтения может потерять assignment, roster или abort. Исправлен rollback при
явной ошибке enqueue, но нет command ID, persistent pending queue и idempotent ACK.

### 4. Web RPC trust boundary небезопасна

`src/game/shared/gamestate/gamestate.cpp` по-прежнему:

- принимает WebSocket без session nonce и надежной Origin проверки;
- назначает privilege первому соединению;
- рассылает return не только исходному connection;
- оставляет generic `cmd`, `setcvar` и `fetch` шире необходимого.

Steam ticket и server token пока проходят через этот bridge/JS. Identity и menu
actions перенесены в C++, но coordinator networking и host FSM еще нет.

### 5. War event durability не транзакционна с match finalization

`finalizeResultLocked` переводит match в `finished` до гарантированно успешного
`war.RecordBattle`. Ошибка append/fsync может оставить finished match без war
event. В `internal/war/log.go` fsync ambiguity также способна дать разное
состояние до и после restart.

## Открытые P1 проблемы

| Подсистема | Проблема | Основные файлы |
| --- | --- | --- |
| Result | Финальный P2P score не latch-ится атомарно при game-over | `greyline_hoststate.cpp`, `greyline_menu_rpc.cpp` |
| Result | Agent реагирует на `Game_Over` раньше final score lines; fallback rounds не всегда равен score | `cmd/greyline-agent/battle.go`, `tf_eventlog.cpp` |
| Result | UDP Source log не аутентифицирован и не фильтруется по source address | `cmd/greyline-agent/main.go`, `internal/srclog` |
| Presence | `Slot.Connected` не обновляется, authenticated participant set отсутствует | `internal/mm/match.go`, `mm.go` |
| Pool | Deregister/re-register server ID может потерять active match/generation | `internal/pool/pool.go` |
| Pool | Race Send против close(mailbox) может panic | `internal/pool/pool.go` |
| Pool | P2P server после Release временно попадает в reusable free pool | `internal/pool/pool.go`, `internal/mm/mm.go` |
| Pool | `Server.Capacity` не участвует в обычном Reserve/top-up | `internal/pool/pool.go`, `internal/mm/mm.go` |
| State | Result принимается до строгого terminal transition; повторный `live` может двигать timeout | `internal/mm/mm.go` |
| Join | Join проверяет назначенную map, но еще не проверяет фактический remote endpoint/server SteamID | `greyline_menu_rpc.cpp`, `greyline.html` |
| P2P boot | Host capability заявляется до Steam/FakeIP preflight | `greyline.html`, `greyline_hoststate.cpp` |
| Races | Player/Public и Pool/Get возвращают данные с непоследовательной mutex ownership | `internal/mm/player.go`, `internal/pool/pool.go` |
| Security | `security.*`, signed results и integrity policy подключены только к retired legacy path | `internal/security`, `internal/legacy/gc`, `cmd/coordinator` |
| Security | HTTP API не имеет встроенного TLS; pool/session/password tokens идут plaintext без reverse proxy | `internal/httpapi`, deployment docs |
| Security | Non-loopback admin listener разрешен без admin key и содержит write endpoint drain | `internal/config`, `internal/httpapi` |

## Открытые P2 проблемы

| Подсистема | Проблема |
| --- | --- |
| Match rules | Видимый `Round_Win` не равен battle end для 5CP/KOTH/CTF; UI должен показывать round progress до winlimit |
| Map rotation | Planned maps параллельных fronts не резервируются; rotation обновляется только после result |
| Map selection | Очередь выше всех envelopes выбирает smallest map вместо largest capacity |
| War UI | Partial push headline может говорить `STAGE COMPLETE`, хотя stage не изменился |
| Contracts | Initial assignment может выдать contract игроку без `AcceptContract`; top-up ведет себя иначе |
| Election | Latency oracle равен `nil`; RTT/SDR pop/dedicated bonus и часть history полей декоративны |
| Config | Migration/reconnect/abandon/max migrations и часть security knobs относятся только к legacy |
| Config | Некоторые явные нули заменяются default из-за проверок `> 0` |
| Legacy | Две большие реализации coordinator/game расходятся и создают ложное покрытие |
| Proto | CI генерирует временный C++, но не проверяет committed Go protobuf на соответствие schema |
| Tests | Нет agent integration tests с fake RCON/UDP/HTTP failures |
| Tests | Нет behavioral tests menu/CEF/WebSocket и полного game-side C++ build gate |
| Release | Coordinator, agent, theater и service files не входят в publish artifacts |
| State files | `war-events.jsonl` и legacy player state закоммичены как mutable runtime data |
| Launcher | `SLR_SNIPER_PATH` в `game/tc2.sh` остается незакавыченным |

## Открытые P3 и визуальные ограничения

- Uniform swap не меняет team-named particles: rockets, stickies, beams, crit,
  burning и около 190 аналогичных call sites.
- HUD, scoreboard, name colors, map signs, respawn visualizers и cart не отражают
  war-side отдельно от RED/BLU game team.
- Viewmodel fallback может снова выбрать skin по исходной game team.
- Host reputation хранится только в памяти coordinator.
- Host migration не реализована: исчезновение P2P host abort/requeue.

## План завершения

### Этап 1. Authoritative game lifecycle

1. Добавить server-side `CGreylineMatchLifecycle` с assignment generation.
2. Latch terminal result в C++ вместе с battle ID, map, score и SteamID presence.
3. Запретить stock map transition до result ACK с bounded fallback timeout.
4. Для dedicated заменить UDP как authority на authenticated plugin/agent channel.
5. Acceptance: закрытое меню и временно недоступный coordinator не теряют result;
   после восстановления один battle создает ровно один war event.

### Этап 2. Надежный transport

1. Ввести command ID, lease generation, poll без удаления и explicit ACK.
2. Сделать assignment/roster/result idempotent и persistent до ACK.
3. Удалять P2P server после battle вместо возврата в free pool.
4. Проверять server capacity и authenticated presence.
5. Acceptance: fault-injection на потерю каждого HTTP response не меняет итог.

### Этап 3. Убрать секреты и host FSM из JS

1. Выполнять hello/fetch/host-register/poll/result в C++.
2. Оставить HTML только presentation war map и несекретные preferences.
3. Ввести nonce-bound per-panel RPC и per-connection returns.
4. Удалить generic unprivileged `cmd`, `setcvar`, sensitive `fetch`.
5. Acceptance: внешний localhost WebSocket не может получить token или выполнить
   command; Legacy VGUI actions работают без HTML host logic.

### Этап 4. Result trust без скрытого голосования

1. Заморозить authenticated participant set на terminal event.
2. Подписывать result digest server lease key или собирать автоматические
   Steam-authenticated attestations без ручной кнопки.
3. Добавить explicit disputed/unwitnessed states и operator workflow.
4. Acceptance: host не может заменить result/headcount; offline witness не
   оставляет match в `live` навсегда.

### Этап 5. War correctness и cleanup

1. Добавить front revision и коммутативную агрегацию до возврата parallel battles.
2. Исправить map reservations, fallback capacity, contracts и partial headlines.
3. Разделить active/legacy config; удалить retired production code после переноса
   нужных identity/lifecycle частей.
4. Сделать durable transaction result -> war event -> match finished.

### Этап 6. Build, security и release

1. Полный Linux/Windows game build в CI и agent integration fault tests.
2. Закрыть workflow PAT, admin listener, TLS deployment и runtime state files.
3. Публиковать coordinator/agent/config/theater вместе с game artifacts.
4. Добавить CEF UI tests для identity, join, P2P boot и reconnect.
