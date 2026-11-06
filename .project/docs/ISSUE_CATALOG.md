# Полный начальный каталог issues

D84 · 2026-11-06 · GitHub sigaev-pro/bc-indexer. 84 issues: 9 BOOT, 68 child tasks E01–E07, 7 gates. Все product runtime результаты на момент создания NOT_RUN. Source tasks: [TASK_DECOMPOSITION](TASK_DECOMPOSITION.md); запуск/роли: [DEVELOPMENT_AUTOPILOT](../../DEVELOPMENT_AUTOPILOT.md). Стабильный ID/marker — ключ reconciliation, номер GitHub назначается отдельно.

Профили ETH/BSC/POLY раскрываются в семь профилей D82; COMMON runtime механизмы не получают автоматический общий PASS. Задачи без собственного AC-owner всё равно имеют artifact acceptance в исходном каталоге. Семейства AC и конкретные variants сохраняются, не пересоздаются под новые IDs. BOOT issues — организация исполнения и закрытие открытых критериев, не новые требования к данным продукта.

## BOOT-01 — Связать репозиторий и опубликовать исходную базу

<!-- extractor-task:BOOT-01 -->

Этап: BOOT. Роль: DevOps. Применимость/модуль: COMMON.

Зависимости: нет.

Результат: Проверить origin/branch/history, закрепить spec commit, опубликовать базу в пустой main без force; подготовить .gitignore для secrets/runs/state/worktrees.

Приёмка: Remote identity подтверждена; tracked files проверены на secrets; сохранена история; исходный v2 не менялся; повторный запуск ничего не перезаписывает.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-02 — Адаптировать и закрепить роли agency-agents

<!-- extractor-task:BOOT-02 -->

Этап: BOOT. Роль: Coordinator / Technical Writer. Применимость/модуль: COMMON.

Зависимости: BOOT-01.

Результат: Project-local роли и source/hash lock ревизии b859c515d813efcfc4755d3471dd9e7686c7706d; явные правила scope, handoff, allowed paths, review.

Приёмка: Один writer/worktree; reviewer независим; UI-only/retry defaults удалены; roles discovery или explicit prompt fallback проверены; нет глобальной установки всех агентов.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-04 — Проверить unattended доступы и runner

<!-- extractor-task:BOOT-04 -->

Этап: BOOT. Роль: DevOps. Применимость/модуль: COMMON.

Зависимости: BOOT-01.

Результат: Preflight Codex/GitHub/Git/Docker/network/ресурсов/secrets; report с capabilities и ограничениями; testnet signer готовится отдельно.

Приёмка: Разрешённые sandbox операции реально доступны; managed блокер явно останавливает действие; credentials не в Git/logs; нет опасного bypass.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-05 — Подготовить CI и правила независимой приёмки

<!-- extractor-task:BOOT-05 -->

Этап: BOOT. Роль: DevOps / Reviewer. Применимость/модуль: COMMON.

Зависимости: BOOT-01, BOOT-04.

Результат: Required checks для doc/code/test evidence и exact commit; PR flow без выдачи одного actor за двух независимых GitHub людей.

Приёмка: Изменённый после review commit не merged; failed/missing checks блокируют; workflow доступен выбранному repo; admin bypass не используется.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-03 — Создать и проверить durable controller

<!-- extractor-task:BOOT-03 -->

Этап: BOOT. Роль: DevOps / Protocol Engineer. Применимость/модуль: COMMON.

Зависимости: BOOT-02, BOOT-04, BOOT-05.

Результат: .project/automation/controller.py: verify-bootstrap/run/resume/status/stop, DAG scheduler, leases, durable state, workers/reviewers, bounded attempts/budget.

Приёмка: Изолированные failure tests: exit0 без evidence не DONE; crash/restart не дублирует mutations; stale lease/CI/review отвергнуты; stop/recovery не теряет work; fake GitHub backend не создаёт лишние реальные issues.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-06 — Проверить стабильные кандидаты и inventory версий

<!-- extractor-task:BOOT-06 -->

Этап: BOOT. Роль: Architect / DevOps. Применимость/модуль: COMMON.

Зависимости: BOOT-01.

Результат: ComponentRegistry источников stable и исходный candidate lock всех native/supporting/toolchain/library/image компонентов; compatibility risks по семи профилям.

Приёмка: Точные pins/digests, нет latest; актуальность проверена по официальным источникам; неподтверждённая совместимость не PASS. Полный updater/build proof остаётся E01/E07.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-07 — Закрыть TEST-OPEN-01: конец проверяемого NATS потока

<!-- extractor-task:BOOT-07 -->

Этап: BOOT. Роль: Architect / QA. Применимость/модуль: COMMON.

Зависимости: BOOT-01, BOOT-02.

Результат: Конкретный алгоритм границы/завершения local и public reader, включая reorg, незавершённые parts, лишние/пропущенные записи и timeout.

Приёмка: Независимый review контрпримеров; решение внутри гарантий записано и зеркала согласованы. Если нужна смена scope — BLOCKED_DECISION, а не фиктивное закрытие.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-08 — Закрыть TEST-OPEN-02: corpus и независимые expected

<!-- extractor-task:BOOT-08 -->

Этап: BOOT. Роль: Native Engineer / QA. Применимость/модуль: COMMON.

Зависимости: BOOT-06, BOOT-07.

Результат: Именованные cases/variants всех семи profiles/forks, contract scenarios, источники independent expected, минимальные seeds/soak quotas, configurable RunPlans.

Приёмка: Каждый mandatory variant назначен; нет ethalon из проверяемого normalizer; public/local различены; standard plan <=1800 секунд без preparation; документация не объявлена runtime PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## BOOT-09 — Принять подготовку и открыть E01

<!-- extractor-task:BOOT-09 -->

Этап: BOOT. Роль: Reviewer. Применимость/модуль: COMMON.

Зависимости: BOOT-01, BOOT-02, BOOT-03, BOOT-04, BOOT-05, BOOT-06, BOOT-07, BOOT-08.

Результат: Полный reconciled backlog 84 issues с mapping/acyclic DAG и принятыми BOOT outcomes; verify-bootstrap.

Приёмка: Все BOOT dependencies приняты с commits/evidence; отсутствуют фиктивные DONE; controller готов, вопросы закрыты или E01 остаётся заблокирован.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## E01-01.evidence — CaseRegistry/CaseResult/RunManifest schemas: полные profile/fork/fixture/suite identities D82, applicability о

<!-- extractor-task:E01-01.evidence -->

Этап: E01. Роль: QA / Engineer. Применимость/модуль: `test/evidence`; COMMON.

Зависимости: BOOT-09.

Результат: CaseRegistry/CaseResult/RunManifest schemas: полные profile/fork/fixture/suite identities D82, applicability отдельно от verdict, versioned evidence; NOT_RUN без выдуманных actual

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DEV-01, REQ-DEV-04. AC: AC-DEV-01-01, AC-DEV-04-02, AC-DEV-04-03.

Контракт: EVID. Inputs: SPEC → принятые требования.

## E01-01.builds — Изолированный build/artifact manifest каждого клиента и supporting зависимости

<!-- extractor-task:E01-01.builds -->

Этап: E01. Роль: DevOps. Применимость/модуль: `build/native`; ETH/BSC/POLY.

Зависимости: E01-01.evidence, BOOT-09.

Результат: Изолированный build/artifact manifest каждого клиента и supporting зависимости; source/patch/compiler/lock определены, native internal types не склеены в общий graph

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DEV-03, REQ-OPS-10. AC: AC-OPS-10-01, AC-OPS-10-03, AC-DEV-03-01.

