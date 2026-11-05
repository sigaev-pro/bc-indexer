# Native capture, state retention и lifecycle recovery

Exact current data/finality/GC semantics — [CURRENT_SEMANTICS](CURRENT_SEMANTICS.md), D62/D63. Semantic identity и связь decision→batch — [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md). GC не использует generic native finalized getter без provenance.

2026-11-05 · DOC-REPAIR-03 · D57/D58. Нормативное уточнение [архитектуры](COLLECTION_ARCHITECTURE.md), [модели изменений](BALANCE_MODEL.md) и раннего описания capture в [RocksDB protocol](ROCKSDB_DELIVERY_PROTOCOL.md). Это проект native patches; наличие подходящих upstream API не доказывает работу будущей сборки. E01/E04/E05 остаются обязательными gates.

## Поддержанный профиль и владельцы

Первый профиль: обычный последовательный native import, hash state scheme и durable pin provider. Path backend до отдельного доказательства восстановления не поддерживается. Полный архив не требуется. Любая настройка pruning/сжатия/замены state, способная удалить данные живого pin, несовместима; preflight проверяет effective native config до нового capture. Нельзя молча включить менее надёжный режим.

| Клиент / source commit | Execution seam и дополнительные пути |
|---|---|
| Geth `9dffe6e976cbbb87a84c5fd395448f0950290def` | До `processor.Process`; `EXEC_READY` после native validation, до commit state/head; Engine forkchoice/SetCanonical также проходят decision bridge |
| BSC `cdb7548b5baacfdae92f9f63437d6456411665f3` | Import seam и `Parlia.applyTransaction`, включая actualTx substitution, nonce/finalise; отдельные native finality setters |
| Bor `a16f123d9b4f41a552a3a98d93a0340430838576` | Активный `processBlockForImport → ProcessBlock`, обычный commit; canonical/whitelist paths, StateSync и native Heimdall resolver |

Bor baseline отклоняет `EnablePipelinedImportSRC`, `EnableParallelStatelessImport`, `EnablePreconfs` и принудительный parallel processor. Проверяются effective config и реальные execution paths, не только названия CLI flags. `WaitForPipelinedStateCommit` не используется как durability barrier: выбранный source возвращает nil также по timeout. [Bor blockchain.go](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/blockchain.go).

D60 отдельно отключает BlockSTM/parallel EVM: `--parallelevm.enable=false --parallelevm.enforce=false`. Это не автоматическое следствие D58; пользователь подтвердил последовательное исполнение транзакций. Спекулятивные parallel attempts не входят в первый capture-профиль.

Один observation guard сериализует операции данной ноды, меняющие capture/canonical/finality state. Порядок locks: observation guard → native locks. Вложенный native вызов передаёт guard token, не захватывает его повторно. Worker не берёт native locks и не читает mutable VM/StateDB; он владеет только переданными immutable chunks. Полная карта callers и проверка deadlock входят в E01, нельзя считать обычный chain event feed достаточным.

## Admission и source pin

`observation_id=(producer_id,run_id,observation_sequence)` относится к native наблюдению, а не к transport batch. Observation counter выделяется durable; отсутствие публичных событий у rejected/side-branch observation не создаёт дыр в delivery sequence. Последний выделяется отдельно по [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md).

1. На native boundary проверить block/profile и необходимые до исполнения native условия. Начать `INPUT_PREPARING`; сетевой кандидат ещё не считается принятым capture.
2. Защитить parent root от native удаления. Для hash provider: удержать root в trie cache, выполнить `triedb.Commit(parentRoot,false)`, затем `nativeDB.SyncKeyValue()`. RAM `Reference` без flush/sync не является durable pin.
3. Сохранить pin ledger и input capsule фрагментами. Закрывающий sync `ADMITTED` с manifest/digest разрешён только после durable native state и inputs. До ADMITTED capture hooks/Process не выполняются.
4. Pin содержит native_store_id, chain/genesis, source/profile, state scheme, parent hash/root, обязательные header/code/trie/consensus input references. Pruning/delete paths обязаны учитывать ledger. После освобождения pin физическое удаление не обязано быть мгновенным; доступность старого состояния больше не обещается.

Сбой до ADMITTED допускает уборку только неопубликованного INPUT_PREPARING после сверки ссылок. Сбой после ADMITTED сохраняет обязательство завершить/разрешить наблюдение. Native datadir с другой identity при сохранённом WAL — ошибка, не новый H. Неподдерживаемый provider не заменяется вызовом `Journal`: path Journal предназначен для shutdown и в выбранных fork может переводить store в read-only. API: [Geth triedb](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/triedb/database.go), [SyncKeyValue](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/ethdb/database.go).

