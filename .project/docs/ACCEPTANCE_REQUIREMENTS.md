# Полный реестр требований и приёмки

Родительские REQ раскрыты в [195 атомарных сценариях](ACCEPTANCE_CASES.md); результаты, владельцы, зависимости и точное соответствие AC задачам — [TASK_DECOMPOSITION](TASK_DECOMPOSITION.md). Это содержание ТЗ по D64, не исполняемый backlog агента.

DOC-FINAL-01 · 2026-11-05. Реестр покрывает обязательное поведение текущего ETH/BSC/POLY live продукта, а не только 41 находку аудита. Нормативные детали — MESSAGE_CONTRACT, CURRENT_SEMANTICS, NATIVE_CAPTURE_PROTOCOL, DELIVERY_STATE_MACHINE, CONTROL_WIRE_CONTRACT, LIVE_CONFIG_SPEC и DEPLOYMENT_BOOTSTRAP_CONTRACT; полный нормативный текст включён в root v3.

Все runtime verdict сейчас **NOT_RUN**. Ни одна строка не означает успешный тест. Имена fixture families из предыдущих документов сохранены; новые WIRE/ID/LAYOUT/RPC/CUTOVER/DATA/FEE/FINAL/GC конкретизируют их. Сценарии должны быть воспроизводимы и проверяться независимым expected по IMPLEMENTATION_GATES. Для требований native semantics PASS нужен отдельно ETH/BSC/POLY; network-specific строки проверяются в своей сети. Optional extension проверяется только при объявленном включении и не блокирует default поставку.

## Scope и результат

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-SCP-01 | Первый контур ровно ETH/BSC/POLY Mainnet | CONFIG-01: три accepted profiles; Base/Arbitrum/testnet/неизвестный profile отвергнут, не запускается другой клиент; E01/E06 |
| REQ-SCP-02 | Native источник, NATS — конечная граница | E07 path audit: hooks/StateDB/receipts native, JSON-RPC/debug_trace не основной tx feed; нет аналитического writer/consumer продукта |
| REQ-SCP-03 | Только новый live H+1, pending независимо | BRANCH/START: initial sync и downtime range не экспортируются; сохранённые obligations завершены, новый boundary записан без окна; E01/E05/E06 |
| REQ-SCP-04 | Успешные/failed/native system tx обязательны | E04 corpus: ordinary/failed/BSC system/Bor StateSync и пустой блок; unknown type вызывает error без skip |
| REQ-SCP-05 | Outside-tx balance/legacy receipt optional | E04-02: default выключен, source execution сохраняется, tx effects не теряются; при включении отдельные typed records/fixtures без fake tx |
| REQ-SCP-06 | Нет обещаний downstream/HA/полноты/истории/performance | E07 scope audit: ни PubAck, ни fixture PASS не обозначены archive/HA/global balance completeness; отсутствуют запрещённые компоненты |

## Native capture

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-CAP-01 | Отдельные pinned client builds и реальные paths | PROFILE-01: transfer/system/canonical callers реально проходят bridge; synthetic runner сопоставлен native import; E01 |
| REQ-CAP-02 | Durable admission до принятого execution | CAPTURE-01: crash до/после ADMITTED; до него нет принятого obligation, после него replay не теряется; E01/E05 |
| REQ-CAP-03 | Hash provider и persistent state/input pins | PIN-01: потерять RAM trie cache, pruning живого pin/wrong datadir/path backend; recovery либо preflight reject, не silent skip; E01/E05 |
| REQ-CAP-04 | Static inputs + append-only consumed dependencies | CAPTURE: crash после ответа Heimdall до передачи; retry использует прежний ответ/sequence, final digest закрыт; E01/E05 |
| REQ-CAP-05 | Owned snapshot без mutable alias | OWNED-01: overlapping CALL input/output, delayed worker, соседние tx одного key; исходные значения не изменились; E01/E04 |
| REQ-CAP-06 | Bounded collector RAM, safe-point backpressure | OWNED/MEM: один field/tx/manifest больше буфера, slow disk/receiver; chunks/spool без потери, I/O/wait вне hooks; E01/E03/E04 |
| REQ-CAP-07 | Corpus/child-root/decision/mirror durable ordering | CAPTURE/DECISION: power loss у каждого marker, native-only replay не меняет public IDs/bytes; E01/E05 |
| REQ-CAP-08 | Semantic identity стабильна до transport attempt | ID-01: crash/retry DELIVERY_STAGING/native marker; прежние activation/revision/source, ровно один batch allocation; E02/E03/E05 |
| REQ-CAP-09 | Bor/BSC native scopes не смешиваются | SCOPE: StateSync after до block-alloc; BSC actualTx/nonce/finalise; execution result совпадает с native; E01/E04 |
| REQ-CAP-10 | Local fatal завершает соответствующую ноду | POISON/ENOSPC: normalization/corruption/sync failure — exit без false READY; соседние сети продолжаются; E03/E05/E07 |

