# Исполняемый план, модули и доказательства

2026-11-05 · DOC-REPAIR-05. Уточнение [DEVELOPMENT_PLAN](DEVELOPMENT_PLAN.md), последовательность D40 **E01 → E02 → E03 → E04 → E05 → E06 → E07 сохраняется**. Все перечисленные ниже code/build/runtime tasks имеют статус TODO до отдельного разрешения на реализацию. SPECIFIED в [реестре исправлений](DESIGN_REPAIR_STATUS.md) означает только готовый проверяемый проект.

## Ранний harness и зависимости gates

| Этап / существующие ID | Что доказать до выхода | Что ещё не считается доказанным |
|---|---|---|
| E01-01…05, отдельно .ETH/.BSC/.POLY | Изолированные pinned builds; минимальный native fixture runner; реальные execution/canonical/finality callers; admission/pin/owned snapshot/decision marker seam; native-only recovery; отсутствие bypass обязательного пути | Полный protobuf, production transport и вся семантика E04 ещё не готовы |
| E02-01…05/08/10 | Компилируемые схемы/валидаторы, AUTH/SELF/LIFE, receipt provenance, immutable IDs/bytes, transport/chunk layout, bounded serialization и spool; closed extension registry | Работа с настоящим NATS и весь native crash matrix ещё не приняты |
| E03-01…05/11 | Реальные RocksDB/gRPC/NATS, delivery-only fault matrix от durable intent/READY; Prepare/Release/fencing/destination/drain; integration smoke с E01 fixtures | Полная комбинация native multi-block reorg/finality+delivery не называется пройденной до E05 |
| E04-01/03/04 | Все обязательные native tx paths/fields выбранных трёх live profiles, включая native system tx; rollback/existence/fees/SELFDESTRUCT/Bor scopes; большой hook field и worker stalls | Исторические профили, optional outside-tx extension и performance SLO не являются gate |
| E05-01…04 | Полный native→WAL→Gateway→NATS crash/reorg/finality/restart matrix; durable H и known-branch replay; cleanup/GC/late callback, downgrade/upgrade/drain | HA, backup и восстановление потерянного ACKed NATS store не обещаются |
| E06-07…09/12/13 | Полный пользовательский YAML/native config/init/Compose/Kubernetes комплект; 11 services/14 volumes, artifact proof, wrong profile/peer, H+1 и независимый replay | Ранний fixture runner не заменяет этот комплект; history CLI не добавляется |
| E07-02/03/08/09 | End-to-end acceptance отдельно ETH/BSC/POLY; clean environment reproduction, runbook, isolation и опубликованные сообщения | Публичная open-source упаковка и performance/production-методика отложены |

E01-01 делится до начала исполнения на `.build.ETH/BSC/POLY`, `.supporting`, `.harness`, `.artifacts`. Native source state fixtures, bodies/headers/consumed Heimdall responses, fork config, secrets для тестов и команды запуска перечисляются **до** соответствующего proof. Нельзя требовать скрытую уже синхронизированную ноду автора. Test-only harness использует pinned native execution/decision paths и детерминированные inputs; соответствие реальному import path проверяется отдельно. Mainnet-only продукт не запрещает локальный test-only execution harness и fault injection.

E01-02 делится на `.admission`, `.ownership`, `.pin`, `.decision` для каждой сети; E01-04 проверяет их ранние power-loss seams до определения общей production модели. Минимальный внутренний spool/marker proof не обязан уже быть schema E02. E04 расширяет семантическое покрытие, E05 сочетает доказанные seams в полный lifecycle matrix; окно native/WAL нельзя впервые исследовать лишь в E05.

E03 использует настоящий NATS с минимальными test CA/credentials/stream bootstrap и независимыми test directories. Этот ранний harness создаётся в E01/E03, полный replaceable user deployment — E06. REC-T01 разбит: E01 доказывает native admission/state seam, E03 — delivery часть, E05 — совмещённые native crash interleavings. Ни один частичный PASS не обозначается полным REC-T01. Остальные REC-T01–17 применяются только в текущем live scope; historical variants помечаются DEFERRED.

## Независимые эталоны

Каждый semantic fixture содержит pinned source state/blocks/fork и независимо подготовленные expected outcomes: native receipts/state commitments плюс вручную проверяемый execution/action сценарий либо отдельный reference path. Проверка только hash корня не доказывает полноту trace; только receipts не доказывают before/after. Expected output не вычисляется тем же normalizer, который тестируется. Общий protobuf decoder допустим; общий ошибочный алгоритм expected semantics — нет.