Контракт: STACK, BOOT. Inputs: E01-01.evidence → формат manifest.

## E01-01.harness — Воспроизводимый минимальный runner с явными inputs/state/test credentials/supporting services и независимым ex

<!-- extractor-task:E01-01.harness -->

Этап: E01. Роль: QA / Engineer. Применимость/модуль: `test/harness`; ETH/BSC/POLY.

Зависимости: E01-01.builds, E01-01.evidence, BOOT-09.

Результат: Воспроизводимый минимальный runner с явными inputs/state/test credentials/supporting services и независимым expected; скрытая нода автора не нужна

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: EVID, BOOT. Inputs: E01-01.builds → закреплённые клиенты; E01-01.evidence → evidence schema.

## E01-02.pins — Hash-provider/pin seam: source root/input остаётся доступным до допустимого release

<!-- extractor-task:E01-02.pins -->

Этап: E01. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/state_provider`; ETH/BSC/POLY.

Зависимости: E01-01.harness, BOOT-09.

Результат: Hash-provider/pin seam: source root/input остаётся доступным до допустимого release; неподдержанный backend отвергается

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-03, REQ-OPS-01, REQ-OPS-03. AC: AC-CAP-03-01, AC-CAP-03-02, AC-OPS-01-02, AC-OPS-01-03, AC-OPS-03-02.

Контракт: NATIVE, CONFIG. Inputs: E01-01.harness → runner и parent state.

## E01-02.inputs — Static capsule и append-only consumed dependency log

<!-- extractor-task:E01-02.inputs -->

Этап: E01. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/source_inputs`; ETH/BSC/POLY.

Зависимости: E01-02.pins, BOOT-09.

Результат: Static capsule и append-only consumed dependency log; возвращённый native execution ответ сначала durable, повтор не подменяет его свежим

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-04. AC: AC-CAP-04-01, AC-CAP-04-02, AC-CAP-04-03.

Контракт: NATIVE. Inputs: E01-02.pins → закреплённый state/input owner.

## E01-02.owned_capture — Owned capture seam: immutable chunks/snapshots, ограниченный дополнительный RAM, safe-point wait вне callbacks

<!-- extractor-task:E01-02.owned_capture -->

Этап: E01. Роль: Protocol Engineer. Применимость/модуль: `core/capture`; ETH/BSC/POLY.

Зависимости: E01-01.harness, BOOT-09.

Результат: Owned capture seam: immutable chunks/snapshots, ограниченный дополнительный RAM, safe-point wait вне callbacks; mutable native alias не уходит worker

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-05, REQ-CAP-06. AC: AC-CAP-05-01, AC-CAP-06-02.

Контракт: NATIVE. Inputs: E01-01.harness → execution fixtures.

## E01-02.admission — Guard/pin/ADMITTED seam: обязательство обнаружимо после sync admission, до него публичного результата нет

<!-- extractor-task:E01-02.admission -->

Этап: E01. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/admission`; ETH/BSC/POLY.

Зависимости: E01-02.inputs, E01-02.owned_capture, E01-02.pins, BOOT-09.

Результат: Guard/pin/ADMITTED seam: обязательство обнаружимо после sync admission, до него публичного результата нет

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-02. AC: AC-CAP-02-01, AC-CAP-02-02.

Контракт: NATIVE. Inputs: E01-02.pins → pin contract; E01-02.inputs → capsule/log; E01-02.owned_capture → ownership boundary.

## E01-02.decision_bridge — Child-root/native marker/mirror seam: authoritative decision и одно delivery obligation восстанавливаются

<!-- extractor-task:E01-02.decision_bridge -->

Этап: E01. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/decision`; ETH/BSC/POLY.

Зависимости: E01-02.admission, BOOT-09.

Результат: Child-root/native marker/mirror seam: authoritative decision и одно delivery obligation восстанавливаются; rejected/no-event decision не создаёт public batch

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-07. AC: AC-CAP-07-01, AC-CAP-07-02, AC-CAP-07-03, AC-CAP-07-06.

Контракт: NATIVE, DELIVERY. Inputs: E01-02.admission → admitted observation.

## E01-02.lifecycle_seams — Карта реальных execution/system/canonical/finality callers, включая event без нового блока и known branch

<!-- extractor-task:E01-02.lifecycle_seams -->

Этап: E01. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/lifecycle`; ETH/BSC/POLY.

Зависимости: E01-02.decision_bridge, BOOT-09.

Результат: Карта реальных execution/system/canonical/finality callers, включая event без нового блока и known branch; ни один обязательный caller не обходит guard

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-01, REQ-LIF-05, REQ-LIF-06. AC: AC-CAP-01-01, AC-LIF-05-02, AC-LIF-05-03, AC-LIF-06-04, AC-LIF-06-05.

Контракт: NATIVE, SEM. Inputs: E01-02.decision_bridge → decision marker interface.

## E01-03.smoke_corpus — Минимальный независимый corpus transfer/nested revert/fees/system/empty

<!-- extractor-task:E01-03.smoke_corpus -->

Этап: E01. Роль: QA / Engineer. Применимость/модуль: `test/native_corpus`; ETH/BSC/POLY.

Зависимости: E01-01.harness, E01-02.decision_bridge, BOOT-09.

Результат: Минимальный независимый corpus transfer/nested revert/fees/system/empty; полный production semantic coverage этим не объявляется

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: NATIVE, SEM, TYPES. Inputs: E01-02.decision_bridge → native execution output; E01-01.harness → independent expected.

## E01-04.early_faults — Evidence ранних admission/pin/EXEC_READY/decision/H seams при power loss

<!-- extractor-task:E01-04.early_faults -->

Этап: E01. Роль: QA / Engineer. Применимость/модуль: `test/native_faults`; ETH/BSC/POLY.

Зависимости: E01-02.lifecycle_seams, E01-03.smoke_corpus, BOOT-09.

Результат: Evidence ранних admission/pin/EXEC_READY/decision/H seams при power loss; full native+Gateway matrix явно остаётся E05

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-SCP-03. AC: AC-SCP-03-01.

Контракт: NATIVE, EVID. Inputs: E01-02.lifecycle_seams → caller coverage; E01-03.smoke_corpus → inputs/expected.

## E01-05.type_paths — Матрица действующих native transaction paths

<!-- extractor-task:E01-05.type_paths -->

Этап: E01. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/type_mapping`; ETH/BSC/POLY.

Зависимости: E01-03.smoke_corpus, BOOT-09.

Результат: Матрица действующих native transaction paths; unknown type не пропускается, BSC system и Bor StateSync обязательны

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: TYPES, SEM. Inputs: E01-03.smoke_corpus → native corpus.

## E02-01.semantic_descriptors — Manifest semantic identity: activation/revision/source стабильны до delivery attempts

<!-- extractor-task:E02-01.semantic_descriptors -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/semantic`; ETH/BSC/POLY.

Зависимости: E01-02.decision_bridge, G.E01.

Результат: Manifest semantic identity: activation/revision/source стабильны до delivery attempts; decision→batch выделяется один раз

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: MSG, CONTROL, NATIVE. Inputs: E01-02.decision_bridge → decision interface.

## E02-05.actions_model — Типизированная модель native monetary actions, общий execution_order, cause equality и RollbackScope refs/tree

<!-- extractor-task:E02-05.actions_model -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/balance`; ETH/BSC/POLY.

Зависимости: E01-03.smoke_corpus, G.E01.

