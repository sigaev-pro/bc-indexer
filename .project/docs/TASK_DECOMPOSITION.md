# Декомпозиция требований ТЗ по результатам

D82: [TESTING_CONTRACT](TESTING_CONTRACT.md) расширяет outputs существующих задач без новых child IDs: E01-01.evidence — schema v3/profile registry; E01-01.harness — runner/fixtures/deadlines; E07-02.e2e — TC01–TC12; E07-09.nats_acceptance — reader/mutations; E07-09.requirement_verdict — семь профилей и все suites. Build/bootstrap tasks доказывают каждый профиль; mainnet proof не принимается за testnet. 63 REQ / 211 AC / 68 child tasks / 7 gates.

2026-11-05. Только проектирование и документация, D64. Этот файл описывает задачи **проектируемого процесса**, а не разрешает их выполнение. Код продукта, сборки, runtime-тесты и deployment не выполняются. Все runtime verdict — **NOT_RUN**. Существующие parent ID E01–E07 сохранены; дочерние ID уточняют результат, не переиспользуют снятые ID.

Нормативное поведение задаёт [единое ТЗ](../evm-transaction-extractor-tz-v3-codex.md). [DEVELOPMENT_PLAN](DEVELOPMENT_PLAN.md) сохраняет parent-задачи и историю scope. Здесь определены владельцы, артефакты, зависимости и привязка к [ACCEPTANCE_CASES](ACCEPTANCE_CASES.md); [ACCEPTANCE_REQUIREMENTS](ACCEPTANCE_REQUIREMENTS.md) остаётся реестром 63 REQ. Изменение поведения через задачу запрещено: обнаруженное противоречие возвращается в ТЗ и решения.

## Правила чтения карточек и графа

- `ETH/BSC/POLY` — одно описание задачи с независимыми applicability/result slots каждого из семи mainnet/testnet профилей D82. PASS одной сети не закрывает остальные. `COMMON` означает общий механизм, а не общий writable store; сетевые варианты AC сохраняются.
- Владелец — единственный ответственный модуль, не назначение конкретному человеку или агенту. Review/independent oracle не переносит ответственность с owner.
- Столбец «Зависимости → получаемый выход» содержит обязательные child ID и конкретный вход. Любая задача E02–E07 также требует gate предыдущего этапа; эта общая зависимость не повторяется в каждой строке. E01 получает зафиксированное ТЗ как внешний вход `SPEC`, а не результат будущего этапа.
- Gate `G.E0n` — метка графа, не новая продуктовая задача. Он зависит от всех обязательных детей этапа E0n. Следующий этап зависит от gate, а не от будущего полного proof. Внутри этапа разрешены только указанные зависимости; зависимости на последующий этап запрещены.
- Parent завершён только после всех своих обязательных детей и их applicability slots. `E04-02.optional_scope` условный: default OFF проверяется обязательно; enabled-ветка не блокирует gate без объявленного включения. Снятые/отложенные parent ID не имеют обязательных детей.
- У каждого выхода есть собственный invariant в таблице. Дополнительные точные сценарии и expected результаты назначены в таблице REQ/AC → задача ниже. Каждый AC имеет одного ответственного task ID; зависимости могут использовать его evidence, но не дублируют verdict.
- Артефакты в таблицах — **предписанные результаты описываемого процесса**. Сейчас создаются только их спецификации. Любая будущая проверка сопровождается evidence manifest по EVID: input/expected provenance, network/profile/build, fault point, actual output, verdict. Здесь evidence выполнения не создаётся.

## Ссылки на контракты

| Код | Нормативный раздел / тематическое представление |
|---|---|
| NATIVE | [Native admission, pins, decision и recovery](NATIVE_CAPTURE_PROTOCOL.md) |
| SEM | [Точные семантика и finality текущих сетей](CURRENT_SEMANTICS.md) |
| MSG | [Envelope, records и lifecycle](MESSAGE_CONTRACT.md) |
| TYPES | [Native transaction variants](TRANSACTION_TYPE_REGISTRY.md) |
| CONTROL | [IDs, binding, wrapper, layout, RPC](CONTROL_WIRE_CONTRACT.md) |
| DELIVERY | [Durable state machine](DELIVERY_STATE_MACHINE.md) |
| CONFIG | [Role YAML, native profile, команды и режимы](LIVE_CONFIG_SPEC.md) |
| BOOT | [Init, secrets, bootstrap, lifecycle окружения](DEPLOYMENT_BOOTSTRAP_CONTRACT.md) |
| TOPO | [Состав и размещение компонентов](NODE_DEPLOYMENT_SPEC.md) |
| PORTS | [Порты и экспонирование](NETWORK_PORTS.md) |
| STACK | [Компоненты, toolchains и build boundaries](COMPONENT_STACK.md) |
| EVID | [Независимые эталоны, gates, evidence](IMPLEMENTATION_GATES.md) |

## E01 — источник и ранние доказательства

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e01-01-evidence"></a>E01-01.evidence | `test/evidence`; COMMON | CaseRegistry/CaseResult/RunManifest schemas: полные profile/fork/fixture/suite identities D82, applicability отдельно от verdict, versioned evidence; NOT_RUN без выдуманных actual | SPEC → принятые требования | EVID |
| <a id="task-e01-01-builds"></a>E01-01.builds | `build/native`; ETH/BSC/POLY | Изолированный build/artifact manifest каждого клиента и supporting зависимости; source/patch/compiler/lock определены, native internal types не склеены в общий graph | E01-01.evidence → формат manifest | STACK, BOOT |
| <a id="task-e01-01-harness"></a>E01-01.harness | `test/harness`; ETH/BSC/POLY | Воспроизводимый минимальный runner с явными inputs/state/test credentials/supporting services и независимым expected; скрытая нода автора не нужна | E01-01.builds → закреплённые клиенты; E01-01.evidence → evidence schema | EVID, BOOT |
| <a id="task-e01-02-pins"></a>E01-02.pins | `adapters/<client>/state_provider`; ETH/BSC/POLY | Hash-provider/pin seam: source root/input остаётся доступным до допустимого release; неподдержанный backend отвергается | E01-01.harness → runner и parent state | NATIVE, CONFIG |
| <a id="task-e01-02-inputs"></a>E01-02.inputs | `adapters/<client>/source_inputs`; ETH/BSC/POLY | Static capsule и append-only consumed dependency log; возвращённый native execution ответ сначала durable, повтор не подменяет его свежим | E01-02.pins → закреплённый state/input owner | NATIVE |
| <a id="task-e01-02-owned_capture"></a>E01-02.owned_capture | `core/capture`; ETH/BSC/POLY | Owned capture seam: immutable chunks/snapshots, ограниченный дополнительный RAM, safe-point wait вне callbacks; mutable native alias не уходит worker | E01-01.harness → execution fixtures | NATIVE |
| <a id="task-e01-02-admission"></a>E01-02.admission | `adapters/<client>/admission`; ETH/BSC/POLY | Guard/pin/ADMITTED seam: обязательство обнаружимо после sync admission, до него публичного результата нет | E01-02.pins → pin contract; E01-02.inputs → capsule/log; E01-02.owned_capture → ownership boundary | NATIVE |
| <a id="task-e01-02-decision_bridge"></a>E01-02.decision_bridge | `adapters/<client>/decision`; ETH/BSC/POLY | Child-root/native marker/mirror seam: authoritative decision и одно delivery obligation восстанавливаются; rejected/no-event decision не создаёт public batch | E01-02.admission → admitted observation | NATIVE, DELIVERY |
| <a id="task-e01-02-lifecycle_seams"></a>E01-02.lifecycle_seams | `adapters/<client>/lifecycle`; ETH/BSC/POLY | Карта реальных execution/system/canonical/finality callers, включая event без нового блока и known branch; ни один обязательный caller не обходит guard | E01-02.decision_bridge → decision marker interface | NATIVE, SEM |
| <a id="task-e01-03-smoke_corpus"></a>E01-03.smoke_corpus | `test/native_corpus`; ETH/BSC/POLY | Минимальный независимый corpus transfer/nested revert/fees/system/empty; полный production semantic coverage этим не объявляется | E01-02.decision_bridge → native execution output; E01-01.harness → independent expected | NATIVE, SEM, TYPES |
| <a id="task-e01-04-early_faults"></a>E01-04.early_faults | `test/native_faults`; ETH/BSC/POLY | Evidence ранних admission/pin/EXEC_READY/decision/H seams при power loss; full native+Gateway matrix явно остаётся E05 | E01-02.lifecycle_seams → caller coverage; E01-03.smoke_corpus → inputs/expected | NATIVE, EVID |
| <a id="task-e01-05-type_paths"></a>E01-05.type_paths | `adapters/<client>/type_mapping`; ETH/BSC/POLY | Матрица действующих native transaction paths; unknown type не пропускается, BSC system и Bor StateSync обязательны | E01-03.smoke_corpus → native corpus | TYPES, SEM |

