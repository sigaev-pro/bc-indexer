# Контракт сообщений текущей поставки

ETH/BSC/POLY Mainnet, live-only. Точные finality/header/fee semantics — [CURRENT_SEMANTICS](CURRENT_SEMANTICS.md); ID/control/serialization — [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md). Runtime проверок ещё нет.

Тематическое представление [единого ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md); изменения нормативного содержания синхронизируются с root в том же commit.

## 1. Типы и присутствие

| Тип | Инвариант |
|---|---|
| Hash32 / Address20 | Ровно 32 / 20 bytes; не произвольные строки |
| UInt256 | Ровно 32 bytes, unsigned big endian; арифметика без float |
| Delta256 | знак плюс UInt256 magnitude; отрицательный ноль запрещён |
| Timestamp | seconds:u64 + nanos:u32 < 1e9 + precision=`SECONDS \| MILLISECONDS \| NANOSECONDS`; сохраняется доступная source precision |
| NetworkIdentity | family=`EVM`, chain_id:UInt256, genesis_hash:Hash32; chain_key — только alias для маршрутизации |
| ProfileRef | network profile, client commit, adapter version, fork rules version, normalization version; зарегистрированный профиль |
| FieldAvailability | `VALUE(T) \| NOT_APPLICABLE \| UNAVAILABLE(reason_code)`; применяется только к полям с определённой семантикой отсутствия |

Отсутствующий scalar и корректный ноль различаются. Proto scalar с обязательным присутствием использует explicit presence и validator. Для `oneof` требуется ровно один вариант; неизвестные enum values, profile/type/schema versions не публикуются как нормализованные данные. Empty list означает, что источник вернул пустой применимый набор; известная ошибка получения не маскируется пустым списком. Независимый verifier полноты не требуется. `UNAVAILABLE` у обязательных transaction details означает ошибку обработки; неприменимость доказывается профилем.

Произвольные JSON, `Any`, map<string,bytes> не являются обходом типов. Bytes разрешены для hashes/addresses, calldata, code, logs data, source encoding и сериализованного транспортного payload. Каждое сетевое расширение имеет конкретную схему и version gate.