Reorg/finality/known-branch/fault воспроизводятся сохранённым сценарием и seed; ожидание случайного mainnet события не является тестом. Fault harness умеет остановить процесс до/после каждого durable marker и storage sync, задержать callback/worker, потерять только один owner store, подменить destination/config и повторить сообщения. Crash с сохранённой OS cache отдельно от simulated power loss/потери volatile native trie: результаты не смешиваются.

Mutation sanity gate: намеренно испорченные parity handling, SELFDESTRUCT action, receipt created_contract, dropped raw chunk, неверный revision или early ACK должны давать FAIL соответствующего oracle. Проверка не сводится к тому, что serializer и decoder согласны друг с другом. Fixtures и expected artifacts — тестовые файлы разработки, не production consumer/полнота verifier.

## Будущие модули и независимые build graphs

| Каталог | Владение / интерфейс |
|---|---|
| `protocol/proto`, `protocol/validators` | Versioned logical/wire schema, presence/oneof, transport frame и bounded validators; не native DB |
| `core/capture`, `core/normalization` | Owned raw chunks/typed records, spool и disk indexes; только собственные DTO, без импортов внутренних типов сразу нескольких fork |
| `core/delivery`, `core/journal` | Producer intent/READY/plan/release, common error/compatibility contracts и RocksDB adapter |
| `adapters/geth`, `adapters/bsc`, `adapters/bor` | Native admission/mutator/decision/pin bridges, fork semantics, toolchain каждого клиента |
| `gateway` | Fenced ingest, reservations, immutable plans/frames, NATS publisher; отдельный process/store на сеть |
| `config` | Strict project YAML, role checks, effective native profile constraints и DestinationBinding |
| `deploy/compose`, `deploy/kubernetes`, `deploy/init` | Полный user stack, native config templates, artifact acquisition, secrets/bootstrap и owner mapping |
| `fixtures`, `test/harness`, `test/evidence` | Input/expected/fault scenarios, независимый считыватель, evidence manifests |
| `docs` | Решения, source provenance, runbooks и состояние |

Это проект будущего дерева, каталоги кода сейчас не создаются. Geth/BSC/Bor — отдельные Go modules/build graphs с native fork dependencies. Shared core не импортирует все fork state types и не заставляет их обновить SDK/toolchain ради Gateway. NATS SDK только в Gateway. Schema generation проверяется в каждом graph. Rust/Nitro/Reth/ZKsync/Avalanche/backfill не являются prerequisites первого контура. При будущих расширениях их модули добавляются отдельным решением.

## Evidence manifest и stop conditions

Один файловый manifest на fixture run, с schema_version и статусом PASS/FAIL/NOT_RUN/DEFERRED. Обязательные поля:

- requirement_ids и audit_finding_ids; task_id с network suffix; fixture_id/version;
- network/profile/fork; native source commit, patch commit, dependency lock digest, compiler/generator versions, image digest/architecture;
- input artifact paths + SHA-256; expected artifact paths + SHA-256 и независимый expected_source/method;
- scenario/seed, fault boundary, storage/cache model, run command, environment versions;
- actual output/log paths + SHA-256, normalized comparator version, verdict и объяснение различий;
- started/finished time, evidence producer, parent integration run при необходимости.

Артефакты не содержат secrets. Пустое поле image digest у source-only анализа явно означает NOT_RUN для runtime, не PASS. Новый native commit/profile/schema/plan/storage version инвалидирует связанные старые результаты до повторной проверки; несовместимость не маскируется переносом галочки.

При missing mandatory native path, неустранимом snapshot alias, потере recovery pin, нарушении marker ordering, неограниченном collector memory path, конфликте identity/immutable bytes или false ACK gate останавливается. Сохраняются минимальный counterexample и evidence; исправляется механизм и повторяются затронутые проверки. Нельзя молча снижать полноту, отключать обязательный native type, пропускать poison record или называть runtime-профиль supported. Изменение принятого поведения/клиента выносится пользователю; инженерное исправление в рамках контракта выполняется самостоятельно.

Переход к следующему этапу — после evidence всех обязательных подзадач ETH/BSC/POLY, обновления PROJECT_STATUS и commit. Optional/deferred задачи не используются ни как ложный PASS, ни как блокер текущего gate. Реализация не начинается без последующего указания пользователя.

## Traceability 41 находки