`G.E01`: все перечисленные обязательные выходы отдельно ETH/BSC/POLY. Это ранний seam proof; transport, вся семантика и combined fault matrix ещё не приняты.

## E02 — данные, identities и wire

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e02-01-semantic_descriptors"></a>E02-01.semantic_descriptors | `protocol/semantic`; ETH/BSC/POLY | Manifest semantic identity: activation/revision/source стабильны до delivery attempts; decision→batch выделяется один раз | E01-02.decision_bridge → decision interface | MSG, CONTROL, NATIVE |
| <a id="task-e02-05-actions_model"></a>E02-05.actions_model | `protocol/balance`; ETH/BSC/POLY | Типизированная модель native monetary actions, общий execution_order, cause equality и RollbackScope refs/tree/validator; receipt summary не суммируется как второе движение | E01-03.smoke_corpus → reference effects | MSG, SEM |
| <a id="task-e02-08-block_metadata"></a>E02-08.block_metadata | `protocol/block`; ETH/BSC/POLY | Block metadata/lifecycle schema, включая empty; native source counts не превращены в completeness assertion | E01-05.type_paths → профильные native поля | MSG, SEM |
| <a id="task-e02-10-closed_profiles"></a>E02-10.closed_profiles | `protocol/profiles`; ETH/BSC/POLY | Закрытый реестр payload/extensions/presence, D75 receipt kinds/native_type/origins/network details с exact widths/applicability; D80 sources_binding_digest:Hash32 обязателен в SourceObservationRef/L1Checkpoint; unsupported version не получает ANY fallback | E01-05.type_paths → source variants | TYPES, SEM, MSG |
| <a id="task-e02-02-schemas"></a>E02-02.schemas | `protocol/proto`; COMMON | Воспроизводимые schema/bindings и structural validators: required presence, oneof, lengths, unknown values определены | E02-01.semantic_descriptors → identities; E02-05.actions_model → action schema; E02-08.block_metadata → block schema; E02-10.closed_profiles → variants | MSG, CONTROL |
| <a id="task-e02-02-codec"></a>E02-02.codec | `protocol/codec`; COMMON | Bounded typed RecordFragment/tree/blob codec, streaming manifests и exact transport layout/headers/digests; полная Envelope не сериализуется одним message, крупное поле также дробится | E02-02.schemas → typed messages/validators | CONTROL, DELIVERY |
| <a id="task-e02-03-lifecycle_validator"></a>E02-03.lifecycle_validator | `test/lifecycle_validator`; ETH/BSC/POLY | Тестовый lifecycle automaton: reordered Applied/Removed/Status, revision conflicts и reinclusion имеют однозначный результат; production consumer не создаётся | E02-02.schemas → lifecycle messages; E02-01.semantic_descriptors → identity invariants | MSG, SEM |
| <a id="task-e02-04-compatibility"></a>E02-04.compatibility | `protocol/compatibility`; COMMON | Read/replay/new-write capability matrix: immutable pending поддержан либо transition отвергнут; drain не разрешает несовместимый remaining metadata format | E02-02.codec → encoding versions | CONTROL, DELIVERY, CONFIG |

`G.E02`: все обязательные схемы и локальные contract cases определены и проверяемы. Native correctness расширяется в E04; реальная delivery integration — E03.

## E03 — доставка от durable obligation

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e03-01-producer_store"></a>E03-01.producer_store | `core/journal/producer`; COMMON | Producer state machine INTENT/STAGING/READY/SEALED/ACKED_CLEANUP/outbox; closing commits атомарны, attempt не меняет semantic identity | E02-02.codec → immutable bytes; E02-01.semantic_descriptors → descriptors | DELIVERY, CONTROL |
| <a id="task-e03-01-gateway_store"></a>E03-01.gateway_store | `gateway/journal`; COMMON | PREPARING/reservation/RECEIVING/CLOSED/ACKED/RELEASING store; reservation retry не выделяет новую sequence | E03-01.producer_store → batch/plan interface | DELIVERY, CONTROL |
| <a id="task-e03-02-rpc_streaming"></a>E03-02.rpc_streaming | `gateway/ingest`; COMMON | Bounded chunk RPC и IMPORT_SEALED; offset/conflict/manifest validation предшествуют CLOSED | E03-01.gateway_store → Gateway transitions; E03-01.producer_store → producer transitions | CONTROL, DELIVERY |
| <a id="task-e03-02-fencing"></a>E03-02.fencing | `gateway/session`; COMMON | Session/admission/publisher fencing: stale RPC/callback не создаёт state после barrier или Release | E03-02.rpc_streaming → mutating RPC boundary | CONTROL, DELIVERY |
| <a id="task-e03-03-destination"></a>E03-03.destination | `gateway/destination`; COMMON | Pre-plan binding и online account/stream/route plus exact D77 metadata validation; missing/mismatch не перенаправляет pending | E03-02.fencing → authenticated owner; E02-02.codec → binding digest | CONTROL, CONFIG, BOOT |
| <a id="task-e03-03-publisher"></a>E03-03.publisher | `gateway/publisher`; COMMON | NATS PubAck → durable NATS_ACKED → IngestAck одного manifest/plan/destination; partial ACK не завершает batch | E03-03.destination → verified target; E03-02.rpc_streaming → CLOSED frames | DELIVERY, CONTROL, BOOT |
| <a id="task-e03-01-cleanup"></a>E03-01.cleanup | `core/delivery/release`; COMMON | Ordered Release/RecoveryRelease/retired baseline обеих сторон, bounded RELEASING cleanup; ACKed payload не нужен после Gateway loss | E03-03.publisher → IngestAck; E03-02.fencing → barrier | DELIVERY, CONTROL |
| <a id="task-e03-03-maintenance"></a>E03-03.maintenance | `core/delivery/maintenance`; COMMON | Seal/Switch/Resume, D77 target metadata checks и idempotent committed receipt при NATS outage; MAINTENANCE recovery от заданного quiesced producer, без proof native quiescence | E03-01.cleanup → empty delivery frontier; E03-03.destination → binding; E03-02.fencing → session barrier | DELIVERY, CONTROL |
| <a id="task-e03-04-pressure_errors"></a>E03-04.pressure_errors | `core/delivery/errors`; COMMON | Transient/config/poison/ENOSPC behavior и bounded queues; pending без TTL/квоты, local fatal и remote outage различаются | E03-03.publisher → retry points; E03-01.cleanup → cleanup points | DELIVERY, CONFIG |
| <a id="task-e03-05-recovery_matrix"></a>E03-05.recovery_matrix | `test/delivery_faults`; COMMON | Delivery-only fault evidence от заданного durable INTENT/READY; Gateway loss/late RPC/partial cleanup проверены, full REC-T01 не присваивается | E03-03.maintenance → transition machine; E03-04.pressure_errors → error states; E02-04.compatibility → capability matrix | DELIVERY, CONTROL, EVID |
| <a id="task-e03-11-block_lifecycle"></a>E03-11.block_lifecycle | `test/nats_smoke`; ETH/BSC/POLY | NATS smoke corpus block/tx/lifecycle/empty с parts/PubAck; не verifier полного блокчейна | E03-03.publisher → publish path; E01-03.smoke_corpus → native inputs; E02-03.lifecycle_validator → expected lifecycle | MSG, DELIVERY, EVID |

