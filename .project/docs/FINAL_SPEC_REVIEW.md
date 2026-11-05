# Финальная сверка и карта полного grill

Актуализация D73 / DOC-F11-FIX-01: F11 закрыт в документации — bounded typed fragments до сериализации, крупные поля/коллекции, exact manifests/transport/recovery/all-fragments ACK. Текущий каталог: 63 REQ, 164 AC, 68 child tasks и 7 gates. Остальные 13 находок [полного аудита](FULL_AUDIT_APPROVAL_2026_11_05.md) ещё не согласованы; итоговое ТЗ пока не объявляется бездефектным. Ниже сохранена историческая проверка D65 со своими числами. Runtime NOT_RUN.

2026-11-05 · DOC-FINALIZE-03 / D65. [Единое ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md) подготовлено как полная основа для grill: краткое ядро и все нормативные приложения в одном MD. По D64 наша работа навсегда ограничена проектированием и документацией; реализации, сборок, runtime-тестов, deployment и push не было.

## Устройство пакета

- Краткое ядро: 95 строк, 1 461 слово — назначение, границы, ответственность компонентов, сквозной процесс, гарантии, отказы и правила приёмки. Число страниц не является техническим ограничением.
- Приложения A–E сохраняют данные/семантику, capture/recovery, delivery/control, конфигурацию/окружение и приёмку/декомпозицию. Точные контракты не заменены кратким пересказом.
- [63 родительских REQ](ACCEPTANCE_REQUIREMENTS.md) раскрыты в [156 атомарных AC](ACCEPTANCE_CASES.md). Один AC задаёт один исход; именованные эквивалентные variants и сети имеют самостоятельные verdict.
- [68 child tasks / 7 gates](TASK_DECOMPOSITION.md): выход, owner, конкретные зависимости и точное назначение всех AC. Три полные карточки показывают native admission, delivery recovery и Polygon observer/GC evidence.
- [Словарь](../CONTEXT.md): 51 термин, включая различия native/canonical, action/effect, run/process, readiness, ACK/cleanup/GC и drain/Resume/cutover. Исторические понятия помечены DEFERRED D38.
- 41/41 находки прежнего аудита остаются SPECIFIED. Это исторический аудит, а не число всех требований и не runtime verdict.

## Дерево полного grill

Это карта покрытия интервью, не требование повторно согласовать принятые ответы. Родительские ветки задают предпосылки дочерних. Для каждого сценария сначала проверяется существующий контракт; пользователю задаётся вопрос только при реальном несовпадении ожидаемого результата или отсутствии решения.

| Ветка | Зависит от | Что проверяется конкретными сценариями | Основание ответа |
|---|---|---|---|
| G01. Назначение и scope | D64: только ТЗ | Три mainnet profiles; live H+1; failed/system tx и пустой блок; optional outside-tx; отсутствие history/consumer/HA обещаний | REQ-SCP-01…06; принятые D16/D17/D38/D42 |
| G02. Источник и capture | G01 | Crash до/после ADMITTED; static inputs против consumed dependencies; mutable alias; большой trace; pins/pruning; EXEC_READY/child-root/native marker/mirror; recovery до LIVE | REQ-CAP-01…10, NATIVE_CAPTURE_PROTOCOL; D57/D58 |
| G03. Модель данных | G01 | Source encodings и presence; signatures; CALL/CREATE/SELFDESTRUCT; child success при ancestor revert; committed logs; before/after; комиссии без двойного учёта; BSC milliseconds | REQ-DAT-01…10, MESSAGE_CONTRACT/CURRENT_SEMANTICS; D62 |
| G04. Lifecycle и финальность | G02, G03 | Applied ещё не ACKed при reorg; reinclusion; finality без нового блока; BSC early/header provenance; Polygon purge milestone/L1 checkpoint; source outage против authoritative anchor regression | REQ-LIF-01…08; D62/D63 |
| G05. Доставка | G02, G03 | READY/SEALED/CLOSED; COMPLETE/PART boundaries; потеря Prepare/PubAck/IngestAck/ReleaseAck; Gateway/producer store loss; fencing; corrupted immutable pending; backoff против fatal/blocked | REQ-DLV-01…09/12…14, DELIVERY_STATE_MACHINE/CONTROL_WIRE_CONTRACT; D26–D33/D54/D57 |
| G06. Конфигурация и обслуживание | G04, G05 | Bootstrap crash и сохранение ключей; owner/network identities; effective profiles; supporting provenance; drain без будущей финальности; lost switch/Resume reply; destination change без переноса истории; wire/store compatibility | REQ-OPS-01…10, REQ-DLV-10…11/14; D43–D60/D63 |
| G07. Проверяемость и задачи | G01–G06 | Независимый expected; точное место fault и наблюдаемый исход; ранний против полного proof; owner/output/dependencies; applicability и evidence; отсутствие циклов и непокрытых REQ/AC | REQ-DEV-01…05, ACCEPTANCE_CASES/TASK_DECOMPOSITION/IMPLEMENTATION_GATES |
| G08. Фиксация ТЗ | G07 | Краткое ядро не меняет подробные гарантии; единый нормативный root; термины однозначны; история не добавляет требований; новые решения отделены от редакторских правок | D64/D65, этот отчёт, PROJECT_STATUS |

Новая пользовательская развилка в текущей документальной проверке не выявлена. Это не означает, что пользователь уже прошёл новое интервью по всем карточкам или что будущий контрпример невозможен. При новом ответе/контрпримере фиксируются ветка G, REQ/AC, исходный ожидаемый результат, изменение решения и затронутые зависимости. Соседние ветки не переоткрываются без причины.

## Исправления финализации

1. Крупные REQ сохранены как родители; независимые positive/negative и pre/post outcomes получили отдельные AC IDs.
2. REQ и AC связаны с конкретными child tasks, owners, артефактами и prerequisites. Ранний native proof не требует готового production transport; поздний composed proof не объявляется пройденным по раннему.
3. E02 больше не предлагает заново «утвердить спорные поля» или «определить модель»: требуется соответствие уже закреплённым контрактам.
4. Binding сохраняется каждым owner локально до obligations; cross-owner проверка происходит при первом authenticated handshake до Prepare/Publish, а не до любого сетевого обмена.
5. Краткий startup flow исправлен после независимой сверки: RECOVERY_ONLY hooks/decision bridge до local recovery; static inputs/pins до ADMITTED, dynamic dependency observations durable по мере исполнения до передачи native ответа.
6. Словарь отделяет действие от адресных effects и fee summary, transport cleanup от recovery GC, run от процесса, а live boundary от historical boundary.

## Документальные проверки

Проверены локальные ссылки и anchors, структура Markdown-таблиц, совпадение 15 включённых нормативных разделов с тематическими файлами после нормализации заголовков/ссылок, сохранение согласованных контрактов, полное покрытие 63 REQ и однозначное назначение 156 AC задачам. Граф 68 tasks и 7 gates не содержит неизвестных зависимостей или циклов; stages AC и tasks совпадают.

Числа последнего полного прогона фиксируются в PROJECT_STATUS. git diff --check пройден. Исходный v2 сохранён: SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`. Все изменения — документация.

## Граница вывода

SPECIFIED означает законченный проверяемый текст. Runtime каждого AC — NOT_RUN. Реальные fixture artifacts/запуски/evidence, image/dependency locks, build/ABI compatibility, bootstrap genesis payload и power-loss durability не создавались и не доказывались документацией. ТЗ определяет ожидаемое поведение и условия, при которых предполагаемый результат нельзя объявлять подтверждённым.
