# Реестр native transaction types текущей поставки

ETH/BSC/POLY. Source profile и активные fork rules определяют применимость; наличие типа в таблице не включает его раньше native fork. Отложенные сети сохранены в исследовательских документах и истории Git.

Тематическое представление [единого ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md); изменения нормативного содержания синхронизируются с root в том же commit.

## Правила профиля

Ключ варианта: `(network identity, execution profile, native type)`. Один byte type без сети и fork profile недостаточен. Профиль содержит commit клиента, версию адаптера, fork schedule и версию нормализации. Применимость определяется нативными правилами конкретного блока, включая timestamp/height, а не текущими настройками head.

В общей записи нет обязательных для всех типов signature, account nonce, gas price или получателя. Поля располагаются в конкретном варианте `oneof`; отсутствующее по модели поле не подменяется нулём. Типизированная execution projection отделена от исходных полей. Неизвестный тип, неподдержанный fork или отсутствие обязательного поля дают явную ошибку обработки; успешная доставка такого блока не объявляется.

`Q` — UInt256; `A` — Address20; `H` — Hash32; `B` — bytes. `Target` — `Call(A) | Create`; это явное различие, не нулевой адрес. Все поля варианта обязательны, кроме явно отмеченных optional. `[]` — упорядоченная последовательность, в том числе пустая. Нативные более узкие целые дополнительно проверяются на исходную разрядность.

## Стандартные EVM-варианты

D75–D76: receipt encoding registry отдельно определён в [MESSAGE_CONTRACT](MESSAGE_CONTRACT.md). Body receipt type соответствует фактическому native type после системных подстановок: 0 → LEGACY_CONSENSUS_RLP, разрешённый ненулевой type → TYPED_CONSENSUS_ENVELOPE; Bor StateSync 0x7f сохраняет этот type byte. Optional legacy Bor storage receipt имеет отдельный BOR_LEGACY_STORAGE_RLP без native_type, не новый body transaction variant. Пустые ETH/BSC receipt details — явно typed отсутствие дополнительных полей, а не отсутствие общих Receipt fields.

| ID / payload | Поля |
|---|---|
| `0x00 EvmLegacy` | nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, signature:{v:Q,r:Q,s:Q}; replay_protection=`Unprotected \| Eip155(chain_id:Q)` |
| `0x01 EvmAccessList` | chain_id:Q, nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x02 EvmDynamicFee` | chain_id:Q, nonce:u64, max_priority_fee_per_gas:Q, max_fee_per_gas:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x03 EvmBlob` | поля EvmDynamicFee, но target только `Call`; дополнительно max_fee_per_blob_gas:Q, blob_versioned_hashes:H[] |
| `0x04 EvmSetCode` | поля EvmDynamicFee, но target только `Call`; дополнительно authorization_list:Authorization[] |

`AccessEntry={address:A, storage_keys:H[]}`. `TypedSignature={y_parity:0|1,r:Q,s:Q}` относится к внешней typed transaction. `AuthorizationSignature={y_parity:u8,r:Q,s:Q}` и `Authorization={chain_id:Q,address:A,nonce:u64,signature:AuthorizationSignature}` сохраняют исходный authorization tuple. Порядок и дубликаты source lists сохраняются. Нельзя отвергать целую включённую native транзакцию потому, что отдельный tuple имеет parity вне 0/1, неверную подпись, chain_id/nonce или неприменимую authority: native может пропустить tuple и продолжить обработку. Проверки исходной разрядности остаются обязательными; source tuple не заменяется результатом signature recovery. Валидность внешней транзакции и применимость tuple — разные проверки. [EIP-7702](https://eips.ethereum.org/EIPS/eip-7702), [выбранный Geth source](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/tx_setcode.go).

Blob sidecars не являются обязательной частью включённой execution transaction: сохраняются поля native body, blob hashes и применимый receipt; доступность blob data не подменяется обещанием полного DA-архива. Fixtures AUTH-01–04 определены в [реестре исправлений](DESIGN_REPAIR_STATUS.md); runtime-проверки ещё не выполнены.

Это библиотека вариантов, а не разрешение всех пяти типов во всех сетях. ETH/BSC исходники определяют ID 0–4; Base/Nitro/Bor используют только варианты, разрешённые их собственными правилами в данном блоке. Fork validator обязателен. Наличие типа в исходнике само по себе не доказывает активацию.

Источники: [Geth types](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/transaction.go), [BSC types](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/transaction.go).

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

Источники: [state sync payload](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/tx_state_sync.go), [items](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/state_data.go), [legacy receipt](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/bor_receipt.go).

## Capture engines и граница готовности

Current EngineKind = EVM | NATIVE_PROTOCOL. Native protocol frames требуют явного зарегистрированного source boundary; это не фиктивный EVM call. Payload сохраняет raw source encoding и профильный native type. E01 доказывает реальные capture paths закреплённых клиентов; E02 фиксирует generated schema/validators; E04 проверяет каждую применимую разновидность, включая failed и native system execution. Неизвестный тип — явная ошибка, без skip. Runtime verdict NOT_RUN.