`G.E03`: producer/Gateway/NATS delivery guarantees от заданного durable входа. Native admission seam принят отдельно в E01; комбинированный reorg/finality/capture crash proof остаётся E05.

## E04 — полная обязательная семантика

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e04-01-calls"></a>E04-01.calls | `core/normalization/calls`; ETH/BSC/POLY | Полное дерево calls, общий порядок с actions, native scope mapping и outcome/committed; D81 отличает attempted/локально успешный created_address от конечного existence; успешный child не переживает ancestor rollback | E02-02.schemas → call variants; E02-05.actions_model → order/scopes; E01-05.type_paths → native paths | MSG, SEM |
| <a id="task-e04-01-receipts"></a>E04-01.receipts | `adapters/<client>/receipts`; ETH/BSC/POLY | Native receipt consensus/storage registry, D76 bounded byte-equivalent writer, log/create/network-details projection; source и derived значения не подменяют друг друга | E01-05.type_paths → receipt origins; E02-10.closed_profiles → receipt registry; E02-02.codec → D73 fragments/spool | MSG, SEM, TYPES |
| <a id="task-e04-01-authorization"></a>E04-01.authorization | `core/normalization/authorization`; ETH/BSC/POLY по native applicability | Исходные authorization tuples, включая invalid/duplicate, сохранены; validation внешней tx не фильтрует tuples | E04-01.receipts → native outcome; E02-10.closed_profiles → source widths | TYPES, SEM |
| <a id="task-e04-01-block_record"></a>E04-01.block_record | `core/normalization/block`; ETH/BSC/POLY | Native BlockApplied projection: header/RLP/count/optional presence и source timestamp precision точны для empty и nonempty blocks | E02-08.block_metadata → typed block schema; E01-05.type_paths → source header variants | MSG, SEM |
| <a id="task-e04-03-state_diff"></a>E04-03.state_diff | `core/normalization/state`; ETH/BSC/POLY | Transaction-boundary existence/balance/nonce/code/storage before/after; rollback/empty account/reset корректны | E04-01.calls → scopes; E01-02.owned_capture → immutable snapshots | MSG, SEM |
| <a id="task-e04-01-selfdestruct"></a>E04-01.selfdestruct | `core/normalization/selfdestruct`; ETH/BSC/POLY | SELFDESTRUCT action, monetary movement и deletion раздельны при zero/self-beneficiary/revert/fork cases | E04-03.state_diff → boundary diff; E04-01.calls → call scopes | SEM, MSG |
| <a id="task-e04-03-balance_actions"></a>E04-03.balance_actions | `core/normalization/balance`; ETH/BSC/POLY | Причинные денежные движения, fees/refunds/credits; native scope и стабильные order/IDs при capture recovery; net-zero actions не исчезают, summary не удваивает effects | E04-03.state_diff → account delta; E04-01.receipts → fee summary; E02-05.actions_model → action schema | SEM, MSG |
| <a id="task-e04-04-import_pressure"></a>E04-04.import_pressure | `test/native_import`; ETH/BSC/POLY | Полный import path и large-field/slow-worker behavior; native results не меняются, collector memory bounded, системные scopes не смешаны | E04-03.balance_actions → full effects; E04-01.selfdestruct → action semantics; E04-01.block_record → full block projection; E01-02.owned_capture → safe-point seam | NATIVE, SEM, EVID |
| <a id="task-e04-02-optional_scope"></a>E04-02.optional_scope | `protocol/optional_scope`; ETH/BSC/POLY | Scope declaration: default OFF без потери native tx effects; enabled outside-tx/legacy record только с объявленными typed fixtures | E04-03.balance_actions → обязательный tx scope | MSG, TYPES, SEM |

`G.E04`: обязательные current-network semantic cases. Default OFF case обязателен; включённая optional-ветка условна. Historical coverage и performance не выводятся из этого gate.

## E05 — интеграция native lifecycle и recovery

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e05-01-registry"></a>E05-01.registry | `adapters/<client>/lifecycle_registry`; ETH/BSC/POLY | Durable activation/revision registry и native decision mirror; speculative corpus не становится Applied, READY Applied остаётся отменяемым | E02-03.lifecycle_validator → semantic oracle; E01-02.lifecycle_seams → native callers | NATIVE, MSG |
| <a id="task-e05-01-eth_finality"></a>E05-01.eth_finality | `adapters/geth/finality`; ETH | Typed native safe/finalized observations, setter/restart/no-new-block semantics; GC anchor только authoritative finalized ancestry | E05-01.registry → activation refs; E01-02.lifecycle_seams → native setter paths | SEM, NATIVE |
| <a id="task-e05-01-bsc_finality"></a>E05-01.bsc_finality | `adapters/bsc/finality`; BSC | Early VotePool/header-attested evidence раздельны; GC допускает только установленный контрактом header Source | E05-01.registry → activation refs; E01-02.lifecycle_seams → native finality paths | SEM, NATIVE |
| <a id="task-e05-01-polygon_observer"></a>E05-01.polygon_observer | `adapters/bor/finality_observer`; POLY | Native Polygon statuses и L1-finalized checkpoint evidence с D80 sources_binding_digest, закреплённым за observation и неизменным при replay/config replacement; публикация tx/status не ждёт GC source, source outage не выдумывает anchor | E05-01.registry → activation refs; E01-01.harness → own Polygon L1 fixture endpoints | SEM, NATIVE, BOOT |
| <a id="task-e05-01-activation_gc"></a>E05-01.activation_gc | `adapters/<client>/recovery_gc`; ETH/BSC/POLY | GC decision по durable terminal/anchor, ancestry и отсутствию pending; no-anchor сохраняет pins, доказанная regression ниже принятого anchor fatal | E05-01.eth_finality → ETH evidence; E05-01.bsc_finality → BSC evidence; E05-01.polygon_observer → POLY evidence; E03-01.cleanup → delivery status | SEM, NATIVE |
| <a id="task-e05-02-reorg"></a>E05-02.reorg | `test/native_lifecycle`; ETH/BSC/POLY | Reorg/reapply/known-branch/reordered events evidence; Removed создаётся для READY Applied независимо от PubAck | E05-01.registry → registry; E05-01.activation_gc → retained ancestry | NATIVE, MSG, SEM |
| <a id="task-e05-03-drain"></a>E05-03.drain | `adapters/<client>/maintenance`; ETH/BSC/POLY | Native RECOVERY_ONLY DRAIN закрывает все obligations до seal/highwater; retained future-GC pins допустимы, новых admissions/poll нет | E05-02.reorg → pending native transitions; E03-03.maintenance → delivery drain/switch | NATIVE, DELIVERY, CONTROL, CONFIG |
| <a id="task-e05-03-upgrade"></a>E05-03.upgrade | `test/compatibility_recovery`; ETH/BSC/POLY | Old-build drain/compatible replay/rollback matrix с retained lifecycle metadata; incompatible persistent format всегда отвергнут prewrite | E05-03.drain → completed drain; E02-04.compatibility → read/write rules | DELIVERY, CONFIG, NATIVE |
| <a id="task-e05-04-concurrent_fencing"></a>E05-04.concurrent_fencing | `test/native_concurrency`; ETH/BSC/POLY | Гонки native decisions/old worker/session/late PubAck не создают второе действие или resurrected state | E05-03.drain → mode barrier; E05-02.reorg → lifecycle races; E03-02.fencing → transport fencing | NATIVE, DELIVERY, CONTROL |
| <a id="task-e05-03-full_crash_matrix"></a>E05-03.full_crash_matrix | `test/composed_faults`; ETH/BSC/POLY | Полный native→WAL→Gateway→NATS crash/reorg/finality/restart proof, включая cleanup/GC; только здесь combined REC-T01 | E05-04.concurrent_fencing → race evidence; E05-03.upgrade → version recovery; E03-05.recovery_matrix → delivery-only evidence | NATIVE, DELIVERY, SEM, EVID |

