# Исправление 41 проектной находки — DOC-REPAIR

2026-11-05. Авторизация D56; операционные решения D57. Только проектирование и документация. [Аудит](DEEP_DESIGN_AUDIT_2026_11_05.md) сохраняется как исторический список находок на `8a88e5f`; актуальный статус исправлений — здесь.

`SPECIFIED` означает исправленный/дописанный проверяемый контракт документации, не работающий код. `OPEN` — механизм ещё не завершён. `WAITING_USER` — конкретная развилка вынесена пользователю. У всех пунктов остаются будущие implementation/runtime gates E01–E07; их нельзя закрыть документальным commit.

## Подэтапы

| Подэтап | Содержание | Статус |
|---|---|---|
| DOC-REPAIR-01 | Source signature, SELFDESTRUCT, pending activation и ранний Status | SPECIFIED; контракт ниже и в MESSAGE_CONTRACT/реестре типов |
| DOC-REPAIR-02 | Delivery state machine, destination, drain, memory, versions | SPECIFIED; DELIVERY_STATE_MACHINE, D57–D59 |
| DOC-REPAIR-03 | Native admission/state retention/capture/lifecycle | SPECIFIED; NATIVE_CAPTURE_PROTOCOL, D58 |
| DOC-REPAIR-04 | Bootstrap/init/конфигурация/эксплуатация | SPECIFIED; DEPLOYMENT_BOOTSTRAP_CONTRACT, D58/D60 |
| DOC-REPAIR-05 | Исполняемый план, evidence, итоговая сверка | OPEN |

Подэтап завершается проверкой, документированием результата и отдельным commit до перехода дальше.

## Статус каждого пункта

| ID | Статус документации | Контракт / оставшийся шаг |
|---|---|---|
| NAT-01 | SPECIFIED | AuthorizationSignature uint8, source tuples сохранены; AUTH fixtures |
| NAT-02 | SPECIFIED | Owned safe-point chunks, worker без VM/StateDB; OWNED-01 |
| NAT-03 | SPECIFIED | Pre-mutator existence bridge и tx finalization snapshot; EXIST-01 |
| NAT-04 | SPECIFIED | SelfDestructAction, deletion vs monetary projection; SELF fixtures |
| NAT-05 | SPECIFIED | Native StateSync after до SYSTEM_POST block-alloc; SCOPE-01 |
| NAT-06 | SPECIFIED | Receipt provenance, attempted/created/final existence; RECEIPT-01 |
| NAT-07 | SPECIFIED | Removed для durable READY Applied независимо от PubAck |
| NAT-08 | SPECIFIED | PENDING_APPLIED, full evidence snapshots, revision и terminal Removed |
| NAT-09 | SPECIFIED | Guarded H bootstrap и known-branch corpus/replay; BRANCH-01 |
| NAT-10 | SPECIFIED | Native decision marker/mirror/frontier и evidence GC; DECISION-01 |
| DLV-01 | SPECIFIED | Hash state pin/capsule sync до ADMITTED; CAPTURE/PIN-01 |
| DLV-02 | SPECIFIED | Stable delivery batch/attempt; CAPTURE_STAGING отдельно |
| DLV-03 | SPECIFIED | Chunked Prepare, immutable reservation, owner handshake cleanup |
| DLV-04 | SPECIFIED | Bootstrap retired_prefix от producer, lost ReleaseAck отдельно |
| DLV-05 | SPECIFIED | ALREADY_RETIRED без создания keys |
| DLV-06 | SPECIFIED | Session generation + serialized commit + callback fencing |
| DLV-07 | SPECIFIED | Durable DRAIN/seal/switch, explicit LIVE resume |
| DLV-08 | SPECIFIED | Общий immutable DestinationBinding до capture |
| DLV-09 | SPECIFIED | Exact subject/headers/versions и actual size |
| DLV-10 | SPECIFIED | Bounded chunks/spool/disk indexes; native snapshot proof в подэтапе 03 |
| DLV-11 | SPECIFIED | Capability preflight, old-build drain, store-format change запрещён без migration |
| DLV-12 | SPECIFIED | Error classes, fatal stop, immutable READY без auto-re-ID |
| DLV-13 | SPECIFIED | Expand/free space вне DB, recovery/drain, no drop |
| OPS-01 | SPECIFIED | SealGeneration + durable switch handshake обоих owners |
| OPS-02 | SPECIFIED | Phased native init, durable ENV_READY, no key regeneration; BOOT-01 |
| OPS-03 | SPECIFIED | Allowlisted atomic config generations и effective parser; CONFIG-01 |
| OPS-04 | SPECIFIED | Native homes/UID/RO inputs/writable paths; MOUNT-01 |
| OPS-05 | SPECIFIED | Environment CA, SAN/EKU, отдельные JWT, rotation; TRUST-01 |
| OPS-06 | SPECIFIED | Bootstrap/runtime ACL и UserInfo account proof; NATS-01 |
| OPS-07 | SPECIFIED | Независимые native/delivery автоматы, no cyclic health barrier; START-01 |
| OPS-08 | SPECIFIED | Stop-confirm-start, storage attach fencing; REPLACE-01 |
| OPS-09 | SPECIFIED | 14 volumes/11 services, backing-store identity; MOUNT-01 |
| OPS-10 | SPECIFIED | Обязательные lifecycle/error control logs; LOG-01 |
| OPS-11 | SPECIFIED | Exact source/artifact references, Heimdall mainnet genesis acquisition gate; ARTIFACT-01 |
| OPS-12 | SPECIFIED | Peer identity deployment/network/role admission; TRUST-01 |
| DEV-01 | OPEN | Native proof до delivery gate |
| DEV-02 | OPEN | Ранний минимальный harness |
| DEV-03 | OPEN | Независимые fixture oracles |
| DEV-04 | OPEN | Очистка active backlog |
| DEV-05 | OPEN | Актуальные module boundaries |
| DEV-06 | OPEN | Evidence manifest и stop conditions |

