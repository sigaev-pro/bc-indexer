# Точные данные текущих профилей

DOC-FINAL-01 · D62/D63 · 2026-11-05. ETH/BSC/POLY, live-only. Уточняет [MESSAGE_CONTRACT](MESSAGE_CONTRACT.md); field numbers общего Envelope и generated bindings — E02, но значения и поведение определены здесь. Идентификаторы/кодирования — [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md). Поля ниже обязательны, кроме явно optional/oneof availability; нулевое значение не заменяет отсутствие.

## Block, calls и account changes

BlockRef.Timestamp сохраняет source precision. ETH/Bor: `seconds=header.Time`, `nanos=0`, precision=SECONDS. BSC: взять native `MilliTimestamp()`; `seconds=ms/1000`, `nanos=(ms%1000)*1000000`, precision=MILLISECONDS, с checked arithmetic. Pinned BSC вычисляет `Time*1000 + low_u64(MixDigest)`; это правило BSC, не интерпретация MixDigest как времени в Geth/Bor. Raw header.Time/MixDigest сохраняются без изменений. [BSC source](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/block.go#L166-L172).

`BlockRecord={block_ref,block_activation,header,source_transaction_count:u64,initial_lifecycle}`; `BlockRef` содержит number:u64, hash/parent_hash:Hash32 и Timestamp. Source count берётся из native body представления выбранного клиента, не из независимого completeness verifier. Пустой блок даёт BlockApplied с count=0. `header.source_rlp` обязателен для текущих EVM profiles; hash из native source сохраняется, собственная перепроверка всего блока не вводится.

Header common fields: parent_hash/uncle_hash/state_root/transactions_root/receipts_root/mix_digest:Hash32; beneficiary:Address20; logs_bloom:bytes256; difficulty:UInt256; number/gas_limit/gas_used/timestamp_seconds:u64; extra_data:bytes; nonce:bytes8. Optional fork fields: base_fee:UInt256; withdrawals_root/parent_beacon_block_root/requests_hash:Hash32; blob_gas_used/excess_blob_gas:u64. Geth дополнительно optional block_access_list_hash:Hash32 и slot_number:u64. Presence определяется native header/fork; отсутствующее поле не заполняется нулём. Bor ActualTime/AbortRecovery — локальные runtime поля, не consensus header и не source RLP; в публичный Header не включаются.

Точные поля проверены по pinned [Geth Header](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/block.go), [BSC Header](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/block.go), [Bor Header](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/block.go). Наличие future-fork поля в source не означает его текущую активацию Mainnet.

CallFrame сохраняет отдельно caller_address (native from), context_address, code_address, msg_sender и target; нельзя подменить одно другим при DELEGATECALL/delegated code. Для CREATE/CREATE2 `attempted_create_address` хранится, когда native определил адрес; `created_address` присутствует только при локальном успешном создании. `effects_committed=false` может отменять локальный успех ancestor revert; final existence берётся из AccountChange. Применимые input/output/gas/revert данные не обрезаются. Root frame id=0, parent отсутствует, depth=0; остальные parent IDs существуют, order соответствует native входу.

AccountChange дополнительно содержит code_hash_before/code_hash_after:Hash32. Before/after code bytes обязательны при изменении; отсутствие account отделено exists-флагами, не пустым code. StorageChange отсортированы по raw key bytes для детерминированной нормализации; каждый key появляется один раз с tx initial/final значением. AccountChange упорядочены по raw address bytes; execution/action lists сохраняют native chronology. `storage_reset=true` обозначает удаление всего прежнего storage без перечисления всех ключей; subsequent writes после reset представлены явно. Общий state diff — только committed tx result. Reverted log attempts не входят в первый обязательный contract; receipt logs остаются source committed logs.

## Balance actions и комиссия D62

`BalanceAction={action_index:u64,execution_order:u64,cause,engine,kind,from?,to?,amount:UInt256,asset=NATIVE,effects_committed,rollback_scope?,balance_effects[]}`. `BalanceEffect={effect_index:u64,address:Address20,before:UInt256,after:UInt256,native_reason_code:u32}`. Reason code интерпретируется только вместе с ProfileRef/pinned native enum; raw unknown code нового профиля не принимается автоматически. BalanceEffect before/after относятся к самому действию; AccountChange — к целой tx.

Cause — закрытый union: `Frame(frame_id)` / `ExecutionAction(execution_action_id)` / `TransactionProtocol{operation,operation_index,related_execution_actions[]}`. Operation registry v1: GAS_PURCHASE, GAS_RETURN, FEE_RECIPIENT_CREDIT, BSC_SYSTEM_EXECUTION, BOR_STATE_SYNC_EXECUTION, SELFDESTRUCT_FINALIZATION_BURN. BSC/Bor варианты допустимы только в source-registered native system path, не как OTHER для неизвестного действия. kind: TRANSFER/MINT/BURN/FEE/REFUND/SELFDESTRUCT_TRANSFER/NATIVE_PROTOCOL_CHANGE; последний сохраняет фактические effects известной native system операции без выдумывания экономической причины. Для нового обязательного source path расширяется versioned registry до выпуска supported profile.

| Действие | Представление / правило |
|---|---|
| Native value transfer | Одна action объединяет debit/credit одного native transfer invocation; effects содержат обе стороны, в том числе self-transfer. Не образовывать две одинаковые transfers из пары callbacks |
| Покупка газа | operation=GAS_PURCHASE, kind=FEE, from=payer, to отсутствует; amount=фактическое списание, один payer effect |
| Денежный возврат | GAS_RETURN/REFUND, from отсутствует, to=payer; amount=фактический возврат. EVM gas refund counter units — другая величина |
| Вознаграждение из комиссии | FEE_RECIPIENT_CREDIT/FEE, from отсутствует, to=native recipient; amount/credit effect точные. Отсутствующий from не означает mint или zero address |
| SELFDESTRUCT | ExecutionAction сохраняет сам opcode, даже при balance=0; связанная monetary action содержит реальные native effects, не второй выдуманный CALL |
| Finalization burn | SELFDESTRUCT_FINALIZATION_BURN/BURN с related action refs и фактическим debit; сам факт отсутствия recipient не доказывает burn |
| Native system tx | Полная tx/calls, known protocol operations и реальные effects. Не добавлять самостоятельный outside-tx поток вместо обязательной tx |

Effects одного transfer invocation собираются по integration operation ID; не угадываются по соседству одинаковых сумм. Каждый captured balance mutation принадлежит ровно одной action/effect. Одинаковый address может встретиться в нескольких effects по native порядку. Native rollback помечает исходные actions неcommitted; техническое восстановление journal не создаёт второй экономический перевод. Нулевой итог tx не удаляет её ненулевые встречные actions. CallFrame.value, ExecutionAction, BalanceAction и AccountChange — связанные представления; они не складываются друг с другом.

`Receipt.fee_summary={payer?,gas_purchase_debit,gas_return_credit,net_charge,recipient_credits[]}`; все суммы UInt256, credits — `{address,amount}` в native order. Для применимой обычной tx `net_charge = gas_purchase_debit − gas_return_credit`, включает фактические native gas/blob charges из этих operations. Zero-fee system tx хранит применимые нули; неприменимый payer отсутствует. Native implicit burn определяется fee rules и сверяется отдельно; не выдумывать дополнительный payer debit поверх уже списанного газа. Receipt blob_gas_used/blob_gas_price и effective_gas_price сохраняются по применимости. Fee summary — агрегат actions, не дополнительное денежное событие.

На fixtures сумма committed **address effects** согласуется с AccountChange каждой tx; сумма action.amount сама по себе не является суммой переводов или комиссией. Это test oracle, не production balance completeness verifier. Native balance reasons подтверждены [Geth](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/tracing/hooks.go), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/tracing/hooks.go), [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/tracing/hooks.go). Данные из hook — основание capture; соответствие всех native paths доказывается E01/E04.

