# ETH/BSC: native finality, evidence и GC anchor

2026-11-05 · DOC-FINAL-01 / D61–D62 · source research и логический контракт. Сборки, runtime и crash tests не выполнялись. Исследованы только текущие ETH/BSC live profiles; исторический replay и новые сети не добавляются. Этот файл отделяет подтверждённое поведение native source от требуемого adapter bridge. D62 принят: публикуются early vote-only и header-attested evidence с разной provenance; GC только по header-derived finalized Source.

## Проверенные исходники

| Клиент | Pin |
|---|---|
| Geth 1.17.9 | `9dffe6e976cbbb87a84c5fd395448f0950290def` |
| BSC 1.7.8 | `cdb7548b5baacfdae92f9f63437d6456411665f3` |
| Engine API reference, прочитан 2026-11-05 | `ethereum/execution-apis@3284ee80c3da3e516c53782dde75157cccf1ebdc` |

Raw files прочитаны непосредственно из указанных commits. GitHub ссылки ниже фиксируют source и строки. Source доказательство API не означает доказательства durability будущего bridge.

## Ethereum: факты source

1. `CurrentFinalBlock()` и `CurrentSafeBlock()` возвращают отдельные atomic header pointers. Это execution block references, а не beacon epoch/root, количество confirmations или подписи валидаторов. [blockchain_reader.go:56–66](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/blockchain_reader.go#L56-L66).
2. `SetFinalized(header)` меняет pointer и записывает finalized hash в native DB; `SetSafe(header)` меняет только RAM pointer. В этих setters нет проверки monotonic height и отдельного finality feed. Их наличие не доказывает fsync или атомарность с adapter store. [blockchain.go:863–883](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/blockchain.go#L863-L883).
3. Startup читает сохранённый finalized hash и выставляет **оба** pointers в найденный finalized header. Прежний более высокий safe не восстанавливается. Поэтому уменьшение наблюдаемого safe после restart допустимо и не доказывает reorg/finality violation. [blockchain.go:696–705](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/blockchain.go#L696-L705).
4. `forkchoiceUpdated` проверяет существование и canonical hash ненулевых finalized/safe references, затем вызывает setters. Нулевое значение в request пропускает соответствующий setter: оно не очищает ранее известный pointer. Finalized setter вызывается **до** проверки safe, поэтому один лишь итоговый RPC status не заменяет наблюдение фактических native mutations. Даже неизменный head может сопровождаться обновлением finality. [api.go:324–386](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/eth/catalyst/api.go#L324-L386).
5. `ChainHeadEvent` содержит только header; отдельного finality event в `core/events.go` нет. Подписка только на новые blocks не обнаруживает все finality-only updates. [events.go:29–47](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/events.go#L29-L47).
6. Native rewind может вызвать `SetSafe(nil)` и `SetFinalized(nil)`, когда новый head ниже соответствующего pointer. Это существующее native поведение, а не разрешение adapter забыть уже использованный irreversible anchor. [blockchain.go:1195–1204](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/blockchain.go#L1195-L1204).

Engine API определяет safe как canonical block при определённых предположениях о сети/честности, finalized — как наиболее поздний finalized block; safe должен быть предком head или совпадать с ним. Эти два поля не взаимозаменяемы. Нулевые значения предусмотрены протоколом при определённых условиях. [ForkchoiceStateV1](https://github.com/ethereum/execution-apis/blob/3284ee80c3da3e516c53782dde75157cccf1ebdc/src/engine/paris.md#forkchoicestatev1).

**Проектный вывод:** ETH GC anchor строится только по native finalized reference, подтверждённому canonical ancestry и durable adapter/native decision protocol. Safe не разрешает необратимую уборку. Bridge не заявляет самостоятельную проверку beacon consensus и не выдумывает beacon epoch/root, которых нет в этих getters.

## BSC: факты source и важное отличие early finality

1. `CurrentFinalBlock()` в этом pin **не читает** `currentFinalBlock`: он вызывает `PoSA.GetFinalizedHeader` для `CurrentHeader()`. `CurrentSafeBlock()` вызывает `GetJustifiedNumberAndHash` и ищет соответствующий header; ошибки схлопываются в nil. Поэтому один public getter не различает отсутствие evidence и ошибку snapshot. [blockchain_reader.go:57–90](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/blockchain_reader.go#L57-L90).
2. `Parlia.GetJustifiedNumberAndHash` получает snapshot для конкретного branch head и возвращает `snapshot.Attestation.TargetNumber/TargetHash`; если attestation отсутствует, возвращается genesis reference, а не error/unknown. [parlia.go:2217–2238](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go#L2217-L2238).
3. `Parlia.GetFinalizedHeader` начинает с `snapshot.Attestation.SourceNumber/SourceHash`, но может продвинуть результат до snapshot Target по quorum из **VotePool**, ещё не включённому в headers. Проверка использует parent validator snapshot и `ceil(2*N/3)`, не универсальное число подтверждений. При отсутствии attestation или до Plato source возвращает genesis; snapshot error возвращает nil. [parlia.go:2240–2287](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go#L2240-L2287).
4. Snapshot Source — результат native transition logic, его нельзя заменить Source последнего произвольного header attestation. При non-consecutive source/target `updateAttestation` может обновить только Target, сохранив прежний finalized Source. После Fermi допускается иной native ancestor selection. [snapshot.go:199–236](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/snapshot.go#L199-L236).
5. Header attestation имеет typed `VoteAddressSet`, `AggSignature`, `Data={SourceNumber,SourceHash,TargetNumber,TargetHash}`, `Extra`. Native verification проверяет source/target ancestry, quorum и aggregate BLS signature. Snapshot VoteData после merge не обязательно совпадает с одной подписанной `VoteAttestation.Data`; нельзя выдавать snapshot projection за исходный certificate. [vote.go:25–51](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/vote.go#L25-L51), [parlia.go:469–574](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go#L469-L574).
6. `CheckFinalityAndNotify` ищет target header по hash, вызывает `GetFinalizedHeader` и сообщает result; в этой функции нет проверки принадлежности target текущей canonical branch. `FinalizedHeaderEvent` сам по себе не доказывает каноничность captured activation. [parlia.go:2289–2304](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go#L2289-L2304).
7. `NotifyFinalized` обновляет RAM `highestNotifiedFinal` и публикует event; older heights фильтруются, same-height/different-hash event возможен. Это не durable anchor. Feed вызывается как после block import, так и asynchronously после поступления votes. `SetFinalized` отдельно пишет native DB hash, но normal import получает аргумент через тот же `GetFinalizedHeader`, поэтому persisted hash также нельзя без provenance объявлять header-attested. [blockchain.go:1161–1208](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/blockchain.go#L1161-L1208), [blockchain.go:2121–2132](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/blockchain.go#L2121-L2132), [vote_pool.go:224–237](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vote/vote_pool.go#L224-L237).
8. VotePool содержит in-memory maps; `FetchVotesByBlockHash` выбирает votes по target hash и source number. Startup восстанавливает native finalized через Parlia по текущему header. Потеря early vote observation после restart не равна доказанному откату on-chain finality. [vote_pool.go:62–83](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vote/vote_pool.go#L62-L83), [vote_pool.go:363–375](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vote/vote_pool.go#L363-L375), [blockchain.go:876–883](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/blockchain.go#L876-L883).

**Проектный вывод:** BSC `safe` означает native justified, а не ETH safe. Для GC используется только finalized Source восстановимого native header-derived snapshot на проверенной canonical branch. `CurrentFinalBlock`, early event и persisted `LastFinalized` без provenance недостаточны. Конкретный bridge должен извлечь header-derived snapshot **без изменения VotePool или native consensus state**; временно отключать VotePool ради getter запрещено.

## Предлагаемые логические typed поля

Это структура значений, не protobuf field numbers. Hash — 32 bytes, height и локальные sequences — uint64 с explicit presence. Нулевой height допустим только вместе с настоящим genesis hash. All-zero hash не изображает известный block.

```text
BlockRef = { height, hash }
EvidenceOrigin = NATIVE_LIVE | NATIVE_RESTART | RETAINED_DURABLE
ReferenceObservation = oneof {
  Known { block: BlockRef, origin: EvidenceOrigin },
  Unknown { reason: NOT_OBSERVED | NATIVE_REFERENCE_UNSET }
}
EthereumFinalityEvidence = {
  observed_execution_head: BlockRef,
  safe: ReferenceObservation,
  finalized: ReferenceObservation,
  safe_basis: CONSENSUS_SETTER | STARTUP_FINALIZED_FALLBACK | UNSET
}
BscHeaderFinality = oneof {
  AttestedSnapshot {
    snapshot_head: BlockRef,
    justified: BlockRef,
    finalized: BlockRef
  },
  GenesisFallback {
    snapshot_head: BlockRef,
    genesis: BlockRef,
    reason: NO_ATTESTATION | PRE_PLATO
  }
}
BscFinalityEvidence = {
  observed_execution_head: BlockRef,
  observed_native_header_head: BlockRef,
  header_finality: BscHeaderFinality,
  native_effective_finalized: ReferenceObservation,
  effective_basis: HEADER_SNAPSHOT | VOTE_POOL_QUORUM | GENESIS_FALLBACK | UNSET,
  early_observation: oneof { None, EarlyVoteEvidence }
}
EarlyVoteEvidence = {
  origin: EvidenceOrigin,
  evaluation_head: BlockRef,
  source_justified: BlockRef,
  vote_target: BlockRef,
  finalized_candidate: BlockRef,
  validator_snapshot_parent: BlockRef,
  validator_count, quorum_count, matching_vote_count,
  evidence_digest
}
```

`EarlyVoteEvidence` и публичная видимость early branch зависят от решения ниже. Эти поля выводимы из native branch в `GetFinalizedHeader`; существующий `FinalizedHeaderEvent` их не предоставляет. Bridge обязан передать owned snapshot исходных validated vote envelopes и validator snapshot в локальную durable evidence record перед публикацией. `evidence_digest` связывает эту запись; он не выдаётся за независимо проверяемый consensus certificate. Не следует повторно собирать counts поздним чтением уже изменившегося VotePool.

Нужны локальные поля `{native_store_id, source_commit, fork_profile, observation_sequence, native_decision_sequence, evidence_digest}` в durable descriptor. Они обеспечивают provenance/recovery; wire envelope может ссылаться на них согласно общей модели IDs, без повторного нового producer/run namespace. `origin=RETAINED_DURABLE` относится к ранее сохранённому evidence, а не к якобы свежему результату native getter.

Ошибки snapshot/header lookup при обязательной проверке не кодируются как genesis fallback, UNKNOWN или пустой список. Bridge возвращает typed внутреннюю source error и не выпускает ложный complete evidence; GC блокируется. Getter nil при заведомо отсутствующем reference допускает UNKNOWN; ошибка доступности известного обязательного reference — recovery/source error. Для BSC различить эти случаи можно только через новый error-aware native accessor, поскольку public getters теряют причину.

## Связь evidence с activation status

Статус определяется ancestry конкретного `activation.block_ref`, а не сравнением одних heights. Для canonical active A:

| Evidence | Вывод для A |
|---|---|
| ETH finalized известен и A является его предком/самим блоком | ETH_FINALIZED |
| ETH safe известен и покрывает A, finalized ещё не покрывает | ETH_SAFE |
| ETH references известны, но не покрывают A | ETH_CANONICAL_UNFINALIZED |
| ETH нужные references неизвестны | ETH_FINALITY_UNKNOWN; не fabricated finalized |
| BSC header snapshot finalized Source покрывает A | BSC_FINALIZED_HEADER_ATTESTED |
| BSC justified Target покрывает A, Source ещё не покрывает | BSC_JUSTIFIED |
| BSC только early quorum покрывает A | BSC_FINALIZED_VOTE_OBSERVED, если принят early вариант |
| BSC genesis fallback для A выше genesis | BSC_NO_ATTESTED_FINALITY; не ошибка и не число confirmations |
| Полная текущая BSC evidence не покрывает A | BSC_CANONICAL_UNFINALIZED |

Статусы сети — семантическая проекция typed evidence, не общий линейный enum эквивалентных стадий всех сетей. Removed остаётся terminal для данной activation. Поздняя metadata не создаёт новую activation. При known finalized и неизвестном safe finalized остаётся доказанным; UNKNOWN safe не отменяет его. Header head BSC может отличаться от execution head: evidence сначала сопоставляется с canonical execution ancestry; нельзя присвоить side-branch или ещё несогласованный header result активным execution records.

## Native seams, restart без новых events и durable anchor

1. **ETH:** instrument actual `SetSafe`/`SetFinalized` callers и native rewind, а не только block notifications или успешный итог FCU RPC. Observation guard охватывает согласованное чтение head/safe/finalized; durable transition intent/marker и adapter mirror следуют [NATIVE_CAPTURE_PROTOCOL](../NATIVE_CAPTURE_PROTOCOL.md). Source setters сами этого протокола не обеспечивают. Startup fallback provenance сохраняется отдельно до следующего live safe setter.
2. **BSC:** error-aware read-only projection внутри Parlia snapshot path возвращает header-derived Source/Target и fallback reason. Ветку native effective finality инструментировать в месте решения, сохраняя origin и при early варианте использованные votes/counts. Finality feed — сигнал для reconciliation, не authoritative portable proof. Snapshot projection не меняет native решение и не реализует второй независимый consensus engine.
3. Vote-triggered observation сериализуется с canonical/import transition guard. Нельзя добавлять обратный захват guard из callback, уже удерживающего несовместимый native lock: точная lock-order map входит в E01. Для RAM-only safe/early observations создаётся metadata-only durable native decision marker по принятому протоколу; отсутствие native block DB batch не разрешает пропускать marker. Это **будущий patch**, не свойство upstream API.
4. В RECOVERY_ONLY после native recovery и до нового capture выполнить snapshot refresh даже при нуле новых head/finality events. Сравнить сохранённые activations и evidence, восстановить недостающие StatusChanged. Продвижение finality во время downtime может потребовать late Status для старых activations; это не поиск/выгрузка пропущенных транзакций. Только изменившийся публичный snapshot создаёт следующую revision; replay того же durable decision не создаёт revision повторно.
5. ETH startup safe fallback и исчезновение BSC transient early votes не уничтожают ранее сохранённое evidence. Текущая observation может быть ниже/неизвестна; предыдущая имеет `RETAINED_DURABLE` provenance. Если публичный контракт показывает обе, нельзя представить retained как новый native read. Fresh reconciliation заново проверяет ancestry.
6. Durable `IrreversibleAnchor={network_profile, block_ref, evidence_kind, evidence_digest, accepted_decision_sequence}` использует ETH native finalized либо BSC **header-derived finalized Source**, а не safe/justified/early quorum. Candidate принимается только после native canonical ancestry проверки, durable evidence/marker и adapter mirror. Исторический anchor может быть старее H и нужен как reference, без публикации его транзакций.
7. Anchor монотонен по ancestry. Новый candidate должен совпадать с прежним или быть его потомком. Stale callback и lower getter snapshot сначала сопоставляются с authoritative текущим решением; они сами не доказывают regression. Nil/read error блокирует принятие нового anchor/GC, но не доказывает конфликт.
8. **Fatal invariant:** подтверждённый новый native canonical/finality decision удаляет уже принятый irreversible anchor, меняет его hash на той же высоте либо устанавливает несовместимую ветку/финальность ниже сохранённого anchor. Продолжать с уже удалёнными replay dependencies нельзя. Включая operator rewind native store ниже anchor — явная source invariant error, а не штатный reset adapter state. Политика следует уже принятому запрету regression в NATIVE_CAPTURE_PROTOCOL; upstream setters сами её не обеспечивают.
9. Для activation GC одного anchor недостаточно: terminal lifecycle snapshot и связанные Status/Removed должны быть durable, pending transitions/replay/delivery отсутствуют; отбрасываемая branch уже несовместима с принятым anchor либо её нужные records окончательно покрыты им. Нужные другим obligations parent-state pins сохраняются по reference ownership. ACK, TTL или количество блоков не заменяют этих условий. Минимальный anchor/evidence certificate переживает очистку covered payloads; архив всех старых votes/blocks не требуется.

## Принятое решение BSC и границы доказательства

Source обнаруживает две разные native finality поверхности. D62: публиковать раннюю vote-only observation и header-attested evidence с разными provenance; irreversible GC только по header-derived finalized Source. Это сохраняет ранний native сигнал, требует owned/durable early evidence и явного поведения после restart. Вариант без публикации early evidence отклонён. Для ETH дополнительный выбор confirmations/window не нужен: native safe/finalized остаются раздельными.

Обязательные будущие fixtures: **FIN-ETH-01** finality-only FCU при том же head; **FIN-ETH-02** restart safe fallback и zero-hash no-op; **FIN-ETH-03** finalized setter состоялся, поздняя safe validation дала ошибку; **FIN-BSC-01** snapshot Source/Target с non-consecutive transition; **FIN-BSC-02** early quorum, crash/restart без новых block events; **FIN-BSC-03** delayed side-branch/same-height finalized notification; **FIN-BSC-04** nil/error/genesis fallback различены; **FIN-GC-01** durable anchor перед cleanup, crash на каждом marker/mirror/GC boundary; **FIN-GC-02** stale observation против настоящего anchor regression; **FIN-GC-03** finality за downtime для сохранённой activation без history scan.

E01 доказывает reachability всех seams, lock order и snapshot ownership; E02 — typed schema/validators и status projection; E05 — crash/restart/reorg/GC сочетания. Документ закрывает source semantics, но не доказывает безопасную атомарность patch, recovery после power loss или отсутствие непокрытых callers. Неизвестное runtime поведение нельзя заменить галочкой source review.