## Критерии DOC-REPAIR-01

Нормативные файлы: [типизация](TRANSACTION_TYPE_REGISTRY.md), [сообщения](MESSAGE_CONTRACT.md), [рабочее ТЗ](../evm-transaction-extractor-tz-v3-codex.md). Source проверки: EIP-7702, выбранный Geth tx_setcode; EIP-6780 и native SELFDESTRUCT semantics, перечисленные в аудите. Новые scalar/union поля фиксируют логический контракт; field numbers и генерация proto — E02.

| Fixture | Вход | Обязательный результат будущей проверки |
|---|---|---|
| AUTH-01 | Включённая tx с tuple parity 2/255 и валидным соседним tuple | Все исходные tuple сохранены; invalid tuple не делает нормализованный native record ошибочным |
| AUTH-02 | Неподходящий chain_id/nonce/signature отдельного tuple | Source значения не исправляются/не выбрасываются; native execution outcome остаётся authoritative |
| AUTH-03 | Повтор authority/дубликаты tuples | Порядок и количество сохранены, нет дедупликации списка |
| AUTH-04 | Root call revert после authorization processing | Receipt outcome не используется как общий rollback всех эффектов; native surviving code/nonce и fees сохранены |
| SELF-01 | SELFDESTRUCT с нулевым balance | ExecutionAction существует без выдуманного денежного изменения |
| SELF-02 | Старый contract и созданный в той же tx contract при активных правилах | eligibility/deletion_committed различаются по fork; AccountChange соответствует native finalization |
| SELF-03 | Beneficiary=self, повторные действия | Native monetary effect не удваивается; порядок execution actions сохранён |
| SELF-04 | SELFDESTRUCT внутри отменённого ancestor | effects_committed/deletion_committed=false; попытка сохранена, итоговый state не содержит отменённого эффекта |
| LIFE-01 | Reorg после READY Applied, до Publish | Durable Removed создаётся, Applied pending не теряет отмену |
| LIFE-02 | Reorg после первой части Applied / lost PubAck | Removed возможен раньше полной записи, поздний Applied не реактивирует A |
| LIFE-03 | Status r2, Status r1, Applied r0 | Полная запись появляется с последним snapshot r2; старая revision не затирает новую |
| LIFE-04 | Removed до Applied/Status; конфликт same revision | Removed terminal; конфликт contents одной revision — ERROR, не last-write-wins; reapply использует новую activation |

Для проверки документации: source Authorization больше не использует outer TypedSignature; SELFDESTRUCT имеет отдельное action representation; выражение «ранее опубликованные» не ограничивает Removed; переход ABSENT+Status определён. Runtime fixtures пока не запускались.

## Критерии DOC-REPAIR-02

[DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md) — нормативный автомат. Он задаёт переходы/closing markers, retired prefix, fencing и recovery для каждого durable состояния, pre-plan DestinationBinding и исполнимый drain/cutover. Там перечислены ATTEMPT/PREPARE/FRONTIER/FENCE/DEST/DRAIN/MEM/UPGRADE/POISON/ENOSPC fixtures. Старый RocksDB protocol помечен исходным изложением с явным приоритетом нового автомата. До реализации проверяются ссылки/согласованность; runtime fixture не заменяется мысленным walkthrough.

Повторная сверка при подэтапе 03 добавила RecoveryRelease после потери Gateway и producer cleanup, durable RELEASING/cursor и отдельную admission_generation/Resume для возврата из drain в прежнее назначение. Эти контрпримеры расширяют FRONTIER/DRAIN fixtures, не создают новые продуктовые решения.

## Критерии DOC-REPAIR-03

[NATIVE_CAPTURE_PROTOCOL](NATIVE_CAPTURE_PROTOCOL.md): CAPTURE/PIN/OWNED/EXIST/SCOPE/RECEIPT/BRANCH/DECISION/PROFILE-01. Source pins и опасные native paths проверены по исходникам. Runtime completeness native patches, устойчивость при power loss и отсутствие deadlock должны быть доказаны до приёмки профиля; документальный статус SPECIFIED их не заменяет.

## Критерии DOC-REPAIR-04

[DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md): BOOT/CONFIG/MOUNT/TRUST/NATS/START/REPLACE/LOG/ARTIFACT-01. Текущие candidates согласованы с D58/D60. Source/metadata проверка не означает materialization большого Heimdall genesis, image builds или runtime parse. Эти доказательства входят в E01/E06/E07, не скрыты за SPECIFIED.