Input capsule разделена на sealed static-input manifest при ADMITTED и append-only native dependency log. Static manifest: исходные header/body/encodings, parent root/hash, chain config/fork digest, native commit и normalization profile, заранее известные ancestor headers/BSC snapshot dependencies. Log: request sequence, exact request digest, response chunks/digest и durable consumed frontier. Итоговый consumed-input manifest/digest закрывается при EXEC_READY; именно на него ссылается native decision marker. ADMITTED не объявляет ещё неизвестные внешние ответы уже сохранёнными. Внутренние offsets: observation_sequence, attempt_no, raw_record_sequence, raw_chunk_sequence, exec_ready_digest, native_decision_sequence; они не заменяют event/source/publication sequences.

Bor recording resolver сохраняет exact request identity и exact response/digest sync **до передачи результата native execution**. Повтор уже использованного запроса читает сохранённый ответ, не актуальный новый ответ Heimdall. Ещё не завершённый запрос после crash может требовать доступного Heimdall; полностью offline replay всех ADMITTED не обещается. Нет обязательного ответа — нет EXEC_READY; retry либо явная source error, без пропуска. Запрос зависит от native state, поэтому универсальный prefetch только из header не предполагается. [Bor Finalize/CommitStates](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/bor.go).

При retry request sequence начинается с первого логического запроса observation. Сохранённый prefix проверяется по exact request identity; несовпадение не разрешает подставить другой ответ под прежний offset. Новый ответ append разрешён только за durable frontier. Crash после записи ответа до его передачи безопасно повторяет тот же ответ; поэтому log frontier отражает разрешённую передачу, а final manifest подтверждает фактическую последовательность завершённого execution.

## Durable состояния и native decision bridge

| State | Условие закрытия | Восстановление |
|---|---|---|
| INPUT_PREPARING | Capsule/pins ещё неполны | Проверить ownership; завершить подготовку либо убрать безопасные orphan fragments |
| ADMITTED / EXECUTING | Source pin и input manifest durable | Новая capture attempt из тех же inputs; старый source не освобождать до durable замены |
| EXEC_READY | Полный immutable normalized record corpus, receipts и snapshots, native validation прошла | Сохранить corpus; разрешить/восстановить native decision; native-only replay допустим для утраченного ещё не committed native state |
| NATIVE_DECIDED | Native authoritative marker durable | Идемпотентно создать lifecycle transition и delivery intent |
| DELIVERY_STAGING / READY | Registry/revisions и closing READY согласованы | Доставка по своему автомату; native execution не повторять |
| NATIVE_REJECTED | Авторитетный native rejection, включая validation failure | Не создавать Applied; закрыть observation и освободить неиспользуемые pins |
| FATAL_CAPTURE_ERROR | Нельзя безопасно сохранить/нормализовать данные | Остановить native процесс; сохранить восстановимые данные, не выдавать consensus-invalid |

EXEC_READY содержит **record corpus без публичной activation/event identity**, а не уже опубликованный Envelope. Спекулятивный branch не публикуется сам по себе. Только native canonical/lifecycle решение определяет activation, revisions и public events. При повторном применении той же branch возможен reuse corpus с новой activation.

Canonical decision сначала закрывает semantic descriptors: activation refs/revisions/source_sequence/kind и stable entity ordinals. Decision→batch allocation atomic; только затем DELIVERY_STAGING выделяет event IDs и строит Envelope. Retry этой стадии не меняет semantic IDs/revisions; повтор native marker находит прежний batch либо retained applied-decision frontier.

Crash после EXEC_READY до native state commit может уничтожить mutable StateDB. Corpus не заменяет полное native state: optional system scopes могут в нём отсутствовать. Для завершения штатного import разрешён native-only replay из parent pin и сохранённой capsule; он не заменяет corpus и не создаёт новые public IDs/bytes. Сравниваются profile/input digest, native resulting state root/receipt commitments и применимые captured outcomes. Несовпадение — source invariant error. READY/SEALED transport replay сам по себе native execution не требует.

Перед authoritative native decision marker выполняется durable child-root barrier: штатный StateDB commit, flush получившегося trie root через поддержанный hash provider и nativeDB.SyncKeyValue. Sync только KV metadata не сохраняет ещё не выгруженный trie cache. Пока child root/decision не durable, parent pin остаётся для native-only replay; при READY transport bytes никогда не регенерируются даже если отдельное native recovery требует исполнения. Этот порядок проверяется power-loss fixture без сохранённой RAM.

Перед native canonical/finality mutation адаптер sync сохраняет transition intent. Native patch добавляет в завершающий authoritative native DB batch marker `{native_store_id,decision_sequence,observation_id,old_head,new_head,decision_kind,evidence,input_digest,exec_digest}` и выполняет SyncKeyValue. После этого адаптер строит ограниченными фрагментами mirror: activation refs, revisions, Removed/Applied/Status descriptors и delivery payload. Closing sync batch атомарно делает новую registry generation и READY видимыми; большой reorg не обязан помещаться целиком в WriteBatch. После durable mirror native marker удаляется отдельным sync шагом.

