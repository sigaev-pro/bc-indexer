# Arbitrum Classic: нативный historical capture

E00-15, source audit 2026-10-15. Это исследование и проектное предложение; сборка, доступность полного архива и runtime-полнота не проверены. Граница продукта: нормализованные данные → NATS. Реализация не начата.

## Вывод

Для истории Arbitrum One до Nitro нужен отдельный Classic backend: Go adapter + C++ AVM, работающий с native ArbCore. Найдены реальные пути к результатам транзакций, logs, block metadata, execution cursor и call traces. Готового доказанного capture всех account/storage before/after и причинных balance actions в проверенном интерфейсе нет. Это **не доказательство невозможности**: найдены конкретные структуры ArbOS и точки мутаций, которые можно наблюдать на уровне AVM. Такой observer ещё нужно разработать и проверить без изменения исторической программы.

До приёмки этого backend диапазон Classic имеет явный статус `UNSUPPORTED` либо `UNAVAILABLE`, в зависимости от причины. Нельзя объявлять Nitro-only загрузку полной историей Arbitrum One или молча обрезать запрошенный N–M.

## Версии и граница

| Компонент | Проверенный pin / значение | Значение для разработки |
|---|---|---|
| Classic | v1.4.7, `dd4595f894b52c73a293a0c4bce9411afb12da12` | Отдельная устаревшая кодовая база; pin кандидата, не проверенная сборка |
| ArbOS submodule | `234cf670016d675095110cd944cb82fde9c460b8` | Источник семантики accounts/calls; не доказательство, что одна версия описывает всю историю |
| Classic go-ethereum submodule | `8dd7cd4c1cb45c7286b32b553886b7ec4f79fa29` | Используется в Classic projection; не заменяется Geth/Nitro общего релиза |
| Nitro genesis | 22 207 817 | Classic: номера `< 22 207 817`; Nitro: `>= 22 207 817`; граничные fixtures обязательны |

