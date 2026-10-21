# Реестр native transaction types

> D42 (2026-10-21): текущий этап — Ethereum Mainnet, BNB Smart Chain (BSC) Mainnet и Polygon PoS Mainnet. Base/Arbitrum и их адаптеры/fixtures отложены; исследования и типизированные расширения ниже сохранены справочно, не блокируют текущую версию. Исторические сценарии отложены D38. Действующие live-типы транзакций ETH/BSC/Polygon, включая native system types, остаются обязательными. У каждой сети собственные WAL и журнал Gateway.

> Актуальная граница D23–D25 (2026-10-16): live и history работают в разных pod/process; история запускается простой командой N–M. Пересечения и повторные загрузки допускают дубли. Дедупликация и независимая проверка полноты блока выполняются за пределами сборщика. Здесь проверяется доставка подготовленных сообщений: RECEIVED → WAL_SAVED → NATS_ACKED. BlockComplete/coverage report и общий координатор устранения overlap не требуются. Подробности: [LIVE_HISTORY_DELIVERY](LIVE_HISTORY_DELIVERY.md).

E00-14, 2026-10-15. Проект нормативной модели для пяти сетей, основанный на закреплённых исходниках [CLIENT_MATRIX.md](CLIENT_MATRIX.md). Это не результат исполнения fixtures и не подтверждение активности каждого типа в Mainnet. Контракт сообщений: [MESSAGE_CONTRACT.md](MESSAGE_CONTRACT.md).

## Правила профиля

Ключ варианта: `(network identity, execution profile, native type)`. Один byte type без сети и fork profile недостаточен. Профиль содержит commit клиента, версию адаптера, fork schedule и версию нормализации. Применимость определяется нативными правилами конкретного блока, включая timestamp/height/ArbOS version, а не текущими настройками head.

В общей записи нет обязательных для всех типов signature, account nonce, gas price или получателя. Поля располагаются в конкретном варианте `oneof`; отсутствующее по модели поле не подменяется нулём. Типизированная execution projection отделена от исходных полей. Неизвестный тип, неподдержанный fork или отсутствие обязательного поля дают явную ошибку обработки; успешная доставка такого блока не объявляется.

`Q` — UInt256; `A` — Address20; `H` — Hash32; `B` — bytes. `Target` — `Call(A) | Create`; это явное различие, не нулевой адрес. Все поля варианта обязательны, кроме явно отмеченных optional. `[]` — упорядоченная последовательность, в том числе пустая. Нативные более узкие целые дополнительно проверяются на исходную разрядность.

## Стандартные EVM-варианты

| ID / payload | Поля |
|---|---|
| `0x00 EvmLegacy` | nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, signature:{v:Q,r:Q,s:Q}; replay_protection=`Unprotected | Eip155(chain_id:Q)` |
| `0x01 EvmAccessList` | chain_id:Q, nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x02 EvmDynamicFee` | chain_id:Q, nonce:u64, max_priority_fee_per_gas:Q, max_fee_per_gas:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x03 EvmBlob` | поля EvmDynamicFee, но target только `Call`; дополнительно max_fee_per_blob_gas:Q, blob_versioned_hashes:H[] |
| `0x04 EvmSetCode` | поля EvmDynamicFee, но target только `Call`; дополнительно authorization_list:Authorization[] |

`AccessEntry={address:A, storage_keys:H[]}`. `TypedSignature={y_parity:0|1,r:Q,s:Q}`. `Authorization={chain_id:Q,address:A,nonce:u64,signature:TypedSignature}`. Порядок и дубликаты source lists сохраняются. Blob sidecars не являются обязательной частью включённой execution transaction: сохраняются поля native body, blob hashes и применимый receipt; доступность blob data не подменяется обещанием полного DA-архива.

Это библиотека вариантов, а не разрешение всех пяти типов во всех сетях. ETH/BSC исходники определяют ID 0–4; Base/Nitro/Bor используют только варианты, разрешённые их собственными правилами в данном блоке. Fork validator обязателен. Наличие типа в исходнике само по себе не доказывает активацию.

