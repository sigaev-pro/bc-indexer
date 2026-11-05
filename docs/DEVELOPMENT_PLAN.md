# Последовательность разработки и задачи

D64: этот документ описывает порядок разработки проектируемой системы как часть ТЗ. Реализация никогда не входит в нашу работу; E01–E07 не являются исполняемым backlog агента. [Gates](IMPLEMENTATION_GATES.md) и [63 требования](ACCEPTANCE_REQUIREMENTS.md) задают проверяемое содержание ТЗ. 41/41 находки аудита SPECIFIED; runtime NOT_RUN.

Тематическое представление [единого ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md); изменения нормативного содержания синхронизируются с root в том же commit.

## Правила выполнения

По D64 единственная цель нашей работы — идеальное ТЗ; реализации не будет никогда. Этапы E01–E08 ниже — спецификация порядка разработки и приёмки описываемой системы, не поручение их выполнять. Версии клиента, библиотек, runtime, хранилищ и инфраструктуры в ТЗ нельзя выбирать по наличию на машине.

Каждая задача имеет постоянный ID. Статусы: TODO, IN_PROGRESS, BLOCKED, DONE, DONE_DOCS_ONLY, DEFERRED_BY_USER, OPTIONAL, REMOVED/OUT_OF_SCOPE. По умолчанию незавершённая задача без явной пометки — TODO; deferred/optional не входят в обязательный gate. DONE требует артефакта и evidence по IMPLEMENTATION_GATES. Перед началом записать ID в PROJECT_STATUS; после завершения сохранить результат, проверки, следующий шаг и commit. SPECIFIED документации не является DONE реализации.

Крупную задачу перед исполнением разбивать на подзадачи с суффиксами .1, .2 и отдельными критериями; уже присвоенные ID не переиспользовать. Коммиты, публикации и деплой фиксировать только после фактического выполнения с hash/URL и результатом проверки. Сохранение файлов не означает наличие удалённой резервной копии.

## Принятая последовательность

E00 Решения → E01 Native-захват трёх сетей → E02 Общая типизированная схема → E03 Независимая доставка каждой сети через собственные WAL/Gateway в NATS → E04 Полные детали транзакций → E05 Reorg и восстановление каждой сети → E06 Live-конфигурация и доставка → E07 Функциональная приёмка трёх сетей → E08 Другие сети. История отложена, не блокирует цепочку.

Принято D16: конечный результат — унифицированные сообщения в NATS.IO. Последующая обработка исключена из этапов, стека и приёмки. Проверяющий считыватель — только тестовый инструмент.

Действующая граница D36–D42: live без полного snapshot адресов, без истории и поиска пропусков; хронологические связи и детали сохраняются в сообщениях. Open-source намерение сохраняется, лицензия/публикация отложены D41. YAML live/Gateway и применение перезапуском описаны в LIVE_CONFIG_SPEC по D39/D48; найденные технические пробелы не возвращают закрытый выбор hot reload.

Принято D42: первый контур — Ethereum Mainnet, BSC Mainnet и Polygon PoS. Все три проверяются в E01 до общей схемы E02; Base/Arbitrum отложены.

Задачи текущего этапа выполняются отдельно для ETH/BSC/POLY; родитель закрывается после трёх сетей. Подзадачи .BASE/.ARB отложены D42. Исторические задачи отложены D38. Каждая сеть имеет собственные WAL, журнал Gateway и процессы; evidence хранится раздельно.

Приняты D14/D15/D19: отдельный native node/adapter/WAL/Gateway каждой сети, общий NATS с изолированными streams/subjects. Команда N–M отложена D38; текущий live YAML и restart приняты D39/D48.

Принято D36: основной сборщик только live, history отдельным сервисом на узле ноды по N–M. Default три часа и история через start_block/reload/apply отменены. Единый интерфейс без Kubernetes-зависимости, независимые сети и RocksDB/ACK сохраняются. D37 закрывает CFG-R3: поиск пропусков и организация их восполнения вне scope; pending WAL доставляется автоматически.