Повтор marker/intent узнаётся по decision_sequence и digest и не создаёт вторую revision. Адаптер хранит applied-decision frontier; уборка payload по ACK этот frontier не удаляет. Сбой после READY до удаления marker вызывает проверку уже завершённого mirror. Для decision без публичных событий всё равно фиксируется durable decision checkpoint; пустой transport batch не требуется.

Многошаговая native перестройка canonical mappings до конечного marker не считается завершённым решением. Startup сначала выполняет штатное native DB recovery; затем сопоставляет pending intent, восстановленный head/finality и marker. Отсутствие marker не доказывает успех: bridge либо завершает допустимую native операцию из intent, либо фиксирует её native rejection. Если исход нельзя однозначно установить по native данным, остановка с source invariant error; запрет угадать новый head и забыть intent. Атомарность конечного native decision/marker для каждого caller — обязательный integration gate E01/E05.

## Owned capture и bounded RAM

D74: единый tx-local execution_order и identity создания rollback scope фиксируются на синхронных native boundaries до передачи owned snapshots worker. Scope ledger связывает каждое создание native snapshot с уникальным нормализованным scope_id и parent; повтор native ID не находит завершённую область как новую. Worker не восстанавливает chronology по очереди завершения записи на диск или равным amounts. Transfer callbacks объединяются только по доказанному invocation identity; SELFDESTRUCT projection получает order своего opcode. Raw spool сохраняет entry/operation/snapshot/revert связи, достаточные для нормализации по CURRENT_SEMANTICS. При recovery того же capture corpus логические order/IDs/scopes воспроизводятся и сверяются с durable prefix; native journal restore не считается новым денежным действием. Неопределённый порядок/область обязательного эффекта — normalization error до EXEC_READY/READY, без silent fallback. Сбор этих метаданных не меняет native результат или правило D58 о безопасных паузах VM.

Hooks не выполняют дисковые/сетевые операции, не ждут worker и не передают borrowed pointers за пределы callback. Native safe point **до hook/до mutation** подготавливает owned snapshot. Пока исходный VM view стабилен, крупное поле копируется ограниченными chunks; между chunks runner может ждать writer по D58. Пауза не меняет native результат и не выполняется внутри hook. Native heap/state cache — отдельная память самой ноды; bounded collector RAM не означает ограничение общего VM heap.

Raw spool: BEGIN_FIELD/CHUNK/END_FIELD, sequence, length, digest. Worker обрабатывает только owned chunks. Нет полной копии всей транзакции, manifest или одного большого byte field в памяти сборщика. Общие chunk/control/WriteBatch границы заданы в автомате доставки. Для before/touched/parent indexes используются disk-backed keys и ограниченные страницы.

| Наблюдение | Точка чтения / правило |
|---|---|
| existence и initial account values | Explicit pre-mutation bridge перед первым CreateAccount/CreateContract/balance/nonce/code/storage/selfdestruct mutation; первый before по raw sequence |
| storage/code bytes | Snapshot до соответствующего изменения, chunks owned; не читать позднее изменившийся StateDB из worker |
| rollback | Native snapshot/revert scope IDs и execution order; attempted actions сохраняются, committed flags вычисляются по scopes |
| tx after | После native tx Finalise, до следующей tx/system scope; native thread перечисляет touched keys страницами и читает конечный StateDB |
| deletion | Отдельно fork eligibility, native finalization и итоговый exists; нулевой balance/code не заменяет exists |
| EIP-7702 | Authorization processing входит в tx scope до root call; surviving authorization changes не отменяются только из-за root revert |

Существующие balance/code hooks недостаточны: [Geth statedb_hooked](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/state/statedb_hooked.go) делегирует создание account без полного нужного bridge. Патчи должны покрывать также finalization deletion и no-op/empty account cases. Самостоятельный production completeness verifier этим не вводится.

## Bor/BSC boundaries и receipt provenance

Bor сохраняет native порядок: `SYSTEM_PRE(checkAndCommitSpan) → TX_STATE_SYNC(CommitStates) → SYSTEM_POST(block-alloc changeContractCodeIfNeeded) → receipt construction`. Before/after обязательной StateSync tx снимаются вокруг CommitStates, **after до block-alloc**. Receipt присоединяется после штатного формирования. Native операции не переставляются; optional публикация outside-tx scopes не влияет на их native исполнение. Source receipt bytes/logs сохраняются точно; protocol-origin logs при необходимости имеют отдельный typed Bor provenance, а не переписанный receipt. [Bor source](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/bor.go).