Источники: [Geth types](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/types/transaction.go), [BSC types](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/transaction.go).

## Base

| ID / payload | Поля и применимость |
|---|---|
| `0x7e BaseDeposit` | source_hash:H, from:A, target:Target, mint:Q с native bound u128, value:Q, gas_limit:u64, is_system_transaction:bool, input:B |

Deposit не имеет обычной подписи и account nonce. `deposit_nonce` в receipt — отдельное optional поле, не основание синтезировать nonce исходной транзакции. `deposit_receipt_version` также optional и зависит от fork. `is_system_transaction` сохраняется из source и проверяется по fork, а не выводится из отсутствия подписи.

Типизированный `BaseReceiptExtension`: deposit_nonce?:u64, deposit_receipt_version?:u64. Типизированный `BaseFeeDetails` по применимому fork: l1_base_fee:Q, l1_fee_overhead?:Q, l1_base_fee_scalar:Q, l1_blob_base_fee?:Q, l1_blob_base_fee_scalar?:Q, operator_fee_scalar?:Q, operator_fee_constant?:Q, da_footprint_gas_scalar?:u16, tx_l1_cost?:Q. Отсутствие данных отличается от неприменимости. Коэффициенты сохраняются как native integers; RPC float `l1FeeScalar` не используется как точный источник.

Кодовые ветви будущих AA/Eip8130 (`0x79`) не считаются подтверждённым действующим Mainnet-контрактом. Новый fork требует отдельной версии реестра, полей, validator и fixtures до объявления покрытия.

Источники: [deposit](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/transaction/deposit.rs), [deposit receipt](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/receipts/deposit.rs), [native fee information](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/evm/src/l1block.rs). Конкретная nullable-семантика проверяется в E02 fixtures.

## Arbitrum Nitro

| ID / payload | Native поля |
|---|---|
| `0x64 ArbDeposit` | chain_id:Q, l1_request_id:H, from:A, to:A, value:Q |
| `0x65 ArbUnsigned` | chain_id:Q, from:A, nonce:u64, gas_fee_cap:Q, gas:u64, target:Target, value:Q, data:B |
| `0x66 ArbContract` | chain_id:Q, request_id:H, from:A, gas_fee_cap:Q, gas:u64, target:Target, value:Q, data:B |
| `0x68 ArbRetry` | chain_id:Q, nonce:u64, from:A, gas_fee_cap:Q, gas:u64, target:Target, value:Q, data:B, ticket_id:H, refund_to:A, max_refund:Q, submission_fee_refund:Q |
| `0x69 ArbSubmitRetryable` | chain_id:Q, request_id:H, from:A, l1_base_fee:Q, deposit_value:Q, gas_fee_cap:Q, gas:u64, retry_target:Target, retry_value:Q, beneficiary:A, max_submission_fee:Q, fee_refund_addr:A, retry_data:B |
| `0x6a ArbInternal` | chain_id:Q, data:B |
| `0x78 ArbLegacyImported` | legacy:EvmLegacy, hash_override:H, effective_gas_price:u64, l1_block_number:u64, sender_override?:A |

У `0x64–0x6a` нет native signature: нулевые V/R/S из универсальных accessor не становятся подписью. Nonce есть у Unsigned и Retry, но Retry пропускает nonce check; у Deposit/Contract/SubmitRetryable/Internal такого поля нет. Deposit/Internal также не содержат native gas limit/fee cap.

У Internal effective sender выводится как ArbOS address и помечается `DERIVED`. У SubmitRetryable исполнение обращается к ArbRetryableTx precompile с ABI calldata; это не `retry_target/retry_value/retry_data`. Сохраняются обе структуры: исходный вариант и execution projection с происхождением `NATIVE_DERIVED`. Обязательные сведения о взимании L1/L2 fees и retryable effects связываются с actions; более подробный состав `ArbitrumExecutionDetails` требует fixtures E02, не маскируется generic bytes.