Результат: Типизированная модель native monetary actions, общий execution_order, cause equality и RollbackScope refs/tree/validator; receipt summary не суммируется как второе движение

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-05. AC: AC-DAT-05-03.

Контракт: MSG, SEM. Inputs: E01-03.smoke_corpus → reference effects.

## E02-08.block_metadata — Block metadata/lifecycle schema, включая empty

<!-- extractor-task:E02-08.block_metadata -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/block`; ETH/BSC/POLY.

Зависимости: E01-05.type_paths, G.E01.

Результат: Block metadata/lifecycle schema, включая empty; native source counts не превращены в completeness assertion

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: MSG, SEM. Inputs: E01-05.type_paths → профильные native поля.

## E02-10.closed_profiles — Закрытый реестр payload/extensions/presence, D75 receipt kinds/native_type/origins/network details с exact wid

<!-- extractor-task:E02-10.closed_profiles -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/profiles`; ETH/BSC/POLY.

Зависимости: E01-05.type_paths, G.E01.

Результат: Закрытый реестр payload/extensions/presence, D75 receipt kinds/native_type/origins/network details с exact widths/applicability; D80 sources_binding_digest:Hash32 обязателен в SourceObservationRef/L1Checkpoint; unsupported version не получает ANY fallback

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-02, REQ-DAT-09, REQ-LIF-07. AC: AC-DAT-02-03, AC-DAT-09-03, AC-LIF-07-07.

Контракт: TYPES, SEM, MSG. Inputs: E01-05.type_paths → source variants.

## E02-02.schemas — Воспроизводимые schema/bindings и structural validators: required presence, oneof, lengths, unknown values опр

<!-- extractor-task:E02-02.schemas -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/proto`; COMMON.

Зависимости: E02-01.semantic_descriptors, E02-05.actions_model, E02-08.block_metadata, E02-10.closed_profiles, G.E01.

Результат: Воспроизводимые schema/bindings и structural validators: required presence, oneof, lengths, unknown values определены

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-01. AC: AC-DAT-01-01, AC-DAT-01-02.

Контракт: MSG, CONTROL. Inputs: E02-01.semantic_descriptors → identities; E02-05.actions_model → action schema; E02-08.block_metadata → block schema; E02-10.closed_profiles → variants.

## E02-02.codec — Bounded typed RecordFragment/tree/blob codec, streaming manifests и exact transport layout/headers/digests

<!-- extractor-task:E02-02.codec -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/codec`; COMMON.

Зависимости: E02-02.schemas, G.E01.

Результат: Bounded typed RecordFragment/tree/blob codec, streaming manifests и exact transport layout/headers/digests; полная Envelope не сериализуется одним message, крупное поле также дробится

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-04. AC: AC-DLV-04-01, AC-DLV-04-02, AC-DLV-04-04, AC-DLV-04-05, AC-DLV-04-06, AC-DLV-04-07, AC-DLV-04-08, AC-DLV-04-09, AC-DLV-04-12.

Контракт: CONTROL, DELIVERY. Inputs: E02-02.schemas → typed messages/validators.

## E02-03.lifecycle_validator — Тестовый lifecycle automaton: reordered Applied/Removed/Status, revision conflicts и reinclusion имеют однозна

<!-- extractor-task:E02-03.lifecycle_validator -->

Этап: E02. Роль: QA / Engineer. Применимость/модуль: `test/lifecycle_validator`; ETH/BSC/POLY.

Зависимости: E02-01.semantic_descriptors, E02-02.schemas, G.E01.

Результат: Тестовый lifecycle automaton: reordered Applied/Removed/Status, revision conflicts и reinclusion имеют однозначный результат; production consumer не создаётся

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-LIF-03. AC: AC-LIF-03-01, AC-LIF-03-02, AC-LIF-03-03.

Контракт: MSG, SEM. Inputs: E02-02.schemas → lifecycle messages; E02-01.semantic_descriptors → identity invariants.

## E02-04.compatibility — Read/replay/new-write capability matrix: immutable pending поддержан либо transition отвергнут

<!-- extractor-task:E02-04.compatibility -->

Этап: E02. Роль: Protocol Engineer. Применимость/модуль: `protocol/compatibility`; COMMON.

Зависимости: E02-02.codec, G.E01.

Результат: Read/replay/new-write capability matrix: immutable pending поддержан либо transition отвергнут; drain не разрешает несовместимый remaining metadata format

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: CONTROL, DELIVERY, CONFIG. Inputs: E02-02.codec → encoding versions.

## E03-01.producer_store — Producer state machine INTENT/STAGING/READY/SEALED/ACKED_CLEANUP/outbox

<!-- extractor-task:E03-01.producer_store -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `core/journal/producer`; COMMON.

Зависимости: E02-01.semantic_descriptors, E02-02.codec, G.E02.

Результат: Producer state machine INTENT/STAGING/READY/SEALED/ACKED_CLEANUP/outbox; closing commits атомарны, attempt не меняет semantic identity

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-08, REQ-DLV-02. AC: AC-CAP-08-01, AC-DLV-02-01, AC-DLV-02-02, AC-DLV-02-03, AC-DLV-02-04, AC-DLV-02-05.

Контракт: DELIVERY, CONTROL. Inputs: E02-02.codec → immutable bytes; E02-01.semantic_descriptors → descriptors.

## E03-01.gateway_store — PREPARING/reservation/RECEIVING/CLOSED/ACKED/RELEASING store

<!-- extractor-task:E03-01.gateway_store -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `gateway/journal`; COMMON.

Зависимости: E03-01.producer_store, G.E02.

Результат: PREPARING/reservation/RECEIVING/CLOSED/ACKED/RELEASING store; reservation retry не выделяет новую sequence

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-03. AC: AC-DLV-03-04.

Контракт: DELIVERY, CONTROL. Inputs: E03-01.producer_store → batch/plan interface.

## E03-02.rpc_streaming — Bounded chunk RPC и IMPORT_SEALED

<!-- extractor-task:E03-02.rpc_streaming -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `gateway/ingest`; COMMON.

Зависимости: E03-01.gateway_store, E03-01.producer_store, G.E02.

Результат: Bounded chunk RPC и IMPORT_SEALED; offset/conflict/manifest validation предшествуют CLOSED

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-03. AC: AC-DLV-03-01, AC-DLV-03-02.

Контракт: CONTROL, DELIVERY. Inputs: E03-01.gateway_store → Gateway transitions; E03-01.producer_store → producer transitions.

## E03-02.fencing — Session/admission/publisher fencing: stale RPC/callback не создаёт state после barrier или Release

<!-- extractor-task:E03-02.fencing -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `gateway/session`; COMMON.

Зависимости: E03-02.rpc_streaming, G.E02.

Результат: Session/admission/publisher fencing: stale RPC/callback не создаёт state после barrier или Release

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-08, REQ-OPS-05. AC: AC-DLV-08-01, AC-DLV-08-02, AC-OPS-05-01.

Контракт: CONTROL, DELIVERY. Inputs: E03-02.rpc_streaming → mutating RPC boundary.

## E03-03.destination — Pre-plan binding и online account/stream/route plus exact D77 metadata validation

<!-- extractor-task:E03-03.destination -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `gateway/destination`; COMMON.

Зависимости: E02-02.codec, E03-02.fencing, G.E02.

