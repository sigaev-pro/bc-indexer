# Ethereum и BSC: проверка native clients

Проверено: 2026-10-15. Задача E00-13. Это исследование исходников и рекомендация для ТЗ, не проверка работающих нод. Граница проекта: native capture → нормализация → NATS. RPC не предлагается как источник production-выгрузки; внутренние протоколы синхронизации клиента и диагностика — отдельная роль.

## Рекомендуемый стек и закреплённые исходники

| Сеть | Рекомендация для прототипа | Проверенный snapshot | Язык/toolchain | Консенсус |
|---|---|---|---|---|
| Ethereum Mainnet | Собственная сборка Geth с Go live tracer и небольшим мостом canonical lifecycle | `v1.17.8`, `a579077007b98217c3e253a66e4b452ca0c32b96` | Go; `go.mod`: `go 1.25.0`; точный patch build toolchain ещё закрепить | Execution client + отдельный Ethereum consensus client; конкретный CL ещё выбрать |
| BSC Mainnet | Собственная сборка официального BSC client с Go live tracer, Parlia system-tx coverage и мостом lifecycle | `v1.7.8`, `cdb7548b5baacfdae92f9f63437d6456411665f3` | Go; `go.mod`: `go 1.25.0`; отдельный модуль/сборка от Ethereum | Parlia встроен в BSC client; не требуется Ethereum Beacon client |

