# Дерево решений и журнал интервью

## Delegated technical decision — D90 (2026-11-06)

BOOT-08 defines TEST-OPEN-02 under D84 within D82/D83's accepted guarantees. Accepted BOOT-06 supplies the candidate registry; accepted BOOT-07/D87 supplies finite reader completion. This decision requires independent exact-commit review before BOOT-08 is accepted. It is not a new owner approval, an accepted runtime client pin, or product runtime evidence.

The [concrete corpus](../testing/corpus/README.md) assigns all 217 ACs and 677 named baseline variants, plus explicit contract, source-fork/type/entrypoint, size, topology, compound-fault, pairwise, seed, soak, and public variants. The source projection binds exact candidate source commits and blobs. Deterministic expansion preserves existing AC/task/gate ownership and separate per-profile/suite/mode/dimension obligations. A static validator catches omissions and invalid allocations but never grants READY or runtime PASS.

Source groups resolve into exact native predicates and runtime intervals at E01/E06; block/time/total-difficulty/epoch clocks are distinct. Historical or future native seams do not prove current full E2E. Custom local genesis/config/validator/supporting identities remain test-only; production identity checks are not disabled. Missing legitimate native admission remains an explicit unresolved requirement.

Independent oracles use native source inventories, original consensus bytes, native state commitments/proofs, and separately authored contract ledgers/reference execution. Public smoke independently inventories every required logical identity in the closed D87 window and checks its declared source/header/receipt/delivery fields; it does not claim unverified internal trace semantics. Deeper public-contract assertions require actual independent prestate/environment evidence. The full local/native trace matrix remains mandatory.

Controlled contracts pin their source, compiler/build settings, EVM target, and actual init/runtime bytecode outputs per interval. Preparation may build expected fixtures but cannot count as SUT execution. Mainnet remains passive; testnet signers require exact identity, verified owned deployment, funding, nonce control and spending limits. Synthetic large-record/wire tests cannot close native E2E.

Initial floors per profile/current interval are eight seed indices with 256 actions each; standard soak of 600 seconds and 1,000 actions; explicitly extended soak of 3,600 seconds and 10,000 actions. A backlog case reaches 32 durable unacknowledged batches and max(64 MiB, twice configured collector queue capacity) in exact pending publication bytes. These are engineering starting floors, not measured throughput. Increased volumes/parallelism are configurable; reductions remain diagnostic PARTIAL with visible NOT_RUN cells. Every standard parent run still finishes all required phases within 1800 seconds, excluding preparation. Multiple standard runs may form a campaign; D83 retries and result invalidation remain unchanged.

The candidate deltas explicitly include Sepolia Amsterdam/Gloas, canceled Hoodi activation, BSC Jenner/CAS20/payment-lane rules and separate schedules, and Bor Uburu StateSync body/hash/receipt/event paths. Source declarations never substitute for feature activation or public signer authority. E01/E06 own actual ProfileManifest/genesis/compiled-fixture hashes and source applicability proofs; those outputs remain pending. E07 owns actual complete corpus execution. No E01 work, node runtime, public transaction, active source-pin change, or new product API/wire guarantee is authorized by this BOOT-08 change.

A generic example per family, normalizer-generated expected results, blanket N/A for old forks, silent quota reductions, and substituting synthetic/seam evidence for native E2E were rejected because they hide missing obligations. D90 defines the finite proof obligations without claiming exhaustive unbounded states. Product runtime remains NOT_RUN.

## Delegated technical decision — D87 (2026-11-06)

BOOT-07 defines finite NATS reader completion under D84's delegated authority. This is a technical decision within the accepted scope, not a new owner approval or a runtime result. Independent review of the exact candidate is required before BOOT-07 acceptance closes TEST-OPEN-01.

The test runner freezes a finite plan, preserves independent source evidence and durable delivery evidence, stops LIVE, and completes the existing drain protocol. The actual terminal native decision frontier includes admitted recovery work and stop races; every public decision maps to one whole batch. Existing DRAINED/SealAck/highwater evidence closes publication obligations. The harness stops publication workers, records all attempted publications, and seals only its isolated JetStream test streams. A seal acknowledgment alone is not a terminal write barrier. The default test fence stops every old NATS process and restarts the same stores/topology with publishing disabled, then proves recovered stream/log completion before sampling each upper bound. Exact version/topology conformance evidence is mandatory. This test-only fence is distinct from product SealGeneration and adds no production API, message, command, or guarantee.

Exact source-derived expected sets expose an omitted whole batch; the exhaustive attempt census and full stream coverage expose unexpected output and retention gaps; full fragment/batch reconstruction exposes partial data. A timeout does not resolve an attempted publication at the server. Exact duplicate retries are allowed only after byte equality checks; a retry rejected solely by the test fence is diagnostic only when its required part already has durable delivery and stored-byte proof. A rejected first part or changed/extra output does not gain this exception. A public reorg inside the realized window is included; a later reorg is outside that run and cannot make the finite test prove permanent canonicality. Cutover keeps separate destination evidence and does not copy acknowledged history. Reader completion, producer delivery proof, and semantic comparison remain separate conditions.

Deadlines never establish success. All run phases through comparison remain inside D83's standard 1800-second budget. Declared negative cases may prove specific rejection behavior without claiming successful delivery or reader completion. A diagnostic retry uses a separate clean run and cannot erase the original FAIL. The exact contract, ReaderBoundaryManifest fields, and required counterexamples are in [TESTING_CONTRACT](TESTING_CONTRACT.md); the canonical specification and executor mirror carry the same text.

Alternative quiet-period, block-height-only, received-count-only, and producer-manifest-only completion rules were rejected because they can miss late reorg events, fragments, extra data, or an omitted batch. Freezing the entire public chain is unnecessary: the test closes only the collector's admitted window. TEST-OPEN-02 remains assigned to BOOT-08. Product runtime and performance remain NOT_RUN; BOOT-07 document checks cannot accept E01 or E07.

## Latest decision — D88 (2026-11-06)

The owner reported that the OAuth authorization code did not arrive and offered SSH authentication to recover BOOT-05. Under the existing D84/D86 launch, use the owner's already configured SSH identity for Git transport in this repository. The coordinator verified GitHub authentication as `sigaev-pro` with `BatchMode=yes` and `StrictHostKeyChecking=yes`, then changed only this repository's origin to `git@github.com:sigaev-pro/bc-indexer.git`. No key material, global Git/SSH configuration, or gh authentication was changed. Keep gh for permitted issue, PR, and API operations. The pending OAuth workflow-scope refresh is no longer the chosen prerequisite for Git publication; any later API permission failure remains a separate blocker. This transport choice does not relax branch protection, exact-commit CI, independent review, or the stop before E01. Successful SSH authentication is not proof of a successful push or live CI; verify each remote step separately.

## Latest decision — D86 (2026-11-06)

The user said to start after the coordinator proposed completing BOOT and stopping before E01. This launch authorizes BOOT-01 through BOOT-09 under the existing issue, review, and commit rules, including the initial baseline push to the verified empty repository and issue/PR updates. Finish all independent bootstrap work that the available access and resources allow. Record external blockers without claiming completion. Stop after BOOT; accepting BOOT-09 does not authorize starting E01 in this run. A later stage requires a new explicit launch. Preserve this stage limit in launch instructions and persistent controller state.

## Latest decision — D85 (2026-11-06)

The user requires all 84 existing issues to be rewritten in clear, simple American English. All project-authored text must use English: issue titles/bodies/comments, PRs/reviews, code comments/docstrings, documentation, commits, test descriptions, CLI/help/error/log messages, and automation output. Avoid British expressions, regional idioms, jargon added for style, and vague promotional language. Preserve exact technical names, IDs, APIs, and source data. This task translates the issues, their local source catalog, the project rules, and the bootstrap prompt; it does not silently change product requirements or bulk-translate historical specifications. New project text must follow the rule immediately. Conversation with the user may continue in the language they use; project artifacts remain English.

## Последнее решение — D84 (2026-11-06)

Пользователь явно изменил направление: «мы переходим к разработке». Прежний запрет D64 на реализацию отменён этим решением; его исторические записи сохраняются как superseded. Разрешены код, сборки и runtime-тесты для принятого ТЗ в порядке E01–E07. До первого продуктового изменения весь известный объём должен быть разбит на issues, зависимости и gate issues; работа идёт поэтапно. Пользователь поручил отдельный регламент ролей agency-agents и автономного запуска без постоянного участия, выбрал GitHub sigaev-pro/bc-indexer для issues/PR. Текущий результат — runbook, полный каталог и опубликованные issues, а не молчаливый запуск многодневной разработки. Технические решения внутри принятого контракта делегируются; scope/необратимые внешние действия и внешние блокеры не обходятся. D70 и продуктовые требования сохраняются. TEST-OPEN-01/02 выделяются отдельными подготовительными issues с независимой проверкой; код следующих этапов не используется для сокрытия незавершённых критериев.

## Последнее решение — D83 (2026-11-06)

Пользователь поручил зафиксировать уточнения тестового контура. Предел 30 минут относится к самому стандартному прогону, не подготовке. Способ подготовки/запуска и объёмы настраиваются при сохранении обязательного покрытия. В ТЗ нужен отдельный скрипт проверки и обновления всех закреплённых компонентов до актуальных стабильных совместимых версий; floating latest запрещён. Само обновление выполняется в проектируемой системе через candidate lock и проверку, не автоматическую подмену working versions. Собственные контролируемые контракты на testnet разрешены как основной способ задания известных действий; известные контракты/транзакции дополняют corpus. Подробные независимые эталоны допустимо доработать позднее, без фиктивного PASS. Точный критерий завершения потока пользователь явно отложил («с этим разберемся потом»): TEST-OPEN-01 остаётся открытым условием полной приёмки. Принята политика одного чистого повтора: первый FAIL сохраняется, успешный retry означает нестабильность и не закрывает gate; после исправления нужен новый успешный прогон. Public smoke повторяется перед приёмкой релиза. D64/D70 сохраняются: сейчас только документация, никаких скриптов/контрактов продукта или runtime запусков.

## Последнее решение — D82 (2026-11-06)

Пользователь согласовал максимально широкий тестовый контур и поручил «реализуй в тз». Mainnet-only ограничение D42 расширено на явно перечисленные действующие официальные testnet Ethereum/BSC/Polygon; произвольная EVM-сеть автоматически не поддерживается. Для каждого профиля обязательны применимые сценарии, независимо подготовленные expected, реальный NATS и тестовый reader, локальные воспроизводимые native проверки и отдельный публичный smoke. Быстрые прогоны не выполняют публичную синхронизацию; подготовка артефактов и публичные проверки учитываются отдельно. Fault, границы данных, lifecycle, upgrades, изоляция, mutation sanity, длительные и seeded прогоны входят в каталог. NOT_RUN/FAIL не заменяются PASS; N/A требует обоснования. Совместимость testnet с закреплёнными клиентами остаётся обязательным proof, не объявляется проверенной по наличию network name. D64/D70 сохраняются: только ТЗ; reader — средство приёмки, не production consumer. Нормативный контракт и начальный реестр — TESTING_CONTRACT.md.

## Общая сверка после D81 (2026-11-05)

Пользователь поручил перепроверить всю документацию на согласованность. DOC-ALL-DOCS-VERIFY-01 сверяет весь набор файлов; это не новая поведенческая развилка. Редакционная коррекция сохраняет D57: fatal source regression останавливает native owner и не допускает новые события/GC конфликтующего решения; прежний корректный CLOSED batch независимого Gateway остаётся под обычным delivery protocol. Исправлены текущий счётчик AC и маркировка исторических материалов. Решения D16–D81, scope и версии схем не меняются; реализация/runtime/push не добавляются.

## Последнее решение — D81 (2026-11-05)