BSC system tx capture использует реальные hash/index/payload после actualTx substitution в Parlia.applyTransaction. Final after берётся после native nonce/finalise, до выхода из tx boundary. [Parlia source](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go).

| Поле projection | Источник и различие |
|---|---|
| effective_gas_price | Native Message.GasPrice и активные fee rules; не неинициализированное поле receipt struct |
| gas_used / cumulative | Native receipt execution accounting; не сумма guessed call gas |
| execution failure/revert | Native root execution result + receipt status; Go callback err не единственный признак EVM failure |
| attempted CREATE address | Execution attempt, может существовать при failed constructor |
| created contract | Только успешное native создание; отдельно final existence после rollback/SELFDESTRUCT |
| source receipt | D75–D76: body consensus bytes по native MarshalBinary byte contract, полученные из исходного native Receipt потоковым byte-equivalent writer в spool; storage/typed outer RLP wrapper не подменяют этот формат. Derived projection сохраняется отдельно |

[Geth state_processor](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/state_processor.go) — источник makeReceipt; публичные поля задаёт [MESSAGE_CONTRACT](MESSAGE_CONTRACT.md).

## Startup, known branch и lifecycle GC

До нового import: compatibility preflight → native/adapter store identity → установка hooks/decision bridge в RECOVERY_ONLY → local recovery ADMITTED/EXEC_READY/decision markers → reconciliation сохранённых activations с native canonical/finality. В RECOVERY_ONLY разрешены сохранённые obligations, но не новые live admissions. READY delivery идёт независимо от новой синхронизации. После initial native sync под observation guard завершаются текущие mutation, читается H/hash, sync сохраняется live boundary и admission переключается в LIVE для H+1. Hooks уже установлены; sync до boundary не создаёт публичный capture. Нельзя поставить H двумя асинхронными подписками с окном между ними. DRAIN остаётся RECOVERY_ONLY.

Known block/side branch может стать canonical без execution callback. До подтверждения canonical transition bridge проверяет наличие соответствующего corpus; при отсутствии выполняет targeted native replay нужной ветки с удержанными inputs/state. Это исключение для live branch/recovery из v2, не history CLI и не сканирование downtime gap. Недоступные необходимые inputs/state дают source error, не silent skip/new H. Данные ветки должны быть закреплены до native операции, которая сделает их необходимыми для принятого transition; pin после pruning не восстанавливает потерянное.

При restart сверяются только сохранённые obligations/activation refs и текущая native evidence. Новые данные периода простоя не разыскиваются. Finality change обрабатывается и без следующего block event. DRAIN восстанавливает прежние obligations и доставку; новый live boundary/admission не создаёт.

ACK payload не удаляет lifecycle refs, revisions и нужные replay pins. GC требует durable terminal evidence (включая окончательную native finality для активной записи), отсутствия связанных pending transitions/replay/delivery и irreversible anchor поддержанного native profile. Одного Removed без такого anchor недостаточно для уборки всех branch dependencies. При отсутствии anchor refs/pins сохраняются; TTL/произвольное число confirmations не подставляются. Regression ниже уже принятого irreversible anchor — source invariant error. После закрытия pin replay именно этого старого corpus больше не обещается.

## Проверяемые gates

| Fixture family | Обязательное доказательство |
|---|---|
| CAPTURE-01 | Power loss на каждом pin/capsule/ADMITTED/EXEC_READY/native marker/READY/cleanup шаге; нет пропуска или false Applied |
| PIN-01 | Потеря RAM trie cache, pruning живого pin, wrong datadir, несовместимый path backend; replay либо preflight reject |
| OWNED-01 | Overlap CALL input/return memory, задержанный worker, две tx на один key, единичное поле больше RAM буфера |
| EXIST-01 | Empty CREATE, create/delete/revert, существующий пустой account, zero changes; before/after точной tx |
| SCOPE-01 | Bor StateSync + block-alloc; BSC system actualTx/nonce/finalise; outside-tx optional выключен |
| RECEIPT-01 | Failed/success CREATE, constructor revert, fee variants, included failed tx, source encodings |
| BRANCH-01 | Known block без callback, targeted replay, reorg и finality во всех окнах H bootstrap; без gap scan |
| DECISION-01 | Много-блочный reorg, marker cleanup crash, finality за downtime без нового head; повтор не увеличивает revision |
| PROFILE-01 | Отказ всех запрещённых Bor paths/config overrides, отсутствие deadlock и неизменность native execution results |

Source inspection выполнена; сборки, fault injection и эти fixtures пока не запускались. При невозможности pin/decision bridge хотя бы для одного обязательного path профиль не принимается; ослабление guarantees требует нового решения пользователя.
