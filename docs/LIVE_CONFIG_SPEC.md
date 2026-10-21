# CFG-S04 — конфигурация live/Gateway

2026-10-21. Технический проект для текущего scope D38. Заменяет активную часть [предыдущего draft](CONFIG_CLI_DRAFT.md). Команды и поля — проектные интерфейсы, ещё не реализованы. История и загрузка пропусков не входят в этот документ.

## Команды и исполнение

| Команда | Назначение |
|---|---|
| `extractor live --config network.yml` | Запуск выбранного native клиента со встроенным live-адаптером |
| `extractor gateway --config network.yml` | Gateway одной сети, доставка в NATS |
| `extractor config check --config network.yml --role live` | Проверка YAML и локальных предпосылок выбранной роли: live либо gateway |

Единая CLI-точка входа выбирает установленную native сборку; она не объединяет Go/Rust-клиенты в один бинарник. Hooks/Inspector работают внутри native исполнения. Команда не добавляет hooks к произвольной уже запущенной upstream ноде и не открывает её занятый datadir. Launcher передаёт сигналы дочернему процессу и не оставляет его работать после своего завершения.

CLI не скачивает клиенты, не создаёт pods и не вызывает API Kubernetes/Docker. Окружение запускает независимые процессы каждой сети. Добавление сети не меняет настройки других сетей. Native supporting clients запускаются окружением, не загрузчиком. Команд history/resume/apply и параметров диапазона блоков нет.

## Общая схема YAML v1

Один YAML — одна сеть. Изменения применяются рестартом соответствующей роли, watcher отсутствует. Неизвестные и дублирующиеся ключи запрещены на всех уровнях. Нет скрытой подстановки environment или произвольных CLI overrides. Относительные пути разрешаются относительно YAML-файла.

| Поле | Тип | Обязательность / значение |
|---|---|---|
| `schema_version` | integer | Обязательно, ровно `1` |
| `network.profile` | enum | Обязательно, один из пяти профилей ниже |
| `native.kind` | enum | Обязательно live, соответствует профилю |
| `native.launch_args_file` | path | Обязательно live, YAML-массив строк native аргументов |
| `native.datadir` | path | Обязательно live, единственный writable datadir клиента |
| `delivery.gateway_endpoint` | URI | Обязательно live: `unix:///absolute/path` либо `https://host:port` |
| `delivery.tls` | tagged object | Обязательно live: disabled для UDS, mutual для TCP |
| `storage.live_dir` | path | Обязательно live: каталог RocksDB адаптера |
| `storage.gateway_dir` | path | Обязательно gateway: отдельный каталог RocksDB |
| `gateway.listen` | URI | Обязательно gateway: UDS либо TCP, те же формы адреса |
| `gateway.tls` | tagged object | Обязательно gateway: disabled для UDS, mutual для TCP |
| `gateway.nats.server` | URI | Обязательно gateway: текущий единственный NATS instance |
| `gateway.nats.tls` | tagged object | Обязательно gateway: disabled, server либо mutual |
| `gateway.nats.credentials_file` | path | Optional gateway; отсутствие — без credentials |
| `runtime.log_level` | enum | Optional: debug/info/warn/error, default info |

Объекты TLS: `mode: disabled` запрещает дополнительные поля; `server` требует `ca_file`; `mutual` требует `ca_file`, `cert_file`, `key_file`. Проверка имени сервера обязательна при TLS. Для NATS disabled соответствует `nats://`, остальные режимы — `tls://`. Server-only TLS для адаптер↔Gateway не разрешён. UDS защищается правами файловой системы. Противоречивые режимы — ошибка до запуска.

Структура всего документа проверяется целиком. Обязательные поля, локальные файлы и credentials проверяются только для запускаемой роли. Live не требует чтения NATS credentials; отсутствующая секция другой роли допустима. Секций ClickHouse, history, coverage и startup-уведомлений нет.

## Native-профили

| network.profile | native.kind | Выбранная реализация |
|---|---|---|
| ethereum-mainnet | geth | Go/Geth: native hooks/state journal, canonical notifications |
| bsc-mainnet | bsc | Go/BSC: tx execution и Parlia Finalize |
| base-mainnet | base-reth | Rust/Base: executor, Inspector, canonical ExEx |
| arbitrum-one | nitro | Nitro Go и native зависимости: ArbOS/Stylus execution paths |
| polygon-pos-mainnet | bor | Go/Bor: tx execution, StateSync/Finalize; supporting Heimdall |

Сборки и toolchains закрепляются по [CLIENT_MATRIX](CLIENT_MATRIX.md) и [COMPONENT_STACK](COMPONENT_STACK.md). YAML не выбирает произвольный executable/version и не добавляет новый тип блокчейна без адаптера. Исторические Classic/replay профили сейчас не предлагаются. Эта таблица использует ранее собранную документацию, не утверждает совместимость по результатам runtime-тестов.