Основание: [Protobuf field presence](https://protobuf.dev/programming-guides/field_presence/). Protobuf по умолчанию не проверяет эти доменные ограничения; нужен validator E02.

## 2. Envelope и идентичность

`Envelope={schema_version, network, profile_ref, event_id, producer_id, run_id, source, capture_mode, payload}`.

`source={node_instance_id,writer_epoch,source_sequence}`. Текущий capture_mode=LIVE; recovery сохранённого live события не меняет его mode. SYNC/BACKFILL отложены и не принимаются первым live profile. `payload` — закрытый union ниже. Версия wire schema не выводится из номера Markdown v3.

`BlockRef={number:u64,hash:Hash32,parent_hash:Hash32,timestamp:Timestamp}`. `InclusionRef={network,block_hash,native_origin=BLOCK_BODY,native_position:u64,transaction_hash:Hash32}`. Позиция — native body index, не номер receipt/прихода сообщения. Один tx hash не идентифицирует включение в ветку. Optional legacy receipt — отдельный auxiliary record, не fake body tx.

`ActivationRef={inclusion_ref,producer_id,run_id,activation_id}`; `BlockActivationRef={network,block_hash,producer_id,run_id,activation_id}`. Разные lifecycle события имеют разные event_id; retries сохраняют ID и bytes. Persistent counters, fixed encodings и semantic/public identity boundary — CONTROL_WIRE_CONTRACT. Новый случайный event ID при retry запрещён.

## 3. TransactionRecord

| Поле | Состав и правило |
|---|---|
| inclusion, block_activation | Связь с конкретной веткой и активацией блока |
| native_payload | Ровно один вариант из [реестра типов](TRANSACTION_TYPE_REGISTRY.md); native type проверяется с сетью/fork |
| source_hash | Native Hash32, hash_kind=NATIVE_TRANSACTION; Bor legacy receipt имеет отдельный auxiliary origin |
| raw_encodings | Непустой список `{kind,bytes}`: EVM_LEGACY_RLP / EIP2718_ENVELOPE / BOR_STATE_SYNC_ENVELOPE по native варианту; source bytes без перекодирования |
| execution_projection | Typed effective sender/target/value/input и provenance=`ENCODED \| NATIVE_DERIVED \| NOT_APPLICABLE`; исходные native поля не перезаписываются |
| receipt | Typed outcome, fees/gas, committed logs и применимые network receipt details |
| calls | Упорядоченные execution frames, включая failed/reverted calls |
| actions | Причинные native balance actions, включая fee/refund/mint/burn и компенсирующие действия |
| execution_actions | Типизированные действия исполнения, не сводимые к balance transfer; обязательный SelfDestructAction при его native исполнении |
| rollback_scopes | Упорядоченный по scope_id список RollbackScope транзакции; scope 0 — транзакционная граница, остальные — наблюдённые native scopes; точные правила D74 в CURRENT_SEMANTICS |
| state_changes | Итоговый transaction-boundary diff затронутых accounts/storage после rollback, включая реально сохранённые fees/nonce |
| execution_position | operation_index:u64 — native body position текущей body-транзакции, равен inclusion.native_position; execution BlockRef, при отличии от inclusion block связь задаёт профиль. Не tx-local номер TransactionProtocol operation |
| capture_status | По receipt/calls/actions/state_changes/network details: CAPTURED либо NOT_APPLICABLE по profile. Известная ошибка — local fatal до READY, не публичная успешная запись с ERROR/пустыми обязательными details |

`ExecutionProjection` — union EvmMessage / NativeProtocolExecution. `Effective<T>={value:T,provenance=ENCODED|NATIVE_DERIVED}`. EvmMessage: sender:Effective<Address20>, target?:Effective<Address20>, value:Effective<UInt256>, input:Effective<bytes>, nonce:Effective<u64>, gas_limit:u64, effective_gas_price:UInt256. Original caps/access/blob/authorization/signature остаются в native_payload. NativeProtocolExecution первого профиля: operation=BOR_STATE_SYNC, record_count:u64; source records — typed Bor payload. Отсутствующие в native протоколе sender/nonce/signature не заполняются нулём и не выдумываются.

`Receipt`: native outcome=`Status(success:bool) | PreStatusStateRoot(Hash32)`; execution result=`SUCCESS | REVERT | FAILURE`; cumulative_gas_used и gas_used по применимости; effective_gas_price, blob_gas_used, blob_gas_price с явным присутствием. Отдельные optional Address20: attempted_create_address и created_contract (только успешное создание); итоговый existence находится в AccountChange. Logs, logs_bloom, source_receipt_encoding с encoding kind сохраняются отдельно от projection. fee_summary и отдельные фактические BalanceEffects заданы в CURRENT_SEMANTICS; summary повторно не суммируется. Pre-Byzantium source root не превращается в RPC status: результат берётся из native execution. Field provenance и границы Bor/BSC — [NATIVE_CAPTURE_PROTOCOL](NATIVE_CAPTURE_PROTOCOL.md). Network details — закрытые варианты ETH/BSC/Polygon.

`BorReceiptDetails.source_log_scopes` — список `{transaction_log_index, scope=SYSTEM_PRE | TX_STATE_SYNC | SYSTEM_POST}` для source logs, атрибутируемых native instrumentation; порядок/bytes исходных logs не меняются. Native system log не выдаётся за действие StateSync tx. Полнота этой provenance проверяется SCOPE-01; отдельная публикация outside-tx событий остаётся optional. Field numbers и generated bindings — E02.

`Log={address,topics:Hash32[],data,transaction_log_index,block_log_index}`. Receipt содержит только committed logs. Reverted log attempts не входят в receipt logs и не являются обязательным отдельным payload первой версии.

`CallFrame={frame_id,parent_id?,execution_order,depth,engine,kind,caller_address?,context_address?,code_address?,msg_sender?,target?,value?,input,output,gas?,gas_used?,outcome,effects_committed,error_code?,revert_data?,attempted_create_address?,created_address?}`. IDs/order/depth/gas — u64; addresses — Address20, value — UInt256, input/output/revert_data — bytes; outcome=SUCCESS|REVERT|FAILURE. error_code, если присутствует, — UTF-8 native diagnostic string в namespace profile_ref, без обещания общего межсетевого enum; revert_data сохраняется отдельно. Текущие engine=EVM|NATIVE_PROTOCOL и kind=CALL|CALLCODE|DELEGATECALL|STATICCALL|CREATE|CREATE2|NATIVE_PROTOCOL. Native protocol frame создаётся только при зарегистрированном source execution boundary. Поля creation/caller уточняет CURRENT_SEMANTICS. Успешный child внутри reverted ancestor имеет effects_committed=false; сортировка по depth не заменяет order.

BalanceAction/BalanceEffect и closed TransactionProtocol operations заданы в CURRENT_SEMANTICS по D62. Cause включает Frame, ExecutionAction и TransactionProtocol. Все фактические gas purchase/return/fee credits сохранены, receipt fee_summary отдельный агрегат. Никакого OTHER для неизвестного source path и двойного учёта transfer callbacks. +10 и −10 остаются отдельными actions при нулевом итоге.

D74: calls, execution_actions и actions используют один tx-local execution_order наблюдаемых операций; это не три независимых счётчика. CallFrame отмечает вход, не весь интервал исполнения. Общий номер у SELFDESTRUCT и его денежной проекции разрешён только через точный cause=ExecutionAction; причинная ссылка Frame сама по себе не равенство порядка. Тип `RollbackScope={scope_id:u64,parent_scope_id?:u64,owner_frame_id?:u64,rolled_back:bool}` и invariants дерева/ссылок/committed заданы в CURRENT_SEMANTICS. Схема сохраняет различие absent и scope_id=0. Фрагментация D73 передаёт список scopes как typed nodes; chunks/retries не создают новых execution_order или scope IDs.

`ExecutionAction={execution_action_id,execution_order,frame_id,payload}`: закрытый union, текущий обязательный вариант — `SelfDestructAction={contract_address,beneficiary,balance_at_entry,effects_committed,deletion_eligible_by_rules,deletion_committed}`. Все scalar имеют presence; addresses/amount соответствуют общим типам. `frame_id` ссылается на исполнявший opcode frame, а не на искусственный CALL. Native pseudo enter/exit для SELFDESTRUCT нормализуются в это действие и не создают второй transfer. `deletion_eligible_by_rules` описывает fork/creation eligibility, `deletion_committed` — сохранённый результат этого действия после rollback/finalization; при effects_committed=false deletion_committed=false. Конечное существование account проверяется отдельно через AccountChange, а не выводится только из opcode. Повторные/отменённые действия сохраняются со своим порядком, даже если сумма равна нулю. Денежная проекция использует `BalanceAction.cause=ExecutionAction(execution_action_id)` (добавленный вариант cause); execution action и его balance projection не суммируются как два изменения. Self-beneficiary/возможный burn следуют native fork rules, не универсальному правилу transfer. [EIP-6780](https://eips.ethereum.org/EIPS/eip-6780). Точные field numbers — E02; смысл полей фиксирован этим контрактом.

`AccountChange={address,existed_before,exists_after,balance_before,balance_after,nonce_before,nonce_after,code_hash_before,code_hash_after,code_before,code_after,storage_reset,storage_changes[]}`. Address20, bool exists/reset, UInt256 balances, u64 nonce, Hash32 code hashes, bytes code. `StorageChange={key:Hash32,before:Hash32,after:Hash32}`. CURRENT_SEMANTICS определяет presence/order/reset. Данные относятся к tx boundary; actions и diff не суммируются как независимые изменения.

### 3.1. Исходное receipt-кодирование D75–D76

`SourceReceiptEncoding={kind:ReceiptEncodingKind,native_type?:u32,bytes}`. kind и непустые bytes обязательны; наличие native_type определяется таблицей. Это закрытый registry v1 внутри logical schema v2 D73, не новый transport version. Нулевой/неизвестный enum, неверная применимость origin/network/profile и отсутствующий обязательный receipt отвергаются до READY. Поле bytes передаётся по D73 через BlobRef/BlobChunk; размер не разрешает усечение.

| Enum value / kind | Применимость и native_type | Точный byte contract |
|---|---|---|
| 1 LEGACY_CONSENSUS_RLP | Обязательный body receipt с native Receipt.Type=0; native_type обязательно присутствует и равен 0 | RLP списка consensus-полей native receipt; нет ведущего type byte 0 |
| 2 TYPED_CONSENSUS_ENVELOPE | Обязательный body receipt с ненулевым native type; native_type обязателен, 1…127, только разрешённые сетью/fork registry значения | Один исходный type byte, затем native consensus receipt RLP. Внешняя RLP-string wrapper не добавляется |
| 3 BOR_LEGACY_STORAGE_RLP | Только optional BorLegacyReceiptRecord origin=BOR_LEGACY_SYNTHETIC_RECEIPT; native_type отсутствует: storage bytes не содержат type byte | Исходный raw storage RLP native Bor receipt, без перекодирования в consensus receipt и без добавления Bloom |

Значение 0 зарезервировано как INVALID, не default-успех. Для обязательных ETH/BSC/Polygon body tx `Receipt.source_receipt_encoding` содержит ровно один вариант 1/2. native_type берётся из фактического native Receipt и сверяется с реальной включённой транзакцией по pinned profile, у BSC — после actualTx substitution. Отдельного выдуманного BSC system type нет. Bor StateSync body type 0x7f использует вариант 2 с native_type=127; обычные Bor body receipts — 1/2 по фактическому type. Наличие type в коде не разрешает его вне активных native fork rules.

Эталон вариантов 1/2 — результат `Receipt.MarshalBinary` закреплённого клиента на исходном native Receipt после штатного формирования consensus fields. `Receipt.EncodeRLP` для typed receipt имеет дополнительную внешнюю RLP-string упаковку, поэтому его полный результат не взаимозаменяем с MarshalBinary. Consensus содержимое — PostStateOrStatus, CumulativeGasUsed, Bloom и Logs; log сохраняет native address/topics/data. Status=0/1 и применимый pre-status state root не заменяются нашим execution outcome. Контракт не утверждает, что bytes были получены из сетевого пакета: это точное native consensus-кодирование исходного результата. Источники кодеков: [Geth](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/receipt.go), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/receipt.go), [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/receipt.go).

D76: API MarshalBinary с полным возвращаемым byte array не является обязательным collector path. Допустим потоковый writer/адаптация native-кодека с побайтно тем же результатом из owned native receipt fields в disk spool; длины вложенного RLP рассчитываются проходами по spool без полного дополнительного receipt/log-data буфера. Это не сборка из нормализованных Receipt/Log и не изменение native исполнения, fork rules или consensus bytes. Native reader/codec commit фиксирует ProfileRef. Ошибка кодека или несогласованные source данные — явная normalization error, не fallback в storage/JSON/пустые bytes. Дополнительная память collector ограничена независимо от размера receipt; существующая память native execution оценивается отдельно по D58. До READY bytes фиксируются с логической записью, после READY повторная генерация вместо сохранённых bytes запрещена.

Validator проверяет точное RLP framing по выбранному kind, допустимые scalar widths/status-root, native_type/profile/origin и отсутствие trailing bytes; для общих consensus-полей проверяет согласованность с типизированным Receipt. Чтение/проверка крупного encoded receipt также потоковые: decoder не обязан создавать полный массив bytes или второе дерево native Receipt/Logs. Length overflow, truncated/невалидный RLP и расхождение обязательных source полей не проходят READY/CLOSED. Форматные проверки не заменяют независимый native byte oracle при приёмке.

Consensus bytes не содержат gas_used отдельной tx, effective/blob gas price, fee_summary, transaction/block hash, индексы включения/logs, attempted/created address или Bor provenance. Эти данные остаются в общих типизированных Receipt/Log/TransactionRecord и берутся из своих native источников; отсутствие в consensus bytes не означает отсутствие в публичной записи. Обязательное значение 0 отличается от отсутствующего/неприменимого. Перекодирование consensus bytes ради включения этих полей запрещено.

Для optional Bor legacy `raw_encoding` имеет тип SourceReceiptEncoding, kind=3, native_type absent. Сохраняется raw storage value, соответствующее native `ReadBorReceiptRLP`, с decode-проверкой native storage codec; нельзя повторно кодировать derived Receipt и выдавать его за исходные storage bytes. Storage consensus-подмножество не содержит Bloom; доступные derived bloom/hash/index сохраняются отдельно в source_receipt/auxiliary metadata с native provenance. Source-only status не доказывает исполнение всех system calls. При включённом optional профиле corrupt/missing обязательный source не превращается в пустую успешную auxiliary запись. Kind 3 запрещён в body Receipt, а kinds 1/2 запрещены вместо raw_encoding legacy auxiliary. Источник: [Bor raw storage reader](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/rawdb/bor_receipt.go). Историческое имя BOR_RECEIPT_STORAGE_RLP из исследования заменено этим registry; alias на wire не принимается.

### 3.2. Применимость receipt network details D75

`Receipt.network_details` — обязательный closed oneof: EthReceiptDetails / BscReceiptDetails / BorReceiptDetails. Отсутствие дополнительных полей не означает отсутствующий Receipt или отсутствие его общих полей.

| Профиль обязательной body tx | Дополнительные поля первого профиля |
|---|---|
| ETH | EthReceiptDetails — явно присутствующий пустой typed variant; дополнительных полей нет |
| BSC, включая system tx | BscReceiptDetails — явно присутствующий пустой typed variant; общие gas/log/fee поля сохраняются в Receipt |
| Polygon, обычная body tx | BorReceiptDetails с присутствующим пустым source_log_scopes; обычные receipt logs принадлежат этой tx |
| Polygon StateSync 0x7f | BorReceiptDetails.source_log_scopes описывает каждый исходный receipt log ровно один раз по transaction_log_index, в порядке source logs; SYSTEM_PRE / TX_STATE_SYNC / SYSTEM_POST определяются native instrumentation |

Для StateSync без logs список source_log_scopes пуст. Повторы/пропуски/индекс вне исходного receipt и неизвестный scope запрещены. Provenance не фильтрует/переставляет logs и не меняет consensus bytes; SYSTEM_PRE/POST не становятся действиями StateSync tx. Reverted log attempts не добавляются в receipt. Неподходящий network variant, unknown extra field или неподдержанный native extension требуют явной ошибки и versioned registry/profile update до поддержки; Any/JSON map и выдуманные нулевые поля не допускаются. Optional legacy Bor — отдельный auxiliary payload со своей схемой, не этот body oneof.

## 4. События и передача блока

| Payload | Смысл |
|---|---|
| TransactionApplied | Нормализованное native включение, producer/run и его наблюдаемая активация |
| TransactionRemoved / TransactionStatusChanged | Native reorg/finality observations; ссылки на активацию данного producer |
| BlockApplied | Native BlockRef/header metadata, producer/run, source transaction count при наличии; пустой блок также публикуется |
| BlockRemoved / BlockStatusChanged | Native lifecycle evidence данного producer/run |
| SystemBalanceApplied/Removed/StatusChanged | Optional вне-транзакционный scope D17 |
| BorLegacyReceiptApplied/Removed/StatusChanged | Optional typed auxiliary source receipt; не body transaction |

BlockComplete и CoverageUpdated исключены D25. Source metadata не является результатом собственного пересчёта/сверки хеша или полного inventory. Текущий native decoder сохраняет данные ETH/BSC/Polygon по закрытым профилям; production completeness verifier не добавляется. Classic/AVM и история отложены D38/D42 и не задают обязательств текущего wire contract.

D79/F02: одно native decision с публичными событиями создаёт один delivery batch с полным упорядоченным набором semantic descriptors этого решения. Batch может охватывать несколько блоков при reorg либо содержать только status/finality события. Decision без публичных событий фиксирует decision checkpoint без batch allocation. Decision→batch_id и счётчик сохраняются атомарно; повтор marker восстанавливает прежнюю связь. Большие descriptor/payload списки пишутся фрагментами; лимиты WriteBatch/RPC не делят semantic decision на дополнительные batches. Closing marker закрывает весь manifest, не очередной chunk.

После durable сохранения полного batch/messages — WAL_SAVED; после PubAck всех frames всех его событий — NATS_ACKED. Partial event/block ACK не завершает batch и не разрешает его cleanup. Это локальный учёт передачи полного набора событий решения, не утверждение, что источником выданы все данные блокчейна. Отдельное публичное событие прогресса/coverage и UI не требуются.

Optional BorLegacyReceiptRecord описан в [POLYGON_LEGACY_CAPTURE.md](research/POLYGON_LEGACY_CAPTURE.md): source receipt/hash/index/encoding, без фиктивной transaction envelope. Его source-only ограничения сохраняются, auxiliary completeness verifier не вводится.

## 5. Lifecycle, finality и поздняя metadata

| Текущее состояние | Событие | Результат |
|---|---|---|
| ABSENT | Applied(A, baseline revision=0) | ACTIVE(A), исходный typed lifecycle snapshot |
| ABSENT | StatusChanged(A, revision>0) | PENDING_APPLIED(A), сохранить последнее полное evidence по revision; не выдумывать TransactionRecord |
| PENDING_APPLIED(A) | Applied(A, revision=0) | ACTIVE(A), исходная запись плюс уже полученное более позднее evidence |
| PENDING_APPLIED(A) | StatusChanged(A, revision выше) | PENDING_APPLIED(A), заменить snapshot более поздним |
| ABSENT / PENDING_APPLIED(A) | Removed(A) | REMOVED(A), tombstone сохраняет смысл отмены до позднего Applied |
| ACTIVE(A) | retry Applied(A), те же bytes | Тот же факт, не новое включение |
| ACTIVE(A) | StatusChanged(A, revision выше) | ACTIVE(A), новое evidence |
| ACTIVE(A) | Removed(A) | REMOVED(A), терминальное для A |
| REMOVED(A) | поздний StatusChanged/Applied(A) | Не реактивирует A |
| REMOVED(A) | возвращение того же блока | Новый Applied(B), B≠A |

Независимые live producer/run могут наблюдать одно включение с разными activation identity; общий registry и подавление повторов не требуются. При доставке Removed раньше Applied ссылка сохраняет смысл отмены; поздний Applied не оживляет удалённую активацию. Revision монотонна внутри объекта lifecycle данного producer/run, не между независимыми runs или сетями. History/backfill не входят в текущий контракт.

Applied несёт initial lifecycle snapshot с revision=0; неизвестное на этом этапе finality evidence имеет явный профильный статус, не выдуманный finalized. Каждый StatusChanged несёт полный replacement snapshot evidence своей активации, не delta patch, и durable положительную revision. Более старая revision не заменяет новую; одна и та же revision с разным содержимым — конфликт/ERROR, не last-write-wins. Removed терминален независимо от порядка доставки; более поздняя запись не реактивирует A, новый период каноничности имеет другую activation_id. Writer не создаёт новые Status для terminal A. Эти правила задают смысл wire events и тестового валидатора, не добавляют production consumer.

Native bridge отслеживает любую durable активацию с READY Applied, которая может быть опубликована; reorg создаёт Removed даже до первого Publish/PubAck Applied. Факт внешней доставки не угадывается. Прогресс наблюдения имеет batch/activation identity: одного `(run,block_number,block_hash)` недостаточно для двух активаций одной ветки. Applied, lifecycle state и его исходная revision фиксируются атомарно по протоколу WAL. Для событий до READY, не допускавшихся к публикации, применяются правила незакрытой попытки.

Finality — closed EthereumEvidence/BscEvidence/PolygonEvidence по CURRENT_SEMANTICS. D62 различает BSC early votes/header-attested; D63 отделяет native Polygon status от L1 checkpoint GC. Safe/justified/milestone не объявляются универсально эквивалентными finalized; точные поля, availability, ancestry и regression rules определены, E02/E05 проверяют их реализацию.

Поздняя finality передаётся StatusChanged. Отсутствующие обязательные execution details не разрешается выдавать как полную транзакцию с обещанием дослать. Если потребуется изменяемая non-finality metadata, до реализации вводится отдельный typed event/version с fixture; свободного patch JSON нет. Fork-specific L1/batch сведения, недоступные на данном этапе, имеют явно определённый pending статус, не нулевое выдуманное значение.

## 6. Транспорт и порядок

Полный Prepare/Publish/Release, immutable header contract и bounded serialization задаёт [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md). Нормативная единица retry — сохранённый publication plan с destination и exact frame/header bytes, а не повторная сериализация record новой сборкой.

Одна логическая Envelope кодируется последовательностью ограниченных типизированных `RecordFragment`, затем каждый такой fragment упаковывается в `TransportFrame`. Envelope остаётся моделью данных, но единого сериализованного Envelope protobuf-message больше нет. Ни один этап не требует собрать его bytes или полное дерево объектов. Контракт фрагментов ниже — D73/F11; точные transport tags, manifests, headers и layout — [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md).

Transport payload — `CompleteFragment{fragment_bytes}` либо `FragmentPart{part_index,part_count,fragment_total_bytes,data}`. COMPLETE/PART выбирается отдельно для каждого RecordFragment. Несколько фрагментов одной записи могут иметь разные формы; менять форму конкретного фрагмента в сохранённом plan нельзя. Все frames записи имеют одинаковые event_id, record_digest и chain_sequence; fragment_index различает сообщения структуры записи, part_index — транспортные части одного такого сообщения.

record_digest связывает упорядоченные исходные bytes всех содержательных RecordFragment; fragment_digest проверяет bytes одного фрагмента. Формула исключает завершающий seal из record_digest, чтобы не было самоссылки. READY фиксирует bytes фрагментов, SEALED дополнительно фиксирует transport layout/headers/bytes. Digest не заменяет tx hash или semantic event ID. [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/): повторная сериализация другим runtime/version вместо сохранённых bytes запрещена.

Лимит учитывает полную сериализованную TransportFrame и NATS headers, а не только data. D52: default `transport.max_frame_bytes=2097152` (2 MiB) задаётся YAML Gateway и валидируется до отправки; NATS server/stream настроены на этот предел, gRPC учитывает собственную обёртку. Размер RecordFragment ограничен отдельно: 65536 bytes, независимо от NATS limit. При меньшем доступном frame фрагмент передаётся через FragmentPart. Изменение лимита относится к новым планам; сохранённые pending bytes не перекодируются. При несовместимом уменьшении лимита — явная ошибка конфигурации до публикации, pending сохраняются до восстановления достаточного лимита. [Подробности](LIVE_CONFIG_SPEC.md). Pending spool без TTL/прикладной квоты по D28. Post-ACK retention вне scope. При невозможности разместить запись — backpressure/явная ошибка и незавершённый блок; обрезать данные запрещено.

Gateway назначает chain_sequence в пределах publication_epoch своего store; все части имеют одну пару epoch/sequence. Epoch сохраняется при restart с диском и меняется при создании нового store. Publication plan и точные frame bytes сохраняются producer до первой публикации; после потери Gateway store replay использует прежнюю пару и bytes. Это порядок регистрации в одном epoch, не block chronology, не NATS stream sequence и не глобальный порядок через потерю диска. Части разных live events могут чередоваться. Протокол Prepare/Publish/IngestAck/Release: [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md).

Execution chronology задают block parent links, native transaction position, operation_index и action/frame order. Delivery chronology задают chain_sequence и transport metadata. `part_index` задаёт только сборку bytes. Нельзя сортировкой chain_sequence восстанавливать blockchain chronology.

NATS message ID стабильно различает `(network,event_id,fragment_index,form,part_index)`. После PubAck всех transport frames всех RecordFragment всех событий закрытого decision batch Gateway sync фиксирует NATS_ACKED и подтверждает adapter batch_id/digest/plan identity. ACK seal, одного фрагмента или event не разрешает очистить весь batch. NATS deduplication window не обеспечивает вечный exactly-once: контракт допускает повторы с тем же ID/bytes. ACK не подтверждает работу downstream consumers. Durable plan/WAL, повторный запуск, потеря состояния и decision-batch IngestAck/Release определены в [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md).

### 6.1. Типизированный формат RecordFragment — D73

Формат сохраняет все поля логической схемы из разделов 1–5, включая полные bytes, строки, списки, nested messages, native extensions и различие absent/empty/zero. Это закрытое типизированное представление дерева по schema_version: произвольные поля, opaque Any/JSON/map и неизвестные типы запрещены. Числа field_tag и type_key закрепляет единый versioned registry логической схемы в E02-02.schemas; fragment codec не придумывает их по порядку появления. Изменение registry проходит обычную проверку совместимости. Наличие codec не закрывает отдельные открытые вопросы о содержании native схем.

Версии D73: logical schema_version=2; record_format_version=1; transport/layout/header/control/manifest/reservation=2. NetworkIdentity и DestinationBinding digest/schema остаются v1. Это замена прежнего wire-проекта, без автоматической миграции pending: несовместимые старые pending сначала drain старой сборкой по D59. Markdown v3 не является wire version.

Каждый сериализованный RecordFragment, включая wrapper, имеет длину 1…65536 bytes. Внутри разрешён ровно один из четырёх payload. Точные tags:

| Message | Fields: tag → type |
|---|---|
| RecordFragment | 1 record_format_version(u32=1), 2 event_id(bytes40), 3 fragment_index(u64), oneof: 4 header(RecordHeader) / 5 items(TypedItems) / 6 blobs(BlobChunks) / 7 seal(RecordSeal) |
| RecordHeader | 1 schema_version(u32=2), 2 network(NetworkIdentity), 3 event_family(u32), 4 event_kind(u32), 5 root_type(u32), 6 node_count(u64), 7 blob_count(u64), 8 total_blob_bytes(u64), 9 fragment_count(u64) |
| TypedItems | 1 items(repeated TypedItem) |
| TypedItem | 1 node_id(u64), 2 parent_id(optional u64), 3 field_tag(optional u32), 4 element_index(optional u64), oneof: 5 message(MessageValue) / 6 list(ListValue) / 7 bool_value(bool) / 8 uint32_value(u32) / 9 uint64_value(u64) / 10 enum_value(u32) / 11 bytes_value(BlobRef) / 12 utf8_value(BlobRef) |
| MessageValue | 1 type_key(u32), 2 direct_child_count(u64) |
| ListValue | 1 count(u64) |
| BlobRef | 1 blob_id(u64), 2 length(u64), 3 sha256(bytes32) |
| BlobChunks | 1 chunks(repeated BlobChunk) |
| BlobChunk | 1 blob_id(u64), 2 offset(u64), 3 data(bytes, 1…16384 bytes) |
| RecordSeal | 1 fragment_count(u64), 2 content_bytes(u64), 3 record_digest(bytes32) |

Все перечисленные поля обязательны, кроме трёх явно optional связей TypedItem и невыбранных oneof alternatives. Scalar zero/false присутствуют явно. Бинарное кодирование: tags по возрастанию, minimal unsigned varints, protobuf length-delimited, никаких unknown/duplicate singular fields; repeated только в указанном порядке. Bool только 0/1, enum только разрешённое registry значение. Логические UInt256, Hash32, Address20 представлены bytes_value с проверкой schema-specific длины/кодирования; подписанные/другие скаляры не вводятся этим форматом и требуют отдельной версии схемы. Это каноническое кодирование именно этого ограниченного формата, не любого Protobuf.

Дерево типизированных узлов:

1. node_id непрерывны от 0 в depth-first preorder. Узел 0 — message логической Envelope с root_type из header, без parent_id/field_tag/element_index. Остальные имеют parent_id<node_id. У message child есть field_tag из schema и нет element_index; поля идут по возрастанию tags. Тип и presence соответствуют schema, singular field не повторяется, oneof содержит только выбранную ветку.
2. List — отдельный узел даже при count=0; его children имеют только element_index=0…count−1 и тип элемента из schema. MessageValue.direct_child_count и ListValue.count совпадают с фактическим числом непосредственных детей. Preorder запрещает вернуться в уже закрытое поддерево. Неограниченная вложенность проверяется disk-backed stack/index, без рекурсивного RAM object graph.
3. Каждый bytes/UTF-8 узел имеет BlobRef, где blob_id=node_id. Поле length — число bytes, SHA-256 — digest исходного содержимого. Даже фиксированные hash/address bytes идут по этому правилу. Absent — нет узла; empty — есть BlobRef(length=0,SHA256(empty)), без BlobChunk. Дедупликация одинаковых значений разных полей не меняет их identity. UTF-8 проверяется инкрементально без normalization; граница chunk может разрезать многобайтовый символ.
4. BlobChunks идут по возрастающему blob_id, затем offset. Для непустого blob offsets непрерывны от 0, размер очередного chunk=min(16384,remaining). Пропуски, пересечения, лишние bytes/chunks, неизвестный blob_id, неверный length/digest запрещены. Hash и длина каждого blob проверяются независимо от record_digest.

Порядок фрагментов: index 0 — ровно один header; затем непустые TypedItems для всех узлов; затем непустые BlobChunks для всех непустых blobs; последний — ровно один seal. Индексы непрерывны, fragment_count включает header и seal. Nodes содержат только ограниченные scalars/refs, не содержимое blobs. Items и chunks упаковываются максимальным непустым префиксом соответствующего потока, который помещается в 65536 bytes с фактическим fragment_index и wrapper. Header и seal всегда отдельны. Один TypedItem обязан помещаться; текущая фиксированная таблица это обеспечивает. Пустые items/blobs fragments запрещены. Для маленькой Envelope также используются header/items/[blobs]/seal; отдельного монолитного fast path нет.

Producer использует disk spool и несколько проходов: фиксирует дерево, lengths и blob hashes; рассчитывает индексы/разбиение items/blobs; после этого пишет header с итоговыми counts, затем фрагменты и seal. Header не участвует в упаковке items/blobs, поэтому подсчёт fragment_count не циклический. Списки и крупные поля поступают потоково; полный исходный blob, дерево либо запись не выделяются в памяти. Временные индексы, стек и manifest entries могут расти на диске; рабочие буферы ограничены fragment/RPC/WriteBatch лимитами. Disk exhaustion и capture backpressure сохраняют ранее принятые правила D57/D58, без сокращения данных.

Для исходных bytes фрагментов B0…B(K−2), исключая seal B(K−1):

`record_digest = SHA256(UTF8("extractor.record.v1") || 0x00 || concat(u64be(index) || u64be(length(Bi)) || Bi))`.

`content_bytes=sum(length(B0)…length(B(K−2)))`; `record_length=content_bytes+length(seal)`; `fragment_digest[i]=SHA256(Bi)`, включая seal. Header и seal содержат один K; seal.record_digest совпадает с READY descriptor и transport metadata. Header metadata совпадает с логической Envelope и descriptor. Все счётчики, суммы, offsets и индексы — checked u64; overflow отвергается до READY, без wrap/truncate. Отдельного прикладного предела общего размера записи или поля нет; это не обещание бесконечного диска/счётчиков.

Полная запись валидна только после проверки всех фрагментов, schema/tree/blob invariants, counts и seal digest. node_count>=1, fragment_count>=3; blob_count и total_blob_bytes равны числу BlobRef и сумме их lengths. Type keys и field tags положительны и принадлежат registry; Delta256 сохраняется составным message «знак + magnitude», а не новым signed scalar. Event identity, fragment_index/count, network и digests внутри фрагментов, manifest и transport wrapper согласованы. Получение seal отдельно не означает полноту. Независимый decoder может обходить дерево и blobs потоково через bounded disk-backed indexes; создавать downstream consumer в этом проекте не требуется. Конфликтующий повтор того же fragment/part index — ошибка; идентичный повтор идемпотентен. Gateway завершает validation до CLOSED и публикует только закрытую запись по принятому batch-протоколу. Предел одного Protobuf-message [меньше 2 GiB](https://protobuf.dev/programming-guides/proto-limits/#total-size-of-the-message) больше не ограничивает общий объём логической записи; подход соответствует [bounded messages для больших наборов данных](https://protobuf.dev/programming-guides/techniques/#large-data-sets).

## 7. Маршрутизация

Для каждой сети отдельные streams/subjects/limits: `evm.v3.<chain_key>.<event_family>.<event_kind>`. Текущие family=tx|block; optional balance.system|bor.legacy_receipt. Kind=applied|removed|status. Optional families разрешены только при declared profile feature; default первая версия outside-tx публикацию не включает, её включение требует соответствующих schema/fixtures. Markdown v3 не задаёт wire version.

Полная запись и её части направляются в один subject своего logical event; identity сети из frame проверяется против route. D48: stream и subject prefix задаются в YAML Gateway. D54: logical destination (account/stream/subject mapping) фиксируется в durable publication plan и ACK; изменение назначения допускается только после завершения pending адаптера и Gateway. Смена URL того же назначения допустима после проверки идентичности. Подтверждения разных назначений не образуют общий NATS_ACKED. Перенос pending не проектируется; точная идентификация назначения и handshake/drain checks заданы в CONTROL_WIRE_CONTRACT и DELIVERY_STATE_MACHINE, их runtime-проверки относятся к E03. Post-ACK retention не входит в гарантию сборщика.

## 8. Проверки для последующих этапов

| Fixture | Обязательный результат |
|---|---|
| Failed tx + успешный child в reverted parent | Gas/nonce сохранены по native правилам; child actions не committed |
| Компенсирующие transfers | Оба actions есть, net diff может быть 0 |
| BSC Parlia system | Фактический wire type/signature не потеряны; native Finalize capture проверен |
| Bor StateSync / legacy synthetic | Body 0x7f и optional legacy source receipt не смешаны; fixtures POLY-L01…07 из исследования E00-15 |
| Пустой блок / ошибка нормализации | Metadata пустого блока передаётся; известная ошибка даёт ERROR, а не NATS_ACKED |
| Parts reorder/duplicates/corruption | Сборка по индексу, повторы идемпотентны, конфликт bytes/digest отклоняется |
| Live reorg во время публикации | Дубли разных runs допустимы; lifecycle references не смешиваются, ACK учитывает все части полного decision batch |
| Неизвестный тип/версия/fork | Явная ошибка нормализации, без фиктивного поддержанного payload |
| Optional balances выключены | Передаётся заявленный transaction scope; полнота всех балансов не заявляется |

Это acceptance matrix будущих E01/E02/E03/E04/E05, не отчёт о пройденных тестах. E02 фиксирует compiled field numbers/closed validators текущих профилей; E03 проверяет delivery и bounded collector memory, без performance SLO. Текущий Bor StateSync обязателен. Историческая выгрузка не входит в active backlog.