## Общий lifecycle и evidence

`AnchorRef={height:u64,hash:Hash32}`. `NativeDecisionRef={node_instance_id,producer_id,run_id,decision_sequence:u64}`. `EvidenceOrigin=NATIVE_LIVE|NATIVE_RESTART|RETAINED_DURABLE`. `ReferenceObservation=Known{block:AnchorRef,origin}|Unknown{reason=NOT_OBSERVED|NATIVE_REFERENCE_UNSET}`. All-zero hash не кодирует известный genesis; настоящий genesis имеет source hash.

`LifecycleSnapshot={native_decision,observed_execution_head:AnchorRef,evidence}`; evidence — ровно EthereumEvidence/BscEvidence/PolygonEvidence, соответствующее profile. Applied revision=0; StatusChanged={activation_ref,revision>0,snapshot} — полная замена. Removed={activation_ref,revision>0,native_decision,reason=NATIVE_CANONICAL_CHANGE}; revision выделяется из того же activation counter, но terminal смысл не зависит от порядка доставки. Повтор native decision не создаёт новую revision. Projection статуса определяется ancestry точного activation block hash, не одним height.

Неизвестный optional finality reference допустим на Applied; неизвестные обязательные execution details — capture error. Неудачный lookup известного обязательного header/snapshot не превращается в Unknown/genesis. Stale callback сам по себе не меняет snapshot: native bridge под observation guard читает согласованное состояние, сохраняет owned evidence и marker. После restart выполняется refresh и без нового head event; повтор уже сохранённого решения идемпотентен.

