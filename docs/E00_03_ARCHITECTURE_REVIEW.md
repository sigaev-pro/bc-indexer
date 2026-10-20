# E00-03.07 — итоговая сверка архитектуры

2026-10-16. Проверка локальной документации по принятым D16–D33. Новое исследование версий, код, сборки, runtime-тесты и deployment не выполнялись. E00-03 закрывается как архитектурный блок ТЗ; весь E00 и готовность продукта этим не объявляются завершёнными.

## Граница текущего ТЗ

- Пять сетей: Ethereum, BSC, Base, Arbitrum One и Polygon PoS. Извлечение через native clients/executors; RPC не заменяет production capture применимых calls/actions/state changes.
- Все родные транзакции, включая failed и protocol/system types, с применимыми деталями. Самостоятельные изменения балансов вне транзакций optional. Неизвестный обязательный тип — ERROR, не silent skip.
- Строгая общая типизация с network extensions. Одна обычная транзакция — одно сообщение, большая — parts без усечения. Metadata пустого блока и lifecycle — отдельные типы, не искусственные транзакции.
- Отдельные live/history pods/processes. History — конечная команда N–M с включительными границами. Пересечения допустимы, общего dedup coordinator нет. Независимость сетей не отменяет общую инфраструктуру NATS и native L1 dependencies.
- Конечная гарантия: prepared block batch сохранён локально и все его сообщения/части получили PubAck NATS. Закрытый batch — фактически подготовленные данные, не собственное доказательство полноты native блока.
- RocksDB, накопление без TTL/прикладной квоты, payload до ACK. Один NATS instance. Restart с диском автоматический; потеря диска — новый run и ручная история. HA, backup payload, dedup, production block verifier, downstream storage/processing и UI вне текущего scope.
- Open-source и пользовательские .yml обязательны. Подробности CFG/лицензии ещё открыты, не подменяются предположениями.

## Матрица компонент → стек → состояние → гарантия → проверка

| Компонент | Стек / граница | Владелец и состояние | Гарантия проекта | Проверка будущей реализации |
|---|---|---|---|---|
| Native runtime пяти сетей | [CLIENT_MATRIX](CLIENT_MATRIX.md): Go Geth/BSC/Nitro/Bor, Rust Base; Classic Go/C++ AVM | Native client владеет chain state/consensus | Наблюдение не меняет исполнение; недоступная история не обрезается молча | E01-01…05, E04; ETH/BSC/Base/AP/CL/POLY fixtures в research |
| Capture + lifecycle bridge | Native hooks/observer своего producer/run | Capture checkpoint, branch identity, activation references | Каноничность берётся из native client; speculation не публикуется как canonical; нет общего live/history registry | E04/E05, REC-T01/14 |
| Normalizer/codec | Protobuf, Go bindings; Base tonic/prost; [MESSAGE_CONTRACT](MESSAGE_CONTRACT.md) | Исходные Envelope bytes, event IDs, part layout | Явные типы/presence и сохранение применимых деталей; ошибки не маскируются пустотой | E02 и контрактные fixtures, REC-T09/15 |
| Producer WAL | RocksDB 10.4.2; grocksdb v1.10.2; Rust rocksdb 0.24.0/sys 0.17.3+10.4.2 | Отдельный каталог owner; STAGING/READY, cursors, plan, cleanup/release | Sync commit перед durable статусом; незакрытые данные не публикуются; pending не удаляются | E03-01/04/05, REC-T01/02/07/08/10/11/13 |
| Adapter → Gateway | gRPC, UDS локально / TCP+mTLS между pods; [COMPONENT_STACK](COMPONENT_STACK.md) | Producer хранит plan и точные frame bytes до публикации | Prepare/Publish/IngestAck/Release; повторные bytes неизменны | E03-02/03, REC-T03/04/06/15/16 |
| Gateway сети | Go 1.27.2, gRPC-Go 1.84.0, nats.go 1.54.0; свой RocksDB | Publication epoch/sequence, CLOSED frames, PubAck и release | Только после PubAck всего batch выдаётся IngestAck; single writer/session; at-least-once | E03/E05, REC-T04/05/06/12/16 |
| NATS | Один JetStream instance; file streams, replicas=1, Compose/Kubernetes | Streams создаёт deployment; publisher не управляет downstream | PubAck — граница передачи, не consumer ACK и не бессрочное хранение | E03-03/11, E07; source packaging в research/NATS_DEPLOYMENT_STACK |
| History job | Native replay worker, отдельные ресурсы/state и RocksDB | N/M/run_id, capture/delivery cursors | Конечная команда, restart с диском; нет overlap/dedup барьера | E06, REC-T08/13/14/16 |
| Acceptance harness | Go/protobuf; тестовый считыватель NATS | Только тестовые fixtures/результаты | Проверяет контракт и доставку известных примеров; не production consumer/verifier | E01–E07 после отдельного разрешения на реализацию |

