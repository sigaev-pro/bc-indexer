# Polygon: native finality evidence и граница lifecycle GC

DOC-FINAL-01 / D61/D63, 2026-11-05. Статус: source research и проект логической схемы; не результат сборки/runtime. D63 принят пользователем: Polygon auto-GC разрешён только после подтверждения checkpoint в finalized состоянии собственного Polygon supporting L1; обычные native события передаются сразу. Действуют D42/D44/D58/D60: Polygon PoS Mainnet, собственные Bor/Heimdall и отдельные supporting Geth/Prysm, ordinary sequential Bor import.

## Проверенные pins и главный вывод

| Компонент | Проверенный source commit |
|---|---|
| Bor v2.10.3 | `a16f123d9b4f41a552a3a98d93a0340430838576` |
| Heimdall v0.13.2 | `03090c94e7eaef344cd16efd6c968431c4041847` |
| Supporting Geth v1.17.9 | `9dffe6e976cbbb87a84c5fd395448f0950290def` |

В Bor `finalized` означает проверенный локальный milestone whitelist, а при его отсутствии/несовпадении — checkpoint whitelist. Это не проверка включения checkpoint transaction в finalized Ethereum block. Сам getter сначала выбирает milestone, даже если checkpoint выше; нельзя заменять его результат `max(milestone, checkpoint)`. Если подходящей записи нет, возвращается ошибка `No finalized block`. [Bor eth/api.go:56–77](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/api.go#L56).

Native whitelist достаточен для передачи **native finality observation** с честно названным основанием. Он не является самостоятельным доказательством безусловной необратимости: source допускает replacement/purge, а ошибки записи whitelist логируются без возвращения ошибки вызывающему коду. По D63 одного whitelist для auto-GC недостаточно. Дополнительный finalized-state check собственного Polygon L1 Geth описан ниже; он не возникает автоматически из Heimdall checkpoint ACK.

## Какие объекты действительно существуют

| Объект | Native поля и смысл | Чего он не доказывает |
|---|---|---|
| Heimdall milestone | `proposer`, `start_block`, `end_block`, `hash` **конечного Bor block**, `bor_chain_id`, `milestone_id`, `timestamp`, `total_difficulty`; отдельно последовательный milestone number | Не L1 checkpoint, не L1 transaction/receipt; number не входит в сам Milestone |
| Heimdall milestone proposition/pending | Набор `block_hashes`, start, parent, TD; может участвовать в pending tracking при меньшей поддержке | Не stored milestone и не whitelist |
| Heimdall checkpoint buffer | Кандидат checkpoint в buffer | Не ACKed checkpoint, не Bor whitelist |
| Heimdall stored checkpoint | `id`, `proposer`, `start_block`, `end_block`, `root_hash`, `bor_chain_id`, `timestamp`; записывается после успешного post-handler ACK | Не Ethereum-finalized inclusion; `root_hash` не Bor end block hash |
| Bor checkpoint whitelist | `(exists, end_block_number, end_block_hash)` после проверки checkpoint root на локальных Bor headers | Не исходный checkpoint root/id и не receipt provenance |
| Bor milestone whitelist | `(exists, end_block_number, end_block_hash)` после native проверки локальной цепи | Не future queue и не lock голосования |
| Bor `finalized` projection | Выбранный milestone, затем checkpoint, с локальным canonical hash match и height ≤ head | Не generic `core.SetFinalized`, не Ethereum finality |

Поля заданы [Heimdall milestone.proto:13–34](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/milestone/milestone.proto#L13) и [checkpoint.proto:15–35](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/checkpoint/checkpoint.proto#L15). Milestone создаётся в `PreBlocker` из range с порогом `totalVotingPower*2/3 + 1`; отдельная ветвь `totalVotingPower/3 + 1` обслуживает pending tracking и не подменяет stored milestone. `milestone_id` строится из `aggregatedProposers`, поэтому его нельзя объявлять block hash или использовать вместо `(number, endBlockHash)`. [Heimdall app/abci.go:742–858](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/app/abci.go#L742).

Checkpoint post-handler выставляет `checkpointObj.Id = msg.Number`, сохраняет checkpoint и очищает buffer. Это отличает stored checkpoint от proposal и от раннего события при поступлении ACK message. [Heimdall side_msg_server.go:492–573](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/x/checkpoint/keeper/side_msg_server.go#L492).

## Getters, setters, события и recording seams

| Seam | Проверенное поведение | Требование к проектируемому bridge |
|---|---|---|
| `FetchMilestone(ctx)` / `GET /milestones/latest` | Получает последний объект, без его последовательного number | Сохранять exact request/response до принятия результата; number не вычислять из отдельного count |
| `GetMilestoneByNumber` / `GET /milestones/{number}` | `QueryMilestoneRequest.number:uint64`; response содержит Milestone | Сохранять requested number вместе с response |
| `GetMilestoneCount` / `GET /milestones/count` | `count:uint64`, самостоятельный запрос | Не считать атомарно согласованным с отдельным latest |
| `FetchCheckpoint(ctx,-1)` / `GET /checkpoints/latest` | Последний stored checkpoint; nonnegative number → `/checkpoints/{number}` | Сохранять `id` из raw Heimdall response; Bor projection это поле теряет |
| `GET /checkpoints/buffer` и `/checkpoints/count` | Buffer отдельно от stored checkpoint; count = `ack_count` | Никогда не повышать buffer/count до finalized |
| Milestone WS | JSON-RPC `subscribe`, request `id=0`, query `tm.event='NewBlock' AND milestone.number>0`; событие содержит `number` | `id=0` — transport correlation, не durable observation ID; сохранять исходный event, не только Bor projection |
| `handleMilestone` | Проверяет end hash; out-of-sync/mismatch кладёт в future queue, только success вызывает `ProcessMilestone` | Future observation и accepted whitelist — разные typed outcomes |
| `handleWhitelistCheckpoint` | Проверяет root range; на success получает локальный end block hash и вызывает `ProcessCheckpoint` | Сохранять root и derived end hash как разные поля |
| `GetWhitelistedMilestone/Checkpoint`, `ProcessMilestone/Checkpoint` | Get возвращает exists/number/hash; Process меняет whitelist и fork-validation cache | Сериализовать изменения с canonical guard и durable decision marker |
| `getFinalizedBlockNumber` → `CurrentFinalizedBlock(number)` | Проекция whitelist; second getter читает canonical hash по number | Формировать snapshot под тем же guard, исключить race между selection и block lookup |
| `PurgeWhitelisted*`, `PurgeMilestonesAfter`, rewind/SetHead | Могут отозвать whitelist или изменить canonical ancestry | Обязательные decision seams, а не скрытая административная уборка |

Primary sources: [Heimdall milestone queries](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/milestone/query.proto#L20), [checkpoint queries](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/checkpoint/query.proto#L32), [Bor HTTP client paths and fetch methods](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/heimdall/client.go#L91), [Bor WS subscription](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/heimdallws/client.go#L241), [Heimdall milestone event attributes](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/x/milestone/types/events.go#L13), [Bor native handlers](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/handler_bor.go#L30), [whitelist service](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/downloader/whitelist/service.go#L188), [canonical finalized lookup](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/blockchain_reader.go#L71).

Bor HTTP Milestone uses base64 hash decoding, while WS event carries hex. Bor WS parser does not retain event `number`; failed decimal parsing can leave zero values. Bor Checkpoint struct omits Heimdall `id`. The extractor must validate the recorded source envelope before producing typed evidence; a successfully constructed native Go struct is not sufficient evidence of valid/complete fields. [HTTP Milestone](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/heimdall/milestone/milestone.go#L13), [HTTP Checkpoint](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/heimdall/checkpoint/checkpoint.go#L13), [WS parser:348–366](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/heimdallws/client.go#L348).

Native source has checkpoint polling every 100 seconds; milestone polling every 2 seconds when WS is not configured, and WS processing with periodic re-processing of the latest received milestone. This is source behavior, not an extractor latency guarantee. A new block event alone is insufficient to notice these updates. [Bor backend.go:893–937](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/backend.go#L893), [WS processing:1013–1048](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/backend.go#L1013).

Generic `BlockChain.SetFinalized(header)` persists a separate finalized hash; the Polygon RPC getter described above does not read it. Capturing only this setter would miss Polygon native finality. [Bor blockchain.go:1840–1850](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/blockchain.go#L1840), [Bor eth/api_backend.go:100–112](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/api_backend.go#L100).

## Proposed typed logical schema

This is a logical contract for subsequent integration into the normative message schema, not generated Protobuf or a claim that these structures already exist upstream. Every hash below has a named domain and exact length; uint256 contract values are not silently narrowed to uint64.

```text
PolygonFinalitySnapshot {
  bor_chain_identity: ChainIdentity
  observed_head: BorBlockRef
  observation: NativeDecisionRef
  milestone: Evidence<HeimdallMilestoneEvidence>
  checkpoint: Evidence<HeimdallCheckpointEvidence>
  milestone_whitelist: Evidence<BorWhitelistEvidence>
  checkpoint_whitelist: Evidence<BorWhitelistEvidence>
  native_finalized: Evidence<BorFinalizedProjection>
  l1_checkpoint: Evidence<L1CheckpointEvidence>
}

Evidence<T> = Known(T) | NotObserved | Unavailable(SourceFailureCode)
            | NotEnabled(ProfileFeature)

HeimdallMilestoneEvidence {
  source: SourceObservationRef
  heimdall_chain_identity: ChainIdentity
  number: Optional<uint64>       // only explicit query selector/event attribute
  milestone_id: string           // opaque native identifier
  proposer: Address20
  start_block, end_block, timestamp, total_difficulty: uint64
  end_block_hash: Hash32
  native_outcome: Observed | FutureQueued | LocallyVerified | Rejected
}

HeimdallCheckpointEvidence {
  source: SourceObservationRef
  heimdall_chain_identity: ChainIdentity
  checkpoint_id: Optional<uint64> // required when full Heimdall response retained
  proposer: Address20
  start_block, end_block, timestamp: uint64
  root_hash: Hash32
  state: Buffered | StoredAcknowledged
  locally_verified_end: Optional<BorBlockRef>
}

BorWhitelistEvidence {
  kind: Milestone | Checkpoint
  anchor: BorBlockRef
  native_decision: NativeDecisionRef
  source_evidence: Optional<SourceObservationRef>
  canonical_match: Verified
  ancestry_binding_digest: Hash32
}

BorFinalizedProjection {
  basis: MilestoneWhitelist | CheckpointWhitelist
  anchor: BorBlockRef
  whitelist_decision: NativeDecisionRef
}

SourceObservationRef {
  source_store_identity, chain_identity
  adapter_observation_id, request_sequence
  operation: MilestoneLatest | MilestoneByNumber | MilestoneCount
           | CheckpointLatest | CheckpointByNumber | CheckpointCount
           | CheckpointBuffer | MilestoneNewBlockEvent
  selector: exact typed request parameters or event locator
  committed_heimdall_block: Optional<HeimdallBlockRef>
  exact_response_digest: Hash32
  durable_response_ref
}
```

`NotObserved` means no such evidence has been obtained; `Unavailable` means a specific attempted retrieval failed; `NotEnabled` is only a declared profile limitation. None of them means block zero, false finality, or permission to discard an earlier accepted anchor. A decoded response without independently retained Heimdall height/hash does not acquire a fabricated `committed_heimdall_block`. A WS envelope identifier is not a consensus proof by itself.

The snapshot belongs to one existing block/transaction activation and its durable lifecycle revision. `end_block >= activation.height` alone is insufficient: local canonical ancestry must include the activation's exact block hash under the selected anchor. A later native observation can replace published evidence via `StatusChanged`; it must not silently rewrite an already published revision. Startup may find a persisted whitelist without its original full Heimdall response: publish the narrower known whitelist evidence, leaving original source fields absent, until verified reacquisition. Do not fill checkpoint root with the whitelist end hash.

`SourceObservationRef` is separate from execution-input recording. A finality poll/event has its own observation identity; replay of one saved finality decision reuses its exact response, while a later poll may legitimately receive different `latest` data. Cross-observation caching of a mutable latest response is not replay correctness.

## What checkpoint ACK establishes about L1

`MsgCpAck` contains `from`, checkpoint `number`, proposer, Bor start/end and root. It contains no L1 tx hash, block hash, receipt index or log index. `SideHandleMsgCheckpointAck` compares those values with RootChain state via `GetHeaderInfo`; this helper calls `headerBlocks(checkpoint_id * child_block_interval)` using `bind.CallOpts{Context: ...}` without a finalized block selector. Consequently, even a successfully committed Heimdall ACK is not evidence that this project's supporting Geth has finalized the submission block. [MsgCpAck fields](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/checkpoint/tx.proto#L54), [ACK side validation:312–373](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/x/checkpoint/keeper/side_msg_server.go#L312), [GetHeaderInfo:354–376](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/helper/call.go#L354).

Do not infer stronger semantics from the name `GetConfirmedTxReceipt`: that separate helper supports a confirmations fallback when finalized API is unavailable, and is not called by the ACK handler above. This fallback does not meet a profile requiring actual L1 consensus finality. [Heimdall helper/call.go:889–955](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/helper/call.go#L889).

The pinned RootChain binding exposes `headerBlocks(uint256)` returning root/start/end/createdAt/proposer and `NewHeaderBlock` with indexed proposer/headerBlockId/reward and start/end/root. Event signature topic is `0xba5de06d22af2685c6c7765f60067f7d2b08c2d29f53cdf14d67f6d1c9bfb527`. Contract address and child-block interval come from verified chain parameters, not guessed defaults. [RootChain headerBlocks binding](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/contracts/rootchain/rootchain.go#L240), [NewHeaderBlock event](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/contracts/rootchain/rootchain.go#L411).

## L1-backed evidence: принятый D63 и минимальный observer

Собственный Polygon supporting Geth/Prysm предоставляет дополнительное evidence; внешний oracle, downstream component и Ethereum transaction extractor не нужны. Минимальный выбранный технический путь — `FinalizedContractState`: readonly вызов `RootChain.headerBlocks` в точно зафиксированном finalized L1 block hash. Receipt inclusion показан как отдельный возможный формат evidence, но не обязателен для D63 и не добавляет обязательную подписку на L1 транзакции.

```text
L1CheckpointEvidence {
  l1_chain_identity, supporting_node_identity
  root_chain_address: Address20
  checkpoint_id: uint64
  header_block_id: UInt256
  child_block_interval: uint64
  bor_start, bor_end: uint64
  checkpoint_root: Hash32
  locally_verified_bor_end: BorBlockRef
  bor_range_binding_digest: Hash32
  finalized_l1_anchor: EthereumExecutionBlockRef
  verification: FinalizedReceiptInclusion | FinalizedContractState
}

FinalizedReceiptInclusion {
  transaction_hash: Hash32
  receipt_block: EthereumExecutionBlockRef
  transaction_index, log_index: uint64
  receipt_digest, event_digest, ancestry_binding_digest: Hash32
}

FinalizedContractState {
  call_block_hash: Hash32       // exact finalized anchor hash
  root_chain_params_digest: Hash32
  calldata_digest, result_digest: Hash32
  result_created_at: UInt256
  result_proposer: Address20
}
```

Новый `PolygonCheckpointObserver` — часть native Bor adapter, а не отдельный сервис. Его worker выполняет сетевые reads вне VM hooks/native locks и передаёт immutable результат в existing observation/decision bridge. Он не нормализует и не публикует Ethereum transactions; это control evidence для Polygon. Путь источников конкретен:

1. После bootstrap identity checks получить через собственный Heimdall stored checkpoint `GET /checkpoints/latest` с `id`, range, proposer/root и `bor_chain_id`. `GET /chainmanager/params` даёт `params.chain_params.root_chain_address`, Bor/Heimdall chain IDs; `GET /checkpoints/params` даёт `params.child_chain_block_interval`. Записать exact responses/digests и сверить с проверенной bootstrap chain identity. Раздельные запросы не объявляются атомарным snapshot: повторная проверка параметров перед commit либо pinned context должна исключить смесь конфигураций. Изменение адреса/interval инвалидирует кандидат, не автоматически переписывает прежний anchor.
2. У собственного supporting Geth вызвать `eth_getBlockByNumber("finalized", false)` и сохранить `F={chain_identity,number,hash,parentHash}`. Если finalized отсутствует, нет нового GC anchor; не подставлять latest/safe/confirmations. Уже готовая доставка Polygon не ждёт этого запроса.
3. Вычислить в UInt256 `header_block_id = checkpoint.id * child_chain_block_interval`, без unchecked narrowing; interval должен быть ненулевым. ABI calldata — selector `0x41539d4a` плюс 32-byte big-endian `header_block_id`. Выполнить `eth_call({to:root_chain_address,data:calldata}, {blockHash:F.hash, requireCanonical:true})`. Это точный JSON-RPC selector, поддерживаемый pinned Geth; удобный `CallContractAtHash` без canonical requirement не использовать без дополнительных проверок.
4. Декодировать строго пять ABI результатов `root,start,end,createdAt,proposer`. Проверить exact equality root/start/end/proposer с записанным stored checkpoint, корректность uint256 значений и chain binding. Нулевой/отсутствующий contract entry не является подтверждением. Повторно проверить, что F остаётся canonical и находится на ancestry текущего own-L1 finalized anchor; одно `number <= finalized.number` недостаточно. Если finalized продвинулся, F может остаться пригодным — не требуется совпадение с самым новым head.
5. Проверить локальный Bor checkpoint root над точной непрерывной canonical range; сформировать end `BorBlockRef` и digest immutable range binding. Отложенное вычисление допускается вне locks по закреплённым header inputs, но окончательное принятие проходит под observation guard с повторной проверкой canonical ancestry/end hash и отсутствия конфликтующей native decision. Root comparison отдельно от полного Bor hash — ограничение commitment ниже сохраняется.
6. Через native decision protocol sync сохранить candidate source evidence, finalized-state result и accepted anchor до того, как любой lifecycle GC станет видимым. Anchor содержит evidence ref, Bor end/hash, L1 F, checkpoint ID/root и параметры адреса/interval. Только затем existing pin-release protocol проверяет terminal/pending/replay/delivery guards. Crash replay использует сохранённое evidence, не новый ответ mutable `latest` под старым observation ID.

Источник chain parameters и точные REST поля: [chainmanager query](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/chainmanager/query.proto#L12), [chain parameters](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/chainmanager/chainmanager.proto#L9), [checkpoint parameters](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/proto/heimdallv2/checkpoint/checkpoint.proto#L38). Geth проверяет `RequireCanonical` и получает state именно по найденному header: [eth/api_backend.go:270–292](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/eth/api_backend.go#L270). Это проектируемый adapter path на проверенных native capabilities; upstream Heimdall helper без finalized selector не объявляется готовой реализацией.

На startup observer сначала восстанавливает durable anchor/незавершённый decision, затем проверяет identity и сохранённую Bor/L1 ancestry до нового GC. Отсутствие сети, ещё не синхронизированный supporting L1, незавершённый checkpoint, unavailable state у F или незакрытый source read означают `Unavailable`/pending: retry, сохранить pins, продолжать допустимую native delivery. При state-pruning можно выбрать более новый finalized F и повторить новый observation, поскольку требуется подтвердить существование checkpoint в finalized state, а не воспроизвести старый state через произвольное время. Успешно сохранённое evidence не требует бесконечного хранения всей L1 state.

Неверный chain/contract identity, malformed обязательное evidence или подтверждённый конфликт ниже уже принятого anchor — явный source invariant error по D57; не silently retry с другим адресом. Root/range mismatch нового кандидата не повышает anchor: observer сохраняет исход, native reorg/status path продолжает свой штатный разбор; если конфликт затрагивает принятый anchor, процесс останавливается. Более низкий checkpoint от отстающего Heimdall не понижает durable anchor. Unavailable ancestry из-за отсутствующих данных не доказывает reorg и не разрешает удаление; требуемые компактные ancestry/evidence inputs удерживаются до завершения проверки/GC decision.

For receipt inclusion: obtain `NewHeaderBlock` from the configured RootChain, fetch successful canonical receipt from own Geth, verify exact tx/log/address/event tuple and checkpoint fields, and prove receipt block is on the ancestry of the own Geth finalized execution anchor. Height comparison alone is insufficient. Record responses and ancestry binding before GC. The receipt locator must be independently acquired from own L1 observations; it cannot be invented from Heimdall checkpoint ID. Retrieval/index pruning failure leaves evidence unavailable and pins retained. Own Geth returns receipts through canonical transaction lookup, but separate RPC calls still require consistency checks. [Geth receipt lookup:1589–1604](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/internal/ethapi/api.go#L1589), [Geth finalized lookup:103–108](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/eth/api_backend.go#L103).

A receipt is **not logically mandatory** if the intended assertion is that this exact checkpoint already exists in finalized RootChain state. An alternative is a read-only `headerBlocks(header_block_id)` call at the exact independently validated finalized L1 block hash, with canonicality required/rechecked and full result match. This proves finalized contract state, not a particular submission event, and therefore must use a different evidence variant with no fabricated receipt fields. Geth supports hash-pinned `eth_call`; its convenience `CallContractAtHash` uses `requireCanonical=false`, so the proposed checker must additionally enforce canonicality rather than inheriting that default. [Geth ethclient.go:597–603](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/ethclient/ethclient.go#L597).

Neither variant cryptographically commits the entire Bor header or state root. Bor checkpoint leaves hash padded `number`, `time`, `txRoot`, `receiptRoot`; the verifier separately checks local parent/hash continuity and aborts a changed end hash. Therefore an L1 checkpoint supports the checkpoint commitment plus the locally verified Bor range under native consensus assumptions. It is not an independent proof of every captured state field or of the full Bor block hash. [Bor root calculation:287–422](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/api.go#L287).

## Regression, durability and GC policy boundary

The following are source facts, not hypothetical API naming concerns:

1. `finality.Process` overwrites number/hash without a monotonicity guard; DB write failure only logs. A successful native method return is not a new durable extractor decision. [finality.go:76–86](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/downloader/whitelist/finality.go#L76).
2. `PurgeMilestonesAfter` explicitly removes stale milestone whitelist/locked/future state above a rewind block, in memory and DB. The mismatch verifier invokes it after successful rewind or reorg. [milestone.go:378–428](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/downloader/whitelist/milestone.go#L378), [bor_checkpoint_verifier.go:179–206](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/eth/bor_checkpoint_verifier.go#L179).
3. Heimdall has a fork-height-gated deletion of one known faulty milestone. This is historical source evidence of correction capability, not an assertion that v0.13.2 routinely rolls back committed milestones. [app/abci.go:642–657](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/app/abci.go#L642), [known faulty milestone match](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/x/milestone/keeper/keeper.go#L398).

Project inference and D63 boundary: raw whitelist data remains publishable native evidence; only the additional own-L1 finalized checkpoint check permits auto-GC. This delays pin release, not ordinary Applied/Status/Removed delivery. Native whitelist-only deletion is excluded from this profile.

This policy does not make catastrophic consensus failure, administrative destructive reset or protocol intervention recoverable after inputs have been deleted. The existing [native capture protocol](../NATIVE_CAPTURE_PROTOCOL.md) requires a fatal error on regression below an accepted irreversible anchor. A lower or temporarily unavailable **getter projection alone** is not proof of a conflicting chain: recheck ancestry and source availability first. A confirmed conflicting hash/ancestry below the accepted anchor must stop the native capture process before further publication/GC; do not lower the persisted anchor or silently resume from a new H.

Ordinary NATS ACK still only removes transport payload allowed by the existing ownership protocol; it does not settle lifecycle dependencies. A Removed activation can release branch dependencies only once an accepted D63 anchor on the surviving branch excludes that activation and all pending transitions/replay/delivery obligations are closed. Динамический checkpoint root подтверждает range commitment, а не отдельное доказательство всего состояния блока; эту границу нельзя расширять словом «необратимость».

## Required fixtures and remaining proof limits

| Fixture | Required observation |
|---|---|
| POLY-FIN-01 | Empty whitelist, milestone-only, checkpoint-only, both present; milestone-first projection even if checkpoint is higher; canonical hash mismatch |
| POLY-FIN-02 | Milestone fetched ahead of head/mismatching head enters future queue, does not become accepted finalized evidence; later successful verify updates status without a new block event |
| POLY-FIN-03 | Buffer vs stored ACK vs locally verified whitelist; root and end hash cannot be swapped; missing ID remains absent |
| POLY-FIN-04 | WS/HTTP exact bytes, base64 vs hex, malformed number/hash, event number vs opaque milestone ID, latest/count race |
| POLY-FIN-05 | Whitelist replacement/purge/rewind, getter temporarily unavailable, lower stale response, conflicting ancestor; distinguish availability from actual finality violation |
| POLY-FIN-06 | Crash at source recording, native whitelist mutation/DB error, marker sync, mirror READY, GC intent/release; no lost/duplicate revision |
| POLY-FIN-07 | Checkpoint root over locally continuous range; concurrent reorg, missing header, wrong root, same range with another end hash |
| POLY-FIN-08 | Correct checkpoint root at unfinalized L1 block, wrong contract/ID/interval, unsupported finalized API, stale/lower Heimdall response; no confirmations fallback and no number-only ancestry acceptance |
| POLY-FIN-09 | Exact finalized block hash, canonicality change between reads, missing state then newer finalized F, matching contract state without receipt locator; only state evidence is produced |
| POLY-FIN-10 | GC blocked by pending status/Removed/replay, branch activation below surviving anchor, restart with narrower native whitelist provenance, retained monotonic accepted anchor |

Not proved here: exhaustive caller coverage of the native patches; crash/power-loss durability; CometBFT proof extraction at the observed Heimdall height; runtime correctness of guarded root/ancestry checks; exact deployed RootChain code/address matching verified bootstrap parameters; availability of already-pruned L1 receipt/state. These are concrete implementation gates, not reasons to fabricate missing fields.

D63 закрывает поведенческий выбор. Нового вопроса пользователю для этого контракта нет. Сборка, patch caller coverage и runtime fixtures остаются обязательными gates реализации; source research не заменяет их.
