# Клиенты, стек и нативное получение данных

> D42 (2026-10-21): текущий этап — Ethereum Mainnet, BNB Smart Chain (BSC) Mainnet и Polygon PoS Mainnet. Base/Arbitrum и их адаптеры/fixtures отложены; исследования и типизированные расширения ниже сохранены справочно, не блокируют текущую версию. Исторические сценарии отложены D38. Действующие live-типы транзакций ETH/BSC/Polygon, включая native system types, остаются обязательными. У каждой сети собственные WAL и журнал Gateway.

> Актуальная граница D23–D25 (2026-10-16): live и history работают в разных pod/process; история запускается простой командой N–M. Пересечения и повторные загрузки допускают дубли. Дедупликация и независимая проверка полноты блока выполняются за пределами сборщика. Здесь проверяется доставка подготовленных сообщений: RECEIVED → WAL_SAVED → NATS_ACKED. BlockComplete/coverage report и общий координатор устранения overlap не требуются. Подробности: [LIVE_HISTORY_DELIVERY](LIVE_HISTORY_DELIVERY.md).

Проверено 2026-10-15. Результат E00-13: исследование официальных документов, release metadata и исходников пяти сетей. Код сборщика не написан, клиенты не собирались и не запускались. Ни одна строка этой матрицы не означает пройденную runtime-приёмку.

## Рекомендуемый стек первого контура