Encoding различается: custom `MarshalBinary` даёт type+RLP, но pinned `UnmarshalBinary` их отвергает; native `DecodeRLP` разрешает arb parsing. Для `0x78` `Hash()` возвращает hash_override, trie encoding использует embedded legacy, а MarshalBinary — wrapper. Поэтому обязательны source hash и encoding kind; правило `keccak(raw)==hash` применяется только там, где подтверждено профилем.

Наличие `0x78` не решает extraction Arbitrum Classic. E00-15 определил отдельный native профиль ниже Nitro boundary 22 207 817, описанный далее. Его сборка, архив, observer и all-epoch decoders ещё не приняты runtime-проверками; историю от genesis нельзя объявлять полностью поддержанной.

Источники: [native structs](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/arb_types.go), [encoding and hash](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/transaction.go), [legacy wrapper](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/arbitrum_legacy_tx.go), [sender](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/arbitrum_signer.go).

## Arbitrum Classic: отдельный native origin

Исследование E00-15: [ARBITRUM_CLASSIC_CAPTURE.md](research/ARBITRUM_CLASSIC_CAPTURE.md). Classic v1.4.7 `dd4595f894b52c73a293a0c4bce9411afb12da12`, ArbOS source pin `234cf670016d675095110cd944cb82fde9c460b8`. Это Go + C++ AVM, а не ещё один Nitro/Geth tracer. Source pin не заменяет machine-code identity конкретной исторической эпохи.

`ClassicNativeTransaction`: origin=`ARBITRUM_CLASSIC_AVM`, request_id:H, avm_log_index, native_result_tx_index, optional ethereum_projection_index; provenance={l1_sequence_number:Q,parent_request_id:H,index_in_parent:Q}; sender, block/timestamp metadata, native inbox kind, optional L2 subtype, native result enum, gas/fee data, typed payload. Точные типы индексов и gas/fee bounds сверяются с native decoder в E02, без усечения до u64.

| Известный payload-кандидат | Подтверждённые native поля |
|---|---|
| ClassicUnsignedTransaction | MaxGas:Q, GasPriceBid:Q, SequenceNum:Q, DestAddress:A, Payment:Q, Data:B |
| ClassicRetryable | Destination:A, Value:Q, Deposit:Q, MaxSubmissionCost:Q, CreditBack:A, Beneficiary:A, MaxGas:Q, GasPriceBid:Q, Data:B |

Signed/contract/compressed/deposit variants и native result enums требуют отдельного закрытого mapping в E02. Значения parser constants и источники перечислены в исследовании; это не обещание применимости всех control/simulation kinds к Mainnet transactions. Raw request bytes сохраняются дополнительно, но не заменяют типизированные поля. Неподдержанный decoder означает неполное покрытие, а не generic payload, принятый как полный.

Receipt source hash для Classic — native MessageID/request_id. Normalized hash kind=`NATIVE_REQUEST_ID`; raw kinds=`CLASSIC_REQUEST_BYTES | CLASSIC_AVM_VALUE`. Ethereum projection хранится отдельно и может отсутствовать. Native retryable projection создаёт dummy LegacyTx с нулевыми полями; эти значения не становятся исходными nonce/gas/signature.

Полный source inventory читается из persisted AVM TxResult/BlockInfo log range, с сохранением связи с Ethereum projection; отдельный runtime verifier полноты не вводится. Функция FilterEthTxResults способна исключать неподдающиеся конвертации результаты; нельзя считать projected body полным inventory. Каждый persisted результат явно классифицируется; control/batch input сам по себе не объявляется транзакцией. Unknown kind блокирует COMPLETE. Исследование показывает возможность фильтрации в коде; конкретные Mainnet примеры ещё требуют fixtures.

Native GetBlockResults проверяет BlockInfo и декодирует TxResult из префикса длиной BlockStats.TxCount; остальные AVM logs не становятся транзакциями. Принадлежность результату блока задаётся этим native range, а не только IncomingRequest.L2BlockNumber: source допускает расхождение для RevertCode. Исходное поле request сохраняется отдельно, без переопределения канонического BlockRef.

