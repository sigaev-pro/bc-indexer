# E00-03 — компоненты, стек и границы ответственности

> D26–D31 (2026-10-16): [текущие требования доставки и восстановления](DELIVERY_RECOVERY.md). Restart с сохранённым диском автоматический; после потери диска история задаётся заново N–M. WAL копится без TTL/прикладной квоты до подтверждения NATS всех сообщений/частей блока. После ACK payload и временные статусы очищаются; остаётся минимальное рабочее состояние для restart. Сейчас один NATS; HA/backup и дальнейшая судьба подтверждённых данных вне scope. Backend принят D33: RocksDB. E00-03.06 завершён как проект: [pins](research/ROCKSDB_STACK.md), [протокол и REC-T01–16](ROCKSDB_DELIVERY_PROTOCOL.md).

2026-10-15. Пользователь поручил разбить E00-03 и выполнять поэтапно с сохранением результатов и необходимыми вопросами. Это проектирование; разработка и развёртывание не начаты. Принятые решения — [DECISIONS.md](DECISIONS.md); общая архитектура — [COLLECTION_ARCHITECTURE.md](COLLECTION_ARCHITECTURE.md).

## Порядок и критерии завершения

| Подэтап | Содержание | Сохраняемый результат / критерий | Статус |
|---|---|---|---|
| E00-03.01 | Свести компоненты, принятые решения и открытые зависимости | Карта ниже; факты отделены от предложений, повторные вопросы исключены | DONE |
| E00-03.02 | Определить границу поставки и способы запуска | D21/D22: Compose + Kubernetes, NATS внутри обеих сред | DONE |
| E00-03.03 | Закрепить стек и интеграции | [COMPONENT_STACK](COMPONENT_STACK.md): версии-кандидаты, common SDK и packaging; build-проверки в E01 | DONE — проектный baseline |
| E00-03.04 | Определить владельцев состояния | [STATE_OWNERSHIP](STATE_OWNERSHIP.md): capture, lifecycle, IDs/bytes/parts, прогресс и ACK; RocksDB/физическое хранение уточнены в .06 | DONE — логическая модель |
| E00-03.05 | Описать live и историю | D23–D25: отдельные pods, простая команда N–M, дубли допустимы, локальный прогресс до NATS; [результат](LIVE_HISTORY_DELIVERY.md) | DONE |
| E00-03.06 | Определить границы доставки и поведение при сбоях | D26–D33, [pins](research/ROCKSDB_STACK.md), [protocol/REC-T01–16](ROCKSDB_DELIVERY_PROTOCOL.md); .06-A–D закрыты как проект | DONE — без build/runtime приёмки |
| E00-03.07 | Свести итог и проверить согласованность | [Итоговая матрица](E00_03_ARCHITECTURE_REVIEW.md): scope, компоненты/стек/состояние/гарантии/fixtures и отдельные незакрытые пункты ТЗ | DONE — документация |

Выполняем подэтапы последовательно. Факты и независимые проверки можно готовить заранее, но зависимый выбор не считается принятым без ответа. Новые решения не пересматривают D16–D20 молча. E00-03.06 и финальный gate не могут быть DONE лишь потому, что REC отложен.

## E00-03.01 — результат

### Уже принято пользователем

- Пять сетей: Ethereum Mainnet, BSC Mainnet, Base Mainnet, Arbitrum One, Polygon PoS.
- Полные включённые транзакции и применимые детали; typed block metadata/lifecycle. Самостоятельные изменения балансов вне транзакций optional по D17.
- Конечная граница — нормализованные сообщения в NATS JetStream. Последующая обработка вне задачи.
- Обычно одна транзакция — одно сообщение; крупная запись разбивается без усечения.
- Каждая сеть имеет свой node/adapter/WAL/Gateway и служебный журнал. Общий NATS с раздельными потоками/subjects/лимитами; добавление сети не зависит от прогресса остальных.
- D36: основной сборщик только live; история N–M запускается отдельным сервисом на узле ноды. Default три часа и догрузка через YAML отменены.
- Open-source и пользовательские `.yml`; лицензия, подробная YAML schema и управление reload ещё не выбраны.
- Перезапуск с тем же YAML и сохранённым диском продолжает работу; при потере диска новый run/ручной N–M. REC принят D26–D31, RocksDB — D33, протокол завершён в .06.