| Сеть | Клиент / срез исходников | Стек сборщика | Способ получения данных | Сопутствующие компоненты |
|---|---|---|---|---|
| Ethereum Mainnet | [Geth v1.17.8](https://github.com/ethereum/go-ethereum/releases/tag/v1.17.8), `a579077007b98217c3e253a66e4b452ca0c32b96` | Go; исходники требуют `go 1.25.0`, patch-компилятор закрепить на сборке | Встроенный live tracer, state journal и отдельное наблюдение canonical/reorg | Ethereum consensus client; конкретный клиент/версия пока не выбран |
| BSC Mainnet | [BSC v1.7.8](https://github.com/bnb-chain/bsc/releases/tag/v1.7.8), `cdb7548b5baacfdae92f9f63437d6456411665f3` | Go; директива `1.25.0`; собственная сборка BSC | Live tracer в обычном execution и Parlia Finalize/system-tx paths | Parlia внутри клиента; Ethereum consensus client не нужен |
| Base Mainnet | [base/base v1.5.0](https://github.com/base/base/releases/tag/v1.5.0), `e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832`; dependency base/reth `7134be3b51cf9d9504f988bff3266a23085f0bd9` | Rust `1.96.0`, edition 2024 | Native Base executor + Inspector + захват per-tx state/receipt; ExEx для canonical lifecycle | `base-reth-node` и `base-consensus`, L1 доступ для нативной derivation |
| Arbitrum One / Nitro | [Nitro v3.12.1](https://github.com/OffchainLabs/nitro/releases/tag/v3.12.1), `70fa99a6e711f09eedd96a907290c3df0f3bcf7d`; own Geth `5eee8f546e7602b7deeef7909f291bbebc77e233` | Go адаптер; Nitro: Go toolchain `1.25.12`, Rust `1.93.0`, cgo/C/C++/WASM зависимости | Live tracer, ArbOS special paths и Stylus HostIO, native lifecycle notifications | L1 Ethereum/blob data и sequencer feed; Classic история — отдельный runtime |
| Polygon PoS | [Bor v2.10.2](https://github.com/0xPolygon/bor/releases/tag/v2.10.2), `9c445ef603e71469e1c176d8e57e7cf94864ade7`; [Heimdall-v2 v0.12.2](https://github.com/0xPolygon/heimdall-v2/releases/tag/v0.12.2), `ced0710a3f6e269b8d5e996eb7d2b740fd9f80c5` | Go адаптер Bor; Bor go.mod/Dockerfile `1.26.5`, release note `1.26.8` — сверить при сборке; Heimdall `1.26.8` | Live tracer + state journal + отдельный StateSync/Finalize path | Heimdall-v2 и необходимые ему chain/state-sync источники |

Каждый pin — срез исследования и рекомендуемая отправная точка, не бессрочно выбранный production release. Директивы языка и заявленные toolchains не равны проверенной сборке. Nitro Dockerfile дополнительно использует Go `1.25.9` в WASM builder; все effective versions фиксируются в build manifest. Полные ссылки на manifests, code paths и ограничения: [Ethereum/BSC](research/ETHEREUM_BSC_CLIENTS.md), [Base](research/BASE_CLIENT.md), [Arbitrum/Polygon](research/ARBITRUM_POLYGON_CLIENTS.md).

Для общей части предлагается Go Gateway + protobuf/gRPC + NATS JetStream. Версии общего стека и RocksDB выбраны последующими E00-03.03/06: [COMPONENT_STACK](COMPONENT_STACK.md), [ROCKSDB_STACK](research/ROCKSDB_STACK.md). Эта source-матрица сама по себе не доказывает совместную сборку. Нативные адаптеры остаются в языке клиента. Это позволяет не переносить EVM/runtime в общий сервис и не связывать Go-форки с несовместимыми зависимостями в один бинарник.

## Что действительно подтверждено исходниками

| Данные | Ethereum/BSC/Bor | Base | Nitro |
|---|---|---|---|
| Полный состав транзакций | Native block body; дополнительные пути Parlia/StateSync обязательны | BaseTxEnvelope и native block body, включая deposits | Body с типами ArbOS, internal/deposit/retryable и обычными tx |
| Вызовы и результат | OnEnter/OnExit, tx start/end | Inspector в реально включённом execution factory | Geth tracing + ArbOS wrappers; Stylus HostIO отдельно от EVM opcodes |
| Receipts и сохранённые logs | Итоговый native receipt; сырые log callbacks могут быть отменены | Native receipt builder после исполнения, deposit-specific fields | Итоговый native receipt и execution result |
| Изменения состояния | Hooks + rollback-aware journal; raw callback delta недостаточна | Per-tx ResultAndState до commit плюс доступ к before-state/journal | State hooks плюс special ArbOS paths и rollback |
| Canonical/reorg | Отдельные internal chain events/commit points; OnBlockEnd недостаточен | ExEx committed/reorged/reverted; отдельно safe/finalized | ExecutionEngine.Reorg и own Geth chain events; отдельно L1-derived статусы |

Это доступные точки интеграции, а не готовый экспорт всех полей нашего контракта. У всех клиентов нужны собственные нормализация, журнал доставки, проверка полноты и связь исполнения с каноническим включением. Рекомендации основаны на [source audit Ethereum/BSC](research/ETHEREUM_BSC_CLIENTS.md#ethereum-пути-получения-данных), [Base capture map](research/BASE_CLIENT.md#карта-источников-данных), [Nitro/Bor capture map](research/ARBITRUM_POLYGON_CLIENTS.md).

## Последствия для строгого общего контракта

1. Общая запись обязана хранить network identity, block hash, tx index, native transaction type, inclusion/lifecycle и версию схемы. Нельзя ограничить вход обычным Ethereum decoder или фильтром успешных/value-transfers.
2. Обязательные base fields плюс закрытый реестр typed extensions. Неприменимое поле, потерянное поле и неизвестный тип — разные состояния. Конкретные схемы каждого native типа завершить до заявления поддержки.
3. Internal call остаётся частью транзакции. VM kind должен различать EVM и Stylus; HostIO не следует выдавать за EVM opcodes. Детали общего call/result/state слоя не обрезаются.
4. Проверка полноты сравнивает tx hashes/indices/types/count с родным составом блока. Исторические synthetic records Bor требуют отдельного source-kind/count, иначе они либо потеряются, либо дадут ложное расхождение с body.
5. Статусы execution/canonical/safe/finalized различаются. Speculative tracing и preconfirmation не создают окончательно включённую транзакцию сами по себе.

Это архитектурные выводы исследования, не новый пользовательский scope. Всё относится к уже принятому сбору всех транзакций до NATS; самостоятельные balance changes вне транзакций остаются optional по D17.

## Различия, которые нельзя скрывать общей схемой

**BSC.** GitHub latest API возвращал `v1.8.0-alpha`, но release notes прямо запрещают production use. Для анализа взят mainnet maintenance `v1.7.8`. В нём Parlia отдельно выполняет системные транзакции и вызывает tracing callbacks; захват только обычного tx loop неполон. [Alpha release](https://github.com/bnb-chain/bsc/releases/tag/v1.8.0-alpha), [подробный source audit](research/ETHEREUM_BSC_CLIENTS.md#bsc-обычные-и-системные-транзакции).

**Base.** Собственный `base/base` содержит Base-specific executor, deposits и будущие типы. Присутствие enum в коде не означает активацию в Mainnet. На дату проверки Denim/200ms native blocks ещё планировался для Mainnet, Everest — Planning/TBD; в документации имя будущей AA-транзакции EIP-8430, в pinned code — Eip8130. Не переносить будущие правила в исторический профиль молча. [Base audit](research/BASE_CLIENT.md#типы-и-исторические-версии), [Denim](https://docs.base.org/upgrades/denim/overview), [Everest](https://docs.base.org/upgrades/everest/overview).

**Arbitrum.** Native special transactions могут исполняться вне обычного EVM path; StartBlock/retryable/deposit обязательны. Stylus — отдельный runtime. Nitro начинается с блока 22 207 817; более ранний диапазон требует Classic. E00-15 закрепил candidate `v1.4.7`, `dd4595f894b52c73a293a0c4bce9411afb12da12`, Go + C++ AVM, и ArbOS source pin `234cf670016d675095110cd944cb82fde9c460b8`. [Classic source profile](research/ARBITRUM_CLASSIC_CAPTURE.md) определяет native AVM result inventory, отдельную Ethereum projection и проект consensus-neutral observer. Полнота state/actions, archive availability и all-epoch decoders ещё не доказаны: CL-01…08/AP-06 остаются обязательными. Это ограничение поддержки истории, а не разрешение обрезать диапазон. Official guide image v1.4.6 и source candidate v1.4.7 требуют отдельной проверки совместимости.

**Polygon.** До Madhugiri Bor state sync имеет derived hash и отдельный synthetic receipt вне body; после перехода это native StateSyncTx `0x7f`, последняя tx body. Pinned Mainnet config задаёт переход на 80 084 800. Новый тип обязателен; [legacy profile E00-15](research/POLYGON_LEGACY_CAPTURE.md) определяет optional auxiliary receipt отдельно от TransactionRecord. Source receipt status/logs не доказывают полную историю system execution; missing receipt не означает отсутствие действий. Native state-sync execution требуется независимо от публикации optional расширения. Code-upgrade attribution, archive inputs и переходы проверяются POLY-L01…07/AP-04.

## Историческое исполнение

Общий способ — отдельное последовательное native replay, начиная с доступного parent state, с теми же chain rules и адаптером. Это предлагаемая реализация, не завершённая capability. Наличие bodies/receipts не заменяет state и повторное исполнение для внутренних вызовов. Source node может потребовать долгую синхронизацию/архив независимо от того, что основной extractor по D36 экспортирует только live, а history-сервис выполняет явный N–M. [Geth archive](https://geth.ethereum.org/docs/fundamentals/archive), [Base snapshots](https://docs.base.org/specifications/node-operators/snapshots), [Nitro/Bor ограничения](research/ARBITRUM_POLYGON_CLIENTS.md).

| Сеть | Условия и дополнительные проверки |
|---|---|
| Ethereum | State/body выбранного fork; snap/known blocks не доказывают наличие captured calls; archive history должна реально существовать |
| BSC | Native BSC replay; официальная archive-инструкция описывает BSC Erigon, что не доказывает совместимость его datadir с BSC/Geth |
| Base | Archive/full/minimal snapshots имеют разный состав; проверить нужное state/history, native upgrade gates и выбранный executor |
| Arbitrum | Nitro archive и native inbox/ArbOS inputs; до 22 207 817 — отдельный Classic reader/replayer с AVM state |
| Polygon | Bor parent state/body плюс исторические Heimdall StateSync inputs; fixtures до и после Madhugiri |

History worker не должен открывать writable datadir live-ноды вторым независимым процессом. Поддержанный доступ к state, копия или отдельный executor проверяются на E01/E06. Это не финализация отложенного пользовательского интерфейса YAML/команд.

## Изоляция, нагрузка и поставка

По D19 сети независимы на уровне процессов, состояния доставки, конфигурации и прогресса. Base/Nitro имеют нативную L1-зависимость; они не должны зависеть от работоспособности процесса Ethereum extractor. Отдельно задаваемые L1 endpoints — инфраструктура клиента, а не межсетевой вызов сборщиков. Внутри одного профиля сети допускаются companion processes: например Bor + Heimdall.

Hooks не бесплатны: в проверенных исходниках Geth tracer отключает parallel BAL path, Bor tracer не использует parallel V2, Nitro tracing повторно исполняет блок. Полнота важнее предположения о нулевом overhead; задержка live и влияние replay требуют измерения. [Ethereum/BSC findings](research/ETHEREUM_BSC_CLIENTS.md), [Nitro/Bor findings](research/ARBITRUM_POLYGON_CLIENTS.md).

Upstream лицензии различаются: Geth/BSC/Bor — LGPL/GPL в зависимости от части; Base workspace — MIT; Nitro — BSL 1.1 с собственным grant. Это не отменяет open-source нашего проекта, но не позволяет обещать единую permissive лицензию всем встроенным клиентам. Способ поставки патчей/исходников/образов завершить в E00-08 до публикации. [License sources Ethereum/BSC](research/ETHEREUM_BSC_CLIENTS.md), [Base manifest](research/BASE_CLIENT.md), [Nitro/Bor license sources](research/ARBITRUM_POLYGON_CLIENTS.md#лицензии-как-свойства-upstream).

## Проверяемые следующие действия

| ID | Что делать | Граница завершения |
|---|---|---|
| E00-14 | Завершить карту native tx types → единые типы/обязательные поля и зарегистрированные extensions | Для всех пяти сетей и исторических профилей нет молчаливого generic JSON/unknown-type fallback; документирована граница каждого типа |
| E00-15 | DONE: Classic native profile и legacy Bor synthetic representation | Два исследования, source origins, ограничения и fixtures; runtime-доказательство и доступность архивов отдельно в E01/AP-06 |
| E01-01 | Собрать выбранные pinned клиенты и закрепить toolchains, submodules, image digests | Воспроизводимая сборка после разрешения на код; source audit сам её не закрывает |
| E01-02…05 | Проверить native capture, все native tx types, receipt/state/rollback, canonical linkage | Отдельные доказательства ETH/BSC/BASE/ARB/POLY, включая важные fork boundaries |
| E04/E06 | Проверить полноту действий и live/replay equivalence, state availability и overhead | Нет недоставленных обязательных записей под статусом complete; ограничения нагрузки измерены |

Подробные будущие проверки находятся в research-файлах, включая отдельные E00-15 Classic/Polygon legacy. Все fixture IDs подчинены задачам основного плана; они не помечены выполненными. E00-13/14/15 закрывают исследования и логический проект, не runtime-полноту. Для завершения E00-11 остаются окончательный стек/владение состоянием, точные гарантии, детализация контракта, управление историей и отдельный REC. Эти решения не скрыты за словами «поддержка всех сетей».