## Стек по этапам

| Этап / компонент | Стек | Способ реализации | Что ещё выбрать |
|---|---|---|---|
| E00 документация | Markdown, журнал решений, короткие ADR | Документы с ID и точкой продолжения | D52: локальный Git принят, remote/backup не требуются |
| E01–E07 Ethereum | Go + Geth v1.17.9, source seams проверены | Native capture/pin/decision bridge; отдельный build | Toolchain/build proof; Prysm выбран, runtime ещё не проверен |
| E01–E07 BSC | Go + BSC v1.7.8, source-verified candidate | Обычный execution + Parlia system tx | Live fixtures/recovery inputs и отдельный build proof |
| E01–E07 Polygon PoS | Go Bor v2.10.3 + Heimdall-v2 v0.13.2 | Последовательный native capture D58/D60, StateSync scopes; legacy outside-tx optional | Build/runtime и ARTIFACT-01, live source inputs; historical fixtures deferred |
| E02 общий контракт | Protocol Buffers v3, Go bindings | Схемы из v3 после проверки модели; независимые валидаторы и fixtures | Версии protoc/plugins; нужен ли Buf; стратегия schema compatibility |
| E03 Gateway | Go 1.27.2, gRPC-Go 1.84.0, nats.go 1.54.0 — кандидаты .03 | Отдельный сервис сети; [стек](COMPONENT_STACK.md) и [владельцы состояния](STATE_OWNERSHIP.md) | Совместная сборка E01; fencing/storage определены .06, тесты E03/E05 |
| E03 локальный WAL / journal | RocksDB 10.4.2; grocksdb v1.10.2 | Sync WriteBatch, READY/ACKED cleanup, immutable plan и release | [Pins](research/ROCKSDB_STACK.md), [REC-T01–17](ROCKSDB_DELIVERY_PROTOCOL.md); build/runtime tests E01/E03 |
| E02–E07 тестовый считыватель | Go + protobuf bindings | Проверка bytes, полей, частей и lifecycle на выбранных примерах в NATS против native fixtures | Только приёмочный harness, не промышленный сервис обработки |
| E00–E07 пользовательская конфигурация | Strict YAML live/Gateway + штатные native configs | LIVE_CONFIG_SPEC и DEPLOYMENT_BOOTSTRAP_CONTRACT | Restart принят; schema/parser/build smoke E01/E06, hot reload не открыт |
| DEFERRED_BY_USER D38: native backfill | Native executors | Диапазоны/history CLI вне текущей поставки | Не prerequisite E01–E07 |
| E06–E07 окружение | Compose/Kubernetes, один NATS replicas=1 | Полный комплект и bootstrap по DEPLOYMENT_BOOTSTRAP_CONTRACT | Immutable builds/images и функциональные fixtures; hardware/performance thresholds и post-ACK HA вне scope |
| E08 дополнительные сети/клиенты | Go или Rust по нативному клиенту | Повторное использование проверенного контракта; собственные execution/canonical integrations | Приоритет после первого контура, commits, runtime и покрытие |

Матрица конкретных клиентов и проверенных исходников — [CLIENT_MATRIX.md](CLIENT_MATRIX.md); это source-verified рекомендации, не проверенные сборки. Основания механизмов и история — DECISIONS.md. Внутренние WAL/journals и Rust crates остаются кандидатами. Прикладная обработка после NATS не входит.

## E00 — согласование и восстановимые документы

Зависимости: нет. Стек: Markdown. Результат: утверждённые требования, стек и порядок с незакрытыми рисками, явно вынесенными за принятую поставку.