`G.E05`: полные combined guarantees каждой сети. Ранняя карта callers E01 и delivery-only E03 остаются самостоятельными evidence, не переименовываются в полный proof.

## E06 — пользовательский комплект и эксплуатационные процедуры

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e06-07-yaml"></a>E06-07.yaml | `config`; ETH/BSC/POLY | Strict role config/effective native preflight и side-effect-free check; storage paths от YAML, Polygon sources_file absolute-only; только declared keys/profiles/modes, binding_file — desired descriptor | E02-02.codec → descriptor parser; E05-03.drain → mode contract | CONFIG, CONTROL |
| <a id="task-e06-13-artifact_lock"></a>E06-13.artifact_lock | `deploy/artifacts`; ETH/BSC/POLY | User artifact manifest: source/patch/toolchain/image/genesis identity; неправильный artifact отвергается до init | E01-01.builds → build manifest; E06-07.yaml → profile identity | BOOT, STACK |
| <a id="task-e06-13-init"></a>E06-13.init | `deploy/init`; ETH/BSC/POLY | Idempotent phase-driven native init; повтор/partial init сохраняет keys/state и проверяет genesis/ENV_READY | E06-13.artifact_lock → verified artifacts; E06-07.yaml → paths/profile | BOOT |
| <a id="task-e06-13-topology"></a>E06-13.topology | `deploy/compose+kubernetes`; ETH/BSC/POLY | 11 services/14 volumes с проверяемыми backing owners; adapter/Gateway и Polygon L1 изолированы | E06-13.init → initialized homes; E06-07.yaml → role stores | TOPO, BOOT |
| <a id="task-e06-13-secrets"></a>E06-13.secrets | `deploy/trust`; ETH/BSC/POLY | CA/SAN/EKU/role/network admission, private keys/JWT/credentials и rotation; bootstrap admin не попадает Gateway | E06-07.yaml → connection roles | BOOT, CONFIG |
| <a id="task-e06-13-nats_bootstrap"></a>E06-13.nats_bootstrap | `deploy/nats`; COMMON | Создание/проверка accounts/streams/ACL и D77 identity metadata; CREATE/read-back, отказ adoption missing keys, compatible UPDATE сохраняет посторонние keys; runtime без admin/reset | E06-13.secrets → trust material; E03-03.destination → expected binding | BOOT, CONTROL |
| <a id="task-e06-13-network"></a>E06-13.network | `deploy/network`; ETH/BSC/POLY | Port mapping/advertise/internal links, включая Bor observer→собственная supporting L1; наружу только P2P, localhost/чужая L1 identity отвергнуты | E06-13.topology → services; E06-13.secrets → authenticated connections | PORTS, BOOT, TOPO |
| <a id="task-e06-08-config_replace"></a>E06-08.config_replace | `deploy/config_generation`; ETH/BSC/POLY | Config replacement меняет только allowlist, применим после restart; immutable state/keys/genesis не перетираются | E06-13.init → phase state; E06-07.yaml → validated desired config | BOOT, CONFIG |
| <a id="task-e06-08-maintenance"></a>E06-08.maintenance | `test/user_maintenance`; ETH/BSC/POLY | Исполнимый пользовательский stop-confirm-start/drain/switch/Resume/upgrade сценарий, включая shared-volume и delayed-writer ошибки | E06-08.config_replace → config generations; E06-13.network → reachable services; E06-13.nats_bootstrap → target; E05-03.upgrade → version rules | BOOT, CONFIG, DELIVERY, CONTROL |
| <a id="task-e06-09-live_start"></a>E06-09.live_start | `test/user_startup`; ETH/BSC/POLY | Clean startup/sync→H+1 и independent pending; NATS outage не блокирует safe capture, restart одной сети не требует остальных | E06-13.network → links; E06-13.nats_bootstrap → NATS; E06-08.config_replace → effective configs | CONFIG, NATIVE, BOOT |
| <a id="task-e06-12-logs"></a>E06-12.logs | `config/control_logs`; ETH/BSC/POLY | Обязательные structured control states/errors при debug OFF; secrets отсутствуют, SIGKILL не сообщает graceful STOPPED | E06-09.live_start → startup states; E06-08.maintenance → maintenance/error states | BOOT, CONFIG, DELIVERY |
| <a id="task-e06-13-clean_bootstrap"></a>E06-13.clean_bootstrap | `test/deployment`; ETH/BSC/POLY | Полный user-stack reproduction evidence из чистого окружения; ранний harness не подменяет комплект | E06-12.logs → diagnosable states; E06-13.artifact_lock → artifacts; E06-13.topology → owner map | BOOT, TOPO, EVID |

`G.E06`: полный комплект по обязательным BOOT/CONFIG/MOUNT/TRUST/NATS/START/REPLACE/LOG/ARTIFACT cases. Это не публичная упаковка и не production/performance методика.

## E07 — приёмочный результат