**Общий интерфейс, native параметры.** `native.launch_args_file` — YAML sequence строк, передаваемых отдельными argv без shell/eval/glob expansion. Штатный конфиг клиента может подключаться его native аргументом. Настройки consensus/rollup/Heimdall соединений и JWT остаются в штатной конфигурации выбранного клиента. Это не универсальный `rpc_url` и не RPC-замена native capture.

Профильная обвязка добавляет network selector, datadir и параметры адаптера. Пользовательские аргументы и штатный конфиг не могут переопределить их. Проверяется эффективная конфигурация parser-ом закреплённой версии клиента, включая aliases; конфликты дают ошибку, не «последний параметр победил». До открытия цепочки сверяется profile/chain identity. Неподдержанная форма native конфигурации явно отвергается.

Для каждого профиля при реализации потребуются native args/config templates и проверка их загрузки настоящим клиентом. Имена проектных полей выше не выдаются за существующие upstream флаги. Реальные native флаги и различия их синтаксиса остаются ответственностью профильной обвязки. Новое исследование версий и сборки в этом шаге не выполнялись.

## Состояние и запуск

1. Проверить CLI/YAML, пути, типы, профиль и эффективную native конфигурацию до открытия writable состояния. Ошибка не должна частично применять настройки.
2. Эксклюзивно открыть состояние owner. Native datadir, live_dir и gateway_dir не могут совпадать или быть вложены друг в друга; symlinks разрешаются перед сравнением. Второй writer получает ошибку.
3. Сверить сохранённые network/profile, роль, wire schema/build identity. Несовместимый WAL не удалять и не переинициализировать молча. Сеть нельзя сменить, сохранив прежний каталог.
4. Восстановить pending delivery по [RocksDB-протоколу](ROCKSDB_DELIVERY_PROTOCOL.md), сохраняя IDs, bytes и parts. Приём новых live данных в WAL не должен ждать доступности NATS.
5. Активировать live capture после готовности native источника. Начальная синхронизация ноды не считается заказом на выгрузку её прошлых блоков. Native адаптер согласует момент активации и подписку на head так, чтобы не терять события между ними. Конкретные readiness условия проверяются для каждого клиента на этапе реализации.
6. Получать native события, нормализовать, сохранять в WAL, передавать через Gateway до NATS PubAck. Не обходить диапазон старого checkpoint→head. Восстановление конкретного незакрытого capture, текущие reorg/finality сохраняют существующий контракт.

Не вводятся TTL, квота pending или удаление при недоступности NATS. Физическая ошибка записи явная; WAL_SAVED не объявляется без durable записи. Готовность источника и готовность публикации — разные состояния.

## Валидация и остановка

Ошибка содержит код, путь поля и причину, без секретов и полного дампа native argv. `config check` не создаёт DB/stream/run, не запускает ноду, не публикует данные и не обещает доступность сети. Online handshake выполняется при запуске выбранной роли.

Проектные exit codes: 0 — успешный check/штатное завершение; 2 — CLI/YAML/native config; 3 — занятое или несовместимое состояние; 4 — неподдержанная сборка/несовместимый источник; 5 — невосстановимая runtime/дисковая ошибка. Исходная native ошибка сохраняется в диагностике. Недоступность Gateway/NATS вызывает retry, не успешное завершение.

SIGINT/SIGTERM прекращает новые чтения и закрывает состояние, сохраняя durable pending; CLI возвращает 130/143. Остановка не ждёт бесконечно NATS PubAck. STAGING восстанавливается по протоколу; неподтверждённый payload не удаляется. SIGKILL восстанавливается следующим запуском как crash.

Будущие проверки: одинаковые команды всех профилей; неверные kind/chain; неизвестные/дублирующиеся ключи; TLS mismatch; недоступные credentials; конфликты native args; общий/занятый datadir; pending при остановке и NATS outage; отсутствие стартовой истории; изоляция сетей. Это требования к будущим тестам, не проведённые проверки.

Численные настройки buffers/chunk/concurrency и ограничения payload согласуются с протоколом и E00-04; произвольные defaults здесь не вводятся. Версии библиотек YAML/launcher и совместная сборка закрепляются на этапе реализации, без изменения native toolchains по умолчанию.

## Результат CFG-S04

Технический проект live/Gateway подготовлен. CFG-R4 снят, вопросов об истории нет. Следующий шаг CFG-S05 — сверить основной ТЗ, архитектуру и критерии, включая запуск native клиента через общую CLI и момент включения live capture. Эти технические предложения не подменяют пользовательские решения и runtime evidence. Реализация остаётся вне разрешённого scope.