- [x] E00-01 Проверить исходные файлы, локальные инструменты и первичные документы. Артефакт: факты в DECISIONS.md; ограничения окружения отмечены отдельно от проектных решений.
- [x] E00-02 Завершить исходную рамку: набор пяти сетей, YAML/история/live и сохранение действий приняты; точные mainnets подтверждены D13, Q1a закрыт. Восстановление вынесено в E00-06, новые уточнения команды и независимых процессов — в E00-09. Исторический результат исследования сохранён; текущий состав ограничен D42.
- [x] E00-03 Архитектурный блок .01–07 завершён: компоненты, pins/candidates, интеграция, владельцы состояния, доставка и проверки сведены в [итоговой матрице](E00_03_ARCHITECTURE_REVIEW.md). Не закрывает CFG/OSS/приёмку всего ТЗ и не разрешает реализацию.

Декомпозиция и результаты: [E00_03_COMPONENT_PLAN.md](E00_03_COMPONENT_PLAN.md). Подэтапы: E00-03.01 карта компонентов → .02 граница поставки → .03 стек/интеграции → .04 владельцы состояния → .05 live/history → .06 доставка/сбои → .07 итоговая сверка. Это завершённый исторический архитектурный блок. REC выбран D26–D33; E00-07/CFG закрыт D39, история/E00-09 отложены D38. Позднейшие технические уточнения D52/D54 и [аудит](DESIGN_AUDIT_2026_11_04.md) не являются выполненными runtime-проверками.

- [x] E00-04 Согласовать последовательность и границы приёмки по D40: независимые WAL/процессы сетей, live-only; performance requirements исключены, production-методика отложена. Не выбранные supporting profiles явно остаются E00-11.
- [x] E00-05 D52/D64: локальных Git-коммитов документации достаточно; remote/дополнительный backup не требуется. Постоянная цель — только ТЗ, без перехода к реализации.
- [x] E00-06 REC завершён: D26–D31 определяют restart с диском, новый run/ручной N–M при потере диска, накопление без TTL/квоты, один NATS, очистку после ACK. .06 закрепляет протокол; HA/backup и post-ACK retention вне scope, RTO не обещается.
- [x] E00-07 Согласовать live YAML/CLI: LIVE_CONFIG_SPEC, CFG-S01–05 и D39. Ноду запускает окружение, live с H+1 после синхронизации; pending WAL независимо. История отложена; реализация не выполнена.
- [ ] E00-08 DEFERRED_BY_USER (D41): лицензия, публичный repository и release workflow (OSS-01/02) отложены; завершение текущего ТЗ не блокируют, публикация не разрешена.
- [ ] E00-09 DEFERRED_BY_USER (D38): история, диапазоны и history CLI/YAML отложены.

- [x] E00-10 Привести ТЗ и план к D16. Готово: граница NATS, последующие компоненты сняты, R07 и точка продолжения сохранены.
- [x] E00-11 DONE как документация: действующие решения, все 41 findings SPECIFIED, native/message/delivery/bootstrap contracts, DEP-04, IMPLEMENTATION_GATES и единое root v3. Source inspection не является runtime proof; E01–E07 остаются содержанием ТЗ, их выполнение не входит в работу по D64.

- [x] E00-12 Зафиксировать D17–D20 в ТЗ, архитектуре и плане. Готово: полнота всех транзакций обязательна, системные балансные изменения optional, типизация/части/topology приняты, JOB-01 отложен.


- [x] E00-13 Проверить клиенты/стек/source capture paths всех пяти сетей. Готово: [CLIENT_MATRIX.md](CLIENT_MATRIX.md), три research-файла, immutable source pins и явные ограничения. Сборки/fixtures не выполнены и не требуются для закрытия исследовательского пункта. Исторический результат исследования сохранён; текущий состав ограничен D42.
- [x] E00-14 Подготовить карту native transaction types → общая схема и типизированные расширения. Результат: [TRANSACTION_TYPE_REGISTRY.md](TRANSACTION_TYPE_REGISTRY.md), [MESSAGE_CONTRACT.md](MESSAGE_CONTRACT.md); обязательные/неприменимые поля, engine kind, fork profiles, lifecycle и parts описаны. Это логическая модель; точные execution/finality extensions и runtime fixtures остаются E02, Classic/legacy — E00-15.
- [x] E00-15 Уточнить Classic native state/action capture и legacy Bor synthetic representation. Результат: [Classic](research/ARBITRUM_CLASSIC_CAPTURE.md), [Polygon legacy](research/POLYGON_LEGACY_CAPTURE.md); native источники/pins, typed profiles, limitations и fixtures CL-01…08/POLY-L01…07 определены. Runtime-покрытие, archive availability и consensus-neutral observer не доказаны и остаются E01/AP-06/E02/E04/E06. Историю нельзя молча отсечь; legacy вне-транзакционный scope optional по D17. Runtime-задачи Classic/истории отложены D38/D42 и не блокируют текущую версию.