## Данные и нормализация

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-DAT-01 | Closed typed schema, presence и exact integer widths | DATA/schema: unknown enums/version, lengths, missing vs zero, malformed union; reject без ANY/JSON fallback; E02 |
| REQ-DAT-02 | Source tx/receipt encodings и native поля | AUTH/DATA: closed consensus/storage registry, exact native bytes/type/origin, bounded byte-equivalent receipt writer; тип/подпись/access/blob/auth fields и projection раздельны; E02/E04 |
| REQ-DAT-03 | Authorization tuples сохраняются без фильтрации | AUTH-01…04: invalid parity/nonce/chain tuple, duplicates, root revert; source tuples не теряются; E02/E04 |
| REQ-DAT-04 | Полное call tree и общий tx-local порядок | DATA: CALL/CALLCODE/DELEGATECALL/STATICCALL/CREATE/CREATE2/precompile, value=0, caller/context/code; общий execution_order calls/actions, отдельные body/protocol/effect indices; E04 |
| REQ-DAT-05 | Outcome/rollback/committed и публичные scopes различаются | DATA/SELF: scope tree/refs/reused native ID; ancestor/caught revert; сохранённые fees/nonce/authorization; validator и стабильные order/scopes при recovery; E02/E04 |
| REQ-DAT-06 | SELFDESTRUCT отдельное действие | SELF-01…04: zero balance/self-beneficiary/old/new contract/ancestor revert; opcode/projection связаны одним order, finalization burn отдельный; action, monetary effects и deletion не смешаны; E02/E04 |
| REQ-DAT-07 | Точный tx boundary state diff/existence | EXIST/DATA: empty CREATE, delete/reset/recreate, touched storage twice, fees/auth вне root; initial/final и code hashes точны; E04 |
| REQ-DAT-08 | Все monetary actions, компенсации и D62 fees | FEE-01: gross purchase/refund/credits, same recipient/payer, blob/zero-fee; address effects согласованы с state, aggregate не удваивает; E04 |
| REQ-DAT-09 | Receipt/log/create provenance и network details | RECEIPT/DATA: native status/revert/gas price, failed CREATE attempt vs success, common fields отдельно от consensus bytes, typed ETH/BSC empty extras и точная Bor scope mapping, no reverted receipt logs; E02/E04 |
| REQ-DAT-10 | Полные native block metadata, включая empty | DATA: исходный header/fork fields/RLP/count, zero tx, optional fields отсутствуют корректно, не synthetic completeness report; E02/E04 |

## Lifecycle, finality и GC

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-LIF-01 | Только native canonical activation публикуется | BRANCH: rejected/speculative block не Applied; known branch без callback получает corpus/replay до decision; E01/E05 |
| REQ-LIF-02 | READY Applied всегда может получить Removed | LIFE-01/02: reorg до publish/после одной part; отмена сохраняется независимо от PubAck; E02/E05 |
| REQ-LIF-03 | Reordered lifecycle детерминирован | LIFE-03/04: Status r2/r1 до Applied, Removed до Applied, same-revision conflict, reapply новый A; terminal смысл сохранён; E02/E05 |
| REQ-LIF-04 | Finality-only/restart события не теряются | FINAL/DECISION: setter/event без нового block, crash marker/mirror, повтор; ровно правильный snapshot/revision; E01/E05 |
| REQ-LIF-05 | Ethereum safe/finalized semantics точны | FINAL-ETH: safe startup fallback допустим, zero FCU no-op, actual setter before RPC error; GC только finalized ancestry; same-head authoritative finalized regression ниже accepted anchor — fatal; E01/E05 |
| REQ-LIF-06 | BSC early/header evidence разделены | FINAL-BSC: VotePool-only, merged header snapshot, side branch, restart loss, missing lookup; GC только accepted header Source; E01/E05 |
| REQ-LIF-07 | Polygon native statuses отдельно от D63 anchor | FINAL-POLY: milestone purge/whitelist-only, checkpoint root vs end hash, exact finalized contract read; публикация не ждёт L1 GC; E01/E05 |
| REQ-LIF-08 | GC не теряет pending/нужную ancestry | GC-01: no anchor/pending/replay сохраняют pins; crash release, removed branch, same-head finality regression и доказанный anchor conflict; нет TTL guesses; E05 |

