# Исправление 41 проектной находки — DOC-REPAIR

2026-11-05. Авторизация D56; операционные решения D57. Только проектирование и документация. [Аудит](DEEP_DESIGN_AUDIT_2026_11_05.md) сохраняется как исторический список находок на `8a88e5f`; актуальный статус исправлений — здесь.

`SPECIFIED` означает исправленный/дописанный проверяемый контракт документации, не работающий код. `OPEN` — механизм ещё не завершён. `WAITING_USER` — конкретная развилка вынесена пользователю. У всех пунктов остаются будущие implementation/runtime gates E01–E07; их нельзя закрыть документальным commit.

## Подэтапы

| Подэтап | Содержание | Статус |
|---|---|---|
| DOC-REPAIR-01 | Source signature, SELFDESTRUCT, pending activation и ранний Status | SPECIFIED; контракт ниже и в MESSAGE_CONTRACT/реестре типов |
| DOC-REPAIR-02 | Delivery state machine, destination, drain, memory, versions | IN_PROGRESS |
| DOC-REPAIR-03 | Native admission/state retention/capture/lifecycle | IN_PROGRESS; вопрос pruning/backend ожидает ответа |
| DOC-REPAIR-04 | Bootstrap/init/конфигурация/эксплуатация | IN_PROGRESS; кандидат Heimdall и Bor import flags требуют ответа |
| DOC-REPAIR-05 | Исполняемый план, evidence, итоговая сверка | OPEN |

Подэтап завершается проверкой, документированием результата и отдельным commit до перехода дальше.

## Статус каждого пункта

| ID | Статус документации | Контракт / оставшийся шаг |
|---|---|---|
| NAT-01 | SPECIFIED | AuthorizationSignature uint8, source tuples сохранены; AUTH fixtures |
| NAT-02 | OPEN | Owned snapshot между hooks/worker |
| NAT-03 | OPEN | Existence/creation/deletion source map |
| NAT-04 | SPECIFIED | SelfDestructAction, deletion vs monetary projection; SELF fixtures |
| NAT-05 | OPEN | Bor block-alloc/StateSync scope barrier |
| NAT-06 | OPEN | Receipt source/projection table |
| NAT-07 | SPECIFIED | Removed для durable READY Applied независимо от PubAck |
| NAT-08 | SPECIFIED | PENDING_APPLIED, full evidence snapshots, revision и terminal Removed |
| NAT-09 | OPEN | Known block/side branch targeted recovery |
| NAT-10 | OPEN | Durable lifecycle transition/reconciliation/GC |
| DLV-01 | WAITING_USER | Временное удержание source state и backend capability profile |
| DLV-02 | OPEN | Attempts/STAGING/frontier |
| DLV-03 | OPEN | Идемпотентный Prepare и orphan cleanup |
| DLV-04 | OPEN | Released baseline после Gateway loss |
| DLV-05 | OPEN | Terminal admission после Release |
| DLV-06 | OPEN | Session fencing на commit boundary |
| DLV-07 | OPEN | D57 drain принят; точный автомат завершается |
| DLV-08 | OPEN | Destination binding до Prepare |
| DLV-09 | OPEN | Immutable publish headers |
| DLV-10 | OPEN | Bounded manifests/serialization/validation |
| DLV-11 | OPEN | D57 upgrade policy принят; compatibility matrix завершается |
| DLV-12 | OPEN | Poison/error lifecycle |
| DLV-13 | OPEN | ENOSPC recovery/cleanup |
| OPS-01 | OPEN | Cutover barrier двух owners |
| OPS-02 | OPEN | Partial init |
| OPS-03 | OPEN | Config generations |
| OPS-04 | OPEN | Native paths/UID/mounts |
| OPS-05 | OPEN | Secrets/peer trust |
| OPS-06 | OPEN | NATS bootstrap/verify permissions |
| OPS-07 | OPEN | Независимые startup автоматы |
| OPS-08 | OPEN | Stop-confirm-start |
| OPS-09 | OPEN | Owner/volume mapping |
| OPS-10 | OPEN | Log events |
| OPS-11 | WAITING_USER | Heimdall mainnet compatibility; cold-start artifacts |
| OPS-12 | OPEN | Peer role/network admission |
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