## E01 — воспроизводимая сборка и доказательство захвата

Зависимости: E00; для всех трёх сетей определены варианты сети, кандидаты клиентов и границы прототипов. Стек: родной клиент и его язык для каждой сети. Минимальный тестовый execution harness, затем реальный client import path. Один общий Ethereum-прототип не закрывает E01.

- [ ] E01-01 Закрепить source/patch/toolchain/dependency/artifact pins, отдельные builds и минимальный fixture/bootstrap harness. Готово: clean build и команды, inputs/state/supporting services/test credentials известны до proof; нет скрытой авторской ноды. Подзадачи/manifest — IMPLEMENTATION_GATES.
- [ ] E01-02 Доказать карту native callers, admission/owned snapshots/state pins/decision markers всех трёх сетей по NATIVE_CAPTURE_PROTOCOL. Готово: обязательные типы/поля имеют источник; minimal bridge работает в реальном import path, bypass запрещён; outside-tx публикация optional.
- [ ] E01-03 Захватить transfer, nested call/revert, fee effects, родные системные транзакции и пустой блок. Готово: полные tx records/before/after сверены с независимым результатом. Вне-транзакционные начисления проверяются только при реализации расширения.
- [ ] E01-04 Проверить стабильные offsets и повтор fixture, power loss на ранних admission/pin/EXEC_READY/decision seams, native-only replay и H guard. Готово: независимый expected/evidence; ранняя часть REC-T01 доказана до E02, полный matrix остаётся E05.

- [ ] E01-05 Проверить извлечение поддержанных native типов на fixtures всех трёх сетей, включая failed/system для текущих live fork-профилей; исторические переходы отложены D38. Неизвестный тип даёт явную ошибку. Независимый production verifier состава каждого блока не требуется (D25).

Отложенный контекст D38/D42: Classic/исторические Polygon fixtures из E00-15 не обязательны для текущего live-этапа. Исследования и fixture IDs сохранены. В live остаются все действующие native tx/system types, включая Polygon StateSync, с корректной атрибуцией эффектов.

Gate: по каждой ETH/BSC/POLY доказаны обязательные observation/recovery seams и связь harness с реальным native path. Missing path/pin/decision barrier останавливает этап; E02 не строится на заведомо невосстановимом источнике. Полная семантика/transport matrix остаются E04/E05.

## E02 — модель событий и тестовый валидатор

Зависимости: E01, решения о гранулярности и порядке. Стек: protobuf, Go, детерминированные fixtures; пока без обязательного внешнего сервиса.

- [ ] E02-01 Зафиксировать transaction/system records, IDs, execution order, activation lifecycle и late metadata. Готово: таблица переходов и схемы покрывают выбранные fixtures без искусственных транзакций.
- [ ] E02-02 Создать компилируемые proto и versioned validators. Готово: bindings генерируются воспроизводимо; malformed/conflicting records отклоняются с конкретной причиной.
- [ ] E02-03 Реализовать тестовый валидатор событий. Готово: поля, apply/remove/reapply/status и стабильные IDs проверены на fixtures; промышленный consumer не создаётся.
- [ ] E02-04 Утвердить протокол v3 и правила совместимости. Готово: спорные поля из рабочей редакции разобраны для всех трёх сетей; сетевые различия имеют явное представление.
- [ ] E02-05 Определить модель транзакционных balance actions. Готово: причинные связи, rollback, fees и нулевой итог представлены без потери действий; самостоятельные системные эффекты — optional и не блокируют gate.
- [ ] E02-08 Добавить block metadata: hash, parent, height, native counts при наличии, lifecycle, включая пустые блоки. Локальный delivery batch связывает подготовленные сообщения с прогрессом доставки; без BlockComplete.