Для всех calls/actions/diffs предложен native observer на уровне C++ AVM с декодированием семантики конкретной ArbOS эпохи. Нельзя переписать исторический ArbOS для добавления debug instructions и считать это тем же исполнением: machine hash, gas, logs и sends должны совпадать с baseline. Полнота observer и доступность архива остаются gate E01/AP-06, а не закрытым результатом E00-15.

## BSC / Parlia

`BscClassification = Ordinary | ParliaSystem`. Это типизированная классификация поверх фактического стандартного wire type; signature, nonce и исходный payload сохраняются. Критерий pinned клиента: recipient является system contract, effective gas price равен нулю, recovered sender равен block Coinbase. Проверяется нативным classifier на конкретном блоке. Нельзя классифицировать все zero-fee транзакции или все вызовы системного адреса как ParliaSystem.

Native `Finalize/applyTransaction` является обязательным capture path наряду с обычным циклом. Источник: [classifier и execution](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go).

## Polygon / Bor

| ID / payload | Native поля |
|---|---|
| `0x7f BorStateSync` | state_sync_data:StateSyncItem[]; StateSyncItem={id:u64,contract:A,data:B,l1_tx_hash:H} |

Тип после Madhugiri — реальная body transaction, её нельзя исключить как optional вне-транзакционный эффект. Нативный массив кодируется `0x7f || RLP(items)`, без дополнительной RLP-обёртки структуры. `TxHash` элемента — L1 transaction hash, не хеш включения Polygon.

Нет native signature/chain ID/account nonce/gas/calldata обычной транзакции. Network identity остаётся обязательной в envelope. Zero-address значения accessor не обозначают фактические execution sender/receiver: исполнение использует BorSystemAddress и настроенный state receiver; они отражаются в execution projection/calls.

До Madhugiri (Mainnet boundary 80 084 800) отдельный synthetic receipt имеет derived hash `keccak("matic-bor-receipt-" || uint64BE(block_number) || block_hash)` и позицию после обычных receipts. Он не является body transaction типа 0x7f; dummy LegacyTx из helper не превращается в настоящую подписанную транзакцию. E00-15 определяет optional `BorLegacyReceiptRecord` с origin `BOR_LEGACY_SYNTHETIC_RECEIPT`, storage receipt encoding и отдельным inventory: [исследование и схема](research/POLYGON_LEGACY_CAPTURE.md). Body transactions остаются обязательными на всей истории; optional публикация legacy records не означает optional исполнение state sync самим узлом.

Legacy source receipt содержит native status/logs, но не доказывает полноту calls/actions/diff. Writer создаёт receipt только при дополнительных logs; отсутствие записи не доказывает отсутствие system actions. Поэтому scope source receipt и scope полного system execution разделяются. Схема не обязывает включать это расширение вопреки D17.

Источники: [state sync payload](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/tx_state_sync.go), [items](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/state_data.go), [legacy receipt](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/bor_receipt.go).

## Capture engines и граница готовности

`EngineKind = EVM | STYLUS | CLASSIC_AVM | NATIVE_PROTOCOL`. Engine задаётся на frame/action: одна транзакция Nitro может переходить между EVM и Stylus; Classic исполняет EVM-семантику внутри AVM. Go hooks применяются в Geth/BSC/Nitro/Bor; Base требует Rust inspector + native executor; Classic — Go native reader + C++ observer. Canonical notification не заменяет execution capture. Для native actions без EVM frame сохраняется typed cause, а не выдуманный CALL.

E00-14 фиксирует карту вариантов и правила их представления; E00-15 дополняет её Classic origin и optional legacy Bor receipt. Компилируемые proto, точные registered network execution extensions, all-epoch decoders и fork validators — E02; native runtime-доказательства — E01/E04/E06. Эти зависимости запрещают объявлять полный протокол и всю историю поддержанными по одному source research.
