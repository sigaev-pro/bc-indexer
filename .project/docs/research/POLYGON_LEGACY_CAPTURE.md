# Polygon: legacy Bor receipts и нативный захват

Уточнение D75–D76 (2026-11-05): нормативный receipt registry теперь в [MESSAGE_CONTRACT](../MESSAGE_CONTRACT.md). Старое предложенное имя BOR_RECEIPT_STORAGE_RLP ниже заменено на BOR_LEGACY_STORAGE_RLP (native_type absent); alias на wire не принимается. Историческое исследование и его исходный client pin ниже сохранены; текущий pinned raw storage reader отдельно проверен и указан в нормативном контракте.

E00-15, 2026-10-15. Проверены исходники Bor v2.10.2, commit `9c445ef603e71469e1c176d8e57e7cf94864ade7`. Исследование исходников; нода, архив, replay и fixtures не запускались. Контекст: [матрица клиентов](../CLIENT_MATRIX.md), [реестр типов](../TRANSACTION_TYPE_REGISTRY.md).

## Вывод и граница D17

До Madhugiri Bor synthetic receipt не является подписанной транзакцией и не входит в body. После Madhugiri StateSyncTx `0x7f` — настоящая native body transaction. Для pinned Polygon Mainnet граница — 80 084 800; условие определяется chain config, а не датой выгрузки. Источники: [config](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/params/config.go), [Finalize](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/bor.go#L1234-L1314).

Проектный вывод: body transactions обязательны в обоих периодах; modern StateSyncTx нельзя исключать как optional. Legacy вне-транзакционные эффекты подпадают под разрешение D17 не собирать усложняющее расширение. Это не позволяет обрезать старую историю обычных транзакций или пропускать native state sync при исполнении самого узла: он нужен для правильного состояния последующих транзакций. Выключение публикации расширения и выключение consensus execution — разные вещи.

Legacy receipt при публикации получает собственный typed auxiliary record. Он не маскируется под EvmLegacy, не увеличивает body transaction count и не доказывает полноту всех вне-транзакционных изменений. Выбор включить optional расширение не считается новым принятым решением пользователя.

## Проверенные источники

| Native источник | Что действительно даёт | Чего не доказывает |
|---|---|---|
| `writeBorStateSyncLogs` | Отбирает дополнительные StateDB logs после обычных receipts; до Madhugiri сохраняет отдельный receipt и lookup entry | Полноту операций без logs, успешность каждого внутреннего вызова |
| `ReadBorReceiptRLP / ReadRawBorReceipt` | Native storage receipt bytes из hot DB или canonical freezer; декодирование ReceiptForStorage | Calldata исходных Heimdall events, call tree, balance actions, state diff |
| `DeriveFieldsForBorReceipt` | Derived hash, synthetic index, block/log metadata и bloom | Наличие подписанной transaction envelope или включение receipt в обычный receipts trie |
| `CommitStates / CommitState` | Выбор и исполнение Heimdall events по состоянию, времени, fork rules и overrides | Что произвольный нынешний endpoint содержит все необходимые старые events |
| StateDB/VM tracing во время native replay | Возможная точка захвата execution и изменений, если scope/hook forwarding проверены | Что готовый обычный tx tracer автоматически корректно обрабатывает legacy scope |

Источники: [receipt writer](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/blockchain.go#L5054-L5080), [native receipt reader](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/rawdb/bor_receipt.go#L34-L109), [derived fields](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/bor_receipt.go#L28-L76), [CommitStates](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/bor.go#L2005-L2182).

### Identity и семантика receipt

Native hash: `keccak256("matic-bor-receipt-" || uint64BE(block_number) || block_hash)`. Индекс: количество обычных receipts. Logs следуют за обычными transaction logs; cumulative gas копируется из последнего обычного receipt при его наличии. `NewBorTransaction()` создаёт dummy zero-valued legacy object, но его hash не является источником synthetic identity. Источник: [receipt helpers](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/bor_receipt.go#L18-L76).

Writer сохраняет status=success и не выводит его из результата каждого внутреннего вызова. В `statefull.ApplyMessage` ошибки EVM могут логироваться, а функция возвращает gasUsed,nil; поэтому такой receipt status нельзя нормализовать в утверждение «все system calls успешны». Источники: [writer](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/blockchain.go#L5074-L5078), [ApplyMessage](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/statefull/processor.go#L84-L136).

Writer ничего не пишет, если дополнительных logs нет. Следовательно, missing receipt может означать отсутствие сохранённого объекта, недостаточный архив либо отсутствие дополнительных logs; он не является универсальным подтверждением «никаких system actions». `ReadRawBorReceipt` возвращает nil и при пустых данных, и после ошибки декодирования; адаптер должен различать эти ситуации на уровне raw read/error checks. Источники: [writer guards](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/blockchain.go#L5054-L5068), [reader](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/rawdb/bor_receipt.go#L34-L109).

Legacy receipt является отдельным артефактом native storage. Нельзя включать его в пересчёт стандартного receipts root как дополнительную body transaction: legacy Finalize возвращает обычные receipts, а validator рассчитывает root по ProcessResult. Источники: [Finalize](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/bor.go#L1286-L1312), [validator](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/block_validator.go#L176-L214).

## Native integration, стек и ограничения

Стек — Go внутри pinned Bor: rawdb reader для source receipt; `Finalize → CommitStates → CommitState → statefull.ApplyMessage` для исполнения; VM/StateDB hooks для actions/diff. Heimdall остаётся native dependency профиля Polygon. Экстрактор не получает traces посредством production RPC. Чтение native storage происходит внутри клиента либо через его согласованный read-only snapshot; отдельный writer к открытому datadir запрещён.

Для optional execution capture нужен отдельный scope вокруг фактически применяемых state-sync events, их source ID и порядка. Старые system calls не получают post-Madhugiri `OnTxStart/OnTxEnd`: эти hooks в processor условны на наличие последнего body item типа 0x7f. Нельзя оставлять scope последней пользовательской транзакции открытым и приписывать ей system changes. Источник: [processor hooks](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/state_processor.go#L156-L196).

Граница scope должна отделять state sync, span updates и block-alloc code upgrades. Upstream Finalize выполняет их в одном пути, а комментарий отдельно предупреждает о trace attribution code upgrades. Собственный небольшой patch scope/cause может потребоваться; это задача POC, не уже готовая интеграция. Источник: [Finalize](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/bor.go#L1234-L1314).

Исторический replay требует parent state, body/header/receipts, соответствующего chain config и Heimdall events с их историческими ordering/time/override rules. В pinned `CommitStates` ошибка fetch может вернуть пустой список без ошибки наружу; нельзя принимать это за доказанно пустой inventory. Сверка итогового state root и явная диагностика native inputs обязательны перед COMPLETE. Источники: [fetch/overrides](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/bor.go#L2013-L2103), [event selection/execution](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/consensus/bor/bor.go#L2112-L2182), [state-root validation](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/block_validator.go#L206-L214).

Существующая CLI backfill state-sync entries восстанавливает storage entries из файла; это не доказательство восстановления causal execution/state diff. Наличие файла/архива у проекта не проверено. Источник: [native backfill CLI](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/internal/cli/backfill_statesync_txs.go).

## Проект типизации optional record

`BorLegacyReceiptRecord` — отдельный вариант auxiliary payload, не TransactionRecord и не raw JSON:

| Поле | Тип / правило |
|---|---|
| network, block_activation, block | Общие NetworkIdentity, BlockActivationRef, BlockRef |
| origin | Константа `BOR_LEGACY_SYNTHETIC_RECEIPT` |
| source_receipt_hash | Hash32, native derived hash с проверкой формулы |
| synthetic_receipt_index | u64; равен числу обычных receipts, не body index |
| source_receipt | Typed `{status, cumulative_gas_used, logs[], bloom}`; значения native storage и derived metadata сохраняются, status не означает результат всех calls |
| raw_encoding | `{kind=BOR_RECEIPT_STORAGE_RLP, bytes}`; не transaction envelope |
| coverage | `SOURCE_RECEIPT_ONLY`; никаких заявлений о полноте actions/state diff |

Подробный execution capture — отдельное optional расширение system operations с inventory реально исполненных событий, actions/diffs, вызовами и native causes. Оно может ссылаться на source receipt, если тот существует, но не зависит от наличия logs. Нельзя наполнять отсутствующие поля фиктивным nonce/signature/gas или выводить список всех исполненных events только из receipt.

Lifecycle использует auxiliary inclusion `(network,block_hash,origin,source_receipt_hash)`, activation_id и typed Applied/Removed/StatusChanged. Namespace и wire field numbers закрепляются в E02/E03, если расширение включается. Общие правила retry/parts/reorg действуют без изменений. В block metadata: `body_inventory` отдельно от `auxiliary_inventory`; optional coverage может быть NOT_COLLECTED. Отказ от расширения не блокирует transaction COMPLETE, но отсутствие данных для корректного native replay блокирует его.

## Runtime-проверки — ещё не выполнены

Эти fixtures уточняют AP-04 из предыдущего исследования и подзадачи `.POLY` E01-05/E02/E04/E06; ID здесь — research fixtures, не новые завершённые задачи плана.

| Fixture | Проверяемый результат |
|---|---|
| POLY-L01: блок до Madhugiri с обычными tx и legacy receipt | Body count не увеличен; derived hash/index/log order совпадают с native reader |
| POLY-L02: пустой body, отсутствующий receipt, потерянный/corrupt receipt | Пустой body отличим от missing source; corrupt не превращается в пустые данные |
| POLY-L03: несколько state-sync calls, failure/rollback и отсутствие logs | Receipt status не маскирует execution outcome; actions получают правильный scope |
| POLY-L04: блоки по обе стороны 80 084 800 | Legacy auxiliary и body 0x7f никогда не смешаны и не удвоены |
| POLY-L05: span/code upgrade вместе со state sync | Эффекты не приписаны предыдущей tx или не тому system operation |
| POLY-L06: недоступный Heimdall event, override и archive replay | Нет ложного COMPLETE; state/receipt roots и применённые inputs сверены |
| POLY-L07: overlap, reorg, повторная публикация | Стабильная identity и адресная отмена auxiliary activation |

E00-15 закрывает исследовательскую неопределённость представления. Оно не доказывает доступность Mainnet archive или исполнение всех исторических fork paths.