| Child ID | Owner; применимость | Один выход и обязательный invariant | Зависимости → получаемый выход | Контракт |
|---|---|---|---|---|
| <a id="task-e07-02-e2e"></a>E07-02.e2e | `test/acceptance`; ETH/BSC/POLY | End-to-end native correctness/fault verdict каждой сети с независимым expected; source-only анализ не считается runtime PASS | E05-03.full_crash_matrix → combined inputs/evidence; E06-13.clean_bootstrap → user environment | EVID, MSG, SEM |
| <a id="task-e07-02-scope"></a>E07-02.scope | `test/scope_audit`; ETH/BSC/POLY | Trace boundary native→NATS, отсутствие лишних downstream/HA/history/performance обязательств; optional scope явно отделён | E07-02.e2e → observed product path | EVID, MSG, CONFIG |
| <a id="task-e07-03-runbook"></a>E07-03.runbook | `docs/runbook`; ETH/BSC/POLY | Проверяемый запуск/остановка/диагностика/recovery drill по фактическим states; команды не требуют скрытого окружения | E07-02.e2e → accepted path; E06-12.logs → observable states; E06-08.maintenance → operating procedure | BOOT, CONFIG, DELIVERY |
| <a id="task-e07-08-isolation"></a>E07-08.isolation | `test/network_isolation`; ETH/BSC/POLY | Матрица add/config/stop/restart одной сети без изменения соседних writers/stores; общий NATS не означает shared journal | E07-02.e2e → running acceptance topology | TOPO, DELIVERY, EVID |
| <a id="task-e07-09-nats_acceptance"></a>E07-09.nats_acceptance | `test/nats_reader`; ETH/BSC/POLY | Финальный NATS corpus: fields/parts/lifecycle/empty/duplicates/PubAck согласованы с native expected; production consumer не создаётся | E07-02.e2e → native expected; E07-02.scope → boundary | MSG, CONTROL, EVID |
| <a id="task-e07-09-requirement_verdict"></a>E07-09.requirement_verdict | `test/evidence`; COMMON | Полная CaseRegistry/CaseResult матрица полных identities D82; individual run outcomes, mandatory missing/FAIL/NOT_RUN блокируют gate, исключения требуют rules | E07-09.nats_acceptance → output evidence; E07-08.isolation → isolation evidence; E07-03.runbook → operating evidence | EVID |

`G.E07`: все обязательные AC закрыты по своей применимости и итоговая матрица непротиворечива. В настоящей работе G.E01–G.E07 **не исполняются**, все runtime результаты NOT_RUN.

## Отложенные и снятые parents

E06-01/02/03/05/10/11 — DEFERRED D38; E06-04 — REMOVED D25; E06-06 — REMOVED D36; E07-01 — OUT_OF_SCOPE D40; E07-04/07 — DEFERRED D41. По D16 сняты E02-06/07/09, E03-06/07/08/09/10, E07-05/06. Они не входят в DAG обязательных gates. E08 и прежние снятые IDs не переиспользуются. E04-02 сохраняет только условную enabled-ветку и обязательное подтверждение default OFF.

## REQ/AC → ответственный task

Все 63 REQ и 211 AC имеют точное назначение. Диапазон `…` включает все целые suffix между границами; запятая перечисляет отдельные интервалы. Условность AC-SCP-05-02 задана в каталоге. Stage совпадает со stage AC, поэтому ранняя проверка не зависит от E05/E06. Сценарии читаются в [каталоге AC](ACCEPTANCE_CASES.md); ссылки в столбце task ведут к конкретной строке результата.