После объяснения оставшихся IR-01/IR-03 пользователь поручил: «проведи исследовение и сделай вывод и закоммить». Разрешены исследование источников, приведение пересказа CallFrame к уже принятому точному контракту, устранение устаревших заявлений о неопределённом проектировании, синхронизация документов и локальный commit. Новые гарантии, реализация, runtime-тесты и push не добавляются; D64/D70 сохраняются. Новое существенное расхождение источников с принятой семантикой потребовало бы отдельного решения, а не молчаливой смены контракта.

## Предыдущее решение — D80 (2026-11-05)

IR-02: пользователь выбрал «Добавить поле в оба типа (рекомендую)». В SourceObservationRef и L1Checkpoint обязателен sources_binding_digest:Hash32 — SHA-256 проверенных bytes файла источников, закреплённых для соответствующего observation. Повторная отправка сохраняет прежний digest; замена текущего файла не переписывает старое evidence. Допустимый narrower whitelist без source_evidence не получает выдуманного digest. Разрешены синхронные изменения ТЗ, схемы полей, сценариев и task mapping; D64/D70 сохраняются. Этот ответ закрывает выбор IR-02, но не является отдельным согласованием IR-01/IR-03.

## Поручение на независимую рецензию (2026-11-05)

После завершения D79 пользователь ответил «приступай» на предложение независимой рецензии. Разрешена повторная документальная проверка принятого ТЗ. Это не акцепт ещё не предъявленных новых требований: предложения IR-01–IR-03 записываются отдельно от принятых решений. D64/D70 сохраняются; реализация, runtime-проверки и push не запрошены.

## Предыдущее решение — D79 (2026-11-05)

Пользователь сообщил: «я акцептую все твои решения, завершай». Утверждён оставшийся пакет конкретных предложений полного аудита: F02 (decision batch), F04 (AC/variant evidence), F05 (точные task IDs), F06 (Bor→supporting L1 network edge), F07 (полный account/storage diff в кратком ТЗ), F08 (retention ACK до ReleaseAck), F09 (границы immutable READY/SEALED), F10 (удаление устаревшего active scope) и F14 (актуальный pinned Geth source). Отдельные вопросы по каждому из этих предложений больше не нужны; разрешено завершить исправления, синхронизацию и финальную документальную проверку.

F11/F03/F12/F13/F01 ранее приняты и исправлены. Акцепт не меняет постоянную границу D64: реализации не будет никогда; D70: проектируемый результат заканчивается стандартизированными данными в NATS. Разрешены локальные документальные commits, push не запрошен. Непроверенные runtime-свойства не становятся PASS по этому решению.

Дополнительное подтверждение D79/F02 (2026-11-05, после commit 5267911): пользователь отдельно ответил «Принять исправление F02 (рекомендую)». Одно native decision с публичными событиями создаёт один delivery batch со всеми событиями и общим подтверждением доставки, включая multi-block reorg и finality-only. Decision без публичных событий сохраняет checkpoint без batch; техническое дробление не меняет границу batch. Это подтверждает уже внесённый контракт, новых требований или открытых согласований не добавляет.

## Предыдущее решение — D78 (2026-11-05)

Пользователь ответил «Принять исправление F01 (рекомендую)». Относительные storage paths допустимы и разрешаются относительно каталога YAML, не рабочего каталога запуска. Общий отрицательный AC больше не отвергает любой relative path. Для finality.polygon.sources_file сохраняется отдельное требование абсолютного пути; оба случая получают отдельные проверки. Config check по-прежнему ничего не создаёт. Требование абсолютных путей везде не принято.

Разрешено исправить F01 в документации и приёмке. F11/F03/F12/F13 уже закрыты; остальные 9 находок требуют отдельного решения. D64/D70 неизменны.

## Предыдущее решение — D77 (2026-11-05)

Пользователь ответил «Принять контракт F13 (рекомендую)». Обязательные project-owned NATS stream metadata keys: extractor.network_digest (идентичность сети) и extractor.destination_id (идентификатор назначения). Gateway точно сверяет их с DestinationBinding; missing/mismatch блокирует публикацию с CONFIG_BLOCKED. Bootstrap создаёт/проверяет поля и сохраняет посторонние metadata; автоматические reset/purge запрещены. Account, stream name, subjects, поколения и descriptor digest проверяются отдельно по уже принятому протоколу. Включать binding generation/digest в stream metadata не требуется.

Разрешено завершить F13-контракт в нормативных документах, обоих ТЗ и приёмке. F11/F03/F12 уже закрыты; остальные 10 замечаний требуют отдельного обсуждения. D64 — исключительно документация; D70 — результат до подтверждённой передачи в NATS.

## Предыдущее решение — D76 (2026-11-05)

Пользователь подтвердил уточнение F12/F11: «Да, потоковый вывод с точным совпадением bytes (рекомендую)». Штатный pinned native MarshalBinary задаёт эталон consensus-кодирования, но его API с полным byte array не обязателен для collector path. Разрешён потоковый вывод из исходного native Receipt в disk spool, включая адаптацию кодека для bounded memory, при побайтно идентичном результате. Реконструкция из нормализованных полей запрещена; native execution/consensus bytes не меняются. Это уточнение документации F12, не разрешение реализации. Остальные 11 замечаний не согласованы.

## Предыдущее решение — D75 (2026-11-05)

Пользователь ответил «Принять модель F12 (рекомендую)». Для обязательных body-транзакций сохраняются bytes штатного pinned native consensus-кодека: LEGACY_CONSENSUS_RLP либо TYPED_CONSENSUS_ENVELOPE с исходным receipt type byte, без реконструкции из нормализованных полей. Optional Bor legacy storage receipt использует отдельный BOR_LEGACY_STORAGE_RLP. Общие поля Receipt сохраняются; дополнительные поля первого профиля явно отсутствуют у ETH/BSC, у Bor сохраняются определённые source_log_scopes. Новые native extras требуют обновления registry/profile, не opaque map.

Разрешено завершить точный F12-контракт и синхронно обновить нормативные документы, оба ТЗ и приёмку. F11/F03 уже закрыты; остальные 11 замечаний требуют отдельного решения. D64 — только документация навсегда; D70 — граница результата в NATS.

## Предыдущее решение — D74 (2026-11-05)

Пользователь ответил «Принять модель F03 (рекомендую)» на предложение из FULL_AUDIT_APPROVAL_2026_11_05.md. Приняты единый checked u64 execution_order внутри транзакции; общий номер только у явно связанных представлений одной операции; собственные action/frame IDs и точные области operation_index; публичные RollbackScope с parent/owner/result и уникальными нормализованными IDs вместо переиспользуемых native snapshot IDs. Native fees/nonce и другие пережившие откат изменения сохраняются в соответствующей области; ancestor rollback учитывается в итоговом effects_committed.

Разрешено завершить точный контракт F03, синхронизировать нормативные документы, оба ТЗ и приёмку. F11 уже закрыт D73; остальные 12 замечаний требуют отдельного обсуждения. D64 — только документация навсегда, D70 — результат до подтверждённой доставки в NATS.

## Предыдущее решение — D73 (2026-11-05)

По F11 пользователь выбрал «Типизированные фрагменты (рекомендую)». Одна логическая запись представляется несколькими ограниченными типизированными Protobuf-сообщениями до сериализации, с общим ID, определённым порядком, контролем целостности и подтверждением всех частей в NATS. Полные данные сохраняются; ни общий объём записи, ни отдельное крупное поле не требуют одного гигантского protobuf-message. Усложнение схемы и правил сборки принято. Альтернатива прикладного предела размера записи с остановкой при превышении не выбрана; существующие checked counters/дисковые ограничения не отменяются.

Разрешено завершить и согласованно внести точный F11-контракт в нормативные документы, оба ТЗ и приёмку. Остальные 13 замечаний не утверждены этим ответом и остаются для последовательного обсуждения. Граница D70 — доставка в NATS, без создания downstream consumer; D64 — только проектирование и документация.

## Предыдущее решение — D72 (2026-11-05)

На предложение согласовать весь пакет из 14 исправлений пользователь ответил: «Разбирать по одному, начиная с больших записей». Утверждён только порядок обсуждения: первым F11. Ни весь пакет, ни рекомендуемый вариант типизированных фрагментов этим ответом не согласованы. Сначала объяснить проблему и варианты, получить выбор, затем конкретизировать и исправлять только принятый пункт. Остальные замечания не исправлять автоматически. D64/D70 сохраняются.

## Предыдущее решение — D71 (2026-11-05)

Пользователь поручил изучить проект «до молекул», выявить несостыковки и предъявить их на согласование и исправление. Выполнить полный документальный аудит, показать доказательства и конкретные варианты исправлений. Запрос не считается согласием на ещё не предъявленные изменения поведения. Согласованная граница D70 — стандартизированные данные в NATS; downstream и его экономика не возвращаются в scope. D64: только проектирование и документация.

## Предыдущее решение — D70 (2026-11-05)

Пользователь уточнил: «суть в передаче данных в NATS.IO в стандартизированном формате, на этом задача заканчивается». Это подтверждение границы результата компонента: получение предусмотренных ТЗ данных, нормализация по единой версионированной схеме проекта и подтверждённая доставка в ранее выбранный NATS JetStream. Работа получателя, прикладная бизнес-польза и экономика downstream-систем не являются условиями завершения или приёмки этого ТЗ. Предыдущая CEO/CFO-оценка вышла за границу задачи в этой части; соответствующие блокирующие выводы отзываются.

Подтверждение доставки не подменяет проверку формата/семантики и не отменяет внутренний протокол ACK/cleanup/recovery либо последующие предусмотренные lifecycle-события. Стандартизированный формат означает согласованный контракт проекта; новый внешний отраслевой стандарт не выбран. D64 сохраняется: наша работа — только документация. Технические замечания аудита D68 сохраняют отдельный статус и не исправляются молча вместе с уточнением scope.

## Предыдущее решение — D69 (2026-11-05)

Пользователь запросил очень жёсткую оценку проекта от лица CEO и CFO. Подготовить управленческую оценку двух ТЗ: бизнес-результат, обоснование затрат/сложности, последствия принятых ограничений и готовность к принятию решения. Это аналитический запрос, без изменения scope, бюджета или согласованных требований. Не приписывать проекту отсутствующие доходы, суммы и сроки. D64 сохраняется: реализации никогда не будет в рамках нашей работы.

## Предыдущее решение — D68 (2026-11-05)

Пользователь запросил изучение обоих адресных файлов ТЗ и оценку, всё ли в них идеально. Выполнить документальный аудит и представить конкретные недостатки с доказательствами. Это запрос проверки, без изменения согласованного поведения; исправления ТЗ в этом подэтапе не выполняются. D64 и скрытое расположение вспомогательных материалов по D67 сохраняются.

## Предыдущее решение — D67 (2026-11-05)

Пользователь поручил скрыть всё, кроме двух адресных файлов ТЗ. В корне остаются видимыми TZ_DECISION_MAKER.md и TZ_EXECUTOR.md; прочие документы, исходники ТЗ, журнал решений и служебные правила сохраняются в скрытой папке .project. Это изменение расположения, без удаления материалов или изменения требований. Ссылки должны продолжать работать; D64 сохраняется.

## Предыдущее решение — D66 (2026-11-05)

Пользователь запросил два отдельных файла на языке, понятном человеку и ИИ: для принимающего решения и для исполнителя решений, второй — подробно. Подготовить самостоятельные адресные представления согласованного ТЗ: первое объясняет результат, границы, принятые решения и критерии принятия; второе содержит порядок чтения, пояснения терминов, полные технические контракты и декомпозицию с приёмкой.

Согласованное поведение и ID требований/сценариев/задач не меняются. Каноническая редакция v3 сохраняется; адресные файлы должны соответствовать ей и не создавать конкурирующих требований. D64 действует: это документация проектируемой системы, реализация никогда не входит в нашу работу.

## Предыдущее решение — D65 (2026-11-05)

Пользователь вызвал grill-with-docs, затем уточнил задачу: полностью финализировать ТЗ, чтобы последующий grill был полноценным. Работа охватывает весь согласованный scope, полноту сценариев, ясность терминов, непротиворечивость контрактов, краткость основного текста и пригодность к разбиению на задачи. Применяются grilling и domain-modeling; уже принятые решения не открываются повторно без конкретного противоречия.