## Ethereum evidence

`EthereumEvidence={safe:ReferenceObservation,finalized:ReferenceObservation,safe_basis=CONSENSUS_SETTER|STARTUP_FINALIZED_FALLBACK|UNSET}` плюс common observed head. Known finalized покрывает activation, когда `isAncestorOrSelf(activation.block, finalized)` → ETH_FINALIZED; иначе такое же покрытие known safe → ETH_SAFE; известные references без покрытия → ETH_CANONICAL_UNFINALIZED; отсутствие необходимых references → ETH_FINALITY_UNKNOWN. Unknown safe не отменяет known finalized.

Safe в pinned Geth может восстановиться из finalized после restart и стать ниже прежнего safe; новый snapshot с NATIVE_RESTART допускается. Zero hash в Engine forkchoice input — native no-op, не команда clear. Bridge наблюдает фактические SetSafe/SetFinalized/head mutations, включая их выполнение до возможной последующей FCU ошибки; одного успешного RPC status или ChainHeadEvent недостаточно. GC anchor — только durable accepted finalized на canonical execution ancestry, никогда safe. [Pinned source evidence и seams](research/ETH_BSC_FINALITY_CONTRACT.md).

## BSC evidence D62

`BscEvidence={observed_native_header_head:AnchorRef,header_finality,native_effective_finalized:ReferenceObservation,effective_basis,early_observation}`. effective_basis=HEADER_SNAPSHOT|VOTE_POOL_QUORUM|GENESIS_FALLBACK|UNSET.

`header_finality=AttestedSnapshot{snapshot_head,justified,finalized}|GenesisFallback{snapshot_head,genesis,reason=NO_ATTESTATION|PRE_PLATO}`; все refs — AnchorRef. Это merged native snapshot VoteData.Source/Target, не Source произвольно выбранной последней подписи.

`early_observation=None|EarlyVoteEvidence{origin,evaluation_head,source_justified,vote_target,finalized_candidate,validator_snapshot_parent,validator_count:u64,quorum_count:u64,matching_vote_count:u64,evidence_digest:Hash32}`. Refs — AnchorRef. Bridge до публикации сохраняет owned snapshot фактически проверенных native vote envelopes/validator context в local evidence spool; digest связывает его, но не выдаётся за независимо проверяемый consensus certificate. После restart прежний vote observation может сохраняться только как RETAINED_DURABLE, не выдаваться за текущий VotePool.

Для каждой activation отдельно вычисляются `header_status=FINALIZED_HEADER_ATTESTED|JUSTIFIED|NO_ATTESTED_FINALITY|CANONICAL_UNFINALIZED` и `early_status=VOTE_FINALIZED_OBSERVED|NOT_COVERED|NOT_OBSERVED`: сведения не сворачиваются в один безусловный finalized flag. Header Source должен покрывать activation по ancestry; header head и execution head предварительно согласуются. Early event side branch не финализирует canonical activation. Потеря transient early evidence допустима и не понижает durable GC anchor.