- [ ] E02-10 Завершить строгий общий контракт типов. Готово: versioned envelope, однозначный payload type, обязательные/неприменимые поля, зарегистрированные typed extensions, политика неизвестной версии и fixtures трёх сетей; произвольный JSON не заменяет обязательные поля. Свободные bytes только для исходных бинарных данных и транспортных частей.

Gate: модель позволяет восстановить корректные нативные балансы на выбранных сценариях и объяснить каждую отмену независимо от транспорта.

## E03 — надёжная доставка Adapter → Gateway → NATS JetStream

Зависимости: E02, решения о storage и отказах. Стек: Go, gRPC, выбранное durable storage, NATS JetStream; protobuf bytes из E02.

- [ ] E03-01 Реализовать RocksDB journals по DELIVERY_STATE_MACHINE: bounded closing commits, READY/SEALED, ACKED_CLEANUP/RELEASING, RecoveryRelease/frontiers, admission generations. Готово: pinned API/build и crash/reopen проверки, старый protocol — история и REC каталог.
- [ ] E03-02 Реализовать gRPC и формат размеров/частей по MSG-02. Готово: большие события и повторная публикация проверены без усечения; тест восстанавливает исходные bytes.
- [ ] E03-03 Подключить JetStream и сквозной ACK. IngestAck только после PubAck всех подготовленных сообщений/частей в одном logical destination durable plan; retry сохраняет ID, bytes и маршрут. D54: смена account/stream/subjects запрещена до завершения pending адаптера и Gateway; identity/reconnect/drain checks обязательны. Доставка at-least-once, дубли допустимы.
- [ ] E03-04 Реализовать накопление без TTL/прикладной квоты до NATS ACK, bounded RAM, retry и явные ошибки disk-full/config. Не удалять pending данные; post-ACK retention и HA не проектировать.
- [ ] E03-05 Пройти delivery часть REC-T01–17 и ATTEMPT/PREPARE/FRONTIER/FENCE/DRAIN/DEST/MEM/UPGRADE/POISON/ENOSPC от заданного durable intent/READY. Native admission proof уже в E01; совмещённый multi-block/native fault matrix — E05. History variants DEFERRED D38. Полный REC-T01 пока не отмечать PASS.

Gate: delivery-only guarantees и integration smoke с native fixtures трёх сетей доказаны отдельно. Тест сверяет expected fields/parts/ACK/bytes/destination; полный native crash matrix ещё TODO E05. Production completeness verifier не вводится.

- [ ] E03-11 Доставлять block metadata и lifecycle в NATS, включая пустые блоки. Проверять PubAck всех частей; независимая проверка полноты содержимого вне scope.

## E04 — полная транзакционная и балансовая семантика трёх сетей

Зависимости: E03. Стек: native hooks/journal/state APIs закреплённых клиентов, общий transport E03 и необходимые каждой сети consensus/derivation компоненты. Каждая задача имеет три сетевых подзадач.

- [ ] E04-01 Покрыть call tree, receipt/logs, create/selfdestruct, precompiles, authorization и rollback в согласованном fork profile. Готово: обязательные native fixtures проходят.
- [ ] E04-02 OPTIONAL по D17: оценить и при приемлемой сложности покрыть изменения балансов вне транзакций. Готово для включённого расширения: каталог paths и причин, сверка и отсутствие дублей tx/system. Допустимо NOT_IMPLEMENTED с явным scope; не блокирует основную поставку.
- [ ] E04-03 Проверить нормализацию balance actions, before/after и нулевых суммарных эффектов на выбранных native fixtures. Это тесты разработки, не production balance oracle.
- [ ] E04-04 Проверить реальный live/import path и ограничение работы hooks. Готово: сеть/диск транспорта не выполняются внутри VM hooks; перегрузка останавливает продвижение в определённой точке.