Geth подключается к отдельному consensus client по аутентифицированному Engine API/JWT. Этот RPC — внутренний консенсусный канал, а не источник extraction. Конкретный CL и его toolchain пока не закреплены. [Официальная схема подключения](https://geth.ethereum.org/docs/getting-started/consensus-clients).

Ethereum `releases/latest` на дату проверки указывает `v1.17.8` (публикация 2026-10-08). BSC `releases/latest` указывает **`v1.8.0-alpha`**, хотя GitHub помечает `prerelease=false`. Текст релиза прямо ограничивает его разработкой/testnet и исключает production. Для Mainnet рекомендован последний проверенный maintenance-релиз `v1.7.8` (2026-08-11), а не alpha. SHA alpha: `c5533ab5b7244dc474add10740834417a2c605d7`. [Geth release](https://github.com/ethereum/go-ethereum/releases/tag/v1.17.8), [BSC stable release](https://github.com/bnb-chain/bsc/releases/tag/v1.7.8), [BSC preview release](https://github.com/bnb-chain/bsc/releases/tag/v1.8.0-alpha).

Это исходные версии для проектирования; перед реализацией нужно проверить новые обязательные обновления сети. Директива `go 1.25.0` — требование исходников, не обещание воспроизводимой сборки любым Go ≥1.25. [Geth go.mod](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/go.mod#L1-L3), [BSC go.mod](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/go.mod#L1-L3).

Оба репозитория используют module path `github.com/ethereum/go-ethereum`; сетевые интеграции следует собирать независимо. Общий wire-контракт можно разделять, но импортировать оба форка как один взаимозаменяемый пакет нельзя. В README обоих репозиториев библиотека вне `cmd` обозначена LGPL-3.0, binaries/code в `cmd` — GPL-3.0. Лицензия собственного проекта остаётся отдельным решением; наличие open-source исходников не делает все компоненты permissive. [Geth README](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/README.md#L247-L255), [BSC README](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/README.md#L297-L305).

## Ethereum: пути получения данных

**Подтверждено исходниками.** `LiveDirectory.Register` регистрирует Go tracer; `eth/backend.go` создаёт его из `VMTrace` и передаёт в `VmConfig.Tracer`. Это встроенный extension при сборке, а не внешний polling. Начальная точка адаптера — собственная регистрация рядом с примером `noop`, без копирования бизнес-логики EVM. [Регистрация](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/eth/tracers/live/noop.go#L25-L61), [подключение](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/eth/backend.go#L267-L277).

Это соответствует официальному механизму [Geth live tracing](https://geth.ethereum.org/docs/developers/evm-tracing/live-tracing); гарантии полноты нашего адаптера дополнительно проверяются тестами ниже.

| Данные | Native source | Ограничение |
|---|---|---|
| Транзакция и sender | `OnTxStart` | Идентификатор включения должен также содержать block hash и tx index |
| Итог/receipt/logs | `OnTxEnd`, receipt после исполнения | `OnLog` может видеть события до rollback; receipt хранит сохранённые логи |
| Вложенные вызовы, создание, ошибки | `OnEnter`/`OnExit`, при необходимости V2 | Нужно сохранить структуру frames и `reverted`, не только успешные value transfers |
| Balance/storage/code/nonce actions | State hooks | Простая сумма callback-дельт некорректна без rollback handling |
| Границы блока | `OnBlockStart`/`OnBlockEnd` | Завершение исполнения не равно окончательной canonical/finalized публикации |

Hook signatures и события определены в [hooks.go](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/tracing/hooks.go#L280-L321). `ApplyTransactionWithEVM` вызывает transaction hooks; receipt получает failed/success status независимо от самого наличия транзакции в блоке. Failed included transaction не должна отфильтровываться. До Byzantium receipt может не иметь современного status и содержать post-state; нормализатор должен сохранять историческую семантику, а не придумывать status. [Исполнение/receipt](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/state_processor.go#L264-L344).

**Rollback.** `StateDB.RevertToSnapshot` не генерирует обратные state callbacks сам по себе. В клиенте есть `tracing.WrapWithJournal`, который добавляет revert-события. Рекомендация: явно подключить journal либо реализовать эквивалент с тестами; action record сохраняет исходное действие, frame и признак отката, итоговые state values проверяются отдельно. `VMTrace` сам по себе не доказывает подключение journal. [Hooked StateDB](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/state/statedb_hooked.go#L140-L217), [journal](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/tracing/journal.go).

**Canonical lifecycle.** Исполнение может происходить для блоков, не ставших canonical, и повторяться. Нужен небольшой native bridge к chain/head subscriptions и safe/finalized updates, сверяющий hash/parent и публикующий типизированные lifecycle events. `OnBlockEnd` не является единственным сигналом commit/finality. Проверены `SubscribeChainEvent`, `SubscribeChainHeadEvent`, `CurrentSafeBlock`, `CurrentFinalBlock` и отдельный `reorg` path. Точный момент и порядок callback/commit ещё требуют прототипа. [Reader/events](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/blockchain_reader.go), [block import/reorg](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/blockchain.go#L2370-L2389).

**Стоимость capture.** В закреплённом Geth `supportsParallelExecution` отключает BAL parallel execution при активном tracer. Наличие hook API не означает отсутствие влияния на throughput; сравнить trace/no-trace в POC. Не утверждается, что Amsterdam активирован в Mainnet: это свойство проверенного кода. [Условие parallel execution](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/state_processor_parallel.go#L52-L71).

## BSC: обычные и системные транзакции

В обычном loop `StateProcessor.Process` вызывает `IsSystemTransaction`, откладывает системные транзакции и исполняет их позже через Parlia `Finalize`. Перехват только вызовов обычного `ApplyTransactionWithEVM` **пропускает системные транзакции**. [Разделение потоков](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/state_processor.go#L121-L181).

В проверенном `v1.7.8` проблема решаема штатными hooks: `Parlia.applyTransaction` передаёт tracer в EVM и вызывает `OnSystemTxStart → OnTxStart → OnTxEnd → OnSystemTxEnd`. При импорте `expectedTx` заменяется реальной транзакцией блока после проверки, state получает её hash/index, затем создаётся receipt с logs. Поэтому адаптер должен подключаться ко всему native block execution, включая `Finalize`, а не искусственно создавать «похожие» system transactions из логов. [Parlia execution](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go#L2111-L2214), [system hooks contract](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/tracing/hooks.go#L170-L198).

Рекомендуемое сетевое расширение normalizer: `system_transaction` с типизированной классификацией Parlia, сохраняя оригинальные tx hash/type/index/raw payload. Это настоящие транзакции блока и обязательная часть покрытия; их нельзя исключить вместе с необязательными изменениями балансов **вне транзакций**. Состав всех действий Parlia на исторических форках требует golden fixtures, не только проверки текущего tip.

Для BSC также есть `LiveDirectory`, hooked state, `WrapWithJournal`, chain/head events и отдельный reorg path. Переиспользовать общую логику нормализации можно, но signatures/version-specific integration проверяются отдельно. [Подключение tracer](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/eth/backend.go#L381-L399), [journal](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/tracing/journal.go), [lifecycle reader](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/blockchain_reader.go#L612-L635). Parlia является встроенным consensus engine и взаимодействует с системными контрактами. [Описание Parlia](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/README.md#L40-L45).

## Реестр типов и пределы утверждения «все транзакции»

В обоих закреплённых `core/types/transaction.go` decoder определяет `0x00 Legacy`, `0x01 AccessList`, `0x02 DynamicFee`, `0x03 Blob`, `0x04 SetCode`. Это **инвентаризация decoder**, не доказательство активации каждого типа во всех эпохах обеих сетей. Применимость проверяется chain config и правилами соответствующего блока. BSC system classification — дополнительная семантика, не отдельный EIP-2718 type. [Ethereum types](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/types/transaction.go#L47-L53), [BSC types](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/transaction.go#L47-L53).

Контракт должен сохранять access list, blob hashes/fee fields, authorization list для SetCode, подпись и исходный encoding. Перечень требуется сверить с отдельными native TxData structures на этапе схемы. Blob sidecar payload — отдельный предмет данных, не равен execution transaction; его постоянная доступность не доказана этим исследованием. Неизвестный будущий type должен давать явную ошибку coverage/несовместимости версии, а не тихое пропускание транзакции. «Все» здесь — все включения в блоки заданного диапазона поддерживаемой эпохи, не pending mempool transactions.

## История: native replay и pruning

Историческая детализация требует тела блока и state до него. Достаточно старый starting block без parent state не становится исполнимым от одного изменения YAML. Рекомендуется отдельный native replay worker/процесс с тем же сетевым клиентом и адаптером, с собственным исполнением и лимитами; live не переключается на старую высоту. Не открывать writable datadir работающей ноды вторым процессом; отдельная копия/поддержанный доступ к source state — предмет POC. Это способ исполнения, не выбор отложенного history-control интерфейса.

Проверенные ограничения:

- Geth `eth/state_accessor.go`: hash-scheme helper ограничивает поиск доступного предка `reexecLimit=128`; path helper возвращает ошибку при недоступном историческом state. Это поведение внутренних helper-функций, не обещание произвольного native replay. [Geth state accessor](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/eth/state_accessor.go#L41-L232).
- BSC helper для hash scheme принимает `reexec`, path helper тоже не восстанавливает отсутствующий state автоматически. [BSC state accessor](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/eth/state_accessor.go#L45-L235).
- State history, chain history и transaction lookup index — разные retention knobs. `history.state=0` означает удержание всей state history для path scheme; это не создаёт отсутствующую старую историю задним числом. Наличие ancient bodies/receipts также не доказывает наличие state для повторного исполнения. [Geth flags](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/cmd/utils/flags.go#L280-L335), [BSC flags](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/cmd/utils/flags.go#L371-L420).
- `OnSkippedBlock` существует отдельно от execution callbacks. Синхронизация/наличие уже известного блока не доказывает, что он был заново исполнен и детализирован нашим tracer. Пропущенные capture blocks должны попадать в coverage accounting, затем native replay. [Geth noop hooks](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/eth/tracers/live/noop.go#L51-L59), [BSC noop hooks](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/eth/tracers/live/noop.go#L50-L60).

Полная история от genesis — отдельная проверяемая capability конкретной source node с достаточными chain/state данными. Она не подтверждена статическим просмотром кода. Вопросы восстановления REC и управления YAML/командой здесь намеренно не решаются.

Официальная документация Geth описывает path archive через `history.state=0`, full sync и завершённое индексирование исторического state; `gcmode archive` включает индексирование. Snap-start не доказывает наличие state до стартовой точки. Это инфраструктурная предпосылка replay, а не обязательная загрузка всей истории экстрактором. [Geth archive](https://geth.ethereum.org/docs/fundamentals/archive).

Официальная BSC archive-инструкция описывает **BSC Erigon**, а не выбранный выше BSC/Geth executor. Нельзя считать её доказательством совместимости datadir или автоматической доступности истории нашему native replay. Если архивный источник будет Erigon, понадобится отдельный проверенный способ передачи блоков/state либо другой профиль исполнения; RPC tracing как production shortcut здесь не выбирается. [BSC archive guide](https://docs.bnbchain.org/bnb-smart-chain/developers/node_operators/archive_node/).

## Что доказать прототипами до production-выбора

1. Для каждого блока `captured tx inclusions = block body transactions` по hash/index, включая failed и BSC Parlia transactions; никакого фильтра `value > 0`, `receipt.status == 1` или только обычного execution loop.
2. Совпадение receipt/logs и post-transaction state на успешных, reverted, nested revert, CREATE/CREATE2/SELFDESTRUCT, fee/refund, SetCode и применимых blob cases. Нулевой итог не теряет промежуточные действия.
3. Совпадение live и replay после нормализации, с одинаковыми chain rules; выбор старых fork boundaries и BSC системных операций.
4. Повторное исполнение, side branch, reorg, skipped block, безопасное/финальное состояние не создают ложную canonical историю; порядок capture и lifecycle проверяется отдельно.
5. Нагрузка с hooks/journal/WAL против клиента без capture; историческая задача не нарушает согласованный live lag. У Geth отдельно измерить потерю parallel path при включённом tracer.
6. Предсказуемая ошибка при pruned state/body; отсутствие частичных данных не маскируется флагом «полный блок».

Фактические проверки этой работы: GitHub release metadata, immutable commit sources, Go directive, license declarations, tracing/state/lifecycle/system-tx paths. Сборки, golden fixtures, ноды, замеры производительности и проверка диапазона от genesis не запускались. Версии protocol schemas, transport bindings и WAL этим документом не выбираются.
