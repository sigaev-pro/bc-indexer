# Полная сверка документации после D81

2026-11-05 · DOC-ALL-DOCS-VERIFY-01 · исходная база `967906d`.

## Вывод

Перепроверен весь набор из **64 Markdown-файлов** до добавления этого отчёта. Подтверждены четыре редакционные несостыковки низкой серьёзности; все исправлены в рамках принятых решений. Новых подтверждённых дефектов доставки или потери данных, незамкнутых продуктовых развилок и несовместимых требований текущего scope этот проход не выявил.

Два адресных ТЗ и нормативные источники согласованы после правок. Это результат документальной сверки, не доказательство корректности будущей реализации и не утверждение об отсутствии любого возможного контрпримера. Новые гарантии, runtime PASS, реализация и push не добавлены.

## Найдено и исправлено

Номера строк ниже относятся к исходной базе 967906d; ссылки ведут к актуальным файлам. Все четыре пункта имеют статус **DOC_FIXED**.

| ID | Доказательство и контрпример | Выполненная правка |
|---|---|---|
| V01 — область запрета при fatal regression | CURRENT_SEMANTICS:144, AC-LIF-05-04:109 и AC-LIF-08-05:128 запрещали «новую публикацию» без указания её области. Но D57 останавливает native owner, а DELIVERY_STATE_MACHINE:28–30 разрешает независимому Gateway обслуживать прежний корректный CLOSED batch A. Если новое решение B нарушает anchor, доставка A сама по себе не является нарушением | [CURRENT_SEMANTICS](CURRENT_SEMANTICS.md#durable-anchor-и-очистка) и [два AC](ACCEPTANCE_CASES.md) явно запрещают READY/публикацию событий конфликтующего решения и GC по нему. Старый CLOSED остаётся под обычным delivery protocol, включая CONFIG_BLOCKED; отправленные bytes не отзываются. Новый stop-all RPC не вводится |
| V02 — текущий счётчик AC | DEVELOPMENT_PLAN:3 называл актуальный mapping 63 REQ / 195 AC; каталог уже содержит 199. Это текущий указатель, не историческая статистика | В [плане](DEVELOPMENT_PLAN.md) исправлено 195 → 199; состав требований/сценариев/задач не менялся |
| V03 — статус ADR истории | ADR-0003:2,8–14 имел status:accepted и старые правила startup history/reload/N–M без отменяющей оговорки. Прямое чтение ADR могло вернуть исключённую историю в scope; входящих ссылок на ADR нет, активное ТЗ от этого не менялось | [ADR-0003](adr/0003-config-driven-history.md) помечен superseded by D36/D38, добавлены текущие live H+1/restart границы и ссылка на LIVE_CONFIG_SPEC. Исходный текст сохранён как история |
| V04 — старый аудит выглядит открытым | DEEP_DESIGN_AUDIT:3,7 и нижняя карта PROJECT_STATUS называли прежние 41 находку открытыми. DESIGN_REPAIR_STATUS:5–9 уже устанавливает историческую базу и 41/41 SPECIFIED | В [шапку аудита](DEEP_DESIGN_AUDIT_2026_11_05.md) добавлена ссылка на реестр закрытия; старая карта и список пробелов PROJECT_STATUS явно названы историческими. Исходные findings и evidence не переписаны |

V01 дополнительно перечитан после правки: он не требует приостановки всех Gateway и не разрешает публикацию вопреки destination/config errors. Область остановки соответствует D57. Остальные пункты — счётчик и маркировка истории; новых ответов владельца не требовали.

## Что проверено

Проверка разделена на три независимых прохода и общую сверку результатов: текущие данные/capture/delivery; задачи/приёмка/окружение; словарь/история/приоритет документов. Совпадение текстовых копий проверялось отдельно от смысловых контрпримеров.

| Группа документов | Проверка |
|---|---|
| TZ_DECISION_MAKER, TZ_EXECUTOR, канонический v3 | Граница native→NATS, обязательные данные и исключения; полный embedded body исполнителя; отсутствие новых consumer/history/HA/performance обязательств |
| MESSAGE_CONTRACT, CURRENT_SEMANTICS, TRANSACTION_TYPE_REGISTRY | Receipt/type applicability, наличие полей, actions/scopes/CREATE, lifecycle/revisions, Polygon source digest, fragments/parts и независимые identity/order пространства |
| NATIVE_CAPTURE_PROTOCOL, DELIVERY_STATE_MACHINE, CONTROL_WIRE_CONTRACT | Admission/pins/decision mirror; READY/SEALED; partial/full ACK; RecoveryRelease и retirement; fresh-store replay; fencing; drain/switch/Resume и отложенные ответы |
| LIVE_CONFIG_SPEC, NODE_DEPLOYMENT_SPEC, DEPLOYMENT_BOOTSTRAP_CONTRACT, NETWORK_PORTS, COMPONENT_STACK | Три сети, 11 services/14 volumes, отдельная Polygon L1, роли/JWT/credentials, YAML paths, effective config, размеры и границы startup/recovery |
| ACCEPTANCE_REQUIREMENTS/CASES, TASK_DECOMPOSITION, DEVELOPMENT_PLAN, IMPLEMENTATION_GATES | ID/coverage/mapping/stages/DAG; ранний E01 proof, E03 delivery-only, E05 combined, E06 user deployment; CaseRegistry/CaseResult/RunManifest и применимость вариантов |
| AGENTS, CONTEXT, DECISIONS, PROJECT_STATUS, REMAINING_AGREEMENTS, FINAL_SPEC_REVIEW | Постоянная цель D64, граница D70, определения и действующие решения; текущий статус отдельно от истории |
| BALANCE_MODEL, CLIENT_MATRIX, COLLECTION_ARCHITECTURE, CONFIG_CONTRACT, CONFIG_CLI_DRAFT, DELIVERY_RECOVERY, ROCKSDB_DELIVERY_PROTOCOL, LIVE_HISTORY_DELIVERY, STATE_OWNERSHIP, E00 architecture/component docs | Исторические шапки, ссылки на действующие контракты и отсутствие самостоятельной нормативной силы старых next steps |
| Все audit/review/repair reports, шесть ADR, archive proto, все research files и исходный v2 | Базовые редакции/датировки, принятые versus предложенные варианты, актуализирующие оговорки и источники текущего статуса; неизменяемость v2 |

## Проверки структуры

- **63 REQ / 199 AC / 68 child tasks / 7 gates.** Все REQ покрыты; каждый AC назначен ровно одной существующей задаче с согласованным stage. Дубликатов REQ/AC/child IDs, неизвестных dependencies и циклов нет.
- Все **15 нормативных зеркал** совпадают после предусмотренной нормализации заголовков/ссылок. Тело TZ_EXECUTOR совпадает с каноническим v3.
- Все Markdown-таблицы проверены на число колонок; явные anchors и ссылки проверены. Точные итоговые числа после сохранения отчёта — PROJECT_STATUS.
- Проверены child ID ссылки в подробном ТЗ, DEVELOPMENT_PLAN и IMPLEMENTATION_GATES. Нового параллельного реестра задач нет.
- `git diff --check` пройден. В корне остаются только два адресных ТЗ; отчёт хранится в .project.
- Исходный v2 сохранён: SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`.

## Что не является новым дефектом

- Исторические 156/195 AC и старые source pins в датированных отчётах сохраняют смысл соответствующей базы. Они не заменяются механически на текущие числа/версии.
- REMAINING_AGREEMENTS — снимок после D65. WAL_ALTERNATIVES/WAL_ENGINE_SELECTION — исследования выбора; действующий стек уже выбран. Их старые вопросы не открывают выбор движка заново.
- PREPARING не требуется получать через GetBatchState: BeginPrepare уже возвращает сохранённый offset/reservation.
- Отдельное activation_id без inclusion/block/producer/run qualification не обязано быть глобально уникальным; полной ссылки это не лишает однозначности.
- Gateway loss после producer payload cleanup покрыт сохранением полного IngestAck/outbox и RecoveryRelease.
- E01 не зависит от полного результата E04; E03 test harness не требует deployment E06. COMMON не означает общий writable store.
- DOC evidence, optional/deferred applicability и source review не выдаются за пройденные runtime tests.

## Граница вывода

Внешние releases/URLs и совместимость pinned builds заново полностью не проверялись. Native-код, сборки, fixtures и runtime не исполнялись. Проверка отвечает на вопрос о внутренней согласованности текущего комплекта документации; прежнее исследование источников остаётся со своей датой и ограничениями.

После четырёх правок открытых замечаний этого прохода нет. Обязательная следующая документальная задача не требуется; новый конкретный контрпример или требование оформляется отдельным изменением.