Gate: отсутствие необъяснённых capture gaps на выбранном контрольном диапазоне. Coverage всех исторических версий ещё не подразумевается.

## E05 — каноничность, reorg, finality и restart

Зависимости: E04 и durable guarantees E03. Стек: native notifications + lifecycle bridge каждого producer/run + RocksDB.

- [ ] E05-01 Реализовать canonical activation registry и финальные статусы. Готово: speculative capture не публикуется как canonical; статусы привязаны к правильной активации.
- [ ] E05-02 Реализовать rollback/reapply транзакций и блоков. Готово: reinclusion/возврат ветки дают корректные активации, включая обязательные BSC native system tx и Bor StateSync. Только самостоятельные outside-tx SystemBalanceEvent проверяются при включённом optional расширении.
- [ ] E05-03 Завершить комбинированный native/WAL/Gateway matrix на seams, уже доказанных в E01/E03: reorg/finality, child-root/marker, GC, restart после cleanup, DRAIN/Resume/upgrades. Недоступные inputs дают явную ошибку. Это полный REC-T01 proof, не первое исследование возможности восстановления.
- [ ] E05-04 Проверить fencing и конкурентные источники. Готово: старый writer не публикует после смены; гонки capture/reorg/publish/status покрыты тестами выбранной топологии.

Gate: crash/reorg/finality fixtures сохраняют correct references данного producer/run, включая restart после ACK cleanup. Retry сохраняет identity; разные runs вправе повторно публиковать то же включение. Production verifier/dedup не требуется.

## E06 — live-конфигурация и учёт доставки; исторические задачи отложены

Зависимости: E05 и ранний harness E01/E03. Стек: live YAML/native configs/init, Compose/Kubernetes, независимые owners, RocksDB/NATS. Полный user deployment по DEPLOYMENT_BOOTSTRAP_CONTRACT. История/диапазоны не prerequisites.

- [ ] E06-01 DEFERRED_BY_USER D38: исторический диапазон, state и исторические VM/forks.
- [ ] E06-02 DEFERRED_BY_USER D38: последовательный history replay с checkpoints.
- [ ] E06-03 DEFERRED_BY_USER D38: пересечения live/history и повторные history commands.
- [x] E06-04 REMOVED_BY_D25: coverage report и независимая проверка полноты диапазона исключены из сборщика. ID сохранён для истории решений; не является незавершённой задачей поставки.
- [ ] E06-05 DEFERRED_BY_USER D38: исторические тестовые диапазоны N–M.
- [x] E06-06 REMOVED_BY_D36: вычисление стартовой истории за три часа отменено; ID сохранён, задача не входит в поставку.
- [ ] E06-07 Реализовать YAML live/Gateway по LIVE_CONFIG_SPEC и D39: native запуск окружением, валидация профиля и подключения YAML адаптера, без history/apply и Kubernetes API.
- [ ] E06-08 Проверить сохранение live/delivery progress при restart, config generation replacement, DRAIN/Resume и несовместимых версиях. Hot reload отсутствует. History-часть DEFERRED D38.
- [ ] E06-09 Проверить live с H+1 после синхронизации: согласованность фиксации H и подписки, независимая доставка pending WAL, отсутствие выгрузки начальной синхронизации и влияния на другие сети.
- [ ] E06-10 DEFERRED_BY_USER D38: history CLI N–M и отдельный Job/process.
- [ ] E06-11 DEFERRED_BY_USER D38: параллельный live/history и resource overlap.
- [ ] E06-12 Подключить user config/logs к уже реализованным native/delivery журналам: ADMITTED/EXEC_READY/decision и READY/SEALED/NATS_ACKED/RETIRED. Не создавать второй параллельный журнал ради старых упрощённых названий RECEIVED/WAL_SAVED. Нет dedup/completeness verifier.
- [ ] E06-13 После согласования DEP-01–04 реализовать полный Compose/Kubernetes комплект трёх сетей по D43: native сборки с адаптерами, consensus/supporting clients, конфиги/секреты/соединения, независимые тома/WAL/Gateway и один NATS. Готово: конфигурации описывают все необходимые компоненты, функциональные startup/readiness/restart проверки подтверждают их связи. Performance и production-методика не входят.