Результат: Pre-plan binding и online account/stream/route plus exact D77 metadata validation; missing/mismatch не перенаправляет pending

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-09, REQ-OPS-06. AC: AC-DLV-09-01, AC-DLV-09-02, AC-DLV-09-03, AC-DLV-09-04, AC-DLV-09-05, AC-OPS-06-01, AC-OPS-06-03.

Контракт: CONTROL, CONFIG, BOOT. Inputs: E03-02.fencing → authenticated owner; E02-02.codec → binding digest.

## E03-03.publisher — NATS PubAck → durable NATS_ACKED → IngestAck одного manifest/plan/destination

<!-- extractor-task:E03-03.publisher -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `gateway/publisher`; COMMON.

Зависимости: E03-02.rpc_streaming, E03-03.destination, G.E02.

Результат: NATS PubAck → durable NATS_ACKED → IngestAck одного manifest/plan/destination; partial ACK не завершает batch

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-04, REQ-DLV-05. AC: AC-DLV-04-03, AC-DLV-04-10, AC-DLV-05-01, AC-DLV-05-02, AC-DLV-05-03.

Контракт: DELIVERY, CONTROL, BOOT. Inputs: E03-03.destination → verified target; E03-02.rpc_streaming → CLOSED frames.

## E03-01.cleanup — Ordered Release/RecoveryRelease/retired baseline обеих сторон, bounded RELEASING cleanup

<!-- extractor-task:E03-01.cleanup -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `core/delivery/release`; COMMON.

Зависимости: E03-02.fencing, E03-03.publisher, G.E02.

Результат: Ordered Release/RecoveryRelease/retired baseline обеих сторон, bounded RELEASING cleanup; ACKed payload не нужен после Gateway loss

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-06. AC: AC-DLV-06-01, AC-DLV-06-02, AC-DLV-06-03, AC-DLV-06-04.

Контракт: DELIVERY, CONTROL. Inputs: E03-03.publisher → IngestAck; E03-02.fencing → barrier.

## E03-03.maintenance — Seal/Switch/Resume, D77 target metadata checks и idempotent committed receipt при NATS outage

<!-- extractor-task:E03-03.maintenance -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `core/delivery/maintenance`; COMMON.

Зависимости: E03-01.cleanup, E03-02.fencing, E03-03.destination, G.E02.

Результат: Seal/Switch/Resume, D77 target metadata checks и idempotent committed receipt при NATS outage; MAINTENANCE recovery от заданного quiesced producer, без proof native quiescence

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-09, REQ-DLV-11. AC: AC-DLV-09-06, AC-DLV-09-07, AC-DLV-09-08, AC-DLV-11-02, AC-DLV-11-04, AC-DLV-11-05, AC-DLV-11-06, AC-DLV-11-07.

Контракт: DELIVERY, CONTROL. Inputs: E03-01.cleanup → empty delivery frontier; E03-03.destination → binding; E03-02.fencing → session barrier.

## E03-04.pressure_errors — Transient/config/poison/ENOSPC behavior и bounded queues

<!-- extractor-task:E03-04.pressure_errors -->

Этап: E03. Роль: Protocol Engineer. Применимость/модуль: `core/delivery/errors`; COMMON.

Зависимости: E03-01.cleanup, E03-03.publisher, G.E02.

Результат: Transient/config/poison/ENOSPC behavior и bounded queues; pending без TTL/квоты, local fatal и remote outage различаются

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-13. AC: AC-DLV-13-01, AC-DLV-13-02, AC-DLV-13-04, AC-DLV-13-05.

Контракт: DELIVERY, CONFIG. Inputs: E03-03.publisher → retry points; E03-01.cleanup → cleanup points.

## E03-05.recovery_matrix — Delivery-only fault evidence от заданного durable INTENT/READY

<!-- extractor-task:E03-05.recovery_matrix -->

Этап: E03. Роль: QA / Engineer. Применимость/модуль: `test/delivery_faults`; COMMON.

Зависимости: E02-04.compatibility, E03-03.maintenance, E03-04.pressure_errors, G.E02.

Результат: Delivery-only fault evidence от заданного durable INTENT/READY; Gateway loss/late RPC/partial cleanup проверены, full REC-T01 не присваивается

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-01, REQ-DLV-03, REQ-DLV-04, REQ-DLV-07, REQ-DLV-14. AC: AC-DLV-01-01, AC-DLV-01-02, AC-DLV-03-03, AC-DLV-04-11, AC-DLV-07-01, AC-DLV-07-02, AC-DLV-07-04, AC-DLV-14-01.

Контракт: DELIVERY, CONTROL, EVID. Inputs: E03-03.maintenance → transition machine; E03-04.pressure_errors → error states; E02-04.compatibility → capability matrix.

## E03-11.block_lifecycle — NATS smoke corpus block/tx/lifecycle/empty с parts/PubAck

<!-- extractor-task:E03-11.block_lifecycle -->

Этап: E03. Роль: QA / Engineer. Применимость/модуль: `test/nats_smoke`; ETH/BSC/POLY.

Зависимости: E01-03.smoke_corpus, E02-03.lifecycle_validator, E03-03.publisher, G.E02.

Результат: NATS smoke corpus block/tx/lifecycle/empty с parts/PubAck; не verifier полного блокчейна

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: MSG, DELIVERY, EVID. Inputs: E03-03.publisher → publish path; E01-03.smoke_corpus → native inputs; E02-03.lifecycle_validator → expected lifecycle.

## E04-01.calls — Полное дерево calls, общий порядок с actions, native scope mapping и outcome/committed

<!-- extractor-task:E04-01.calls -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `core/normalization/calls`; ETH/BSC/POLY.

Зависимости: E01-05.type_paths, E02-02.schemas, E02-05.actions_model, G.E03.

Результат: Полное дерево calls, общий порядок с actions, native scope mapping и outcome/committed; D81 отличает attempted/локально успешный created_address от конечного existence; успешный child не переживает ancestor rollback

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-04, REQ-DAT-05. AC: AC-DAT-04-01, AC-DAT-04-02, AC-DAT-04-03, AC-DAT-05-01, AC-DAT-05-02, AC-DAT-05-04, AC-DAT-05-05.

Контракт: MSG, SEM. Inputs: E02-02.schemas → call variants; E02-05.actions_model → order/scopes; E01-05.type_paths → native paths.

## E04-01.receipts — Native receipt consensus/storage registry, D76 bounded byte-equivalent writer, log/create/network-details proj

<!-- extractor-task:E04-01.receipts -->