Pins являются source-checked проектным baseline, не доказательством совместной сборки. Сведения по поддерживающим native clients и historical inputs проверяются отдельно; неизвестное не считается работающим.

## Исправленные несогласованности

1. Убраны устаревшие ожидания ответа ARCH-03/REC и выбора WAL. Принятые D23–D33 не задаются повторно.
2. Общий Canonical Coordinator уточнён как native lifecycle bridge каждого producer/run. Он не устраняет overlap и не назначает общую активацию разным загрузчикам.
3. IngestAck во всех действующих описаниях относится ко всему закрытому block batch. PubAck события/части сам по себе не разрешает удалить block payload.
4. Потоковая передача означает передачу готового batch частями с ограниченной RAM; она не разрешает публикацию STAGING до READY/закрытия publication plan и Gateway CLOSED.
5. chain_sequence читается только с publication_epoch. Порядок доставки не подменяет block/transaction chronology; после потери Gateway диска сохраняются прежние frames pending producer.
6. Удаление payload после ACK отделено от минимального рабочего состояния cursor/session/lifecycle/release. Нельзя обещать корректные Removed/Status после очистки, не сохранив необходимые references или воспроизводимый способ их восстановления.
7. Старые gates, допускавшие трактовку как runtime completeness verifier или запрет дублей разных runs, уточнены как проверки нормализации/доставки выбранных fixtures.
8. Разделены исторические записи решений, действующий контракт, невыполненные задачи реализации и оставшиеся вопросы ТЗ. Исходный v2 и архив proto не изменялись.

## Что остаётся в ТЗ после E00-03

| Пункт | Оставшийся результат | Статус / следующий шаг |
|---|---|---|
| E00-07 + E00-09 | Поля/валидация .yml; reload vs restart; связь автоматической стартовой истории с ручным N–M; реакция на смену диапазона активного задания | OPEN; следующий раунд CFG/JOB-01, без повторного выбора отдельных pods или команды |
| E00-04 | Итоговая последовательность/критерии приёмки и численные ограничения; владельцы ещё не закреплённых supporting-client profiles и source inputs | OPEN; не выдавать аппаратный sizing/throughput за согласованный SLO |
| E00-05 | Процесс локального versioning уже принят и действует; remote/резервирование документов и отдельное разрешение на реализацию ещё не приняты | PARTIAL; никаких push/кодовых работ автоматически |
| E00-08 | Open-source лицензия, состав публикуемых исходников/зависимостей, repository/release workflow | OPEN; архитектурный выбор библиотек не заменяет лицензионную проверку |
| E00-11 | Собрать финальный пакет ТЗ после CFG/приёмки/публикационных решений и явно показать remaining technical gates | PARTIAL; архитектурный раздел проверен, всё ТЗ ещё не финализировано |

Конкретный Ethereum consensus client/version пока не выбран в CLIENT_MATRIX. Это явная внешняя зависимость native runtime, а не отсутствующий элемент потока данных. Deploy profile нельзя считать готовым до её выбора (E00-04 → E01-01). Classic historical state/action capture, archive availability и all-epoch decoders остаются непроверенными ограничениями; их нельзя скрывать за статусом DONE документа.

Точные protobuf field numbers/extension schemas, все historical decoders, image digests, параметры RAM/compaction и transport limits относятся к детальной спецификации/реализации E02/E03/E07 по согласованным критериям E00-04. Это не разрешение приступать к коду. До реализации должен быть указан владелец каждого такого gate; их закрытие не заявляется сейчас.

## Статусы и остановка

- E00-03.01–07: DONE как документы. E00-03: DONE в границах архитектурного блока.
- E00-06: DONE как требования восстановления по D26–D31 и протокол .06.
- E00 в целом: IN_PROGRESS. E00-04/05/07/08/09/11 остаются по таблице выше.
- Следующий шаг: CFG/JOB-01 (E00-07/E00-09), только ТЗ. Ни E01, ни сборки/POC не начинаются автоматически.
- Проверки текущего шага: ссылки/anchors, уникальность task IDs, непротиворечивость scope/статусов, git diff --check, SHA-256 исходного v2. Результаты зафиксированы в PROJECT_STATUS; commit обязателен до следующего шага.