GC anchor — только header-derived AttestedSnapshot.finalized, canonical ancestry и durable evidence. CurrentFinalBlock/LastFinalized hash сами по себе недостаточны: они могут содержать vote-derived результат. Нужен error-aware native snapshot accessor, не пересчёт через public getter, потерявший provenance. [Pinned исследование](research/ETH_BSC_FINALITY_CONTRACT.md).

## Polygon evidence и L1 GC D63

`PolygonEvidence={milestone,checkpoint,milestone_whitelist,checkpoint_whitelist,native_finalized,l1_checkpoint}`. Для этих полей `Evidence<T>=Known(T)|NotObserved|Unavailable{reason=SOURCE_OFFLINE|SOURCE_SYNCING|STATE_UNAVAILABLE|CHECKPOINT_PENDING}`. Unavailable — конкретная попытка optional finality/GC lookup, не замена ошибке обязательного execution capture; не стирает retained accepted anchor. Native malformed/wrong identity — ошибка, не Unavailable.

`HeimdallIdentity={chain_id:string,genesis_document_sha256:Hash32}`; digest относится к проверенному bootstrap genesis JSON, не подменяет block hash. `SourceObservationRef={source_store_identity:Id16,heimdall_identity,observation_id,request_sequence:u64,operation,selector,committed_heimdall_block?,response_digest:Hash32}`. Operation/selector union: MilestoneLatest/CheckpointLatest/CheckpointBuffer/ChainParams/CheckpointParams без selector; MilestoneByNumber/CheckpointByNumber с u64 number; MilestoneEvent с `{height:u64,event_ordinal:u64}`. Optional committed block — `{height:u64,hash:Hash32}`, только если реально получен. Никакого timestamp вместо доказанного source height/hash. Exact responses хранятся local по observation/request, не передаются как arbitrary JSON публичного evidence.

| Evidence type | Точные поля |
|---|---|
| Milestone | source:SourceObservationRef, number?:u64, milestone_id:string, proposer:Address20, start/end/timestamp/total_difficulty:u64, end_hash:Hash32, outcome=OBSERVED\|FUTURE_QUEUED\|LOCALLY_VERIFIED\|REJECTED |
| Checkpoint | source, checkpoint_id?:u64, proposer, start/end/timestamp:u64, root_hash:Hash32, state=BUFFERED\|STORED_ACKNOWLEDGED, locally_verified_end?:AnchorRef |
| Whitelist | kind=MILESTONE\|CHECKPOINT, anchor:AnchorRef, native_decision, source_evidence?:SourceObservationRef, ancestry_binding_digest:Hash32; принят только после canonical match |
| NativeFinalized | basis=MILESTONE_WHITELIST\|CHECKPOINT_WHITELIST, anchor:AnchorRef, whitelist_decision:NativeDecisionRef |
| L1Checkpoint | l1_network:NetworkIdentity, supporting_node_id:Id16, root_chain_address:Address20, checkpoint_id:u64, header_block_id:UInt256, child_block_interval:u64, bor_start/end:u64, checkpoint_root:Hash32, bor_end_ref:AnchorRef, bor_range_binding_digest:Hash32, finalized_l1_anchor:AnchorRef, verification:FinalizedContractState |
| FinalizedContractState | call_block_hash:Hash32, root_chain_params_digest/calldata_digest/result_digest:Hash32, result_created_at:UInt256, result_proposer:Address20 |

Milestone hash — Bor end block hash, checkpoint root — range commitment; они не взаимозаменяемы. Whitelist без исходного сохранённого ответа допускает narrower Known Whitelist с absent source_evidence, не выдуманный checkpoint. Polygon native finalized выбирает milestone whitelist, затем checkpoint fallback; это native observation, которое Bor может пересмотреть при восстановлении. Projection отдельно показывает NATIVE_MILESTONE_COVERED / NATIVE_CHECKPOINT_COVERED / CANONICAL_NO_NATIVE_FINALITY и L1_CHECKPOINT_COVERED / L1_NOT_VERIFIED; без сопоставления block ancestry coverage не присваивается.