Этап: E04. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/receipts`; ETH/BSC/POLY.

Зависимости: E01-05.type_paths, E02-02.codec, E02-10.closed_profiles, G.E03.

Результат: Native receipt consensus/storage registry, D76 bounded byte-equivalent writer, log/create/network-details projection; source и derived значения не подменяют друг друга

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-09, REQ-DAT-02, REQ-DAT-09. AC: AC-CAP-09-01, AC-DAT-02-01, AC-DAT-02-02, AC-DAT-02-04, AC-DAT-02-05, AC-DAT-09-01, AC-DAT-09-02, AC-DAT-09-04.

Контракт: MSG, SEM, TYPES. Inputs: E01-05.type_paths → receipt origins; E02-10.closed_profiles → receipt registry; E02-02.codec → D73 fragments/spool.

## E04-01.authorization — Исходные authorization tuples, включая invalid/duplicate, сохранены

<!-- extractor-task:E04-01.authorization -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `core/normalization/authorization`; ETH/BSC/POLY по native applicability.

Зависимости: E02-10.closed_profiles, E04-01.receipts, G.E03.

Результат: Исходные authorization tuples, включая invalid/duplicate, сохранены; validation внешней tx не фильтрует tuples

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-03. AC: AC-DAT-03-01.

Контракт: TYPES, SEM. Inputs: E04-01.receipts → native outcome; E02-10.closed_profiles → source widths.

## E04-01.block_record — Native BlockApplied projection: header/RLP/count/optional presence и source timestamp precision точны для empt

<!-- extractor-task:E04-01.block_record -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `core/normalization/block`; ETH/BSC/POLY.

Зависимости: E01-05.type_paths, E02-08.block_metadata, G.E03.

Результат: Native BlockApplied projection: header/RLP/count/optional presence и source timestamp precision точны для empty и nonempty blocks

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-10. AC: AC-DAT-10-01.

Контракт: MSG, SEM. Inputs: E02-08.block_metadata → typed block schema; E01-05.type_paths → source header variants.

## E04-03.state_diff — Transaction-boundary existence/balance/nonce/code/storage before/after

<!-- extractor-task:E04-03.state_diff -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `core/normalization/state`; ETH/BSC/POLY.

Зависимости: E01-02.owned_capture, E04-01.calls, G.E03.

Результат: Transaction-boundary existence/balance/nonce/code/storage before/after; rollback/empty account/reset корректны

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-05, REQ-DAT-07. AC: AC-CAP-05-02, AC-DAT-07-01, AC-DAT-07-02.

Контракт: MSG, SEM. Inputs: E04-01.calls → scopes; E01-02.owned_capture → immutable snapshots.

## E04-01.selfdestruct — SELFDESTRUCT action, monetary movement и deletion раздельны при zero/self-beneficiary/revert/fork cases

<!-- extractor-task:E04-01.selfdestruct -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `core/normalization/selfdestruct`; ETH/BSC/POLY.

Зависимости: E04-01.calls, E04-03.state_diff, G.E03.

Результат: SELFDESTRUCT action, monetary movement и deletion раздельны при zero/self-beneficiary/revert/fork cases

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-06. AC: AC-DAT-06-01, AC-DAT-06-02, AC-DAT-06-03, AC-DAT-06-04.

Контракт: SEM, MSG. Inputs: E04-03.state_diff → boundary diff; E04-01.calls → call scopes.

## E04-03.balance_actions — Причинные денежные движения, fees/refunds/credits

<!-- extractor-task:E04-03.balance_actions -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `core/normalization/balance`; ETH/BSC/POLY.

Зависимости: E02-05.actions_model, E04-01.receipts, E04-03.state_diff, G.E03.

Результат: Причинные денежные движения, fees/refunds/credits; native scope и стабильные order/IDs при capture recovery; net-zero actions не исчезают, summary не удваивает effects

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DAT-05, REQ-DAT-08. AC: AC-DAT-05-06, AC-DAT-05-07, AC-DAT-08-01, AC-DAT-08-02, AC-DAT-08-03.

Контракт: SEM, MSG. Inputs: E04-03.state_diff → account delta; E04-01.receipts → fee summary; E02-05.actions_model → action schema.

## E04-04.import_pressure — Полный import path и large-field/slow-worker behavior

<!-- extractor-task:E04-04.import_pressure -->

Этап: E04. Роль: QA / Engineer. Применимость/модуль: `test/native_import`; ETH/BSC/POLY.

Зависимости: E01-02.owned_capture, E04-01.block_record, E04-01.selfdestruct, E04-03.balance_actions, G.E03.

Результат: Полный import path и large-field/slow-worker behavior; native results не меняются, collector memory bounded, системные scopes не смешаны

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-06, REQ-SCP-04. AC: AC-SCP-04-01, AC-SCP-04-02, AC-CAP-06-01.

Контракт: NATIVE, SEM, EVID. Inputs: E04-03.balance_actions → full effects; E04-01.selfdestruct → action semantics; E04-01.block_record → full block projection; E01-02.owned_capture → safe-point seam.

## E04-02.optional_scope — Scope declaration: default OFF без потери native tx effects

<!-- extractor-task:E04-02.optional_scope -->

Этап: E04. Роль: Protocol Engineer. Применимость/модуль: `protocol/optional_scope`; ETH/BSC/POLY.

Зависимости: E04-03.balance_actions, G.E03.

Результат: Scope declaration: default OFF без потери native tx effects; enabled outside-tx/legacy record только с объявленными typed fixtures

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-SCP-05. AC: AC-SCP-05-01, AC-SCP-05-02.

Контракт: MSG, TYPES, SEM. Inputs: E04-03.balance_actions → обязательный tx scope.

## E05-01.registry — Durable activation/revision registry и native decision mirror

<!-- extractor-task:E05-01.registry -->

Этап: E05. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/lifecycle_registry`; ETH/BSC/POLY.

Зависимости: E01-02.lifecycle_seams, E02-03.lifecycle_validator, G.E04.

Результат: Durable activation/revision registry и native decision mirror; speculative corpus не становится Applied, READY Applied остаётся отменяемым

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-07, REQ-LIF-01. AC: AC-CAP-07-04, AC-CAP-07-05, AC-LIF-01-01, AC-LIF-01-02.

Контракт: NATIVE, MSG. Inputs: E02-03.lifecycle_validator → semantic oracle; E01-02.lifecycle_seams → native callers.

## E05-01.eth_finality — Typed native safe/finalized observations, setter/restart/no-new-block semantics

<!-- extractor-task:E05-01.eth_finality -->

Этап: E05. Роль: Native Engineer. Применимость/модуль: `adapters/geth/finality`; ETH.

Зависимости: E01-02.lifecycle_seams, E05-01.registry, G.E04.

Результат: Typed native safe/finalized observations, setter/restart/no-new-block semantics; GC anchor только authoritative finalized ancestry

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-LIF-05. AC: AC-LIF-05-01, AC-LIF-05-04.

Контракт: SEM, NATIVE. Inputs: E05-01.registry → activation refs; E01-02.lifecycle_seams → native setter paths.

## E05-01.bsc_finality — Early VotePool/header-attested evidence раздельны

<!-- extractor-task:E05-01.bsc_finality -->

Этап: E05. Роль: Native Engineer. Применимость/модуль: `adapters/bsc/finality`; BSC.

Зависимости: E01-02.lifecycle_seams, E05-01.registry, G.E04.

Результат: Early VotePool/header-attested evidence раздельны; GC допускает только установленный контрактом header Source

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-LIF-06. AC: AC-LIF-06-01, AC-LIF-06-02, AC-LIF-06-03.

Контракт: SEM, NATIVE. Inputs: E05-01.registry → activation refs; E01-02.lifecycle_seams → native finality paths.

## E05-01.polygon_observer — Native Polygon statuses и L1-finalized checkpoint evidence с D80 sources_binding_digest, закреплённым за obser

<!-- extractor-task:E05-01.polygon_observer -->

Этап: E05. Роль: Native Engineer. Применимость/модуль: `adapters/bor/finality_observer`; POLY.

Зависимости: E01-01.harness, E05-01.registry, G.E04.

Результат: Native Polygon statuses и L1-finalized checkpoint evidence с D80 sources_binding_digest, закреплённым за observation и неизменным при replay/config replacement; публикация tx/status не ждёт GC source, source outage не выдумывает anchor

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-LIF-07. AC: AC-LIF-07-01, AC-LIF-07-02, AC-LIF-07-03, AC-LIF-07-04, AC-LIF-07-05, AC-LIF-07-06, AC-LIF-07-08, AC-LIF-07-09.