Основания: D05/D08/D10/D12–D20 в [журнале](DECISIONS.md). Клиенты, версии, текущий логический формат и отдельные реализации ниже — проверенные кандидаты/проектные решения, а не новые пользовательские ответы.

### Карта компонентов

| Компонент / граница | Обязанность | Уже установленный факт или основание | Что ещё решить |
|---|---|---|---|
| Native runtime каждой сети | Исполнить блок по native правилам и дать результаты/сигналы canonical chain | [CLIENT_MATRIX](CLIENT_MATRIX.md): Geth, BSC, Base, Nitro, Bor; Classic — отдельный historical runtime Arbitrum One | Build/toolchain доказательство, точные supporting clients и доступность historical inputs |
| Adapter внутри native integration | Захватить все применимые transaction details и нормализовать | Go для Geth/BSC/Nitro/Bor; Rust для Base; Classic Go + C++ AVM observer candidate | Роли/стек определены; module boundaries, hook budgets и versioned decoders проверяются E01/E02 |
| Native lifecycle bridge | Связать execution с canonical inclusion, reorg/finality и lifecycle | Это роль контура одной сети; общесетевого coordinator нет | Lifecycle state каждого producer/run; E00-03.04 с поправкой D24 |
| Adapter WAL | Удерживать собранные записи и pending delivery до согласованного ACK | Нужен служебный журнал; это не пользовательское downstream-хранилище | RocksDB принят D33; sync/atomic boundary/cleanup закреплены в ROCKSDB_DELIVERY_PROTOCOL |
| Adapter → Gateway | Передать проверяемую versioned запись или её части | Рабочий проект: Protobuf/gRPC, UDS локально / mTLS TCP между хостами | Baseline transport/pins определены .03/.06; совместный build/размеры frames проверяются E01/E02/E03 |
| Gateway / publisher сети | Валидировать envelope/parts, регистрировать публикацию, отправлять в JetStream и подтверждать доставку | Отдельный Gateway каждой сети принят D19; общий сервис между сетями не вводится | Go/gRPC-Go/NATS Go client — кандидаты; ownership publication mapping и fencing — E00-03.04/06 |
| NATS JetStream | Принять сообщения и выдать PubAck | D22: проект включает развёртывание в Kubernetes/Compose; общий NATS с раздельными streams/лимитами | Один instance/одна replica по D30; дальнейшее хранение после PubAck вне scope |
| History worker | Выполнять выбранный диапазон через native runtime при продолжающемся live | N–M и независимость сетей приняты; Classic нужен только соответствующим диапазонам Arbitrum | Отдельный pod/process и команда приняты D23; YAML/CLI и граница restart live — CFG по D36 |
| Приёмочный считыватель | Проверить опубликованные данные против native fixtures | Тестовый инструмент, не промышленный downstream-компонент | Go/protobuf и матрица fixtures определены; исполняемые тесты E01–E07 |
| Конфигурации запуска | Запустить native компоненты сетей и NATS | D21/D22: Compose и Kubernetes входят в поставку | Recovery принят; sizing/численные критерии E00-04/E07, YAML/reload — E00-07/09 |

Стрелка данных не означает, что каждый логический компонент — отдельный процесс/pod. Принята независимость контуров сетей; роли lifecycle bridge/WAL принадлежат producer; Gateway — отдельный процесс сети со своим каталогом. Конкретные deployment manifests ещё не создавались. У Base/Arbitrum native L1 dependency не является зависимостью от Ethereum extractor.

### Что не требуется спрашивать повторно

Не выбираем заново сети, native extraction, полноту транзакций, общую границу NATS, типизацию/parts, per-network topology и наличие live + N–M. Не подменяем E00-15 runtime-приёмкой: Classic observer и historical archives остаются доказательствами будущих этапов.

### Связь с прежним деревом Q5–Q15