| REQ | AC ID / непрерывные диапазоны | Единственный ответственный task | Stage |
|---|---|---|---|
| REQ-SCP-01 | AC-SCP-01-01…AC-SCP-01-02 | [E06-07.yaml](#task-e06-07-yaml) | E06 |
| REQ-SCP-02 | AC-SCP-02-01 | [E07-02.scope](#task-e07-02-scope) | E07 |
| REQ-SCP-03 | AC-SCP-03-01 | [E01-04.early_faults](#task-e01-04-early_faults) | E01 |
| REQ-SCP-03 | AC-SCP-03-02 | [E05-03.full_crash_matrix](#task-e05-03-full_crash_matrix) | E05 |
| REQ-SCP-04 | AC-SCP-04-01…AC-SCP-04-02 | [E04-04.import_pressure](#task-e04-04-import_pressure) | E04 |
| REQ-SCP-05 | AC-SCP-05-01…AC-SCP-05-02 | [E04-02.optional_scope](#task-e04-02-optional_scope) | E04 |
| REQ-SCP-06 | AC-SCP-06-01 | [E07-02.scope](#task-e07-02-scope) | E07 |
| REQ-CAP-01 | AC-CAP-01-01 | [E01-02.lifecycle_seams](#task-e01-02-lifecycle_seams) | E01 |
| REQ-CAP-02 | AC-CAP-02-01…AC-CAP-02-02 | [E01-02.admission](#task-e01-02-admission) | E01 |
| REQ-CAP-03 | AC-CAP-03-01…AC-CAP-03-02 | [E01-02.pins](#task-e01-02-pins) | E01 |
| REQ-CAP-04 | AC-CAP-04-01…AC-CAP-04-03 | [E01-02.inputs](#task-e01-02-inputs) | E01 |
| REQ-CAP-05 | AC-CAP-05-01 | [E01-02.owned_capture](#task-e01-02-owned_capture) | E01 |
| REQ-CAP-05 | AC-CAP-05-02 | [E04-03.state_diff](#task-e04-03-state_diff) | E04 |
| REQ-CAP-06 | AC-CAP-06-01 | [E04-04.import_pressure](#task-e04-04-import_pressure) | E04 |
| REQ-CAP-06 | AC-CAP-06-02 | [E01-02.owned_capture](#task-e01-02-owned_capture) | E01 |
| REQ-CAP-07 | AC-CAP-07-01…AC-CAP-07-03, AC-CAP-07-06 | [E01-02.decision_bridge](#task-e01-02-decision_bridge) | E01 |
| REQ-CAP-07 | AC-CAP-07-04…AC-CAP-07-05 | [E05-01.registry](#task-e05-01-registry) | E05 |
| REQ-CAP-08 | AC-CAP-08-01 | [E03-01.producer_store](#task-e03-01-producer_store) | E03 |
| REQ-CAP-08 | AC-CAP-08-02 | [E05-02.reorg](#task-e05-02-reorg) | E05 |
| REQ-CAP-09 | AC-CAP-09-01 | [E04-01.receipts](#task-e04-01-receipts) | E04 |
| REQ-CAP-10 | AC-CAP-10-01 | [E05-03.full_crash_matrix](#task-e05-03-full_crash_matrix) | E05 |
| REQ-DAT-01 | AC-DAT-01-01…AC-DAT-01-02 | [E02-02.schemas](#task-e02-02-schemas) | E02 |
| REQ-DAT-02 | AC-DAT-02-03 | [E02-10.closed_profiles](#task-e02-10-closed_profiles) | E02 |
| REQ-DAT-02 | AC-DAT-02-01…AC-DAT-02-02, AC-DAT-02-04…AC-DAT-02-05 | [E04-01.receipts](#task-e04-01-receipts) | E04 |
| REQ-DAT-03 | AC-DAT-03-01 | [E04-01.authorization](#task-e04-01-authorization) | E04 |
| REQ-DAT-04 | AC-DAT-04-01…AC-DAT-04-03 | [E04-01.calls](#task-e04-01-calls) | E04 |
| REQ-DAT-05 | AC-DAT-05-03 | [E02-05.actions_model](#task-e02-05-actions_model) | E02 |
| REQ-DAT-05 | AC-DAT-05-01…AC-DAT-05-02, AC-DAT-05-04…AC-DAT-05-05 | [E04-01.calls](#task-e04-01-calls) | E04 |
| REQ-DAT-05 | AC-DAT-05-06…AC-DAT-05-07 | [E04-03.balance_actions](#task-e04-03-balance_actions) | E04 |
| REQ-DAT-06 | AC-DAT-06-01…AC-DAT-06-04 | [E04-01.selfdestruct](#task-e04-01-selfdestruct) | E04 |
| REQ-DAT-07 | AC-DAT-07-01…AC-DAT-07-02 | [E04-03.state_diff](#task-e04-03-state_diff) | E04 |
| REQ-DAT-08 | AC-DAT-08-01…AC-DAT-08-03 | [E04-03.balance_actions](#task-e04-03-balance_actions) | E04 |
| REQ-DAT-09 | AC-DAT-09-03 | [E02-10.closed_profiles](#task-e02-10-closed_profiles) | E02 |
| REQ-DAT-09 | AC-DAT-09-01…AC-DAT-09-02, AC-DAT-09-04 | [E04-01.receipts](#task-e04-01-receipts) | E04 |
| REQ-DAT-10 | AC-DAT-10-01 | [E04-01.block_record](#task-e04-01-block_record) | E04 |
| REQ-LIF-01 | AC-LIF-01-01…AC-LIF-01-02 | [E05-01.registry](#task-e05-01-registry) | E05 |
| REQ-LIF-02 | AC-LIF-02-01 | [E05-02.reorg](#task-e05-02-reorg) | E05 |
| REQ-LIF-03 | AC-LIF-03-01…AC-LIF-03-03 | [E02-03.lifecycle_validator](#task-e02-03-lifecycle_validator) | E02 |
| REQ-LIF-04 | AC-LIF-04-01…AC-LIF-04-02 | [E05-03.full_crash_matrix](#task-e05-03-full_crash_matrix) | E05 |
| REQ-LIF-05 | AC-LIF-05-01, AC-LIF-05-04 | [E05-01.eth_finality](#task-e05-01-eth_finality) | E05 |
| REQ-LIF-05 | AC-LIF-05-02…AC-LIF-05-03 | [E01-02.lifecycle_seams](#task-e01-02-lifecycle_seams) | E01 |
| REQ-LIF-06 | AC-LIF-06-01…AC-LIF-06-03 | [E05-01.bsc_finality](#task-e05-01-bsc_finality) | E05 |
| REQ-LIF-06 | AC-LIF-06-04…AC-LIF-06-05 | [E01-02.lifecycle_seams](#task-e01-02-lifecycle_seams) | E01 |
| REQ-LIF-07 | AC-LIF-07-07 | [E02-10.closed_profiles](#task-e02-10-closed_profiles) | E02 |
| REQ-LIF-07 | AC-LIF-07-01…AC-LIF-07-06, AC-LIF-07-08…AC-LIF-07-09 | [E05-01.polygon_observer](#task-e05-01-polygon_observer) | E05 |
| REQ-LIF-08 | AC-LIF-08-01…AC-LIF-08-05 | [E05-01.activation_gc](#task-e05-01-activation_gc) | E05 |
| REQ-DLV-01 | AC-DLV-01-01…AC-DLV-01-02 | [E03-05.recovery_matrix](#task-e03-05-recovery_matrix) | E03 |
| REQ-DLV-02 | AC-DLV-02-01…AC-DLV-02-05 | [E03-01.producer_store](#task-e03-01-producer_store) | E03 |
| REQ-DLV-03 | AC-DLV-03-01…AC-DLV-03-02 | [E03-02.rpc_streaming](#task-e03-02-rpc_streaming) | E03 |
| REQ-DLV-03 | AC-DLV-03-03 | [E03-05.recovery_matrix](#task-e03-05-recovery_matrix) | E03 |
| REQ-DLV-03 | AC-DLV-03-04 | [E03-01.gateway_store](#task-e03-01-gateway_store) | E03 |
| REQ-DLV-04 | AC-DLV-04-01…AC-DLV-04-02, AC-DLV-04-04…AC-DLV-04-09, AC-DLV-04-12 | [E02-02.codec](#task-e02-02-codec) | E02 |
| REQ-DLV-04 | AC-DLV-04-03, AC-DLV-04-10 | [E03-03.publisher](#task-e03-03-publisher) | E03 |
| REQ-DLV-04 | AC-DLV-04-11 | [E03-05.recovery_matrix](#task-e03-05-recovery_matrix) | E03 |
| REQ-DLV-05 | AC-DLV-05-01…AC-DLV-05-03 | [E03-03.publisher](#task-e03-03-publisher) | E03 |
| REQ-DLV-06 | AC-DLV-06-01…AC-DLV-06-04 | [E03-01.cleanup](#task-e03-01-cleanup) | E03 |
| REQ-DLV-07 | AC-DLV-07-01…AC-DLV-07-02, AC-DLV-07-04 | [E03-05.recovery_matrix](#task-e03-05-recovery_matrix) | E03 |
| REQ-DLV-07 | AC-DLV-07-03 | [E05-03.full_crash_matrix](#task-e05-03-full_crash_matrix) | E05 |
| REQ-DLV-08 | AC-DLV-08-01…AC-DLV-08-02 | [E03-02.fencing](#task-e03-02-fencing) | E03 |
| REQ-DLV-09 | AC-DLV-09-01…AC-DLV-09-05 | [E03-03.destination](#task-e03-03-destination) | E03 |
| REQ-DLV-09 | AC-DLV-09-06…AC-DLV-09-08 | [E03-03.maintenance](#task-e03-03-maintenance) | E03 |
| REQ-DLV-10 | AC-DLV-10-01…AC-DLV-10-04 | [E05-03.drain](#task-e05-03-drain) | E05 |
| REQ-DLV-11 | AC-DLV-11-01 | [E06-08.maintenance](#task-e06-08-maintenance) | E06 |
| REQ-DLV-11 | AC-DLV-11-02, AC-DLV-11-04…AC-DLV-11-07 | [E03-03.maintenance](#task-e03-03-maintenance) | E03 |
| REQ-DLV-11 | AC-DLV-11-03 | [E05-03.drain](#task-e05-03-drain) | E05 |
| REQ-DLV-11 | AC-DLV-11-08 | [E05-04.concurrent_fencing](#task-e05-04-concurrent_fencing) | E05 |
| REQ-DLV-12 | AC-DLV-12-01 | [E05-03.drain](#task-e05-03-drain) | E05 |
| REQ-DLV-13 | AC-DLV-13-01…AC-DLV-13-02, AC-DLV-13-04…AC-DLV-13-05 | [E03-04.pressure_errors](#task-e03-04-pressure_errors) | E03 |
| REQ-DLV-13 | AC-DLV-13-03 | [E05-03.full_crash_matrix](#task-e05-03-full_crash_matrix) | E05 |
| REQ-DLV-14 | AC-DLV-14-01 | [E03-05.recovery_matrix](#task-e03-05-recovery_matrix) | E03 |
| REQ-DLV-14 | AC-DLV-14-02 | [E05-03.upgrade](#task-e05-03-upgrade) | E05 |
| REQ-DLV-14 | AC-DLV-14-03 | [E06-08.maintenance](#task-e06-08-maintenance) | E06 |
| REQ-OPS-01 | AC-OPS-01-01, AC-OPS-01-04…AC-OPS-01-05 | [E06-07.yaml](#task-e06-07-yaml) | E06 |
| REQ-OPS-01 | AC-OPS-01-02…AC-OPS-01-03 | [E01-02.pins](#task-e01-02-pins) | E01 |
| REQ-OPS-02 | AC-OPS-02-01…AC-OPS-02-02 | [E06-13.topology](#task-e06-13-topology) | E06 |
| REQ-OPS-03 | AC-OPS-03-01, AC-OPS-03-03 | [E06-13.init](#task-e06-13-init) | E06 |
| REQ-OPS-03 | AC-OPS-03-02 | [E01-02.pins](#task-e01-02-pins) | E01 |
| REQ-OPS-04 | AC-OPS-04-01…AC-OPS-04-02 | [E06-08.config_replace](#task-e06-08-config_replace) | E06 |
| REQ-OPS-05 | AC-OPS-05-01 | [E03-02.fencing](#task-e03-02-fencing) | E03 |
| REQ-OPS-05 | AC-OPS-05-02…AC-OPS-05-03 | [E06-13.secrets](#task-e06-13-secrets) | E06 |
| REQ-OPS-06 | AC-OPS-06-01, AC-OPS-06-03 | [E03-03.destination](#task-e03-03-destination) | E03 |
| REQ-OPS-06 | AC-OPS-06-02, AC-OPS-06-04…AC-OPS-06-06 | [E06-13.nats_bootstrap](#task-e06-13-nats_bootstrap) | E06 |
| REQ-OPS-07 | AC-OPS-07-01…AC-OPS-07-03 | [E06-13.network](#task-e06-13-network) | E06 |
| REQ-OPS-08 | AC-OPS-08-01 | [E06-09.live_start](#task-e06-09-live_start) | E06 |
| REQ-OPS-08 | AC-OPS-08-02 | [E06-08.maintenance](#task-e06-08-maintenance) | E06 |
| REQ-OPS-09 | AC-OPS-09-01…AC-OPS-09-02 | [E06-12.logs](#task-e06-12-logs) | E06 |
| REQ-OPS-10 | AC-OPS-10-01, AC-OPS-10-03 | [E01-01.builds](#task-e01-01-builds) | E01 |
| REQ-OPS-10 | AC-OPS-10-02 | [E06-13.clean_bootstrap](#task-e06-13-clean_bootstrap) | E06 |
| REQ-DEV-01 | AC-DEV-01-01 | [E01-01.evidence](#task-e01-01-evidence) | E01 |
| REQ-DEV-02 | AC-DEV-02-01…AC-DEV-02-07 | [E07-02.e2e](#task-e07-02-e2e) | E07 |
| REQ-DEV-03 | AC-DEV-03-01 | [E01-01.builds](#task-e01-01-builds) | E01 |
| REQ-DEV-04 | AC-DEV-04-02…AC-DEV-04-03 | [E01-01.evidence](#task-e01-01-evidence) | E01 |
| REQ-DEV-04 | AC-DEV-04-01, AC-DEV-04-04…AC-DEV-04-05 | [E07-09.requirement_verdict](#task-e07-09-requirement_verdict) | E07 |
| REQ-DEV-05 | AC-DEV-05-01…AC-DEV-05-06 | [E07-09.requirement_verdict](#task-e07-09-requirement_verdict) | E07 |

## Приёмка артефактов без отдельного AC-owner

Эти задачи дают вход другому ответственному task, а не дублируют его AC verdict. Их локальный выход принимается по указанному критерию; интеграционный AC закрывает owner из основной карты. Поэтому обратной зависимости «поставщик ждёт своего потребителя» в DAG нет. Gate требует и локальный выход, и назначенный AC. Для остальных задач локальный критерий — invariant строки плюс их собственные AC.

| Task | Локальная приёмка единственного выхода | Связанный AC того же stage, без смены owner |
|---|---|---|
| E01-01.harness | Паспорт содержит реальные input/state/bootstrap paths, команды и независимый expected; runner вызывает native seam и сохраняет evidence | AC-CAP-01-01 |
| E01-03.smoke_corpus | Есть transfer/nested revert/fees/system/empty inputs с pinned provenance и ожидаемыми результатами; это минимальный corpus | AC-CAP-01-01, AC-CAP-07-01…AC-CAP-07-03 |
| E01-05.type_paths | Каждый обязательный native type/path связан с source caller и минимальным входом; missing path явно FAIL | AC-CAP-01-01 |
| E02-01.semantic_descriptors | Schema сохраняет смысл semantic ref/activation/revision и не смешивает их с delivery attempt ID; vectors проверяют widths/presence | AC-DAT-01-01…AC-DAT-01-02, AC-LIF-03-01…AC-LIF-03-03 |
| E02-05.actions_model | Schema выражает action/link/fee component, общий order и rollback scopes; валидатор отвергает неоднозначные/невозможные ссылки и committed rollback, zero/absence различимы | AC-DAT-01-01…AC-DAT-01-02, AC-DAT-05-03 |
| E02-08.block_metadata | Schema выражает empty block, native header fields, counts и source timestamp precision без BlockComplete | AC-DAT-01-01…AC-DAT-01-02 |
| E02-10.closed_profiles | Каждая variant имеет typed schema/applicability/width; receipt kind/origin/type и network details однозначны; unknown profile отвергается | AC-DAT-01-02, AC-DAT-02-03, AC-DAT-09-03, AC-LIF-07-07 |
| E02-04.compatibility | Матрица read/replay/new-write перечисляет exact versions и prewrite rejection; unsupported field/schema/profile vectors отвергаются | AC-DAT-01-02 |
| E03-11.block_lifecycle | Полученные NATS records/parts совпадают с минимальным independent corpus, empty block сохранён; один transport ACK не считается полным | AC-DLV-04-03, AC-DLV-05-01…AC-DLV-05-03 |
| E06-13.artifact_lock | Manifest связан с materialized source/patch/toolchain/image/genesis digests и native parse evidence; metadata-only не принят | AC-OPS-10-02 |
| E07-03.runbook | Каждая команда воспроизводима на acceptance environment; preconditions, states, ошибки и evidence paths указаны без secrets | AC-DEV-02-01 |
| E07-08.isolation | Для add/config/stop/restart каждой сети есть отдельный observed writer/store trace; соседние owners неизменны | AC-DEV-05-01 |
| E07-09.nats_acceptance | Итоговый независимый reader сопоставляет NATS records/parts/IDs/lifecycle с закреплёнными expected каждой сети | AC-DEV-02-01, AC-DEV-05-01 |


## Шаблон карточки

Карточка содержит: `child_id`, существующий `parent_id`, owner, applicability slots, один output artifact/result, нормативные contract refs, prerequisites `{task_id,required_output}`, invariant/запреты, точные AC IDs с variant/network slots, evidence manifest schema и критерий завершения. Таблица задач задаёт все эти значения, а join REQ/AC и таблица артефактной приёмки задают полные проверки. При развороте карточки нельзя заново выбирать scope, поведение ошибок или границы guarantees. Отсутствующий runtime artifact означает NOT_RUN.

## Полные карточки сложных задач


### Native capture: E01-02.admission

| Поле | Полное значение |
|---|---|
| ID / parent / owner | [E01-02.admission](#task-e01-02-admission) / E01-02 / `adapters/<client>/admission` |
| Применимость | Три самостоятельных slots ETH/Geth, BSC/native BSC, POLY/Bor; у POLY consumed supporting inputs входят в replay obligation |
| Один выход | Native guard/pin/ADMITTED bridge с восстанавливаемым input obligation |
| Контракты | [NATIVE_CAPTURE_PROTOCOL](NATIVE_CAPTURE_PROTOCOL.md), effective profile из [LIVE_CONFIG_SPEC](LIVE_CONFIG_SPEC.md); source mappings из [CURRENT_SEMANTICS](CURRENT_SEMANTICS.md) |
| Входы | E01-02.pins → persistent parent/input pins и backend preflight; E01-02.inputs → static capsule и durable dependency log; E01-02.owned_capture → owned buffer/safe-point interface; SPEC → закреплённые native decisions |
| AC / REQ | AC-CAP-02-01 и AC-CAP-02-02 → REQ-CAP-02, stage E01 |
| Локальный invariant | Sync ADMITTED — граница обнаружимого capture obligation. До неё нет утверждения успешного capture. После неё crash не освобождает pins и не разрешает пропустить obligation по новому H. Observation sequence не означает delivery batch sequence |
| Точные проверки | Для каждого network: power loss до closing sync не даёт ложного принятого capture/READY/Applied; power loss сразу после sync приводит к восстановлению того же obligation из прежних inputs. Expected input digest/parent root и ожидаемая граница признания заданы независимо до запуска |
| Evidence | Отдельный manifest на AC/network/variant по EVID: source/profile/build/versions, input и expected hashes, точная sync cut point/storage model, команды, actual reopen state/log hashes, verdict и reviewer provenance |
| Выход потребителю | E01-02.decision_bridge получает durable observation и pin/input handles. Ранний PASS не утверждает выполненную native decision, READY или PubAck |
| Завершение / сейчас | Критерий: оба AC должны пройти для всех применимых slots; локальный invariant должен быть подтверждён, evidence связан с build. Текущее состояние: **NOT_RUN** |

### Delivery recovery: E03-05.recovery_matrix

| Поле | Полное значение |
|---|---|
| ID / parent / owner | [E03-05.recovery_matrix](#task-e03-05-recovery_matrix) / E03-05 / `test/delivery_faults` |
| Применимость | COMMON engine; ETH/BSC/POLY identities и отдельные stores согласно каждому ALL case; это не общий WAL |
| Один выход | Воспроизводимый delivery-only fault corpus и evidence matrix от заданного durable INTENT/READY |
| Контракты | [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md), [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md), [IMPLEMENTATION_GATES](IMPLEMENTATION_GATES.md) |
| Входы | G.E02 → schemas/validators; E03-03.maintenance → Seal/Switch/Resume recovery; E03-04.pressure_errors → blocked/fatal/retry states; E02-04.compatibility → read/replay capability matrix. Их prerequisites транзитивно дают store/RPC/publisher/cleanup/fencing |
| AC / REQ | AC-DLV-01-01…AC-DLV-01-02 → REQ-DLV-01; AC-DLV-03-03 → REQ-DLV-03; AC-DLV-07-01…AC-DLV-07-02 и AC-DLV-07-04 → REQ-DLV-07; AC-DLV-14-01 → REQ-DLV-14; все stage E03 |
| Локальный invariant | Последняя подтверждённая полная копия не удаляется до разрешённой границы; surviving exact plan/ACK/prefix определяет recovery. Потеря Gateway не означает потерю producer и не разрешает новую semantic identity |
| Fault 1 | До closing marker: STAGING chunks не дают Publish/IngestAck. После sync marker: его immutable state переживает reopen. Oracle различает durable и только RAM bytes |
| Fault 2 | Gateway loss до SEALED при сохранённом producer READY допускает новую reservation/plan по контракту. После SEALED точный issuer plan импортируется через chunked IMPORT_SEALED; bytes/layout/MsgID не меняются |
| Fault 3 | Producer ACKED/outbox N уже durable, payload удалён, retired prefix N−1, Gateway утрачен: полный согласованный RecoveryRelease продвигает prefix до N без payload. Incomplete ACK/prefix jump/conflicting pending отвергается без продвижения |
| Fault 4 | Compatible replacement читает capabilities до writable mutation и повторяет прежние bytes. Эта проверка не разрешает incompatible store-format после drain |
| Evidence | Для каждого case/network/variant сохранены independent initial store image/expected frontier, fault boundary, lost/reordered response schedule, reopen trace, message IDs/bytes, ACK/assertion hashes и EVID manifest. Corrupt/missing evidence — FAIL/NOT_RUN, не PASS |
| Граница результата | Матрица исходит из заданного durable delivery input. Native admission/decision interleavings и producer total-store-loss проверяет E05-03.full_crash_matrix, а не этот task |
| Завершение / сейчас | Критерий: все перечисленные AC/variants должны иметь корректный независимый verdict и связанные store/message artifacts. Текущее состояние: **NOT_RUN** |

### Polygon observer и передача GC evidence: E05-01.polygon_observer

| Поле | Полное значение |
|---|---|
| ID / parent / owner | [E05-01.polygon_observer](#task-e05-01-polygon_observer) / E05-01 / `adapters/bor/finality_observer` |
| Применимость | Только POLY; own supporting Heimdall и Ethereum L1 stack с закреплёнными identities |
| Один выход | Durable typed observation stream: native milestone/checkpoint statuses и отдельно проверенный L1-finalized checkpoint evidence для GC |
| Контракты | Polygon sections [CURRENT_SEMANTICS](CURRENT_SEMANTICS.md), [NATIVE_CAPTURE_PROTOCOL](NATIVE_CAPTURE_PROTOCOL.md); identities [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md) |
| Входы | G.E04 → обязательная semantic model; E05-01.registry → сохранённые activation refs и revision rules; E01-01.harness → pinned own-L1/supporting inputs и независимый fixture environment |
| AC / REQ | AC-LIF-07-01…AC-LIF-07-06, AC-LIF-07-08…AC-LIF-07-09 → REQ-LIF-07, stage E05; структурный AC-LIF-07-07 принадлежит E02-10.closed_profiles |
| Native projection | Startup без сохранённого полного response даёт только narrower Known whitelist с absent source_evidence. Checkpoint root не используется как block hash. Purge milestone допускает native checkpoint fallback, не стирая retained L1 anchor |
| GC source | Exact hash-pinned RootChain read в finalized L1 state F проверяет chain/contract/params, result fields/root и native end ancestry. Accepted evidence связывается с F; latest/safe не заменяют finalized |
| Source provenance D80 | SourceObservationRef и L1Checkpoint содержат обязательный sources_binding_digest:Hash32. O1 сохраняет digest S1 после замены sources_file на S2; новое O2 закрепляет digest S2. Replay/recovery O1 не вычисляет digest по актуальному файлу и не меняет READY/SEALED bytes. Narrower whitelist без source_evidence не получает выдуманного digest |
| Unavailable | Offline/syncing/pending checkpoint/pruned F сохраняют прежний anchor и дают явную availability; tx/status delivery продолжается. Доступный более новый finalized F2 создаёт новый observation, не переписывает observation F |
| Invalid | Wrong identity/contract/params либо malformed result дают явную source/identity error, не Unavailable-success; pins по этим данным не освобождаются |
| Evidence | Каждому AC соответствует отдельный source response/state F/expected observation с independent hash/provenance, schedule и native ancestry; EVID manifest связывает outputs/versions и каждый variant |
| Выход потребителю | E05-01.activation_gc получает accepted durable evidence и ancestry. Он отдельно владеет terminal/pending/shared-pin условиями, anchor commit и restartable release: AC-LIF-08-01…AC-LIF-08-05. Observer не делает release pins самостоятельно |
| Завершение / сейчас | Критерий: все назначенные этой задаче AC/variants должны различать native projection, source unavailable/error и accepted evidence без задержки tx/status по GC source; D80 provenance сохраняется при config replacement/replay. Текущее состояние: **NOT_RUN** |