Контракт: SEM, NATIVE, BOOT. Inputs: E05-01.registry → activation refs; E01-01.harness → own Polygon L1 fixture endpoints.

## E05-01.activation_gc — GC decision по durable terminal/anchor, ancestry и отсутствию pending

<!-- extractor-task:E05-01.activation_gc -->

Этап: E05. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/recovery_gc`; ETH/BSC/POLY.

Зависимости: E03-01.cleanup, E05-01.bsc_finality, E05-01.eth_finality, E05-01.polygon_observer, G.E04.

Результат: GC decision по durable terminal/anchor, ancestry и отсутствию pending; no-anchor сохраняет pins, доказанная regression ниже принятого anchor fatal

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-LIF-08. AC: AC-LIF-08-01, AC-LIF-08-02, AC-LIF-08-03, AC-LIF-08-04, AC-LIF-08-05.

Контракт: SEM, NATIVE. Inputs: E05-01.eth_finality → ETH evidence; E05-01.bsc_finality → BSC evidence; E05-01.polygon_observer → POLY evidence; E03-01.cleanup → delivery status.

## E05-02.reorg — Reorg/reapply/known-branch/reordered events evidence

<!-- extractor-task:E05-02.reorg -->

Этап: E05. Роль: QA / Engineer. Применимость/модуль: `test/native_lifecycle`; ETH/BSC/POLY.

Зависимости: E05-01.activation_gc, E05-01.registry, G.E04.

Результат: Reorg/reapply/known-branch/reordered events evidence; Removed создаётся для READY Applied независимо от PubAck

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-08, REQ-LIF-02. AC: AC-CAP-08-02, AC-LIF-02-01.

Контракт: NATIVE, MSG, SEM. Inputs: E05-01.registry → registry; E05-01.activation_gc → retained ancestry.

## E05-03.drain — Native RECOVERY_ONLY DRAIN закрывает все obligations до seal/highwater

<!-- extractor-task:E05-03.drain -->

Этап: E05. Роль: Native Engineer. Применимость/модуль: `adapters/<client>/maintenance`; ETH/BSC/POLY.

Зависимости: E03-03.maintenance, E05-02.reorg, G.E04.

Результат: Native RECOVERY_ONLY DRAIN закрывает все obligations до seal/highwater; retained future-GC pins допустимы, новых admissions/poll нет

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-10, REQ-DLV-11, REQ-DLV-12. AC: AC-DLV-10-01, AC-DLV-10-02, AC-DLV-10-03, AC-DLV-10-04, AC-DLV-11-03, AC-DLV-12-01.

Контракт: NATIVE, DELIVERY, CONTROL, CONFIG. Inputs: E05-02.reorg → pending native transitions; E03-03.maintenance → delivery drain/switch.

## E05-03.upgrade — Old-build drain/compatible replay/rollback matrix с retained lifecycle metadata

<!-- extractor-task:E05-03.upgrade -->

Этап: E05. Роль: QA / Engineer. Применимость/модуль: `test/compatibility_recovery`; ETH/BSC/POLY.

Зависимости: E02-04.compatibility, E05-03.drain, G.E04.

Результат: Old-build drain/compatible replay/rollback matrix с retained lifecycle metadata; incompatible persistent format всегда отвергнут prewrite

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-14. AC: AC-DLV-14-02.

Контракт: DELIVERY, CONFIG, NATIVE. Inputs: E05-03.drain → completed drain; E02-04.compatibility → read/write rules.

## E05-04.concurrent_fencing — Гонки native decisions/old worker/session/late PubAck не создают второе действие или resurrected state

<!-- extractor-task:E05-04.concurrent_fencing -->

Этап: E05. Роль: QA / Engineer. Применимость/модуль: `test/native_concurrency`; ETH/BSC/POLY.

Зависимости: E03-02.fencing, E05-02.reorg, E05-03.drain, G.E04.

Результат: Гонки native decisions/old worker/session/late PubAck не создают второе действие или resurrected state

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-11. AC: AC-DLV-11-08.

Контракт: NATIVE, DELIVERY, CONTROL. Inputs: E05-03.drain → mode barrier; E05-02.reorg → lifecycle races; E03-02.fencing → transport fencing.

## E05-03.full_crash_matrix — Полный native→WAL→Gateway→NATS crash/reorg/finality/restart proof, включая cleanup/GC

<!-- extractor-task:E05-03.full_crash_matrix -->

Этап: E05. Роль: QA / Engineer. Применимость/модуль: `test/composed_faults`; ETH/BSC/POLY.

Зависимости: E03-05.recovery_matrix, E05-03.upgrade, E05-04.concurrent_fencing, G.E04.

Результат: Полный native→WAL→Gateway→NATS crash/reorg/finality/restart proof, включая cleanup/GC; только здесь combined REC-T01

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-CAP-10, REQ-DLV-07, REQ-DLV-13, REQ-LIF-04, REQ-SCP-03. AC: AC-SCP-03-02, AC-CAP-10-01, AC-LIF-04-01, AC-LIF-04-02, AC-DLV-07-03, AC-DLV-13-03.

Контракт: NATIVE, DELIVERY, SEM, EVID. Inputs: E05-04.concurrent_fencing → race evidence; E05-03.upgrade → version recovery; E03-05.recovery_matrix → delivery-only evidence.

## E06-07.yaml — Strict role config/effective native preflight и side-effect-free check

<!-- extractor-task:E06-07.yaml -->

Этап: E06. Роль: DevOps. Применимость/модуль: `config`; ETH/BSC/POLY.

Зависимости: E02-02.codec, E05-03.drain, G.E05.

Результат: Strict role config/effective native preflight и side-effect-free check; storage paths от YAML, Polygon sources_file absolute-only; только declared keys/profiles/modes, binding_file — desired descriptor

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-01, REQ-SCP-01. AC: AC-SCP-01-01, AC-SCP-01-02, AC-OPS-01-01, AC-OPS-01-04, AC-OPS-01-05.

Контракт: CONFIG, CONTROL. Inputs: E02-02.codec → descriptor parser; E05-03.drain → mode contract.

## E06-13.artifact_lock — User artifact manifest: source/patch/toolchain/image/genesis identity

<!-- extractor-task:E06-13.artifact_lock -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/artifacts`; ETH/BSC/POLY.

Зависимости: E01-01.builds, E06-07.yaml, G.E05.

Результат: User artifact manifest: source/patch/toolchain/image/genesis identity; неправильный artifact отвергается до init

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: BOOT, STACK. Inputs: E01-01.builds → build manifest; E06-07.yaml → profile identity.

## E06-13.init — Idempotent phase-driven native init

<!-- extractor-task:E06-13.init -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/init`; ETH/BSC/POLY.

Зависимости: E06-07.yaml, E06-13.artifact_lock, G.E05.

Результат: Idempotent phase-driven native init; повтор/partial init сохраняет keys/state и проверяет genesis/ENV_READY

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-03. AC: AC-OPS-03-01, AC-OPS-03-03.

Контракт: BOOT. Inputs: E06-13.artifact_lock → verified artifacts; E06-07.yaml → paths/profile.

## E06-13.topology — 11 services/14 volumes с проверяемыми backing owners

<!-- extractor-task:E06-13.topology -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/compose+kubernetes`; ETH/BSC/POLY.

Зависимости: E06-07.yaml, E06-13.init, G.E05.

Результат: 11 services/14 volumes с проверяемыми backing owners; adapter/Gateway и Polygon L1 изолированы

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-02. AC: AC-OPS-02-01, AC-OPS-02-02.