| Прежняя ветка | Где продолжается |
|---|---|
| Q5/Q6: клиенты, hooks, integration | E00-13/15 source research выполнены; E00-03.03 сводит решения, E01 проверяет исполнение |
| Q7: fixtures/oracle/support | Исследовательские fixtures существуют; E00-03.07 связывает их с gate; runtime-проверки позже |
| Q8/Q9: history inputs и live overlap | D23–D25 приняты; оставшиеся YAML/CLI и граница restart live — CFG по D36 |
| Q10/Q11: модель, IDs, lifecycle | E00-14/15 логическая схема; E00-03.04/06 определяют ответственность; compiled schema — E02 |
| Q12/Q13: надёжность, storage, fencing, retention | E00-03.04/06 и REC завершены D26–D33; исполняемые проверки E03/E05 |
| Q14/Q15: итоговый стек, сборка, последовательность | E00-03.03/07; общий план после ответов уточняет E00-04 |

Q16, лицензия, backup документов и разрешение на код не закрываются этой декомпозицией: это отдельные E00-04/05/08.

## E00-03.02 — ответы получены

Оба вопроса проверены против D15/D19 и плана: ранее принято устройство независимых контуров, но не объём готовых конфигураций первой поставки.

| ID | Вопрос / варианты | Рекомендация | Ответ |
|---|---|---|---|
| ARCH-01 | Готовые конфигурации первой поставки | Compose + Kubernetes | D21 accepted: оба |
| ARCH-02 | Развёртывание production/local NATS | Первоначальная рекомендация внешнего production NATS не принята | D22 accepted: NATS внутри Kubernetes и Compose |

ARCH-01 не выбирает автоматически Helm, конкретный Kubernetes provider, HA и ресурсы. ARCH-02 не выбирает replicas, retention и гарантию пережить потерю сервера; это отдельный REC. Для поставляемого NATS проект должен документировать совместимость и необходимые параметры streams/лимитов — их значения ещё не утверждены. В .06 определено: streams/credentials создаёт deployment, publisher лишь публикует/проверяет доступ; NATS один, replicas=1, post-ACK retention вне scope.

## E00-03.03–04 — результаты

[COMPONENT_STACK.md](COMPONENT_STACK.md): native builds отдельно; Gateway Go 1.27.2/gRPC-Go 1.84.0/nats.go 1.54.0, общая Protobuf schema и Rust tonic/prost transport. NATS SDK не навязывает Go 1.26 клиентам с Go 1.25. [NATS_DEPLOYMENT_STACK.md](research/NATS_DEPLOYMENT_STACK.md) подтверждает official Helm chart/server candidates и explicit JetStream/persistent volume configuration. Совместная сборка ещё не проверена.

[STATE_OWNERSHIP.md](STATE_OWNERSHIP.md): native lifecycle bridge отвечает за наблюдения своего producer/run; adapter delivery writer — за event IDs/source bytes, fixed part layout и received ACK; Gateway — за publication mapping/chain_sequence/PubAck. Native runtime определяет каноничность. Read cursor не равен подтверждённой доставке. Backend/fsync/crash-atomicity определены .06; runtime tests ещё не выполнены.

Это технические проектные решения в рамках порученной детализации, не новые ответы пользователя и не runtime-приёмка.

## E00-03.05 — завершён

ARCH-03 закрыт D23: live и history — разные pods/processes, native historical state и ресурсы принадлежат своему worker. D24 разрешает overlap без дедупликации. D25 ограничивает контроль стадиями получения, WAL и NATS PubAck. Результат: [LIVE_HISTORY_DELIVERY](LIVE_HISTORY_DELIVERY.md).

Ответ получен D23. Подробности YAML/reload остаются JOB-01, ресурсная изоляция описана в LIVE_HISTORY_DELIVERY. Отдельный процесс не обязательно означает вторую постоянно синхронизируемую полноценную ноду: native replay и достаточность state/archive проверяются по профилю сети. Недоступный диапазон нельзя молча обрезать.

## Точка продолжения

E00-03.01–07 и родитель E00-03 завершены как архитектурные документы. Результат — [E00_03_ARCHITECTURE_REVIEW](E00_03_ARCHITECTURE_REVIEW.md). E00 в целом не закрыт: следующий CFG/JOB-01 (E00-07/E00-09), затем оставшиеся приёмочные/публикационные вопросы E00-04/05/08/11. Никаких переходов к реализации без отдельного указания; перед следующим документальным этапом обязателен commit.