Pins submodules взяты из [дерева Classic commit](https://github.com/OffchainLabs/arbitrum-classic/tree/dd4595f894b52c73a293a0c4bce9411afb12da12/packages), связь репозиториев — [.gitmodules](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/.gitmodules). [README](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/README.md) прямо называет stack Classic. Граница — [pinned Nitro chain info](https://github.com/OffchainLabs/nitro/blob/70fa99a6e711f09eedd96a907290c3df0f3bcf7d/cmd/chaininfo/arbitrum_chain_info.json).

## Доказанные native data paths

| Требуемые данные | Источник | Что доказано / ограничение |
|---|---|---|
| Исходные execution results | `ArbCore.GetLogs` → native AVM values → `TxResult` / `BlockInfo` | Можно читать исходную последовательность AVM logs; это не EVM event logs |
| Inbox inputs | `ArbCore.GetMessages` | Есть native reader сохранённых входных сообщений; полнота конкретного архива не проверена |
| Блоки и позиции | `BlockInfo.BlockNum`, `BlockStats`, `ChainStats`, `FirstAVMLog`, `LastAVMLog`; `TxResult.TxIndex` | AVM log index, execution index и позиция Ethereum projection — разные поля |
| Receipt details | `TxResult` | `ResultCode`, `ReturnData`, `EVMLogs`, `GasUsed`, `GasPrice`, `CumulativeGas`, `StartLogIndex`, `FeeStats` |
| Calls / create / returns | `AdvanceExecutionCursorWithTracing` и `evm/tracing.go` | Native replay возвращает debug emissions; parser восстанавливает call trace |
| Native machine snapshot | `GetExecutionCursorAtEndOfBlock` → `TakeMachine` | Есть путь к состоянию AVM; успешность зависит от checkpoint/history и slow lookup |
| Balance/nonce/code/storage одного известного адреса/слота | `snapshot.GetBalance/GetTransactionCount/GetCode/GetStorageAt` | Внутренние вызовы ArbOS на snapshot; не готовый enumerable state diff |

Источники: [ArbCore](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-avm-cpp/cmachine/arbcore.go), [TxResult](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/result.go), [BlockInfo](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/blockresult.go), [snapshot getters](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-rpc-node/snapshot/snapshot.go#L506-L547), [TxDB snapshots](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-rpc-node/txdb/txdb.go#L541-L561).

Использование внутренних функций не означает вызов JSON-RPC. Adapter размещается в native процессе/replay worker. Snapshot diagnostic calls исполняются на отделённой копии машины; их результаты и emissions не должны попасть в source inventory как реальные транзакции.

## Ловушка полноты: Ethereum projection фильтрует результаты

`TxDB` сначала получает `TxResult`, затем строит видимый Ethereum block через `FilterEthTxResults`. Фильтр пропускает результат, если `GetTransaction` не смог представить его как Ethereum transaction. `GetTransaction` обрабатывает Retryable отдельно, принимает L2/deposit kinds и отвергает другие kinds / non-convertible messages. Следовательно, брать только получившееся Ethereum body как полное множество native результатов нельзя. Это вывод из кода; наличие конкретного пропущенного production result ещё требует fixture. [Filter/GetTransaction](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/incoming.go#L21-L85), [TxDB projection](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-rpc-node/txdb/txdb.go#L305-L321).

Предложение: полноту Classic проверять по original AVM result range и `BlockInfo`, отдельно сверяя Ethereum projection. Каждый persisted `TxResult` должен получить явную классификацию и запись либо документированное основание, почему это control/diagnostic record вне transaction scope. Failed result не исключается из-за неуспеха. Inbox envelope/batch/control message не превращается автоматически в одну транзакцию: один input может породить несколько результатов. Неизвестный kind — ошибка/неполное покрытие, не silent skip.

Точная проверенная связь: `GetBlockResults` читает `machine.BlockInfo.InitialLogIndex()` и `LogCount + 1` AVM records, декодирует последний как `BlockInfo`, сверяет block number/AVMLogCount, а первые `BlockStats.TxCount` records передаёт в `processBlockResults`, где требуется тип `TxResult`. Unsafe helper читает `FirstAVMLog()` + `TxCount` непосредственно. Поэтому transaction inventory — эти `TxCount` результатов, а не все AVM logs; остальные logs классифицируются отдельно. В `processBlockResults` для `RevertCode` есть исключение из проверки `IncomingRequest.L2BlockNumber == BlockNum`: принадлежность и порядок следует брать из authoritative block log range, не только из поля request. [Native range checks](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-rpc-node/txdb/txdb.go#L113-L165).

Native `ResultCode` шире success/revert: в pin есть congestion, insufficient gas/tx funds, bad sequence, invalid format, gas-limit, retryable auto-redeem и другие коды. Нормализованный статус дополняется строгим native result enum; исходный код сохраняется. [ResultType](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/result.go#L37-L56).

## Почему call trace недостаточно для полного state/actions

Trace frontend разрешает `trace` и `deletedContracts`; `stateDiff` не реализован этим API. Native trace получается через cursor, а ArbOS `evmTracer_emit` выдаёт debug tuple с discriminator `20000`. Trace описывает calls/create/return/revert, но не гарантирует полный список записей storage, fees, escrow и промежуточных balance mutations. [Trace replay](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-rpc-node/web3/trace.go#L276-L365), [trace structs](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/tracing.go), [ArbOS tracing](https://github.com/OffchainLabs/arb-os/blob/234cf670016d675095110cd944cb82fde9c460b8/arb_os/tracing.mini#L21-L54).

Два snapshot чтения до/после дают net difference только для заранее известных адресов/слотов. Они не находят все touched keys и не восстанавливают действия `+10, -10` при нулевом итоге. Поэтому getters полезны для проверки observer, но не являются достаточным capture engine.

Конкретные semantic seams найдены в ArbOS:

| Семантика | Точки в pinned source |
|---|---|
| Account lifecycle / nonce / code | `accountStore_set`, `accountStore_destroyAccount`, `account_checkAndIncrSeqNum`, `account_setNextSeqNum`, `accountStore_createAccountFromEvmCode`, `accountStore_upgradeContractFromEvmCode`, `account_setContractInfo` |
| Balance transfer | `account_addToEthBalance`, `account_deductFromEthBalance`, `accountStore_transferEthBalance` |
| Escrow / fee / retryable funding | `accountStore_newEscrow`, `_accountStore_addToEscrow`, `accountStore_payFromEscrow`, `accountStore_payFullyFromEscrow`; callers determine cause |
| Storage writes | `account_setStorageCell`, `account_setStorage`, `evmCallStack_setTopFrameStorageCell` |
| Rollback / context | `evmCallStack_returnFromCall_impl`, per-frame `accountStore`, `evmCallStack_setAccountInCurrentContext`, `evmCallStack_selfDestructCurrentAccount` |

Источники: [accounts.mini](https://github.com/OffchainLabs/arb-os/blob/234cf670016d675095110cd944cb82fde9c460b8/arb_os/accounts.mini), [evmCallStack.mini](https://github.com/OffchainLabs/arb-os/blob/234cf670016d675095110cd944cb82fde9c460b8/arb_os/evmCallStack.mini), [gasAccounting.mini](https://github.com/OffchainLabs/arb-os/blob/234cf670016d675095110cd944cb82fde9c460b8/arb_os/gasAccounting.mini).

Rollback здесь нельзя свести к немедленной записи в глобальное состояние: при успешном non-static return дочерний `accountStore` переносится в родителя; при ошибке такого переноса нет, хотя storage allocation accounting переносится. Observer должен хранить frame scope и committed/rolled-back outcome. [Native rollback](https://github.com/OffchainLabs/arb-os/blob/234cf670016d675095110cd944cb82fde9c460b8/arb_os/evmCallStack.mini#L907-L919).

## Предлагаемый способ реализации, ещё не доказанный

1. Native reader перечисляет original AVM results и block metadata. Отдельный replay worker получает execution cursor из сохранённого checkpoint и воспроизводит точные historical inputs.
2. Существующие debug emissions дают call tree. Для полного write/action capture добавить observer в C++ AVM host: кандидаты `MachineState::runOne/runOp`, доступ к machine values и version-specific decoder semantic account/frame changes. ArbOS source выше — карта семантики, не готовый stable hook API.
3. Observer сохраняет account/storage before/after и последовательность причинных действий; fee/escrow/deposit/retryable причины связываются с native request/frame/phase. Внутренний escrow — отдельный typed native entity, не выдуманный EVM address.
4. Сравнивать observed replay с исходной машиной: machine hash, gas, inbox progress, log/send accumulators и output. Только при совпадении и fixture coverage разрешать `COMPLETE`.

Источники точек host: [runOne/runOp](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-avm-cpp/avm/src/machinestate/machinestate.cpp#L472), [DEBUG_PRINT emission](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-avm-cpp/avm/src/machinestate/machineoperation.cpp#L885-L889), [cursor hashes](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-avm-cpp/cmachine/executioncursor.go#L146-L159).

**Нельзя просто пересобрать historical ArbOS с дополнительными debug инструкциями и назвать результат canonical replay.** Меняются программа/машинное состояние и потенциально gas/hash. Предложение требует наблюдения исходной программы без изменения её consensus semantics. Наличие исходников нужных исторических ArbOS versions, decoder maps и охват всех mutations остаются проверками E01. Один современный ArbOS submodule pin не закрывает все epochs.

## Предложение строго типизированного Classic payload

Отдельный source origin `ARBITRUM_CLASSIC_AVM`; не Nitro `0x78` и не generic unsigned Ethereum legacy. Network identity остаётся Arbitrum One, а profile и engine — Classic AVM.

| Группа | Поля / смысл |
|---|---|
| Identity | `request_id:Hash32` из `IncomingRequest.MessageID`; native AVM log index; native result tx index; block reference; optional Ethereum projection index |
| Provenance | `l1_sequence_number:UInt256`, `parent_request_id:Hash32`, `index_in_parent:UInt256`; sender, L1 block number, L2 block number/timestamp |
| Native request | Strict inbox-kind enum, optional L2-subtype enum, exact request bytes; typed payload oneof |
| Execution | Native result enum, returndata, gas/fee fields, EVM logs, calls, typed actions/state changes, capture completeness |
| Historical profile | ArbOS version / machine-code identity and adapter decoder version; no implicit claim that latest source handles old epochs |

Request/provenance structs: [request.go](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/request.go#L162-L166), [IncomingRequest](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/request.go#L279-L291). Receipt `TxHash` берётся из MessageID, не универсально вычисляется как `keccak(raw request bytes)`: [receipt construction](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/result.go#L311-L350).

В pin inbox kinds: L2=3, old-init=4, end-block=6, ETH-deposit=7, retryable=9, gas-estimation=10, init=11. L2 subtypes: transaction=0, contract-transaction=1, call=2, batch=3, signed=4, heartbeat=6, compressed-ECDSA=7. Это перечисление parser constants, **не утверждение, что все являются включёнными транзакциями Mainnet**. Control/simulation kinds требуют отдельной классификации; подписанный payload остаётся подписанным, unsigned/deposit/retryable не получают фиктивную подпись. [Kinds](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/message/message.go#L34-L42), [L2 subtypes/payloads](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/message/l2Message.go).

Candidate payloads: `ClassicUnsignedTransaction` (MaxGas, GasPriceBid, SequenceNum, DestAddress, Payment, Data), `ClassicContractTransaction` (native BasicTx), `ClassicSignedTransaction`, `ClassicCompressedECDSA`, `ClassicRetryable` (Destination, Value, Deposit, MaxSubmissionCost, CreditBack, Beneficiary, MaxGas, GasPriceBid, Data). Deposit origin сохраняется отдельно от вложенного payload. Точные field numbers, all-epoch variants и ограничения — E02 после fixtures; generic bytes не заменяют typed known fields. [Retryable fields](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/message/retryable.go#L11-L20).

Особенно важно: `GetTransaction` создаёт для retryable синтетический `LegacyTx` с nonce/gas/gasPrice=0 и вызовом ArbRetryable. Эти нули и этот calldata — projection, не исходная подписанная транзакция. Их нельзя использовать вместо native retryable payload. [Projection](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-evm/evm/incoming.go#L24-L39).

## Исторические inputs и что остаётся неизвестным

Для native replay требуются совместимая Classic database с AVM checkpoints/machine code, original inbox messages до выбранного конца и native outputs/indexes для сверки. Конкретный набор, retention и earliest replayable height проверяются на выбранном архиве. Наличие receipt либо Nitro archive не доказывает наличие Classic execution state.

В Classic config есть checkpoint pruning и `allow-slow-lookup`; при его включении код отключает checkpoint pruning. Однако это не восстанавливает уже утраченные checkpoints/messages и не доказывает доступность полного диапазона. [Configuration](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-util/configuration/configuration.go#L809-L817), [native history access](https://github.com/OffchainLabs/arbitrum-classic/blob/dd4595f894b52c73a293a0c4bce9411afb12da12/packages/arb-avm-cpp/cmachine/arbcore.go#L229-L270).

Официальная инструкция указывает Classic image `offchainlabs/arb-node:v1.4.6-551a39b3` и archive `classic-archive.tar`; это **другой release**, чем source-audit pin v1.4.7. Инструкция объясняет, что Nitro database содержит также pre-Nitro raw blocks, но Nitro не исполняет Classic transactions. Для Classic node требуется отдельный Ethereum L1 endpoint (`--l1.url`), chain ID 42161 и persistent volume. Это native-node dependency, а не разрешение строить extractor на JSON-RPC. [Официальное руководство Classic](https://docs.arbitrum.io/run-a-node/more-types/run-classic-node), [ссылка на archive из руководства](https://snapshot.arbitrum.foundation/arb1/classic-archive.tar).

Не проверены: успешная загрузка полного архива и его checksum, opening/migration compatibility v1.4.6 archive с v1.4.7 pin, исторические ArbOS program versions, размер/скорость replay и полный source→projection mapping. Нужен отдельный pinned fixture manifest; в этом исследовании архив не скачивался, node не запускался.

## Обязательные будущие проверки

Все пункты ниже **не выполнены**; это acceptance inputs E01/E02, а не дополнительное разрешение на реализацию.

| ID | Проверка | Критерий |
|---|---|---|
| CL-01 | Pin compiler/toolchain/submodules; открыть verified Classic archive | Записаны archive provenance/checksum, earliest/latest native inputs и replayable bounds |
| CL-02 | Source inventory против filtered projection | Каждый TxResult классифицирован; ни один conversion error не приводит к silent skip; block counts/positions согласованы |
| CL-03 | Signed/unsigned/deposit/retryable success и failure | Native identity/payload/outcome сохранены; нет фиктивной подписи и projection nonce |
| CL-04 | Native cursor calls/create/selfdestruct/revert | Calls и logs совпадают с original results; diagnostic replay outputs исключены |
| CL-05 | AVM observer: zero-net transfers, fees, escrow/refunds, storage reset/write, nested rollback | Полная ordered action history и committed before/after; никаких изменений machine hash/gas/log/send outputs |
| CL-06 | Несколько исторических ArbOS epochs и code upgrades | Каждый epoch имеет проверенный decoder; неизвестный epoch не получает COMPLETE |
| CL-07 | Последний Classic / первый Nitro / пересекающий границу диапазон | Нет дыр/двойного счёта; единый Arbitrum network, разные source profiles |
| CL-08 | Missing archive/checkpoint/input и pruning | Явный failure/coverage gap; live Nitro продолжает работу независимо от Classic history job |

Исследование устанавливает маршрут реализации и конкретные риски. Полная поддержка Classic остаётся неподтверждённой до CL-01–CL-08.