Разрешена документальная финализация и фиксация результатов проверки. Предложенные ранее 5–8 страниц и конкретная структура приложений не считаются отдельно утверждённым требованием. Техническую редактуру выполнять без изменения согласованного поведения; существенные неразрешённые развилки вынести пользователю. D64 сохраняется: реализации не будет никогда, планы и проверки runtime остаются только содержанием ТЗ.

## Предыдущее решение — D64 (2026-11-05)

Пользователь установил постоянную границу проекта: «реализации не будет никогда, наша задача сводится к идеальному ТЗ». Работа ограничена проектированием, исследованием источников, выявлением противоречий, уточнением требований и доведением документации до полного, однозначного, проверяемого ТЗ. Реализация продукта, сборки, runtime-тесты и deployment не входят в эту работу; переход к ним не планируется и не предлагается. Это постоянная цель проекта, а не временное ожидание разрешения на код.

E01–E07, runtime proofs и критерии приёмки сохраняются как содержание ТЗ для описываемой системы, не как исполняемый backlog этой работы. Прежние указания «после отдельного разрешения начать E01» отменены. Документальные проверки и локальные Git-коммиты сохраняются. Исторические решения и checkpoints не дают основания вернуться к реализации.

## Предыдущее решение — D63 (2026-11-05)

Пользователь принял критерий автоматической очистки Polygon recovery-state: checkpoint должен быть подтверждён finalized-состоянием собственной supporting Ethereum L1-ноды Polygon. Native транзакции и milestone/checkpoint статусы публикуются без ожидания этого условия; задерживается только GC. Native milestone alone не является необратимым anchor, поскольку Bor допускает его purge при восстановлении. Нового внешнего oracle/consumer не вводится; используются уже предусмотренные Polygon L1 Geth/Prysm и native checkpoint commitment. Точные поля/проверка commitment и границы гарантии фиксируются в finality contract; содержимое checkpoint не переименовывается в криптографическое доказательство полного Bor header/state root.

## Предыдущее решение — D62 (2026-11-05)

Пользователь принял два уточнения чистового ТЗ:
1. BSC передаёт и раннюю финальность из голосов в памяти, и evidence, закреплённое аттестациями в блоках, с явным provenance. Ранний сигнал сам по себе не разрешает GC recovery-state; нужна закреплённая native evidence. Потеря transient evidence после restart не равна доказанному откату canonical chain.
2. Комиссионные движения сохраняются раздельно: фактическое списание на покупку газа, денежный возврат неиспользованного остатка и зачисления получателям. Итоговая комиссия отдельно в receipt; её нельзя повторно прибавлять к этим изменениям. Gas refund units не подменяют денежный возврат. Требуется явная модель адресных effects и проверка отсутствия двойного учёта.

## Предыдущее решение — D61 (2026-11-05)

Пользователь поручил довести пакет до чистового ТЗ после перечисления оставшихся работ: устранить старые/противоречивые формулировки, определить точные current-network contracts, согласовать drain/cutover с YAML/CLI, связать все обязательные требования с проверками и установить единую нормативную редакцию. Это разрешает проектирование и изменения документации, не код/runtime/deployment/push. Новые поведенческие развилки требуют вопроса; ранее принятые D16–D60 не открываются повторно. Начат DOC-FINAL: отдельные проверенные/закоммиченные подэтапы, исходный v2 неизменен.

## Предыдущее решение — D60 (2026-11-05)

Пользователь подтвердил отключение BlockSTM в первом capture-профиле Bor: транзакции исполняются последовательно. Это отдельное ограничение от pipelined/stateless import и preconfirmations D58. Native parallel-EVM enable/enforce выключаются; возврат BlockSTM требует отдельного проекта capture спекулятивных попыток и доказательства корректных trace/before/after. Consensus/fork results не меняются, performance-гарантия не добавляется. Source-verified flags: `--parallelevm.enable=false --parallelevm.enforce=false`; runtime-проверка остаётся E01/E04.

## Предыдущее решение — D59 (2026-11-05)

Пользователь подтвердил границу смены назначения: после drain уже NATS_ACKed Applied/история не копируются в новый stream. Новые Removed/Status могут ссылаться на Applied, доставленный в прежнее назначение. Подготовка принимающей стороны и перенос подтверждённой истории вне сборщика; автоматического повторного экспорта и сброса lifecycle identity ради самостоятельной полноты нового stream нет. D54/D57 сохраняются: неподтверждённые обязательства сначала завершаются в старом назначении.

## Предыдущее решение — D58 (2026-11-05)