Gate: полный live stack трёх сетей воспроизводится по сохранённым commands/config/artifact manifests, проходит BOOT/CONFIG/MOUNT/TRUST/NATS/START/REPLACE/LOG/ARTIFACT fixtures. Исторические задачи не участвуют. Ранний harness не заменяет пользовательский комплект.

## E07 — приёмка и первая поставка

Зависимости: текущая live-часть E06. Стек: закреплённые сборки/образы, native fixtures и функциональный тестовый считыватель NATS. Production-методика и performance harness вне текущего ТЗ.

- [ ] E07-01 OUT_OF_SCOPE_D40: hardware-профиль, производительность, численные пороги и production outage window исключены из текущего ТЗ; ID сохранён, поставку не блокирует.
- [ ] E07-02 Выполнить end-to-end native correctness и fault matrix. Готово: все обязательные проверки пройдены; отклонения не скрыты формулировкой supported.
- [ ] E07-03 Подготовить runbook запуска, остановки и диагностики ошибок доставки через логи и recovery drill по REC. UI/визуализация не входят в этап.
- [ ] E07-04 DEFERRED_BY_USER (D41): упаковка release bundle отложена вместе с open-source поставкой. Технические схемы, YAML, описание сборки и результаты функциональных проверок остаются самостоятельными задачами.
- [ ] E07-07 DEFERRED_BY_USER (D41): подготовка open-source поставки отложена. Технические схемы, YAML и описание работы сборщика остаются частью ТЗ; публичный release bundle сейчас не готовится.
- [ ] E07-08 Функционально проверить изоляцию: у каждой сети отдельные WAL адаптера и журнал Gateway; добавление, изменение конфига, остановка и рестарт одного экземпляра не требуют изменения/рестарта остальных. Нет общих writers и ожидания прогресса другой сети. Production-методика отдельно, вне scope.

- [ ] E07-09 Принять результат в NATS на fixtures трёх сетей: типизированные поля, части, lifecycle, допустимые повторы, metadata пустых блоков и PubAck. Без production verifier полноты и performance требований.

Gate: отдельно приняты Ethereum, BSC и Polygon; обязательны согласованные функциональные критерии всех трёх сетей. Base/Arbitrum этот gate не блокируют.

## E08 — перенос на следующие клиенты

Справочная зависимость описываемой системы: E07. В проектируемой поставке E01–E07 охватывают Ethereum, BSC и Polygon. Base/Arbitrum перенесены в отложенные направления E08; исследования сохранены. По D64 выполнение этих этапов не входит в нашу работу.

| Подэтап | Клиент / стек | Способ интеграции и обязательная особенность |
|---|---|---|
| E08-CLIENTS | Дополнительные клиенты уже поддержанных сетей, если потребуются | Отдельная проверка новых реализаций; Base/Arbitrum и их клиенты отложены D42 и не входят в gate первого контура |
| E08-AVAX | Go, нативный C-Chain stack | Исторические/современные execution paths, atomic transactions, acceptance/execution/settlement и балансовые границы |
| E08-ZKSYNC | Rust, EraVM/MultiVM client | Native AA/paymaster/storage semantics, protocol versions, batch/L1 finality и модель нативного баланса |

Для каждого подэтапа X создать собственные задачи X-01…X-05; DONE одного клиента не закрывает соседний:

- [ ] X-01 Зафиксировать repository/commit, runtime, history range, baseline и toolchain; пройти сборку.
- [ ] X-02 Доказать native capture и корректность balance actions на минимальных fixtures.
- [ ] X-03 Реализовать typed extensions и все пути захвата; пройти общие protocol/transport contract tests.
- [ ] X-04 Проверить canonical/finality, reorg, restart и native historical replay, включая локальный прогресс доставки.
- [ ] X-05 Выполнить функциональную приёмку; сохранить release bundle и границы поддержки. Benchmark и production-методика не входят в текущее ТЗ (D40).

Перед запуском конкретного клиента шаблон X должен быть развёрнут в реальные ID задач. Текущие X — шаблон, не исполнимый backlog и не обещание срока.

## Активные задачи и риски

| ID | Статус | Причина / следующий шаг |
|---|---|---|
| E00-01 | DONE | Факты окружения и ссылки записаны в DECISIONS.md |
| E00-02 | DONE | Точные mainnets приняты D13; начальная рамка сохранена, новые уточнения вынесены в E00-09 |
| E00-03 | DONE | .01–07 закрыты как архитектурные документы; [итоговая матрица](E00_03_ARCHITECTURE_REVIEW.md); сборки/код/runtime не выполнялись |
| E00-04 | DONE | D40: последовательность и независимость согласованы; performance/production-методика вне scope; supporting profiles остаются в E00-11 |
| E00-05 | DONE | D52/D64: локального Git достаточно; постоянная цель — только ТЗ, реализация исключена |
| E00-07 | DONE | CFG-S01–05, LIVE_CONFIG_SPEC и D39; только документация, runtime-проверки остаются |
| E00-08 | DEFERRED_BY_USER | D41: лицензия и open-source поставка отложены, текущее ТЗ не блокируют |
| E00-10 | DONE | Граница NATS и снятие downstream задач отражены в действующем ТЗ |
| E00-11 | DONE_DOCS_ONLY | Все 41 findings SPECIFIED; 63 требования с приёмкой, D62/D63, native/delivery/bootstrap, DEP-04 и единое root v3 завершены. Runtime E01–E07 TODO; завершение документации не разрешает реализацию |
| E00-12 | DONE | D17–D20 сохранены с последующими уточнениями D23–D33 |
| E00-09 | DEFERRED_BY_USER | D38: история и пропуски отложены; CFG-R4 снят |
| E00-13 | DONE | CLIENT_MATRIX и три исследования; ограничения и source pins проверены |
| E00-14 | DONE | Реестр native типов и логический контракт; proto/validators/fixtures не реализованы |
| E00-15 | DONE | Source research и исторические typed profiles; runtime/архивы не приняты, fixtures перенесены в последующие этапы |
| E00-06 | DONE | D26–D31: требования REC приняты; RocksDB protocol/REC-T01–16 определены в .06, runtime tests будущие |

Ключевые текущие риски: durable pins/capsules, native system paths/decision callers, owned snapshots, node/WAL boundary, lifecycle replay, ACK cleanup/release/fencing, bounded RAM/write stalls и bootstrap identity. [IMPLEMENTATION_GATES](IMPLEMENTATION_GATES.md) связывает каждый с fixture/этапом/stop condition. Historical state и late Base/Arbitrum metadata относятся к deferred scope; post-ACK архив/HA вне scope.

## Снятые задачи

По D16 исключены задачи последующего хранения и обработки. ID сохранены и не переиспользуются: E02-06, E02-07, E02-09, E03-06, E03-07, E03-08, E03-09, E03-10, E07-05, E07-06. Они не входят в gates и не блокируют поставку. Прежнее назначение отражено в истории решений D09/D11.

Уточнение D17/D25: E04-03 проверяет корректность нормализации на fixtures разработки, не вводит production verifier. Optional операции вне транзакций не блокируют поставку. Задачи native capture закрываются отдельно для всех трёх сетей.