Контракт: TOPO, BOOT. Inputs: E06-13.init → initialized homes; E06-07.yaml → role stores.

## E06-13.secrets — CA/SAN/EKU/role/network admission, private keys/JWT/credentials и rotation

<!-- extractor-task:E06-13.secrets -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/trust`; ETH/BSC/POLY.

Зависимости: E06-07.yaml, G.E05.

Результат: CA/SAN/EKU/role/network admission, private keys/JWT/credentials и rotation; bootstrap admin не попадает Gateway

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-05. AC: AC-OPS-05-02, AC-OPS-05-03.

Контракт: BOOT, CONFIG. Inputs: E06-07.yaml → connection roles.

## E06-13.nats_bootstrap — Создание/проверка accounts/streams/ACL и D77 identity metadata

<!-- extractor-task:E06-13.nats_bootstrap -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/nats`; COMMON.

Зависимости: E03-03.destination, E06-13.secrets, G.E05.

Результат: Создание/проверка accounts/streams/ACL и D77 identity metadata; CREATE/read-back, отказ adoption missing keys, compatible UPDATE сохраняет посторонние keys; runtime без admin/reset

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-06. AC: AC-OPS-06-02, AC-OPS-06-04, AC-OPS-06-05, AC-OPS-06-06.

Контракт: BOOT, CONTROL. Inputs: E06-13.secrets → trust material; E03-03.destination → expected binding.

## E06-13.network — Port mapping/advertise/internal links, включая Bor observer→собственная supporting L1

