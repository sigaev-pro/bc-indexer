# Base Mainnet: клиент и нативный сбор

Проверено 2026-10-15 по официальным документам и закреплённым исходникам. Это исследование для E00-13, не доказательство работоспособности адаптера: сборка, запуск ноды и исполнение fixtures не выполнялись.

## Рекомендация

Кандидат для первого контура — собственная сборка `base/base`, Rust-адаптер внутри execution binary `base-reth-node`; отдельный Go Gateway публикует общий protobuf-контракт. Использовать родные Base executor и chain spec, а не обычный Ethereum Reth. Это проектный вывод из зависимостей и execution path ниже, а не уже выполненная интеграция.

Официальное руководство перенаправляет операторов с `base/node` на `base/base`. В закреплённом compose два сервиса execution/consensus, entrypoint запускает `base-reth-node`, consensus script — `base-consensus`. L1 RPC нужен самой ноде для derivation; это не RPC-источник извлечения транзакций нашего адаптера. [Node guide](https://docs.base.org/base-chain/node-operators/run-a-base-node), [compose](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/docker-compose.yml), [execution entrypoint](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/etc/scripts/node/execution-entrypoint), [consensus entrypoint](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/etc/scripts/node/consensus-entrypoint).

## Зафиксированный срез

| Поле | Проверенное значение |
|---|---|
| Release, возвращённый официальным latest API | `v1.5.0`, опубликован 2026-10-14; это версия исследования, не обещание совместимости без тестов |
| Base commit | `e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832` |
| Dependency | `base/reth`, tag `base-v2.5.2.8` |
| Dependency commit в Cargo.lock | `7134be3b51cf9d9504f988bff3266a23085f0bd9` |
| Toolchain | Rust `1.96.0`; workspace rust-version `1.96`, edition `2024` |
| Workspace license declaration | MIT; это не отдельный аудит всех транзитивных зависимостей |

Источники: [release](https://github.com/base/base/releases/tag/v1.5.0), [resolved commit](https://github.com/base/base/commit/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832), [toolchain](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/rust-toolchain.toml), [Cargo.toml](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/Cargo.toml), [Cargo.lock](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/Cargo.lock). Git tag может перемещаться: воспроизводимость проверять по обоим commit, lockfile и далее digest образа, а не по слову latest.

## Карта источников данных

| Данные | Проверенный участок кода | Предлагаемая интеграция и ограничение |
|---|---|---|
| Состав блока и native transaction types | `BaseTxEnvelope` | Читать полное тело блока; тип и позиции не определять по обычному Ethereum decoder |
| Исполнение каждой tx | `BaseExecutorBuilder` создаёт `BaseEvmConfig`; она предоставляет `BaseBlockExecutorFactory` | Расширить builder/factory в собственной сборке; удерживать native fork rules |
| Call tree, inputs/outputs, revert | `BaseEvmFactory::create_evm_with_inspector`, `BaseEvm` | Свой Inspector. Важно: default `create_evm` использует NoOpInspector; при выключенном inspect callback не вызывается. Наличие Inspector trait само по себе не доказывает live capture |
| Receipt/logs и per-tx state | `execute_transaction_without_commit` → `commit_transaction` | Снять результат и state до `db_mut().commit(state)`, receipt после native receipt builder. Before-state читать на входе/из journal; один итоговый state diff не доказывает сохранность всех промежуточных действий |
| Deposits | Отдельная ветка deposit nonce/receipt version в `commit_transaction` | Сохранять родные поля, не требовать обычной подписи от deposit |
| Canonical lifecycle | ExEx: `ChainCommitted`, `ChainReorged`, `ChainReverted` | Применить нативные уведомления отдельно от capture. ExEx сам по себе не предоставляет полноценный call tree; не брать speculative/pending execution за принятую tx |

Источники: [transaction envelope](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/transaction/envelope.rs), [node builder, L852](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/execution/node/src/node.rs#L852), [ConfigureEvm, L141](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/execution/evm/src/config.rs#L141), [factory](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/evm/src/factory.rs), [EVM inspect flag](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/evm/src/evm.rs#L42), [block executor, L209–commit path](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/evm/src/executor/block_executor.rs#L209), [ExEx handling, L556](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/execution/exex/src/lib.rs#L556).

## Типы и исторические версии

В pinned envelope перечислены Legacy (0), EIP-2930 (1), EIP-1559 (2), EIP-7702 (4), Deposit (126/0x7e), Eip8130 (121/0x79). Это перечень вариантов исходного кода, а не утверждение об активации всех типов в Mainnet. Источник прямо блокирует 0x79 до Everest. [Envelope](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/transaction/envelope.rs), [type constants и activation rejection](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/transaction/tx_type.rs).

На дату проверки официальная страница Everest имеет статус Planning/TBD и называет будущие Base Transactions EIP-8430, тогда как этот код использует имя Eip8130. Нельзя выдавать несовпадение названия или присутствие variant в коде за активный Mainnet тип. Для финального профиля нужны mapping native type ID → schema и fork activation по chain config. [Everest](https://docs.base.org/upgrades/everest/overview).

Denim документирован как будущий переход к полноценным каноническим блокам каждые 200 ms; Mainnet на дату проверки — Planning November 2026, Sepolia — Scheduled October 21. Не смешивать будущие 200-ms блоки с текущими Flashblocks/preconfirmations. Тип времени в общем контракте должен переживать субсекундную нативную точность; конкретные поля проектируются в E02. [Denim overview](https://docs.base.org/upgrades/denim/overview), [native block specification](https://docs.base.org/upgrades/denim/200ms-blocks).

## История и state

Предложение: выполнять последовательный replay закреплённого диапазона на native Base executor с состоянием перед диапазоном, отдельно от live. Тот же capture path должен применяться в обоих режимах. Наличие receipts/body позволяет подтвердить состав блока, но не восстанавливает само по себе все внутренние вызовы и промежуточные действия; поэтому требуется исполнение или ранее сохранённый capture.

Официальные snapshot-профили различаются: minimal содержит latest state/headers и минимум истории; full — ограниченное окно; archive — все доступные transactions/receipts/account/storage history без pruning. Не любой скачанный snapshot позволяет исполнить произвольный старый диапазон. Возможность конкретного N–M доказывается наличием state/body и native fork semantics. [Snapshot profiles](https://docs.base.org/specifications/node-operators/snapshots).

Pinned `.env.mainnet` содержит отдельные настройки pruning для receipts, bodies и account/storage history; запуск через entrypoint передаёт RETH_PRUNING_ARGS. До реализации фиксируется профиль хранения ноды, совместимый с требуемой историей. Это ограничение источника, а не обсуждение пользовательского YAML/job interface. [Environment](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/.env.mainnet), [entrypoint](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/etc/scripts/node/execution-entrypoint).

## Что ещё не доказано

- BASE-P01: сборка pinned Base + pinned Reth с Inspector, включённым в настоящий import path; никакого silent NoOpInspector.
- BASE-P02: normal tx, failed tx, deposit, EIP-7702 и внутренний revert; полные native fee fields, before/after и компенсирующиеся действия. Проверять только активные типы выбранного fork; будущие типы — отдельные fixtures.
- BASE-P03: capture → canonical inclusion → ExEx reorg/reinclusion без двойного учёта; отдельно safe/finalized от consensus layer.
- BASE-P04: один и тот же диапазон live/import и native historical replay даёт равные записи; snapshot/pruning не скрывает отсутствие необходимого state.
- BASE-P05: выбрать окончательный путь поддержанных forks: pinned репозиторий содержит также `common/evm2`, но наличие этого каталога не означает, что именно он выбран default node builder. Проверять реально достижимый executor и upgrade gates. [Alternative executor](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/evm2/src/executor.rs).
- BASE-P06: измерить overhead Inspector и изоляцию history/live; проверить типизированные extensions на всех native fields, а не ограничиться legacy OP полями.

Все BASE-P задачи — проверки будущего E01/E04–E06 после разрешения на код. Они не выполнены этим исследованием. Самостоятельные balance changes вне транзакций optional по D17; deposits и другие родные транзакции обязательны.