| Finding | Нормативный контракт / fixture | Первый proof → итоговый gate |
|---|---|---|
| NAT-01 | TRANSACTION_TYPE_REGISTRY; AUTH-01…04 | E02 → E04 |
| NAT-02 | NATIVE_CAPTURE_PROTOCOL; OWNED-01 | E01 → E04 |
| NAT-03 | NATIVE_CAPTURE_PROTOCOL; EXIST-01 | E01 → E04 |
| NAT-04 | MESSAGE_CONTRACT; SELF-01…04 | E02 → E04 |
| NAT-05 | NATIVE_CAPTURE_PROTOCOL; SCOPE-01 | E01 → E04 |
| NAT-06 | MESSAGE_CONTRACT/native provenance; RECEIPT-01 | E01/E02 → E04 |
| NAT-07 | MESSAGE_CONTRACT; LIFE-01/02 | E02 → E05 |
| NAT-08 | MESSAGE_CONTRACT; LIFE-03/04 | E02 → E05 |
| NAT-09 | NATIVE_CAPTURE_PROTOCOL; BRANCH-01 | E01 → E05 |
| NAT-10 | NATIVE_CAPTURE_PROTOCOL; DECISION-01 | E01 → E05 |
| DLV-01 | NATIVE_CAPTURE_PROTOCOL; CAPTURE/PIN-01 | E01 → E05 |
| DLV-02 | DELIVERY_STATE_MACHINE; ATTEMPT-01 | E03 → E05 |
| DLV-03 | DELIVERY_STATE_MACHINE; PREPARE-01 | E03 |
| DLV-04 | DELIVERY_STATE_MACHINE; FRONTIER-01 | E03 |
| DLV-05 | DELIVERY_STATE_MACHINE; FRONTIER/FENCE-01 | E03 |
| DLV-06 | DELIVERY_STATE_MACHINE; FENCE-01 | E03 → E05 |
| DLV-07 | DELIVERY_STATE_MACHINE; DRAIN-01 | E03 → E05/E06 |
| DLV-08 | DELIVERY_STATE_MACHINE; DEST-01 | E03 → E06 |
| DLV-09 | DELIVERY_STATE_MACHINE; MEM/NATS-01 | E02 → E03 |
| DLV-10 | NATIVE_CAPTURE_PROTOCOL/DELIVERY_STATE_MACHINE; OWNED/MEM-01 | E01/E02 → E04 |
| DLV-11 | DELIVERY_STATE_MACHINE; UPGRADE-01 | E03 → E05/E06 |
| DLV-12 | DELIVERY_STATE_MACHINE; POISON-01 | E03 → E05 |
| DLV-13 | DELIVERY_STATE_MACHINE; ENOSPC-01 | E03 → E05 |
| OPS-01 | DELIVERY_STATE_MACHINE; DRAIN/DEST-01 | E03 → E06 |
| OPS-02 | DEPLOYMENT_BOOTSTRAP_CONTRACT; BOOT-01 | E01 harness → E06 |
| OPS-03 | DEPLOYMENT_BOOTSTRAP_CONTRACT; CONFIG-01 | E01 → E06 |
| OPS-04 | DEPLOYMENT_BOOTSTRAP_CONTRACT; MOUNT-01 | E01 → E06 |
| OPS-05 | DEPLOYMENT_BOOTSTRAP_CONTRACT; TRUST-01 | E03 harness → E06 |
| OPS-06 | DEPLOYMENT_BOOTSTRAP_CONTRACT; NATS-01 | E03 → E06 |
| OPS-07 | DEPLOYMENT_BOOTSTRAP_CONTRACT; START-01 | E01/E03 → E06 |
| OPS-08 | DEPLOYMENT_BOOTSTRAP_CONTRACT; REPLACE-01 | E06 |
| OPS-09 | DEPLOYMENT_BOOTSTRAP_CONTRACT; MOUNT-01 | E06 |
| OPS-10 | DEPLOYMENT_BOOTSTRAP_CONTRACT; LOG-01 | E03 → E06/E07 |
| OPS-11 | DEPLOYMENT_BOOTSTRAP_CONTRACT; ARTIFACT-01 | E01 → E06 |
| OPS-12 | DEPLOYMENT_BOOTSTRAP_CONTRACT; TRUST-01 | E03 → E06 |
| DEV-01 | Этот документ: ранние seams, partial REC-T01 | E01/E03/E05 gates |
| DEV-02 | Этот документ: входы/команды раннего harness | E01/E03 gates |
| DEV-03 | Этот документ: independent expected и mutation sanity | E01/E02 → E07 |
| DEV-04 | DEVELOPMENT_PLAN: статусы каждой задачи и live gates | Backlog export перед каждым этапом |
| DEV-05 | Этот документ: дерево и dependency boundaries | E01 build graph review |
| DEV-06 | Этот документ: evidence manifest/stop conditions | Каждый gate, финально E07 |

Ни одна строка этой таблицы не утверждает выполненный runtime proof. В каждом gate сохраняется конкретный verdict и ссылки на фактические артефакты.