Пользователь принял четыре уточнения DOC-REPAIR:
1. Временно сохранять native state и входы, необходимые для восстановления уже начатого capture; несовместимые pruning/state backend настройки отклонять до запуска. Это не полный архив и не поиск/догрузка истории. Конкретный capability profile и retention handshake определяются техническим проектом.
2. Заменить только кандидата Heimdall v0.12.2 на v0.13.2: [официальный релиз](https://github.com/0xPolygon/heimdall-v2/releases/tag/v0.13.2) требует обновления после активации Karwar. Bor и остальные версии D47 сохраняются. Source/config audit новой версии обязателен; выбор не означает выполненную сборку/runtime.
3. Первый capture-профиль Bor использует обычный проверяемый native import; pipelined import, parallel stateless import и preconfirmations отключены. Возврат ускоренных путей — после отдельного доказательства capture, не скрытый default. Consensus/fork semantics не изменяются.
4. Разрешена техническая пауза в безопасной точке между шагами VM для backpressure worker. Hooks не выполняют дисковые/сетевые операции, native результат исполнения не меняется. Это уточняет D57: ожидание возможно не только на границе импорта, но и в специально определённой VM-safe точке; отсутствие потери данных и bounded RAM сохраняются.

## Предыдущее решение — D57 (2026-11-05)

Пользователь принял три поведения при DOC-REPAIR:
1. Невосстановимая ошибка WAL или нормализации встроенного адаптера завершает весь native процесс соответствующей сети с явной ошибкой. Временное заполнение очереди — ожидание на границе импорта вне VM hooks; это не разрешение пропускать данные. Остальные сети не останавливаются.
2. Drain — отдельный режим запуска той же native ноды: восстановление сохранённых обязательств и доставка, без начала нового live-сбора; после завершения очередей и очистки служебного состояния обоих owners процесс завершается. Постоянный управляющий API и переключение работающей ноды не требуются. Сохраняемые identity/lifecycle и native chain state не удаляются под видом drain.
3. Совместимые версии продолжают старый pending без изменения bytes. Перед несовместимым обновлением — drain старой сборкой. Автоматические миграции WAL/pending и откат уже изменённого несовместимого формата в первой версии не поддерживаются; отказ должен происходить до записи.

Это принятые операционные границы, не готовая реализация. Технические state machines, конфигурация, совместимость и fixtures описываются в DOC-REPAIR. D53/D54 сохраняются; только проектирование/документация.

## Предыдущее решение — D56 (2026-11-05)

После уточнения, что прежний вопрос о числе дефектов не разрешал их исправление, пользователь теперь явно поручил прорабатывать все находки с целью быстро получить результат и задавать вопросы при малейших сомнениях. Разрешены проектирование и изменения документации; реализация/runtime/deployment/push по-прежнему не разрешены. Начат DOC-REPAIR: отдельные завершённые подэтапы, проверки, фиксация состояния и commit до перехода дальше. Конкретные технические исправления в принятом scope выполняются; неоднозначное поведение/гарантии выносятся пользователю до зависимых изменений.

Первый подэтап DOC-REPAIR-01: source authorization signature, SELFDESTRUCT action и lifecycle pending Applied/раннего Status (NAT-01/04/07/08). Независимо прорабатываются механизмы остальных находок. Ответы на отдельные вопросы об ошибке embedded adapter, режиме drain и несовместимых обновлениях ещё не получены; рекомендации не приняты автоматически.

## Предыдущее решение — D55 (2026-11-05)

Пользователь поручил полностью разобрать проект, все детали сквозного flow и порядок разработки, найти проблемы. Начат DOC-AUDIT-02: углублённый сценарный аудит native capture/семантики, delivery state machine, конфигурации/deployment и зависимостей этапов. D53 сохраняется: только проектирование и документация. Результат — трассируемый реестр проблем, сценарии отказов, предложения исправлений и проверок, рекомендуемый порядок разработки; предложения не становятся принятыми решениями автоматически. D54 и прочие закрытые решения не выбирать заново. Исчерпывающий перечень проверенных областей не является доказательством отсутствия всех возможных дефектов будущей реализации.

Технический результат DOC-AUDIT-02 (не новое решение пользователя): [глубокий аудит](DEEP_DESIGN_AUDIT_2026_11_05.md), 41 находка (29 P1, 12 P2), flow восстановления по состояниям, рекомендации детализации E01–E07 и fault matrix. Выбранные upstream tags проверены на существование; concrete native findings подтверждены первичными источниками Geth/Bor/EIP. Сборки/runtime не выполнялись. Исправления остаются открытыми; новых независимых продуктовых вопросов в этом раунде не выявлено. Завершение аудита не закрывает E00-11/DEP-04 и не заменяет консолидацию.

## Предыдущее решение — D54 (2026-11-04)

По результату DOC-AUDIT-01 пользователь выбрал: менять логическое назначение NATS только после завершения pending. Перенос неподтверждённых сообщений в другое назначение в текущем этапе не проектируется. Изменение адреса подключения к тому же логическому назначению допустимо; смена account/stream/subject mapping не должна смешивать подтверждения разных назначений.

Техническое следствие: publication plan и ACK связываются с неизменяемым logical destination; несовместимое изменение при незавершённой доставке отклоняется до публикации с сохранением данных. Завершение pending проверяется по сохранённым состояниям адаптера и Gateway соответствующего контура, а не только по пустой очереди RAM или наличию PubAck одного сообщения. Проверка назначения при reconnect/restart и сценарий частично подтверждённого batch входят в будущие функциональные проверки. Это документальный контракт, не утверждение выполненной реализации.

## Предыдущее решение — D53 (2026-11-04)

Пользователь повторно ограничил работу проектированием и документацией и поручил перепроверить весь пакет, затем задать вопросы по существу. Текущая задача DOC-AUDIT-01: сверка действующих решений, ТЗ, контрактов, конфигурации, deployment и плана; результат — выявленные противоречия, технический остаток и вопросы, ответы на которые действительно меняют поведение системы. Ранее принятые решения и отложенный scope не открывать заново без найденного основания. Новые предложения не считать принятыми. Реализация, runtime/deployment и публикация не разрешены.

DOC-CONSOLIDATE остаётся незавершённой: сначала аудит и согласование найденных смысловых развилок, затем объединение действующего ТЗ. Само поручение проверить документы не означает принятия будущих рекомендаций.

## Предыдущее решение — D52 (2026-10-21)

Пользователь закрыл оставшиеся вопросы:
1. Готовность/синхронизацию нод определять по логам. ТЗ требует понятные сообщения о синхронизации, готовности, ошибках и фиксации live-границы H; отдельная спецификация probe-команд/таймаутов и детектор зависания сейчас не нужны. Это операционная диагностика; встроенный адаптер сохраняет корректную native границу после синхронизации, не заменяет capture парсингом логов.
2. Управление — штатные Docker Compose/Kubernetes запуск, остановка и restart. Отдельные управляющие скрипты/API не обязательны; детализировать их и собственные shutdown таймауты сейчас не требуется. Ранее принятые сохранение pending, обработка сигналов и crash recovery остаются.
3. Лимит одного транспортного сообщения/части — 2 MiB (2097152 байта), включая полную сериализованную TransportFrame и NATS headers. Выбран default `transport.max_frame_bytes` в YAML Gateway; NATS server/stream и gRPC должны его допускать. Большие нормализованные транзакции разбиваются по принятому протоколу, не обрезаются. Сохранённые pending frames не перекодируются при изменении настроек; несовместимый уменьшенный лимит даёт явную ошибку до публикации, требуется вернуть достаточный лимит.
4. На текущем этапе достаточно локального Git. Remote/дополнительный backup не требуются и не блокируют ТЗ; публикация не разрешена.
5. После фиксации пользователь отдельно опишет дальнейшее оформление текущего результата. Это не разрешение на реализацию и не требование заново согласовать уже принятые пункты.

DEP-03.3b закрыт в упрощённой границе пользователя; отдельная probe/shutdown спецификация снята. E00-11 пункт 3 и решение по backup закрыты. E00-05 считается завершённым как документирование процесса: локальные коммиты обязательны, реализация только по отдельному указанию. Оставшаяся техническая сверка ТЗ не объявляется выполненной. [Актуальный остаток](REMAINING_AGREEMENTS.md).

## Предыдущее решение — D51 (2026-10-21)

Пользователь принял порядок подготовки конфигурации нод:
1. Проект поставляет готовые штатные конфиги выбранных клиентов.
2. Пользователь может заменить конфиг целиком и добавить аргументы запуска.
3. Первый старт создаёт необходимые каталоги и native файлы; последующие старты сохраняют существующие данные и ключи.
4. Исходные конфиги читаются из read-only `/config`; файлы, которые клиент должен изменять, находятся в `/data/node`.
5. Подготовка выполняется отдельным init-шагом окружения, не сборщиком, управляющим нодой.

Это закрывает модель подготовки DEP-03.2b.2b.3a, но не точную привязку flags/config keys, файлов и mounts каждого клиента. Init не означает повторную инициализацию цепочки или перегенерацию P2P-ключа при рестарте. Реализация/deployment не разрешены. После фиксации пользователь запросил краткий перечень оставшихся согласований: [REMAINING_AGREEMENTS](REMAINING_AGREEMENTS.md).

## Предыдущее решение — D50 (2026-10-21)

Пользователь делегировал выбор удобной схемы P2P-портов и экспонирования, поручив после фиксации перейти к следующему вопросу. В рамках этого поручения выбран [NETWORK_PORTS](NETWORK_PORTS.md): отдельные порты экземпляров, одинаковые listener/published/advertised номера, явные P2P overrides native defaults; Compose bridge-сети контуров и P2P host mappings; Kubernetes внутренние ClusterIP/headless Services и отдельные P2P NodePort Services с externalTrafficPolicy Cluster. LoadBalancer — опциональный override. Никакой зависимости приложения от Kubernetes API.

Внутренние Geth RPC/Engine API, Gateway и каталоги D49 сохранены. Таблица включает TCP, UDP discovery/QUIC и оставшиеся внутренние endpoints. Публичные адреса задаёт оператор; NodePort не создаёт интернет-маршрут и не обещает HA. Ports конфигурируемы, проверка занятости/диапазона относится к окружению. Изменения listener P2P против upstream defaults выбраны явно для одинакового отображения портов в обеих средах. Это решение агента по делегированию, не отдельно подтверждённые пользователем номера.

DEP-03.2b.2b.2 закрыт как документация. Следующая задача .3 — native flags/config keys и mount mappings, затем DEP-03.3b. Runtime, сборки и deployment не выполнялись.

## Предыдущее решение — D49 (2026-10-21)

Пользователь принял defaults, все переопределяемые:
1. Внутренние native порты сохраняются штатными: Geth RPC 8545, Engine API 8551. Проектный порт Gateway — 50051. Одинаковые порты допустимы в разных контейнерах/Pod с отдельными сетевыми пространствами.
2. По умолчанию наружу публикуется только P2P. RPC, Engine API, Gateway и NATS остаются внутри окружения. Локальная диагностика допускает опциональную публикацию на 127.0.0.1.
3. Каждому экземпляру независимо задаются внешний P2P-адрес/порт и bootnodes/seeds. При общем публичном IP внешние порты различаются, включая две Ethereum-ноды. Объявляемые адреса соответствуют реальной доступности.
4. Пути внутри контейнеров: /config и /run/secrets только для чтения; /data/node — native state, /data/wal — WAL адаптера, /data/journal — журнал Gateway, /data/jetstream — NATS. Процесс получает только нужные каталоги; у каждого владельца отдельные volumes/PVC.
5. P2P-идентичность сохраняется вместе с данными ноды и не меняется при обычном перезапуске.

DEP-03.2b.2b.1 закрывает эти defaults. Точная таблица P2P-портов/протоколов и оставшихся native endpoints — следующий шаг DEP-03.2b.2b.2; конкретные flags/mount mappings ещё не закрыты. Подробности в [NODE_DEPLOYMENT_SPEC](NODE_DEPLOYMENT_SPEC.md). Только ТЗ, без реализации/deployment.

## Предыдущее решение — D48 (2026-10-21)

Пользователь принял шесть пунктов гибкой конфигурации:
1. Отдельный YAML каждой сети Ethereum/BSC/Polygon; изменение одного контура не переконфигурирует остальные.
2. Native клиенты получают штатные конфиги и дополнительные аргументы запуска без ограниченного проектного списка. Все upstream настройки не дублируются в YAML сборщика.
3. YAML сборщика задаёт сеть, WAL, Gateway endpoint, TLS и параметры доставки. Gateway имеет отдельный конфиг своей сети: NATS, subjects/streams и журнал.
4. Образы/версии, порты, P2P-адреса, каталоги, ресурсы и размещение задаются через Compose overrides либо Kubernetes values. Приложение не зависит от Kubernetes API.
5. Секреты — отдельные файлы; конфигурация содержит ссылки на JWT, TLS и credentials. Каждой Geth/Prysm паре принадлежит свой JWT.
6. Проект предоставляет готовые настройки по умолчанию для трёх сетей; пользователь переопределяет нужные параметры. Изменения применяются перезапуском соответствующего компонента, без горячей перезагрузки.

Свобода native аргументов относится к запуску окружением, а не к произвольным ключам типизированного YAML сборщика. Не отменяет независимые WAL, выбранные роли, live-only и границу NATS. Общий NATS остаётся общим транспортом D46. DEP-03.2b.2a закрывает модель конфигурации; конкретные defaults/порты/P2P/mounts — DEP-03.2b.2b. Только документация, не разрешение создавать или запускать реализацию.

## Предыдущее решение — D47 (2026-10-21)

Пользователь принял предложенный набор кандидатов: Geth 1.17.9 и Prysm 7.3.0 для основной Ethereum-ноды и отдельной L1-ноды Polygon; BSC 1.7.8; Bor 2.10.3; Heimdall-v2 0.12.2; NATS server 2.15.1; RocksDB 10.4.2. Gateway libraries без изменений: gRPC-Go 1.84.0, nats.go 1.54.0, protoc 36.2, protobuf-go 1.36.12.

Это выбор версий для проектирования, не подтверждение совместной сборки или runtime-совместимости. Image digests и pins модифицированных клиентских сборок ещё не определены. Старые source audits Geth 1.17.8/Bor 2.10.2 сохраняются с исходными commit hashes; перенос выводов на новые версии требует отдельной проверки изменений native hooks. Источники и оставшиеся проверки: [DEPLOYMENT_VERSION_CANDIDATES](research/DEPLOYMENT_VERSION_CANDIDATES.md).

DEP-03.2b.1 закрывает выбор кандидатов; точные native конфиги/порты/P2P/mounts остаются DEP-03.2b.2, probes/shutdown — DEP-03.3b. Изменения архитектуры сначала согласовываются. Только ТЗ, без реализации и deployment.

## Предыдущее решение — D46 (2026-10-21)

Пользователь согласовал пункты 1–4 предложения DEP-03:
1. Каждый из 11 основных компонентов — отдельный StatefulSet с replicas=1, собственные Service и PVC в Kubernetes; в Compose отдельный сервис и собственные named volumes. Native datadir, WAL адаптера и журнал Gateway раздельны. Возможность переноса между хостами зависит от выбранного хранилища; HA не обещается.
2. Межкомпонентные адреса задаются конфигурацией через устойчивые service DNS, не фиксированные IP. Geth/Prysm пары и L1 Polygon изолированы; адаптер соединяется только с Gateway своей сети.
3. Обычные YAML/native configs отделены от JWT, TLS-ключей и NATS credentials. У каждой пары Geth/Prysm собственный JWT; adapter→Gateway использует gRPC/mTLS. Рабочие секреты не входят в примеры конфигурации.
4. Нет общей цепочки готовности сетей: синхронизация ноды не ждёт NATS; после синхронизации фиксируется H, live начинается с H+1. При недоступности Gateway/NATS адаптер сохраняет данные в собственном WAL. Состояние других сетей не блокирует запуск.

Версии не утверждены этим ответом: это следующий отдельный шаг. Точные native flags, порты и probe-параметры ещё зависят от выбранных версий; не объявлять их завершёнными. Только ТЗ, без реализации и deployment.

## Предыдущее решение — D45 (2026-10-21)

Пользователь выбрал вариант 3: каждый самостоятельный компонент — отдельный сервис/контейнер Docker Compose и отдельный workload/Pod Kubernetes. Geth и Prysm не объединяются в контейнер или Pod; Bor/Heimdall и Gateway также размещаются отдельно. Размещение не требует общего хоста; endpoints задаются конфигурацией. Обновление/рестарт компонента не требует пересоздания остальных, но реальные native зависимости и готовность учитываются.

Адаптер остаётся встроенным в execution-клиент, RocksDB WAL — embedded состоянием своего owner, а не отдельными сетевыми сервисами. Для Polygon отдельные L1 Geth/Prysm без Ethereum-адаптера; их состояние не разделяется с основным Ethereum. Один NATS сохраняется. Раздельное размещение не вводит HA или multi-writer одного WAL. Текущий перечень — 11 основных сервисов: Ethereum 3, BSC 2, Polygon 5, NATS 1; это не число серверов и не обещание отсутствия технических init jobs.

DEP-03.1 (гранулярность размещения) закрыт. Версии, точные конфиги/соединения/тома/readiness и итоговая сверка остаются DEP-03.2–03.3/DEP-04. Только ТЗ, реализация не разрешена.

## Предыдущее решение — D44 (2026-10-21)

Пользователь выбрал Prysm как consensus-клиент Ethereum и отдельную собственную Ethereum-ноду для Polygon (вариант 2). Основной Ethereum-контур: Geth с адаптером + Prysm + собственные WAL/Gateway. Polygon: Bor с адаптером + Heimdall-v2 + отдельный Ethereum L1-комплект, не использующий ноду, WAL или Gateway основного Ethereum-extractor. Для L1-комплекта используется тот же выбранный стек Geth/Prysm, но отдельные процессы, datadirs и credentials; Ethereum-адаптер и публикация Ethereum-транзакций для него не требуются.

Всё входит в проектируемые Compose/Kubernetes конфигурации по D43. Размещение Geth/Prysm в одном или нескольких контейнерах/Pod пользователь не выбрал; прежний вопрос о возможности одного контейнера не считается выбором topology. Совместимые версии Prysm/образы и точные связи ещё требуется определить, сборка/запуск не выполнялись. DEP-02 закрыт как выбор компонентов, DEP-03/04 открыты. Только ТЗ.

## Предыдущее решение — D43 (2026-10-21)

Пользователь отклонил предложение считать готовые ноды и их вспомогательные компоненты внешней предпосылкой. Полный состав нод Ethereum/BSC/Polygon, его компоновка, конфигурация и связи входят в нашу задачу для Docker Compose и Kubernetes. Сюда входят execution-клиенты с адаптерами, необходимые consensus/supporting-компоненты, отдельные WAL/Gateway и ранее принятый NATS.

D39 сохраняет механизм запуска: процессы запускают Compose/Kubernetes, а не extractor через API оркестратора. Это не снимает с проекта ответственность подготовить конфигурацию всего комплекта. Native параметры и адаптерный YAML должны быть согласованы в поставляемых шаблонах. Каждая сеть сохраняет собственные процессы, тома, WAL, журнал и прогресс.

Текущая работа остаётся ТЗ: состав и связи описываем сейчас, реализацию образов/Compose/Kubernetes пользователь ещё не разрешал. Open-source публикация/лицензия по D41 отложены; это не исключает технический комплект развёртывания. Performance требования и production-методика по D40 не возвращаются. Конкретный Ethereum consensus-клиент, его версия и схема необходимых Polygon L1 подключений ещё не выбраны; молча не считать их внешне предоставленными или привязанными к Ethereum extractor.

Пункт 2 E00-11 IN_PROGRESS: граница ответственности принята, осталось согласовать конкретный полный состав и конфигурацию по [NODE_DEPLOYMENT_SPEC](NODE_DEPLOYMENT_SPEC.md). Пункты 3–4 сверки не обсуждались.

## Предыдущее решение — D42 (2026-10-21)

Текущий состав подтверждён пользователем: Ethereum Mainnet, BNB Smart Chain (BSC) Mainnet и Polygon PoS Mainnet. Base и Arbitrum (Nitro/Classic) отложены, не блокируют первую версию. Исследования пяти сетей сохраняются, но обязательные адаптеры, YAML-профили и fixtures текущего этапа — только ETH/BSC/POLY. Каждая сеть имеет собственные WAL, журнал Gateway и независимые процессы по D40.

Пункт 1 сверки E00-11: убрать отложенные сети из обязательной цепочки, исторические сценарии пометить отложенными по D38, публичную упаковку — по D41. Исследования и технические контракты сохранить. Пункты 2–4 сверки отдельно не согласованы. Реализация не начата.

## Предыдущее решение — D41 (2026-10-21)

Пользователь отложил выбор лицензии и open-source поставку. E00-08/OSS-01/02 и E07-07 — DEFERRED_BY_USER; они не блокируют завершение текущего архитектурного ТЗ. Намерение сделать проект open-source сохраняется, но лицензия, публичный repository, release workflow и комплект публичной поставки сейчас не выбираются и не готовятся. Публикация не разрешена. Следующий текущий блок — E00-11: сверка архитектурного пакета и оставшихся supporting-client profiles. Новые изменения сначала согласовывать; реализация не начата.

## Предыдущее решение — D40 (2026-10-21)

1. Принята последовательность: native-захват пяти сетей → единая типизированная схема → доставка через WAL/Gateway/NATS → полные детали транзакций → reorg и восстановление → live-конфигурация → функциональная приёмка. История не входит в обязательную цепочку.
2. Каждая сеть имеет собственный RocksDB WAL адаптера и собственный журнал Gateway, процессы, конфигурацию и прогресс. Общий multi-network WAL, общий writer/координатор и ожидание прогресса другой сети запрещены. Остановка, рестарт и настройка одной сети не требуют действий остальных. Ранее принятый общий NATS остаётся транспортной инфраструктурой; при его недоступности каждая сеть самостоятельно накапливает свой WAL. Нативные L1-зависимости клиентов не превращаются в зависимость от Ethereum extractor.
3. Требования производительности исключены из текущего ТЗ полностью: нет обязательных benchmark, latency/TPS/overhead целей, hardware-профиля или будущего gate по численным порогам. Корректность bounded-memory обработки больших сообщений и сохранность WAL сохраняются как свойства протокола, не performance SLO.
4. Методика production-тестирования отложена. Предложения 24-часового live-прогона и 30-минутного outage не приняты и не являются условиями приёмки. Функциональные fixtures и проверки корректности протокола остаются задачами разработки; production-прогоны, нагрузочные сценарии и их длительности сейчас не описываются.

E00-04 закрывается в согласованной границе после сверки документов. Не выбранные supporting-client profiles остаются явным остатком финальной матрицы E00-11; новым решением они не считаются выбранными. Код и runtime-проверки не разрешены этим решением.

## Предыдущее решение — D39 (2026-10-21)

Пользователь согласовал все четыре результата проверки CFG-S05:
1. Критерии текущего этапа ограничиваются live-сбором, нормализацией, сохранностью WAL и подтверждением NATS. INSERT/таблицы, отсутствие повторных бизнес-эффектов, восполнение пропусков и глубина истории исключены.
2. Активные документы и статусы очищаются от устаревших требований; прошлые решения сохраняются в журнале.
3. Native ноду запускает окружение (Compose/Kubernetes либо штатная команда клиента). Встроенный адаптер получает YAML при запуске ноды. Общий CLI не управляет запуском ноды.
4. После синхронизации ноды фиксируется текущая каноническая голова H; новый live-сбор начинается с H+1. Pending WAL передаётся независимо. Это не догрузка периода остановки. Native подписка и фиксация H должны быть согласованы без потери новых наблюдений; reorg/finality остаются в scope.

CFG-S05 завершается сверкой документов, без реализации. Следующий блок — E00-04: приёмочные параметры и оставшиеся ограничения. Новые изменения за пределами согласованных четырёх пунктов сначала обсуждать с пользователем.

## Предыдущее решение — D38 (2026-10-21)

История и загрузка пропусков целиком отложены пользователем. Текущий этап: live native capture → единая типизированная нормализация → WAL/Gateway → NATS PubAck. History N–M, history resume, диапазоны и соответствующие CLI/YAML не проектируются сейчас и не блокируют ТЗ. CFG-R4.1/CFG-R4.2 сняты; предложения не утверждены. E00-09 и исторические задачи E06 отложены. Pending WAL, live reorg/finality и независимость сетей сохраняются. Следующий шаг — CFG-S04 только для live/Gateway. Реализация не разрешена.

## Предыдущее решение — D37 (2026-10-21)

- D37 accepted: учёт запусков, проверка отсутствующих данных и организация их последующей догрузки — отдельный механизм вне текущего ТЗ. Пользователь обозначил будущую связку «сообщение о запуске в отдельный NATS канал → отдельная таблица ClickHouse → скрипт проверки». Сейчас не проектируются схема/очередь этого механизма, таблица, скрипт или orchestration догрузки.
- Текущий сборщик получает native данные, нормализует их в единый строго типизированный контракт для поддержанных блокчейнов и передаёт в NATS. Live не сканирует и не догружает автоматически исторический разрыв за время остановки. Явный history N–M отдельным сервисом сохраняется по D36.
- CFG-R3 закрыт: новые отсутствующие блоки относятся к внешнему механизму, а повторная отправка уже сохранённого pending WAL остаётся обязанностью сборщика. Восстановление конкретного ранее начатого незакрытого batch не превращается в поиск/проверку полноты диапазона до head.

Следующий остаток E00-07/E00-09 — точный YAML/CLI контракт в CFG-S04; никаких дополнительных вопросов о поиске пропусков/ClickHouse сейчас не задавать. Только ТЗ.

## Предыдущее решение — D36 (2026-10-21)

- D36 accepted: основной загрузчик получает только live данные. Любая запрашиваемая догрузка истории — отдельный сервис, запускаемый отдельной командой с указанием диапазона на том же узле/хосте, что и native нода. Live не запускает историю через изменение YAML/apply. Единый интерфейс всех сетей и native-specific реализация сохраняются; Kubernetes не является зависимостью приложения.
- Отменены стартовая история «последние три часа», конфигурируемый старт истории live-загрузчика и её запуск через reload/apply (соответствующие части D06/D10/D34/D35). Вопросы CFG-R2 о таком apply сняты, не ждут ответа. Ручной N–M, отдельные процессы, независимость сетей, допустимые повторы, RocksDB/ACK и scope до NATS сохраняются.
- Совместное размещение не разрешает двум процессам открывать writable native datadir или RocksDB store. History использует поддержанный native доступ/replay; отсутствие необходимых исторических данных — явная ошибка.
- Требует отдельного уточнения: автоматически ли получать блоки, пропущенные за время остановки live, или их также загружать отдельной history-командой. Повтор отправки уже сохранённого pending WAL остаётся автоматическим по D26; это не новый исторический диапазон. Не считать этот вопрос решённым фразой «только live» без явного разграничения.

Рабочий контракт: [CONFIG_CONTRACT](CONFIG_CONTRACT.md). Только ТЗ; deployment/код не создавались.

## Предыдущие решения — D34–D35 (2026-10-21)

- D34 accepted / CFG-R1.1: одинаковый интерфейс управления для всех поддержанных сетей, реализация учитывает native специфику. Пользователь меняет YAML одной сети и явно вызывает применение (например командой); после этого выполняется её догрузка при продолжающемся live. Остальные сети и компоненты не переконфигурируются и не участвуют в управлении. Kubernetes API/controller/наблюдатель создания Jobs не являются зависимостью этого интерфейса. Это уточняет D06: одного изменения файла недостаточно, требуется явный apply; автоматический file watcher и предложенный Kubernetes-aware launcher не приняты. Обычный путь публикации через Gateway/NATS сохраняется без изменения их контракта.
- D35 accepted / CFG-R1.2: без перезапуска применяется только стартовый блок истории. Остальные настройки требуют перезапуска соответствующего процесса, не других сетей. Команда N–M и её исходные параметры остаются независимыми.

CFG-S02 закрыт по ответам. [CONFIG_CONTRACT](CONFIG_CONTRACT.md) продолжает CFG-S03: верхняя граница догрузки, повторное изменение во время работы и удаление start_block. Точный CLI spelling/transport управления ещё не утверждён; не выдавать пример команды за реализованный интерфейс. Только ТЗ.

## Предыдущее решение — D33 (2026-10-16)

2026-10-20 / CFG-R1: по «продолжай» возобновлены E00-07/E00-09. В [CONFIG_CONTRACT](CONFIG_CONTRACT.md) сохранены принятые ограничения и два открытых вопроса: auto-launch отдельного history pod и hot-reload scope. Исходный диапазон активной ручной команды уже сохраняется по .06. Предложения launcher/структуры YAML ещё не приняты. D06 и D23 сохраняются; вопросы автоматического bootstrap/сдвига границы зависят от ответов. Это не новое пользовательское решение и не разрешение на реализацию.

2026-10-16 / E00-03.07: выполнена [итоговая сверка](E00_03_ARCHITECTURE_REVIEW.md) по существующим D16–D33. Уточнены block-batch ACK, запрет публикации STAGING, per-producer lifecycle_state после ACK cleanup, статусы и дальнейшие gates. E00-03 DONE как архитектурный блок; E00 целиком не завершён. Пользователь отдельно подтвердил границу работы: только ТЗ, без реализации. Следующий CFG/JOB-01; вопросы лицензии/приёмки/резервирования документов остаются открытыми. Новых пользовательских решений в этой сверке не предполагается.

Техническое завершение E00-03.06 (поручение «выполняй»): [RocksDB pins](research/ROCKSDB_STACK.md), [delivery protocol](ROCKSDB_DELIVERY_PROTOCOL.md). RocksDB 10.4.2 / grocksdb v1.10.2 / Rust rocksdb 0.24.0 + sys 0.17.3+10.4.2; sync WriteBatch; Prepare/Publish с durable producer plan и publication_epoch, ACKED cleanup/release, single-writer ownership и REC-T01–16. Это проектный baseline исполнителя, не новые ответы пользователя и не пройденная runtime-приёмка. E00-03.06/E00-06 закрыты как документы; .07 следующий после commit.

- D33 accepted: пользователь утвердил RocksDB для локального WAL/служебного состояния. Embedded engine используется в Go/Rust компонентах с отдельными writable каталогами; отдельный сервер БД не вводится. Базовый вариант — обычный RocksDB; integrated BlobDB остаётся необязательной оптимизацией для будущих измерений, не второй обязательной реализацией.
- Версии RocksDB и bindings, точный протокол durable записи/ACK/checkpoint/очистки и crash fixtures завершаются в E00-03.06. Выбор движка не означает успешные benchmark/build/runtime проверки и не закрывает весь подэтап.

## Предыдущее решение — D32 (2026-10-16)

- D32 accepted: SQLite исключён пользователем из кандидатов WAL/служебного состояния. Поручено исследовать RocksDB и альтернативы. Это не утверждение конкретного engine/binding/version.
- Рекомендация после исследования: RocksDB как baseline; integrated BlobDB — конфигурация для сравнительного POC больших payload; Pebble — резерв для Go. [Исследование](research/WAL_ENGINE_SELECTION.md), [альтернативы](research/WAL_ALTERNATIVES.md). Производительность не измерена, выбор ещё не принят.

## Последние решения — D26–D31 (2026-10-16)

- D26 accepted: при перезапуске с сохранённым диском автоматически продолжать pending WAL; history продолжает исходный N–M. Повторная отправка допустима.
- D27 accepted: при полной потере диска автоматическое восстановление прежнего прогресса/неотправленных данных из резервной копии не требуется. Новый загрузчик запускается заново; нужную историю пользователь задаёт командой N–M. Один YAML не восстанавливает потерянный cursor.
- D28 accepted: при недоступности NATS продолжать сбор и накопление WAL без TTL, прикладного лимита объёма и остановки по такому лимиту. Ничего неподтверждённого не удалять. Физическая ошибка записи (включая исчерпание диска) остаётся явной ошибкой: нельзя объявлять WAL_SAVED, терять данные молча или продолжать продвигать durable cursor. Бесконечная ёмкость не обещается.
- D29 accepted: удалять локальные сообщения блока только после PubAck всех подготовленных сообщений/частей блока и фиксации завершения доставки. Никакого дополнительного срока хранения после ACK не требуется. Это delivery batch по D25, не независимая проверка полноты блока.
- D30 accepted: текущая поставка — один NATS JetStream instance в Kubernetes или Compose; его доступность — эксплуатационное допущение. Production HA, репликация между датацентрами и гарантии пережить потерю сервера отложены и не входят в текущий ТЗ. Допущение доступности не заменяет настоящий PubAck.
- D31 accepted: история служебных статусов после подтверждённой передачи NATS не нужна. Долговременный журнал доставленных блоков не требуется; минимальный restart cursor и активные job parameters — рабочее состояние, не архив транзакций. После ACK дальнейшая судьба данных вне scope сборщика.

Результат REC-раунда: [DELIVERY_RECOVERY.md](DELIVERY_RECOVERY.md). Требования приняты; backend WAL, атомарные операции/очистка и single-writer механизм ещё требуют технического завершения E00-03.06. Не спрашивать повторно о backup, HA и retention уже подтверждённых данных.

## Последние решения — D23–D25 (2026-10-16)

- D23 accepted / ARCH-03: live и history выполняются разными pod в Kubernetes, отдельными процессами/сервисами в Compose. История — простой загрузчик, запускаемый командой «загрузи блоки от N до M». Визуализация и интерфейсы мониторинга сейчас не проектируются. Базовый способ задания истории выбран; детали YAML/reload остаются отдельными и не являются условием запуска N–M.
- D24 accepted: пересечения live/history и повторных диапазонов допустимы. Дедупликация blockchain records и согласование пересечений исключены из текущего ТЗ; их выполняет принимающая сторона. Упоминание пользователем ClickHouse не возвращает его в scope коллектора. Прежние требования одной общей активации для overlap и подавления повторных транзакций отменены; идентификаторы native включений и правила retry доставки сохраняются.
- D25 accepted: проверка полноты/содержимого блоков по вычисленным хешам на стороне сборщика не требуется; это ответственность принимающей стороны. Scope: получить native данные блока, сохранить предназначенные для отправки сообщения в WAL, передать их в NATS и получить PubAck. Source inventory verifier, BlockComplete/coverage COMPLETE как доказательство полноты, coverage report и соответствующие gates отменены. Сохраняются native block/parent hashes, позиции, данные транзакций, типизация и проверки целостности транспортных частей. Native consensus validation клиента не отключается.
- Предложение пользователя: быстрое служебное хранилище каждой сети для наблюдения стадий «получен / в WAL / отправлен в NATS». Логическую модель описать сейчас; backend, durability/cleanup и восстановление выбрать в E00-03.06/REC. Это не сохранённый индекс балансов и не downstream БД.

Эти решения имеют приоритет над прежними формулировками E00-14/.04 об overlap/coverage; исторические исследования остаются справочными, их completeness fixtures не становятся обязательным runtime-верификатором. Обязательство корректно собирать поддержанные native transaction types не отменено; нельзя молча обрезать записи или объявлять транспортный ACK доказательством полноты блокчейна.

## Правило работы с Git (2026-10-15)

Пользователь инициализировал репозиторий и поручил начальный коммит текущего состояния. Принято: после завершения изменений файлов и проверок, перед переходом к следующему этапу/подэтапу создавать Git-коммит. Правило закреплено в [AGENTS.md](../AGENTS.md). Оно распространяется на документацию; разрешение на реализацию или push из него не следует. ARCH-03 этим сообщением не решён.

## Последние решения — D21–D22 (2026-10-15)

- D21 accepted / ARCH-01: в первую поставку входят готовые конфигурации Docker Compose для разработки/локального запуска и Kubernetes для production. Ответ пользователя: «оба».
- D22 accepted / ARCH-02: NATS.IO разворачивается внутри Kubernetes в production и внутри Docker Compose для разработки/локального запуска. Конфигурации NATS входят в поставку проекта; прежняя рекомендация ограничиться внешним production-кластером не принята.
- Эти ответы закрывают E00-03.02 и разрешают продолжить проектирование .03. Они не задают replicas, retention, storage backend, HA или гарантии восстановления; REC остаётся отдельным обсуждением. Инфраструктура сейчас не разворачивается, реализация ещё не начата.

Дата начала: 2026-10-13. Статусы: accepted / proposed / open / deferred / superseded.
Авторитетный журнал решений; план этапов ссылается на ID из этого файла.

## Последние решения — D17–D20 (2026-10-15)

- D17 / accepted: сообщения строго типизированы и унифицированы для поддерживаемых типов блокчейнов. Транзакционные, блоковые и lifecycle сообщения обязательны; сетевые особенности не теряются и имеют типизированное представление. Главный приоритет — все транзакции и их детали в выбранном диапазоне и live. Изменения баланса вне транзакций разрешено опустить, если их сбор усложняет код; это необязательное расширение, не блокирующее основную поставку. D01 в части обязательной полноты вне транзакций заменён. Протокольная операция, представленная родной транзакцией, остаётся обязательной независимо от инициатора.
- D18 / accepted: обычная транзакция передаётся одним сообщением; слишком большая — несколькими частями с общим идентификатором, индексом, количеством и контрольной суммой. Усечение деталей запрещено. Конкретный лимит и wire schema ещё проектируются.
- D19 / accepted: отдельные native node/adapter, WAL и Gateway для каждой сети; общий кластер NATS с отдельными потоками/subjects и лимитами. Конфигурация, запуск, остановка и неисправность процесса одной сети не требуют действий в остальных. Общий NATS остаётся общей инфраструктурной зависимостью; изоляция нагрузки подлежит проверке.
- D20 / deferred by user: YAML и интерфейс управления историей обсуждаются отдельно с приоритетом простоты и эффективности. JOB-01 не имеет выбранного ответа; прежние варианты не реализуются по умолчанию. Уже принятые старт за 3 часа/указанный блок, live и команда диапазона N–M сохраняются.

R07: MSG-01, MSG-02 и DEPLOY-02 закрыты с уточнением optional system balances; JOB-01 отложен. Следующий шаг агента — привести модель и задачи к этим решениям, затем проверить клиенты/стек и подготовить строго типизированный контракт. Новые вопросы по YAML и восстановлению сейчас не задавать.

## Исследование E00-13 — клиенты и стек (2026-10-15)

Пользователь разрешил начать проверку клиентов. Результат сохранён в [CLIENT_MATRIX.md](CLIENT_MATRIX.md), подробные primary-source исследования — [Ethereum/BSC](research/ETHEREUM_BSC_CLIENTS.md), [Base](research/BASE_CLIENT.md), [Arbitrum/Polygon](research/ARBITRUM_POLYGON_CLIENTS.md).

Source-verified рекомендации: Geth v1.17.8, BSC v1.7.8, base/base v1.5.0 с pinned base/reth, Nitro v3.12.1 с own Geth, Bor v2.10.2 + Heimdall-v2 v0.12.2. Go capture для ETH/BSC/ARB/POLY, Rust для Base. Commits, manifests, licenses, native hooks и ограничения источников записаны. Это исследовательский результат, не пользовательское утверждение всех production versions и не выполненная сборка.

Существенные выводы: latest BSC alpha непригоден для заявленной production-цели; Parlia/StateSync/ArbOS требуют специальных путей захвата; default Base Inspector надо включить в реально исполняемый factory; tracing меняет производительность Geth/Bor/Nitro. Arbitrum до Nitro boundary требует Classic candidate v1.4.7 с недоказанным полным state/action capture. Типы будущих forks Base нельзя объявлять активными по наличию enum. Блоки/receipts не гарантируют доступный parent state для replay.

E00-13 закрывает только source research. Далее E00-14 — реестр native типов и строгая схема; E00-15 — Classic/legacy Bor уточнения. E01 build/runtime остаётся невыполненным и требует разрешения на реализацию. Пользовательские решения D17–D20 не изменены; YAML/history control и REC отложены.

## Текущая граница — D16 (2026-10-14)

По прямому уточнению пользователя текущая задача заканчивается передачей унифицированных данных в кластер NATS.IO: сбор из блокчейнов → нормализация → публикация. Запись в базы данных, дальнейшая передача и обработка полностью исключены из текущего ТЗ, плана и приёмки. D09 и часть D11 о хранении/шардировании заменены этим решением; прежние раунды ниже — история обсуждения, не действующие требования. Хронология и связи исходных данных сохраняются в контракте сообщений.

На момент D16 формат дополнительных сообщений и разбиение были открыты; затем приняты D17/D18. Требование D01 о полноте вне транзакций заменено D17 на optional. Текущий результат работы агента — архитектурное ТЗ, а не код.

R06/DB-01 снят. R07 закрыт решениями D17–D19, кроме JOB-01, отложенного пользователем по D20. Топология до NATS принята; управление историей обсуждается отдельно.

## Подтверждённые ограничения

| ID | Статус | Решение и основание |
|---|---|---|
| D01 | partly superseded by D17 | Сохраняются все транзакционные требования ТЗ и добавляются любые сохранившиеся изменения нативных балансов вне транзакций; прямое указание пользователя |
| D02 | accepted | Оригинал v2 неизменяем; отдельная рабочая редакция v3-codex создана по указанию пользователя |
| D03 | accepted | Последовательность разработки, стек, способы реализации, задачи и точка продолжения фиксируются в файлах; прямое указание пользователя |
| D04 | accepted | Текущая работа — интервью и проектирование по grill-with-docs; решения пользователей записываются до следующего раунда |
| D05 | accepted | Первый контур включает Ethereum, BSC, Base, Arbitrum и Polygon. Цель — сразу проверить разные разновидности сетей; прежняя рекомендация начать поставку с одной Ethereum-сети не принята |
| D06 | accepted; reload relation open JOB-01 | Нижняя граница истории задаётся номером блока в YAML; экстрактор перечитывает конфиг и автоматически догружает историю при изменении границы, без отдельных ручных заданий. Если ранний блок не задан, при старте определить блок примерно за 3 часа до текущей головы. Live работает одновременно в реальном времени |
| D07 | accepted | Нужно сохранять действия, вызывающие изменения балансов; одного неразличимого суммарного результата недостаточно. Конкретная модель действий и их связь с балансами ещё проектируются |
| D08 | accepted | Восстановление должно запускаться тем же YAML и восстанавливать данные; подробности восстановления вынести в отдельный пул вопросов по прямому указанию пользователя |
| D09 | superseded by D16 | 2026-10-14: разрабатывается исключительно система сбора данных и загрузки в ClickHouse. Внешнее получение данных строится на запросах к ClickHouse; загрузчик, схема хранения, корректность и полнота записи входят в проект. Прикладные сервисы, аналитические API и интерфейсы пользователей не входят |
| D10 | accepted | 2026-10-14: при старте загружается только выбранный участок истории (по умолчанию последние 3 часа), затем данные пополняются live. Полный начальный срез балансов всех адресов не требуется. В разработке граница может задавать, например, 30 минут; в production — любой требуемый начальный блок вплоть до первого при наличии исходных данных |
| D11 | partly superseded by D16 | ClickHouse хранит хронологию блокчейнов: блоки, транзакции внутри блоков и гранулярные детали. Модель должна поддерживать шардирование; конкретные физические таблицы и ключ распределения ещё выбираются |
| D12 | accepted | Проект будет open-source; конечный пользователь конфигурирует набор поддерживаемых блокчейнов и параметры загрузки посредством YAML (.yml) файлов. Конкретная лицензия, публичный repository и состав hot-reload полей ещё не выбраны |
| D13 | accepted | 2026-10-14: точные сети подтверждены — Ethereum Mainnet, BSC Mainnet, Base Mainnet, Arbitrum One и Polygon PoS |
| D14 | accepted | Сборщик по умолчанию непрерывно отправляет новые блоки дальше по цепочке. Пользователь должен иметь возможность запустить отдельную загрузку диапазона N–M, которая не мешает live. Сборщик не управляет БД; точные границы этой формулировки и сочетание команды с ранее принятым YAML-reload уточняются в R06 |
| D15 | accepted | Каждая сеть — отдельный процесс/узел/pod (Kubernetes, Docker Compose и другие способы запуска). Изменение параметров, добавление или отключение одной сети не требует изменения/перезапуска остальных; единый процесс для всех сетей не соответствует требованию. Общие инфраструктурные зависимости уточняются отдельно |

| D16 | accepted | Текущий этап: сбор → нормализация → NATS.IO. Последующие хранение/обработка исключены. Одна транзакция — основная единица сообщения; разбиение требует обсуждения. Финализировать архитектурное ТЗ до реализации |

Базовый технический контур рабочего ТЗ: native execution, транзакционные Go/Rust-адаптеры по семейству клиента, protobuf/gRPC, Gateway, NATS JetStream, WAL, reorg/finality. Это исходные требования для планирования; версии инструментов, хранилища, топология и все детали v3 не считаются отдельно утверждёнными.

## Дерево зависимостей

- Q1 Набор первых сетей (accepted: пять сетей) → Q1a точные сети/варианты Polygon и Arbitrum → Q5 клиенты/commits и runtime для каждой сети → Q6 hooks и формат клиентской интеграции → Q7 fixtures, oracle и границы поддержки.
- Q2 История первой поставки + Q1 → Q8 стартовый state, способ получения истории и baseline → Q9 backfill/live overlap и необходимая инфраструктура.
- Q3 Гранулярность балансов → Q10 модель операций и стабильные идентификаторы → Q11 порядок, lifecycle, поздние metadata и consumer.
- Q4 Отказоустойчивость → Q12 durable storage, writer fencing и топология → Q13 retention, sizing, backup, восстановление и эксплуатационные лимиты.
- Q5/Q10/Q12 → Q14 фиксированный стек и воспроизводимая сборка → Q15 сквозной план реализации и критерии переходов.
- Q7/Q9/Q11/Q13/Q15 → Q16 финальная сверка понимания, нерешённых ветвей и разрешение на реализацию.

Q5–Q16 — области будущих решений, а не анкета для одновременного ответа. Точные вопросы уточняются после ответов текущего раунда. Факты о клиентах, API, файлах и инструментах проверяет агент; пользователь выбирает требования и компромиссы.

## История раундов R01–R06

Записи ниже отражают прежний ход обсуждения. Требования о последующей обработке отменены D16; они не составляют текущий backlog. Актуальные вопросы — R07 в конце файла.

## R01 — исходные независимые решения

Статус: Q1 принят (пять сетей); Q2 уточнён пользовательским контрактом YAML/3 часа/live; Q3 уточнён требованием сохранять действия и вопросом о ClickHouse; Q4 перенесён в отдельный пул восстановления. Пользователь требует понятного технического языка.

| ID | Вопрос | Рекомендация | Ответ пользователя |
|---|---|---|---|
| Q1 | Набор сетей первого контура? | Прежняя рекомендация Ethereum/Geth-only — superseded | accepted: Ethereum, BSC, Base, Arbitrum, Polygon; разновидности должны проверяться сразу |
| Q2 | Как выбирать и изменять глубину истории? | Предыдущий выбор фиксированного объёма поставки заменён | accepted D06: YAML, автоматическая догрузка, default 3 часа и одновременный live |
| Q3 | Какие данные нужны для балансов? | Прежнее предположение «достаточно только итогового diff» не считать принятым | accepted D07: сохранять действия; модель данных и границы запросов ClickHouse требуют уточнения |
| Q4 | Как восстанавливать систему? | Не выбирать сейчас replication/HA за пользователя | accepted D08: запуск с тем же YAML; детали обсуждать отдельным пулом REC |

Сценарий Q3: в одной успешной транзакции адрес получил 10 и отдал 10. В модели итогового diff balance change отсутствует; внутренние вызовы остаются. Журнал промежуточных сохранившихся изменений требует дополнительных записей и проверки rollback.

Все рекомендации — proposed. Отсутствие ответа, истечение времени или наличие варианта в v3 не переводит его в accepted.

### R02 — текущая ветка: история и запросы балансов

Подтверждено: D06–D08. Точная структура YAML ещё не утверждена; слово «константа» трактуется как настройка, а не неизменяемое значение внутри процесса. Номер является нижней границей по фразе пользователя «самый ранний блок»; имя поля будет отражать это явно.

После R05 точные сети приняты по D13. Не выбраны конкретная библиотека/период перечитывания и способ сочетания YAML с командой истории. Сборщик не получает права удалять историю изменением границы. Полный начальный snapshot исключён из обязательного старта по D10. Production ClickHouse loader включён по D09.

Текущие решения для уточнения:

- CFG-01: прежний вопрос об удалении истории заменён D14: сборщик отправляет данные и выполняет запрошенные диапазоны, а не управляет содержимым БД. Выбор администрирования и сочетание config/commands обсуждаются в R06; разрешение удаления не выдано.
- BAL-01: closed / D10. Сбор ограничивается выбранной историей и live; полный snapshot всех адресов при старте не требуется. Native before/after затронутых адресов сохраняются; для не встречавшихся в покрытии адресов баланс остаётся неизвестным, а не нулевым.
- CH-01: closed / accepted D09. Сбор и загрузка в ClickHouse — цель проекта. Реализация загрузчика и схемы обязательна; SQL-проверки подтверждают пригодность данных. Отдельный прикладной query service не добавляется. Сам адаптер напрямую в ClickHouse не пишет.

Рабочее предложение модели: исходные транзакции/вызовы/системные действия + отдельная проекция before/after по адресам с причинными ссылками; позиции исполнения, flags rollback, lifecycle и coverage обязательны. Приход и расход внутри одной успешной транзакции сохраняют идентичность действий даже при нулевом итоговом diff. Конкретные proto и таблицы утверждаются после разбора модели; прежнюю схему нельзя объявлять достаточной заранее.

### R03 — ClickHouse как конечное хранилище (2026-10-14)

Система завершает полезную работу после корректной записи данных в ClickHouse и отражения готовности соответствующего диапазона для SQL-чтения. Публикация в NATS — промежуточный этап, а не конец приёмки.

Роль NATS в существующем проектном контуре — внутренний транспорт; его удаление или замена не согласованы. Внешние потребители читают ClickHouse, а не должны разбирать protobuf/chunks и следить за NATS для обычного получения данных. Служебные WAL и journals остаются возможными внутренними механизмами надёжности.

Для SQL-чтения в ClickHouse должны быть доступны данные, каноничность и сведения о покрытии/готовности. Native before/after затронутых адресов собираются из родного состояния при исполнении и загружаются в ClickHouse; внешний читатель не обязан выполнять дополнительный RPC к ноде. По последующему D10 полный baseline всех адресов при старте не требуется; неизвестные данные нельзя объявлять нулевыми.

Следующие технические решения: CH-02 таблицы и logical keys; CH-03 insert/retry/commit и готовность многотабличной записи; CH-04 canonical selection, reorg и контрольные SQL; CH-05 batching, драйвер и показатели задержки. Эти решения принимаются в рамках сборщика/загрузчика, без разработки бизнес-приложения.

### R04 — ограниченный старт, хронология, шардирование и open-source

Проверка до изменения документов: в v2 не было явных требований open-source, YAML и шардирования. В v3 и плане было управление глубиной истории через YAML, но не было явного open-source, шардирования и выбора сетей конечным пользователем. D10–D12 закрывают эти пробелы.

Данные организуются логически как network → block → transaction inclusion → calls/logs/actions/state changes, с отдельными системными действиями на уровне блока. Хронология определяется родным порядком и веткой, а не порядком INSERT. Запись блока обязательна также при отсутствии транзакций/действий. Выбор внутренней доставки block metadata ещё проектируется; старое правило об отсутствии block progress events не означает запрет хранить сами блоки.

Шардирование — требование с начала проектирования, а не обещание сразу развернуть кластер конкретного размера. Нужны явные ключи идентичности/порядка, детерминированная маршрутизация, корректные повторы и reorg при распределённом хранении. Shard key, resharding и topology остаются техническими решениями CH-06, проверяемыми до приёмки.

YAML выбирает сети из поддержанных адаптерами, loading bounds и runtime parameters без редактирования исходников. Изменение нижней границы уже обязано применяться без отдельной команды; hot add/remove сетей и reload остальных параметров ещё не согласованы. Пример 30 минут можно задать соответствующим номером блока; отдельный duration-параметр не считается уже утверждённым.

Новые открытые решения: OSS-01 open-source лицензия с проверкой совместимости зависимостей; OSS-02 public repository/release process; CFG-02 структура/версия YAML, network profiles и валидатор; CFG-03 поля reload/restart; CH-06 shard key и критерии распределённой приёмки. Реализацию новых network adapters одним YAML обещать нельзя.

### Отдельный пул REC — восстановление (отложен пользователем)

Обязательное поведение уже принято: запуск с тем же YAML должен запустить восстановление. Следующие вопросы сохранить для отдельного раунда, сейчас не задавать:

- REC-01: что именно потеряно и что сохранилось — adapter state, native node state, Gateway journal, NATS streams, backups? Граница восстановления текущего проекта заканчивается публикацией в NATS по D16.
- REC-02: до какой границы восстанавливать историю, если YAML не содержит раннего блока, а при первом запуске был вычислен default «3 часа»? Рекомендация — сохранять вычисленную границу как durable metadata; при полной утрате этого metadata один относительный default не восстанавливает старую границу.
- REC-03: где доступны данные для replay и как их удерживать при pruning? Конфиг задаёт намерение, но не содержит historical state.
- REC-04: какое время восстановления допустимо и нужен ли резервный сервер? Требования автоматического HA ещё не приняты.
- REC-05: требуется ли повторная публикация всей согласованной истории в пустой NATS stream или только недоставленных диапазонов; как сохранять event identities, epochs и прогресс при таком replay? Восстановление последующих систем не входит в этот раунд по D16.
- REC-06: резервирование самого YAML, resolved config, credentials references, журналов покрытия и документов проекта.

### R01 — уточнение формулировок после ответа пользователя

- Q1a закрыт по D13: Polygon PoS и Arbitrum One подтверждены вместе с остальными mainnets. Прежний вопрос сохранён только как история решения.
- Q2: с какого участка существующей истории должна работать первая версия каждой сети — со всей доступной истории или с явно заданного диапазона блоков. Речь об объёме первой приёмки, а не об исключении backfill из архитектуры.
- Q3: контракт имел 100 нативных монет, внутри одной успешной транзакции получил 10 и отправил 10. Требуется только итог на границах транзакции (100 → 100, при сохранённой трассировке) или также последовательность балансов 100 → 110 → 100? Газ в этом примере оплачивает вызывающий адрес.
- Q4: падение процесса с сохранным диском и физическая потеря сервера/диска — разные сценарии. Нужно выбрать, какой из них обязан автоматически переживать первый релиз. Это вопрос серверов экстрактора, а не сохранения контекста нашего диалога; документы диалога уже сохраняются.

Последствие D05: исследование нативного захвата и проверка схемы проводятся на всех пяти сетях до её утверждения. Общий транспорт можно разрабатывать один раз; приёмка первой поставки требует отдельных результатов всех пяти сетей. Это не утверждает одновременную разработку пятью командами или конкретное число серверов.

## Проверенные факты окружения

Проверено 2026-10-13 read-only инструментами. Это состояние среды, не выбранный стек проекта.

| Факт | Наблюдение |
|---|---|
| Исходники | В начале планирования обнаружены только два ТЗ; кода и манифестов сборки нет |
| Git | Каталог extractor не является git-репозиторием; создавать/подключать remote ещё не начинали |
| Go | Ответ локального инструмента: go1.27.1 darwin/amd64 |
| Rust / Cargo | 1.86.0 |
| Docker CLI / Compose | 29.4.0 / v5.1.2; работоспособность daemon не проверялась |
| Node | v22.21.1; необходимость в runtime проекта не установлена |
| buf | Не найден в PATH |
| protoc | Проверка --version превысила 10 секунд; работоспособность не подтверждена |

При фиксации toolchain перепроверить совместимость с выбранным commit клиента. Ничего не устанавливалось, сервисы не запускались.

## Первичные источники

Проверены 2026-10-13; наличие механизма в документации не является доказательством полноты конкретного адаптера.

- [Geth live tracing](https://geth.ethereum.org/docs/developers/evm-tracing/live-tracing) — исходная документация нативной интеграции; точные hooks проверять на pinned commit.
- [Go gRPC quickstart](https://grpc.io/docs/languages/go/quickstart/) — protobuf compiler, Go bindings и gRPC toolchain; проекту нужны закреплённые версии вместо latest.
- [NATS JetStream](https://docs.nats.io/nats-concepts/jetstream) — persistent streams, consumers и at-least-once; бизнес-идемпотентность остаётся задачей приложения.
- [Rust gRPC repository](https://github.com/grpc/grpc-rust) — URL hyperium/tonic перенаправлен сюда при проверке. Конкретные crate, API и совместимость с Rust клиента требуют отдельной проверки перед выбором; старые имена пакетов не считать автоматически актуальными.
- [Base Run a Node](https://docs.base.org/base-chain/node-operators/run-a-base-node) — повторная проверка после D05: руководство указывает на base/base, прежний base/node перенесён. Выбор commit и точек capture ещё не сделан.
- [Polygon PoS](https://docs.polygon.technology/pos/) — отдельный профиль Polygon PoS с Bor/Heimdall. Пользователь пока не подтвердил, что под Polygon имел в виду именно этот профиль.

## Как продолжать журнал

### R05 — точные сети и применение конфигурации (2026-10-14)

Статус: ответы получены. Q1a закрыт; CFG-01/03 переопределены требованиями D14/D15. Новые неоднозначности вынесены в R06.

| ID | Конкретный вопрос | Рекомендация | Ответ |
|---|---|---|---|
| Q1a | Подтверждаем Ethereum Mainnet, BSC Mainnet, Base Mainnet, Arbitrum One, Polygon PoS? | Да, эти пять production-сетей | accepted D13: да |
| CFG-01 | Управление ранее загруженной историей? | Сохранять; изменение начала не является командой удаления | D14: сборщик не управляет БД; пользователь запрашивает историю N–M параллельно live |
| CFG-03 | Изменение состава сетей? | Прежняя рекомендация одного процесса с общим списком отозвана | accepted D15: каждая сеть отдельный процесс/pod; другие сети не затрагиваются |

После ответов: подготовить проверенную матрицу network → native client/commit → язык → integration path; конкретизировать YAML schema и затем ClickHouse block/tx/details + shard design. Версии и возможности инструментов проверяет агент, а не запрашивает как факт у пользователя. Лицензия выбирается после проверки состава интегрируемого кода; восстановление обсуждается отдельным раундом REC до фиксации durable storage.

### R06 — независимый процесс сети и история N–M

Закрыт в прежней формулировке решением D16. DB-01 снят: последующая обработка не входит. JOB-01 остаётся открытым; DEPLOY-01 ограничен путём до NATS. Предыдущие рекомендации о компонентах после NATS не приняты и не разрабатываются.

## R07 — контракт публикации и границы процессов

Ответы получены 2026-10-15:
1. MSG-01 accepted / D17: строгие унифицированные типы для всех поддерживаемых семейств; все транзакции — главный приоритет. Block/coverage и lifecycle сообщения приняты; изменения балансов вне транзакций можно опустить при усложнении кода. Реализация неизвестного клиента/семейства сама собой из универсальной схемы не возникает.
2. MSG-02 accepted / D18: обычная транзакция одним сообщением, крупная — частями, без усечения. Конкретный лимит и wire details остаются задачами проектирования.
3. DEPLOY-02 accepted / D19: независимые контуры каждой сети до общего NATS; свои Gateway/WAL/потоки/лимиты. Между сетевыми процессами нет зависимостей по управлению или прогрессу.
4. JOB-01 deferred by user / D20: простое и эффективное управление историей/YAML обсудить отдельно; вариант поведения не выбран.

Далее агент проверяет native client/runtime каждой сети, состав tx types и точки захвата; проектирует versioned schema, обязательные поля, registered extensions и typed parts. Не переспрашивать принятые три решения. Управление историей и REC — отдельные пулы, сейчас не открывать.

### E00-14 — технический результат, не новые ответы пользователя

2026-10-15 по указанию «переходи к следующему шагу» созданы [реестр native типов](TRANSACTION_TYPE_REGISTRY.md) и [логический контракт](MESSAGE_CONTRACT.md). Реестр опирается на pinned source; контракт — проект реализации D17/D18, а не компилируемая schema или дополнительные принятые пользователем решения.

- Общая обязательная signature/nonce/gas модель неприменима к deposits и native system types. Используются concrete native variants и отдельная derived execution projection.
- BSC system — классификация; Bor modern StateSync — body transaction; legacy synthetic — отдельная проблема E00-15. Nitro imported legacy требует source HashOverride и различения wrapper/trie encoding.
- BlockComplete требует сверки native inventory с transaction manifest и PubAck обязательных записей. Coverage относится к явному scope и ветке; optional outside-transaction balances не обещают all-balance history.
- CompleteRecord/RecordPart сохраняют исходные bytes и digest; chain_sequence — назначение события к публикации, не порядок блокчейна или завершения частей. Старый proto/Any sketch архивирован.
- E00-15 остаётся следующим действием. Детальные execution/finality extensions, compiled proto и fixtures — E02. REC и JOB-01 не закрыты и не возобновлены этим документом.

### E00-15 — исследование исторических профилей

2026-10-15, прямое указание пользователя «давай E00-15». Результат: [Classic native capture](research/ARBITRUM_CLASSIC_CAPTURE.md) и [Polygon legacy receipts](research/POLYGON_LEGACY_CAPTURE.md). Это техническое исследование и проектные способы реализации; новые продуктовые ответы не приписываются пользователю.

- Classic — отдельный Go/C++ AVM backend в контуре Arbitrum One. Источник inventory — persisted TxResult/BlockInfo range; Ethereum projection способна отфильтровать результаты. Для полного capture предложен version-aware native observer, не изменение исторического ArbOS. Архив, machine compatibility и полнота observer ещё требуют CL-01…08/AP-06.
- Bor до Madhugiri: synthetic source receipt не является body transaction, его status/logs не доказывают все system actions. Определён отдельный optional BorLegacyReceiptRecord. После Madhugiri body StateSyncTx 0x7f обязателен. D17 сохраняется: optional публикация system data не отключает native исполнение, необходимое для правильного состояния цепочки.
- E00-15 закрыт только как исследование профилей, типов и ограничений. Полная историческая runtime-поддержка не подтверждена. Уточнения перенесены в реестр, контракт, ТЗ, матрицу и план; fixture tasks остаются E01/E02/E04/E06.
- Следующий E00-03: свести стек компонентов, владельцев состояния, границы надёжности и оставшиеся решения. REC и JOB-01 остаются отдельными отложенными обсуждениями; реализация не начата.

### E00-03 — поэтапная детализация

2026-10-15 пользователь поручил разбить E00-03, выполнять последовательно, сохранять результаты и задавать необходимые вопросы. [План .01–.07](E00_03_COMPONENT_PLAN.md) отделяет карту компонентов, границу поставки, стек, владельцев состояния, live/history, delivery и итоговую сверку.

E00-03.01 DONE: принятые решения сверены, карта компонентов и зависимости сохранены. E00-03.02 ожидает ARCH-01 (готовые Compose/Kubernetes конфигурации первой поставки) и ARCH-02 (внешний production NATS либо также его deployment configuration). Рекомендации записаны отдельно и не приняты за ответы. D15/D19 фиксируют независимость сетей и общий NATS, но не закрывают эти вопросы о составе поставки.

При сверке исправлены устаревшие REC-01/05: вопросы ограничены native state/journals/NATS согласно D16. Сам REC-раунд не открыт, ответы и гарантии восстановления не выбирались.

REC и JOB-01 остаются отдельными пулами; этот запрос не выбирает storage/HA/retention или YAML reload. Код, сервисы и инфраструктура не создавались.

### E00-03.03–05 — техническая детализация после D21/D22

[COMPONENT_STACK.md](COMPONENT_STACK.md) фиксирует проектный baseline общих компонентов: Gateway Go 1.27.2/gRPC-Go 1.84.0/nats.go 1.54.0, Protobuf/Go и Rust tonic/prost bindings; native toolchains остаются отдельными. Official NATS Helm chart/server candidates и Compose packaging подтверждены в [исследовании](research/NATS_DEPLOYMENT_STACK.md). Версии ещё не прошли совместную сборку, registry digests не закреплены.

[STATE_OWNERSHIP.md](STATE_OWNERSHIP.md) распределяет logical ownership canonical/lifecycle/IDs/source bytes/part layout/publication mapping/ACK. Это техническая модель, не принятые crash/HA/retention гарантии. .03 и .04 завершены только в этих документальных границах.

.05 ожидает ARCH-03: отдельный history-процесс по необходимости с дополнительными CPU/RAM/state либо предварительное измерение ресурсов на POC. Рекомендация отдельного worker не является ответом. Вопрос не повторяет принятую команду N–M и не открывает JOB-01 о YAML/reload. REC остаётся отдельным пулом.

### Примечание о границе D17


Вне транзакций бывают не только награды майнеров: Ethereum withdrawals определены как самостоятельные system-level operations. Источник: [EIP-4895](https://eips.ethereum.org/EIPS/eip-4895), проверен 2026-10-15. Это не отменяет право опустить расширение. Комиссии и системные операции, представленные родными транзакциями, остаются внутри обязательного сбора.

Если расширение не реализовано, нужно явно различать полную историю транзакций и неполную историю всех нативных балансов. Неподдержанный системный scope не блокирует transaction coverage; неизвестный тип транзакции или потеря её деталей — блокирует объявление полноты.

### Проверенные факты NATS для R07 (2026-10-14)

- JetStream добавляет сохранение сообщений и подтверждения к Core NATS; Core NATS сам по себе не даёт эквивалентной границы надёжной публикации. Источник: [JetStream](https://docs.nats.io/nats-concepts/jetstream).
- max_payload по умолчанию 1 MB; верхний предел настройки 64 MB, документация не рекомендует значения выше 8 MB. Это ограничения сервера, не выбранный лимит проекта; дополнительно проверяются настройки stream, клиента и gRPC. Источник: [NATS configuration](https://docs.nats.io/running-a-nats-service/configuration).
- Вывод для проекта: увеличение одного лимита не заменяет решения о крупных транзакциях; разбиение принято D18, конкретный порог остаётся задачей проектирования.