Принятый D63 путь — `PolygonCheckpointObserver` внутри Bor adapter, без нового процесса или Ethereum tx extractor. Network reads выполняются вне hooks/native locks. Endpoint собственной Polygon L1-ноды и Heimdall задаётся окружением; expected chain/contract binding проверяется по bootstrap identity/params, не выбирается по случайному RPC ответу.

1. Получить native stored checkpoint и chain/checkpoint params от собственного Heimdall; сохранить exact responses, interval/RootChain address и source identity. Если callbacks не содержат number/height, не изобретать их; для L1 проверки нужен checkpoint ID из полного query.
2. Получить finalized F у собственной Polygon L1 Geth. При отсутствии F сохранить pins и повторить позже; не использовать latest/safe/число confirmations.
3. Вычислить checked UInt256 `checkpoint_id * child_chain_block_interval`; вызвать RootChain.headerBlocks с selector `0x41539d4a` и u256 аргументом через `eth_call` в `{blockHash:F.hash,requireCanonical:true}`.
4. Строго декодировать root/start/end/createdAt/proposer; сверить root/range/proposer с checkpoint и параметры до/после чтения. Проверить canonical ancestry F относительно текущего собственного L1 finalized, не только высоту. Несовпадение нового candidate не повышает anchor.
5. Native Bor verifier вычисляет checkpoint root по закреплённым header inputs и непрерывной canonical range; повторно сверяет end hash/ancestry под observation guard. Только после durable source evidence/decision/accepted anchor разрешён существующий GC protocol.

Own-L1 RPC здесь — служебная finality evidence нативной зависимости Polygon, не источник транзакций extractor. Receipt locator/subscription не требуются; receipt-based вариант в research — справочная альтернатива, не второй обязательный протокол.

При pruning state у F можно сделать **новое** observation с более новым finalized block; сохранённое old evidence не переписывается. Неизвестная сеть/contract, malformed result или конфликт ниже уже принятого anchor — source invariant error. Недоступность L1/Heimdall задерживает GC, обычная публикация готовых Polygon records продолжается. Drain не ждёт будущего checkpoint ради очистки уже завершённой доставки: остающиеся lifecycle/pins — retained native state, не новый delivery pending. Сохранённый незавершённый GC decision разрешается/отклоняется безопасно до seal; новый optional GC poll в DRAIN не запускается.

Checkpoint commits number/time/txRoot/receiptRoot; полный Bor header/stateRoot в leaf не входит. GC опирается на этот commitment **и** проверенную local Bor ancestry/native consensus, не обещает независимый криптографический proof всех state fields. Source fields, exact REST/RPC и граница — [pinned Polygon research](research/POLYGON_FINALITY_CONTRACT.md).

## Provisioning supporting identities

`finality.polygon.sources_file` — строгий UTF-8 JSON без unknown/duplicate fields, комментариев и подстановок. Схема: `{schema_version:1,l1:{deployment_id:Id16,role:"POLYGON_L1_EXECUTION",endpoint:URI,network:NetworkIdentity},heimdall:{deployment_id:Id16,role:"POLYGON_HEIMDALL",endpoint:URI,chain_id:string,genesis_document_sha256:Hash32}}`; JSON-кодирование NetworkIdentity/Id16/Hash32 совпадает с DestinationBinding. Endpoint L1 точно совпадает с `l1_endpoint`, Heimdall — с effective native Bor config. Весь файл имеет `sources_binding_digest=SHA256(file_bytes)`, включённый в обе source evidence; whitespace меняет этот digest, не deployment ID. Evidence хранит digest и проверенные identity fields, поэтому последующая замена файла не меняет прежнюю provenance.