## Доставка и обслуживание

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-DLV-01 | Единственные durable owners, sync closing markers | REC/journal: reopen/power loss каждого state, незакрытые chunks не публикуются; E03 |
| REQ-DLV-02 | Один decision batch, stable attempt/counters без sequence дыр | ATTEMPT/ID: один public decision — один полный batch, включая multi-block/finality-only; no-events без batch, chunks не делят decision; attempt сохраняет semantic IDs, overflow fatal; E02/E03 |
| REQ-DLV-03 | Prepare/reservation идемпотентны | PREPARE/RPC: потеря replies/chunks, same/conflicting digest, Gateway loss до SEALED; сохранённый seed повторяется; E03 |
| REQ-DLV-04 | Bounded typed fragments и exact wrapper/layout/header/bytes | FRAGMENT/LAYOUT: запись и отдельное поле >2 GiB без монолитного protobuf; schema/presence/blob coverage/seal; COMPLETE/PART boundary, order/digest/size, SHA/MsgID vectors; после READY fragment bytes, после SEALED transport bytes неизменны; all-fragments ACK; E02/E03 |
| REQ-DLV-05 | End-to-end ACK всех частей одного назначения | REC/NATS: partial PubAck, wrong stream, lost bitmap/IngestAck, reordered parts; ChunkAck/CLOSED не разрешают producer cleanup; E03 |
| REQ-DLV-06 | Producer/Gateway cleanup crash-safe | FRONTIER/REC: ACKED/outbox, RELEASING chunks, lost ReleaseAck, recovery без payload; prefix не перескакивает batch; E03 |
| REQ-DLV-07 | Store loss использует surviving owner evidence | REC/FRONTIER: Gateway rebuilt с prefix/SEALED plans; producer loss не получает ложный старый cursor; E03/E05 |
| REQ-DLV-08 | Late session/callback не воскрешает state | FENCE: пауза check→commit, reconnect/Release, late PubAck/Publish retired; old owner отвергнут; E03/E05 |
| REQ-DLV-09 | Binding до offline capture и exact stream metadata | WIRE/DEST: D77 network_digest/destination_id, форматы и missing/mismatch; same target/different URL, separate account/route checks, cutover и committed retry; canonical digest/pre-plan READY связаны; E02/E03/E06 |
| REQ-DLV-10 | Drain закрывает native и delivery obligations | DRAIN: pending native способен создать batch, highwater после него; no new admission/poll, retained GC pins допустимы; E05/E06 |
| REQ-DLV-11 | Cutover/Resume соответствуют YAML | CUTOVER: desired descriptor only, AWAIT_SWITCH, crash каждого intent/commit, old binding LIVE Resume и late old generation; E03/E06 |
| REQ-DLV-12 | ACKed history не мигрирует D59 | DEST/LIFE: новый Removed/Status с прежним Applied reference, только новое назначение; старый ACKed payload не экспортируется; E05/E06 |
| REQ-DLV-13 | Retry/ENOSPC/poison не удаляют pending | POISON/ENOSPC/MEM: transient backoff, CONFIG_BLOCKED, disk expansion/recovery, immutable conflict; нет drop/auto re-ID; E03/E05 |
| REQ-DLV-14 | Upgrade capabilities до mutation | UPGRADE: old-build drain для wire/plan, compatible replay exact bytes, incompatible remaining store reject даже DRAINED; E03/E05/E06 |