<!-- extractor-task:E06-13.network -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/network`; ETH/BSC/POLY.

Зависимости: E06-13.secrets, E06-13.topology, G.E05.

Результат: Port mapping/advertise/internal links, включая Bor observer→собственная supporting L1; наружу только P2P, localhost/чужая L1 identity отвергнуты

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-07. AC: AC-OPS-07-01, AC-OPS-07-02, AC-OPS-07-03.

Контракт: PORTS, BOOT, TOPO. Inputs: E06-13.topology → services; E06-13.secrets → authenticated connections.

## E06-08.config_replace — Config replacement меняет только allowlist, применим после restart

<!-- extractor-task:E06-08.config_replace -->

Этап: E06. Роль: DevOps. Применимость/модуль: `deploy/config_generation`; ETH/BSC/POLY.

Зависимости: E06-07.yaml, E06-13.init, G.E05.

Результат: Config replacement меняет только allowlist, применим после restart; immutable state/keys/genesis не перетираются

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-04. AC: AC-OPS-04-01, AC-OPS-04-02.

Контракт: BOOT, CONFIG. Inputs: E06-13.init → phase state; E06-07.yaml → validated desired config.

## E06-08.maintenance — Исполнимый пользовательский stop-confirm-start/drain/switch/Resume/upgrade сценарий, включая shared-volume и d

<!-- extractor-task:E06-08.maintenance -->

Этап: E06. Роль: QA / Engineer. Применимость/модуль: `test/user_maintenance`; ETH/BSC/POLY.

Зависимости: E05-03.upgrade, E06-08.config_replace, E06-13.nats_bootstrap, E06-13.network, G.E05.

Результат: Исполнимый пользовательский stop-confirm-start/drain/switch/Resume/upgrade сценарий, включая shared-volume и delayed-writer ошибки

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DLV-11, REQ-DLV-14, REQ-OPS-08. AC: AC-DLV-11-01, AC-DLV-14-03, AC-OPS-08-02.

Контракт: BOOT, CONFIG, DELIVERY, CONTROL. Inputs: E06-08.config_replace → config generations; E06-13.network → reachable services; E06-13.nats_bootstrap → target; E05-03.upgrade → version rules.

## E06-09.live_start — Clean startup/sync→H+1 и independent pending

<!-- extractor-task:E06-09.live_start -->

Этап: E06. Роль: QA / Engineer. Применимость/модуль: `test/user_startup`; ETH/BSC/POLY.

Зависимости: E06-08.config_replace, E06-13.nats_bootstrap, E06-13.network, G.E05.

Результат: Clean startup/sync→H+1 и independent pending; NATS outage не блокирует safe capture, restart одной сети не требует остальных

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-08. AC: AC-OPS-08-01.

Контракт: CONFIG, NATIVE, BOOT. Inputs: E06-13.network → links; E06-13.nats_bootstrap → NATS; E06-08.config_replace → effective configs.

## E06-12.logs — Обязательные structured control states/errors при debug OFF

<!-- extractor-task:E06-12.logs -->

Этап: E06. Роль: DevOps. Применимость/модуль: `config/control_logs`; ETH/BSC/POLY.

Зависимости: E06-08.maintenance, E06-09.live_start, G.E05.

Результат: Обязательные structured control states/errors при debug OFF; secrets отсутствуют, SIGKILL не сообщает graceful STOPPED

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-09. AC: AC-OPS-09-01, AC-OPS-09-02.

Контракт: BOOT, CONFIG, DELIVERY. Inputs: E06-09.live_start → startup states; E06-08.maintenance → maintenance/error states.

## E06-13.clean_bootstrap — Полный user-stack reproduction evidence из чистого окружения

<!-- extractor-task:E06-13.clean_bootstrap -->

Этап: E06. Роль: QA / Engineer. Применимость/модуль: `test/deployment`; ETH/BSC/POLY.

Зависимости: E06-12.logs, E06-13.artifact_lock, E06-13.topology, G.E05.

Результат: Полный user-stack reproduction evidence из чистого окружения; ранний harness не подменяет комплект

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-OPS-10. AC: AC-OPS-10-02.

Контракт: BOOT, TOPO, EVID. Inputs: E06-12.logs → diagnosable states; E06-13.artifact_lock → artifacts; E06-13.topology → owner map.

## E07-02.e2e — End-to-end native correctness/fault verdict каждой сети с независимым expected

<!-- extractor-task:E07-02.e2e -->

Этап: E07. Роль: QA / Engineer. Применимость/модуль: `test/acceptance`; ETH/BSC/POLY.

Зависимости: E05-03.full_crash_matrix, E06-13.clean_bootstrap, G.E06.

Результат: End-to-end native correctness/fault verdict каждой сети с независимым expected; source-only анализ не считается runtime PASS

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DEV-02. AC: AC-DEV-02-01, AC-DEV-02-02, AC-DEV-02-03, AC-DEV-02-04, AC-DEV-02-05, AC-DEV-02-06, AC-DEV-02-07, AC-DEV-02-08, AC-DEV-02-09, AC-DEV-02-10.

Контракт: EVID, MSG, SEM. Inputs: E05-03.full_crash_matrix → combined inputs/evidence; E06-13.clean_bootstrap → user environment.

## E07-02.scope — Trace boundary native→NATS, отсутствие лишних downstream/HA/history/performance обязательств

<!-- extractor-task:E07-02.scope -->

Этап: E07. Роль: QA / Engineer. Применимость/модуль: `test/scope_audit`; ETH/BSC/POLY.

Зависимости: E07-02.e2e, G.E06.

Результат: Trace boundary native→NATS, отсутствие лишних downstream/HA/history/performance обязательств; optional scope явно отделён

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-SCP-02, REQ-SCP-06. AC: AC-SCP-02-01, AC-SCP-06-01.

Контракт: EVID, MSG, CONFIG. Inputs: E07-02.e2e → observed product path.

## E07-03.runbook — Проверяемый запуск/остановка/диагностика/recovery drill по фактическим states

<!-- extractor-task:E07-03.runbook -->

Этап: E07. Роль: Protocol Engineer. Применимость/модуль: `docs/runbook`; ETH/BSC/POLY.

Зависимости: E06-08.maintenance, E06-12.logs, E07-02.e2e, G.E06.

Результат: Проверяемый запуск/остановка/диагностика/recovery drill по фактическим states; команды не требуют скрытого окружения

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: BOOT, CONFIG, DELIVERY. Inputs: E07-02.e2e → accepted path; E06-12.logs → observable states; E06-08.maintenance → operating procedure.

## E07-08.isolation — Матрица add/config/stop/restart одной сети без изменения соседних writers/stores

<!-- extractor-task:E07-08.isolation -->

Этап: E07. Роль: QA / Engineer. Применимость/модуль: `test/network_isolation`; ETH/BSC/POLY.

Зависимости: E07-02.e2e, G.E06.

Результат: Матрица add/config/stop/restart одной сети без изменения соседних writers/stores; общий NATS не означает shared journal

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: TOPO, DELIVERY, EVID. Inputs: E07-02.e2e → running acceptance topology.

## E07-09.nats_acceptance — Финальный NATS corpus: fields/parts/lifecycle/empty/duplicates/PubAck согласованы с native expected

<!-- extractor-task:E07-09.nats_acceptance -->

Этап: E07. Роль: QA / Engineer. Применимость/модуль: `test/nats_reader`; ETH/BSC/POLY.

Зависимости: E07-02.e2e, E07-02.scope, G.E06.

Результат: Финальный NATS corpus: fields/parts/lifecycle/empty/duplicates/PubAck согласованы с native expected; production consumer не создаётся

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

Контракт: MSG, CONTROL, EVID. Inputs: E07-02.e2e → native expected; E07-02.scope → boundary.

## E07-09.requirement_verdict — Полная CaseRegistry/CaseResult матрица полных identities D82

<!-- extractor-task:E07-09.requirement_verdict -->

Этап: E07. Роль: QA / Engineer. Применимость/модуль: `test/evidence`; COMMON.

Зависимости: E07-03.runbook, E07-08.isolation, E07-09.nats_acceptance, G.E06.

Результат: Полная CaseRegistry/CaseResult матрица полных identities D82; individual run outcomes, mandatory missing/FAIL/NOT_RUN блокируют gate, исключения требуют rules

Приёмка: Выход и invariant проверены по source task, назначенным AC и artifact acceptance TASK_DECOMPOSITION; independent review exact commit и обязательные проверки PASS. Профили mainnet/testnet не подменяют друг друга.

REQ: REQ-DEV-04, REQ-DEV-05. AC: AC-DEV-04-01, AC-DEV-04-04, AC-DEV-04-05, AC-DEV-04-06, AC-DEV-04-07, AC-DEV-04-08, AC-DEV-05-01, AC-DEV-05-02, AC-DEV-05-03, AC-DEV-05-04, AC-DEV-05-05, AC-DEV-05-06.

Контракт: EVID. Inputs: E07-09.nats_acceptance → output evidence; E07-08.isolation → isolation evidence; E07-03.runbook → operating evidence.

## G.E01 — Принять результаты этапа E01

<!-- extractor-task:G.E01 -->

Этап: E01. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E01-01.evidence, E01-01.builds, E01-01.harness, E01-02.pins, E01-02.inputs, E01-02.owned_capture, E01-02.admission, E01-02.decision_bridge, E01-02.lifecycle_seams, E01-03.smoke_corpus, E01-04.early_faults, E01-05.type_paths.

Результат: все перечисленные обязательные выходы отдельно ETH/BSC/POLY. Это ранний seam proof; transport, вся семантика и combined fault matrix ещё не приняты.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## G.E02 — Принять результаты этапа E02

<!-- extractor-task:G.E02 -->

Этап: E02. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E02-01.semantic_descriptors, E02-05.actions_model, E02-08.block_metadata, E02-10.closed_profiles, E02-02.schemas, E02-02.codec, E02-03.lifecycle_validator, E02-04.compatibility.

Результат: все обязательные схемы и локальные contract cases определены и проверяемы. Native correctness расширяется в E04; реальная delivery integration — E03.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## G.E03 — Принять результаты этапа E03

<!-- extractor-task:G.E03 -->

Этап: E03. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E03-01.producer_store, E03-01.gateway_store, E03-02.rpc_streaming, E03-02.fencing, E03-03.destination, E03-03.publisher, E03-01.cleanup, E03-03.maintenance, E03-04.pressure_errors, E03-05.recovery_matrix, E03-11.block_lifecycle.

Результат: producer/Gateway/NATS delivery guarantees от заданного durable входа. Native admission seam принят отдельно в E01; комбинированный reorg/finality/capture crash proof остаётся E05.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## G.E04 — Принять результаты этапа E04

<!-- extractor-task:G.E04 -->

Этап: E04. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E04-01.calls, E04-01.receipts, E04-01.authorization, E04-01.block_record, E04-03.state_diff, E04-01.selfdestruct, E04-03.balance_actions, E04-04.import_pressure, E04-02.optional_scope.

Результат: обязательные current-network semantic cases. Default OFF case обязателен; включённая optional-ветка условна. Historical coverage и performance не выводятся из этого gate.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## G.E05 — Принять результаты этапа E05

<!-- extractor-task:G.E05 -->

Этап: E05. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E05-01.registry, E05-01.eth_finality, E05-01.bsc_finality, E05-01.polygon_observer, E05-01.activation_gc, E05-02.reorg, E05-03.drain, E05-03.upgrade, E05-04.concurrent_fencing, E05-03.full_crash_matrix.

Результат: полные combined guarantees каждой сети. Ранняя карта callers E01 и delivery-only E03 остаются самостоятельными evidence, не переименовываются в полный proof.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## G.E06 — Принять результаты этапа E06

<!-- extractor-task:G.E06 -->

Этап: E06. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E06-07.yaml, E06-13.artifact_lock, E06-13.init, E06-13.topology, E06-13.secrets, E06-13.nats_bootstrap, E06-13.network, E06-08.config_replace, E06-08.maintenance, E06-09.live_start, E06-12.logs, E06-13.clean_bootstrap.

Результат: полный комплект по обязательным BOOT/CONFIG/MOUNT/TRUST/NATS/START/REPLACE/LOG/ARTIFACT cases. Это не публичная упаковка и не production/performance методика.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## G.E07 — Принять результаты этапа E07

<!-- extractor-task:G.E07 -->

Этап: E07. Роль: Reviewer / QA. Применимость/модуль: ALL: семь profiles.

Зависимости: E07-02.e2e, E07-02.scope, E07-03.runbook, E07-08.isolation, E07-09.nats_acceptance, E07-09.requirement_verdict.

Результат: все обязательные AC закрыты по своей применимости и итоговая матрица непротиворечива. По D84 gates исполняются последовательно; до реальных запусков результаты NOT_RUN.

Приёмка: Все child tasks приняты, profile/variant matrix текущего stage полна; evidence относится к фактическим pins/commits. Промежуточный proof не переименован в окончательный. Следующий этап заблокирован до PASS.

REQ: artifact/gate criteria. AC: artifact/gate criteria, не пропуск проверки.

## Публикация и состояние

Все 84 issues опубликованы: [прямой mapping ID → GitHub URL](ISSUE_MAP.md), номера #1–#84. Все открыты; DONE определяется регламентом, не наличием строки здесь. Повторный publish сверяет marker на всех страницах GitHub и не создаёт дубликаты.