Environment bootstrap один раз создаёт crypto-random Id16 для каждого нового supporting datadir; сохраняет `extractor-deployment-id` внутри корня соответствующего native volume, без использования native DB keys. Эти IDs переживают restart/recreate контейнера с тем же volume. Новый store получает новый ID; клонирование одного ID для двух одновременно работающих stores запрещено. Bootstrap формирует `/etc/extractor/polygon-sources.json` (Compose read-only bind / Kubernetes ConfigMap) из этих IDs и проверенных network/genesis/roles; один и тот же artifact передаётся observer. Потеря identity-файла при существующем store — bootstrap error. После намеренной замены supporting store bootstrap обновляет manifest; старые evidence сохраняют старые IDs, новый source допускается только после identity/ancestry checks, без сброса retained irreversible anchor.

`supporting_node_id` и `source_store_identity` — эти deployment IDs, **не удалённая RPC-аттестация процесса**. Own-source routing обеспечивают private service network, service name и trust/config окружения. RPC проверяет chain/genesis/Heimdall identity, checkpoint и finalized ancestry; одинаковая Ethereum network identity основного и supporting Geth сама не доказывает роль. Никакого `admin` RPC, нового сервиса или вымышленного `getNodeId` не требуется.

## Durable anchor и очистка

Для каждого статуса направление ancestry одно: `isAncestorOrSelf(activation.block, evidence_anchor)`; номер блока сам по себе недостаточен. ETH safe/finalized, BSC attested Source/Target и Polygon locally verified projection применяют именно это направление. GC дополнительно требует соответствующего irreversible evidence и остальных условий ниже.

Authoritative native finalized setter, принявший высоту ниже durable irreversible anchor либо несовместимый hash/branch, — fatal invariant даже при прежнем canonical head. Это новое принятое native решение; его нельзя объявить stale getter/callback. Нулевой no-op, startup fallback/отсутствие RAM marker и delayed observation таким setter decision не являются. Fixture FINAL/GC включает same-head finalized regression и проверяет остановку до очистки/новой публикации.

Общий `AcceptedGcAnchor={network,bor_or_execution_anchor:AnchorRef,native_decision,evidence_digest,profile_ref}`; для Polygon содержит полный L1Checkpoint evidence. Native-only callbacks/safe/early votes/whitelist не заменяют указанный для сети anchor. Его принятие и последующее освобождение refs/pins — отдельные sync decisions; payload ACK не является GC.

Удаление требует одновременно: canonical-finalized либо terminal removed entity находится в покрытой anchor области; отсутствуют относящиеся pending capture/transition/replay/delivery; остаются достаточные compact anchor/ancestry/decision-frontier records. Noncanonical ветка ниже anchor освобождается только при доказанном расхождении с закреплённой canonical ancestry. Live pending pin никогда не снимается только из-за высоты. При отсутствии доказательств сохранить state, без TTL/угаданных confirmations.

Fatal regression — **новое authoritative canonical решение**, не содержащее сохранённый irreversible anchor либо имеющее другой hash на его высоте. Stale callback, lower safe, пропавший VotePool, отстающий Heimdall или недоступный header не являются доказанным regression. Они требуют refresh/retry или блокируют GC. После подтверждённого конфликта нода останавливается по D57; удалённое по принятой финальности прошлое состояние не обещается восстановить.

## Проверки текущей семантики

DATA-01: все применимые header/call/account fields, presence/ноль, пустой блок, CREATE attempt vs success, storage reset. FEE-01: gas purchase/return/recipient credits, blob charge, same payer/recipient, zero-fee system tx, revert и +10/−10; суммы address effects согласованы с tx state, receipt aggregate не удваивает поток. FINAL-ETH: safe fallback, setter до FCU error, finality-only без head, stale event. FINAL-BSC: header snapshot/early VotePool отдельно, restart loss, side branch, source lookup failure. FINAL-POLY: milestone purge, persisted whitelist без source reply, L1 pending/pruned state, exact hash-pinned call/root/ancestry, checkpoint params drift, native publication при unavailable GC. GC-01: crash каждого accepted-anchor/pin-release marker, active pending, removed branch, stale observation vs доказанный конфликт. Всё это будущие E01/E02/E04/E05 fixtures, не результаты реализации.