## Конфигурация и окружение

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-OPS-01 | Strict role YAML и effective native validation | CONFIG: unknown/duplicate keys; relative storage paths от каталога YAML, sources_file Polygon absolute-only; native overrides/profile/pruning/Bor parallel flags, check без mutation; E01/E06 |
| REQ-OPS-02 | 11 services/14 volumes, самостоятельные owners | MOUNT: actual backing volume identities, permissions/UID, separate Polygon L1 и JWT; одинаковый container path не означает shared DB; E06 |
| REQ-OPS-03 | Partial init не разрушает keys/state | BOOT: crash каждой phase, existing nonempty home/wrong genesis/missing ENV_READY, BSC hash init; safe resume/reject; E01/E06 |
| REQ-OPS-04 | Config generation заменяет только allowlist | CONFIG: изменённый native config применён после restart, ключи/genesis/state сохранены, невозможный mix отвергнут; E06 |
| REQ-OPS-05 | mTLS/SAN/EKU/network/role admission | TRUST: чужой CA/SAN/role/network/deployment, credential rotation и UDS ownership; reject до store mutation; E03/E06 |
| REQ-OPS-06 | Минимальные NATS ACL, bootstrap identity и проверка account | NATS: runtime publish/INFO/UserInfo/reply allowed, admin denied; D77 atomic config CREATE/read-back, existing metadata mismatch blocked, compatible UPDATE сохраняет посторонние keys; без retag/PURGE/reset; E03/E06 |
| REQ-OPS-07 | P2P mapping/advertise и внутренние API | CONFIG/START: ports во всех слоях, only P2P external, required links reachable, включая Bor observer→собственная supporting L1; ошибочная основная Ethereum-нода отвергается по role identity; E06 |
| REQ-OPS-08 | Independent startup и stop-confirm-start | START/REPLACE: NATS outage, Bor/Heimdall retry, delayed old writer, native sync отдельно от delivery; E06 |
| REQ-OPS-09 | Обязательные control logs без secrets | LOG: все states/errors видны при отключённом debug, правильная identity, SIGKILL не выдаёт graceful STOPPED; E06/E07 |
| REQ-OPS-10 | Артефакты/совместимость доказаны перед runtime acceptance | ARTIFACT: pinned source/patch/toolchain/image, mainnet genesis SHA/parse, wrong artifact reject, clean bootstrap; E01/E06 |

## Разработка и граница готовности

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-DEV-01 | Порядок E01→E07 без позднего открытия source seam | План/evidence: native proof E01, delivery E03, combined E05; ни одного DONE по ещё будущему proof |
| REQ-DEV-02 | Independent expected и воспроизводимый harness | Fixture passport/seed/input hash + mutation sanity; clean environment без скрытой ноды автора; E01/E02/E07 |
| REQ-DEV-03 | Separate builds и bounded module interfaces | Dependency review: core без внутренних типов трёх fork сразу, NATS SDK только Gateway, Rust/history не prerequisites; E01 |
| REQ-DEV-04 | Evidence traceability и честные verdict | CaseRegistry/CaseResult связывают AC/network/variant/task; applicability отдельно от verdict; PASS/FAIL с полным RunManifest, NOT_RUN без выдуманных actual; version changes invalidates PASS; каждый gate |
| REQ-DEV-05 | Функциональная приёмка трёх независимых сетей | E07: каждый mandatory variant имеет собственный PASS, missing/FAIL блокирует gate; optional/deferred/not-applicable требуют rules; runtime не заменяется source inspection/MD lint |

## Definition of ready для документации

Перед завершением DOC-FINAL: один нормативный root; ни одной поведенческой развилки без ответа; current schemas/IDs/config/операционные переходы замкнуты; каждый обязательный REQ имеет ожидаемый результат и этап; future-proof задачи отделены от неопределённых требований; deferred scope вынесен из основного текста. Проверки ссылок/anchors, source-backed claims и неизменности v2 обязательны. Идеальность будущей реализации не обещается; новые факты возвращаются в decisions/requirements до изменения guarantees.

## Definition of done для реализации

D64: это критерии приёмки описываемой системы в составе ТЗ; реализация и исполнение E01–E07 никогда не входят в нашу работу. NOT_RUN не PASS; audit SPECIFIED не runtime DONE. Требования нельзя понижать ради успешного gate. Предписанный системой процесс проверки сохраняет counterexample input/actual/expected, устраняет причину и повторяет затронутые proofs; предусмотренный пакет приёмки содержит build/artifact locks, configs/runbook и воспроизводимые evidence. Здесь эти условия только проектируются и документируются.
