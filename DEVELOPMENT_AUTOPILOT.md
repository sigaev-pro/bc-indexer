# Разработка extractor с agency-agents

**D92 — Current launch: seven hours of autonomous development.** The owner authorized the accepted E01 through E07 backlog, in dependency and gate order, plus necessary automation work. The shared window runs from 2026-11-07 02:17:27 to 09:17:27 Europe/Moscow. Preparation counts toward the deadline. Technical decisions, independently reviewed PRs, and protected merges do not need another routine confirmation. Preserve consumed budgets and attempts across configuration updates. Record blockers and continue independent work; retain unresolved questions for the final report. Product requirements, production deployment, and access boundaries remain unchanged. See [D92](.project/docs/DECISIONS.md) and [PROJECT_STATUS](.project/PROJECT_STATUS.md).

**D86 — Previous launch: BOOT only.** Bootstrap completed and stopped before E01. Its existing runs keep their original authorization; resume cannot silently adopt D92. The new launch requires its own recorded scope and bounded budget.

**2026-11-07 — Bootstrap complete.** BOOT-01 through BOOT-09 are accepted, and the persistent controller completed a BOOT run with nine DONE tasks and 75 PLANNED tasks. The initial bootstrap preparation instructions below describe completed setup. Use [PROJECT_STATUS](.project/PROJECT_STATUS.md) and the current [automation guide](.project/automation/README.md) for D92. Only E01-01.evidence initially has a configured product-task verifier; later task verification remains blocked until reviewed contracts exist.

**Agent setup:** project roles and the explicit-prompt loader are in [.project/agency/README.md](.project/agency/README.md). BOOT-02 pins a project-local Codex 0.167.0 because the host CLI 0.145.0 cannot run the owner's configured model. Follow that file's exact install command before using the launcher below. Use [PROJECT_STATUS](.project/PROJECT_STATUS.md) for accepted tasks and remaining runtime checks.

**D85 — English-only project output.** All new project text must use clear, simple American English: issues, PRs, reviews, code comments, documentation, commits, test descriptions, CLI messages, logs, and reports. Avoid British expressions, regional idioms, and unnecessary jargon. Follow [.project/AGENTS.md](.project/AGENTS.md). Use the English ISSUE_CATALOG when creating or updating issues; preserve task IDs, dependencies, acceptance criteria, and exact technical names. Every worker and reviewer must enforce this rule.

Решение D84, 2026-11-06. Разработка разрешена: прежнее ограничение «только ТЗ, реализации никогда не будет» отменено прямым указанием пользователя. Продуктовая граница сохраняется: native-данные → стандартизированные записи → подтверждённая доставка в NATS. Репозиторий: [sigaev-pro/bc-indexer](https://github.com/sigaev-pro/bc-indexer). Сначала создаются все issues, затем работа проходит подготовку и E01–E07 по порядку.

**Что готово сейчас:** этот регламент, нормативное ТЗ и [84 опубликованные issues](.project/docs/ISSUE_MAP.md), #1–#84 в выбранном GitHub. Код продукта и управляющий процесс ещё не созданы; ни один runtime gate не пройден. Первые подготовительные issues создают рабочую автоматизацию. Команды ниже различают реально доступный старт через Codex и будущие команды controller, доступные после BOOT-03. Результат bootstrap проверяется отдельно; успешный exit Codex сам по себе не означает готовность автоматизации. Документы пока локальные: BOOT-01 публикует исходную базу, этот проход не выполнял git push.

## 1. Как будет идти работа без постоянного участия владельца

Управляющий процесс выбирает следующую разрешённую issue, запускает исполнителя в отдельном Git worktree, затем независимого проверяющего. После тестов и review публикуется PR. Слияние разрешено только при выполнении критериев на проверенном commit. После всех задач этапа отдельная gate issue проверяет весь этап. Только её успешное закрытие открывает следующий этап.

Один долгий чат не является управляющим процессом. Нужен сохраняющий состояние controller, который переживает завершение/ошибку отдельного `codex exec`, умеет продолжить работу с GitHub/Git/evidence и не считает текст «готово» доказательством. BOOT-03 создаёт его до запуска продуктовых задач. Agency-agents предоставляет инструкции ролей; запуск процессов, scheduling, durable state и права обеспечивает controller и среда.

Владелец один раз готовит доступы и запускает bootstrap. Дальше агенты самостоятельно выбирают технические детали внутри ТЗ, пишут код/документацию, запускают проверки, создают PR и исправляют замечания. При внешнем блокере они сохраняют причину и продолжают независимые разрешённые задачи. Изменение объёма продукта, необратимая операция над пользовательскими данными или недоступные права не угадываются. Безусловную автономность при отсутствии сети, средств testnet, compute или API-квоты обещать нельзя.

## 2. Источники истины и полномочия

- [TZ_EXECUTOR](TZ_EXECUTOR.md) — нормативное поведение; [TZ_DECISION_MAKER](TZ_DECISION_MAKER.md) — краткие границы.
- [.project/docs/DECISIONS.md](.project/docs/DECISIONS.md) — решения; D84 отменяет запрет D64, но не переписывает прошлые результаты.
- [.project/docs/TASK_DECOMPOSITION.md](.project/docs/TASK_DECOMPOSITION.md) — 68 продуктовых child task IDs, outputs/dependencies; [.project/docs/ACCEPTANCE_CASES.md](.project/docs/ACCEPTANCE_CASES.md) — 217 AC с вариантами.
- [.project/docs/ISSUE_CATALOG.md](.project/docs/ISSUE_CATALOG.md) — полный начальный backlog: 9 BOOT + 68 child + 7 gate = 84 issues. Изменение каталога требует versioned reconciliation, не скрытого расширения задачи.
- [.project/PROJECT_STATUS.md](.project/PROJECT_STATUS.md) — последняя подтверждённая точка; GitHub issues/PR/CI и сохранённые manifests — фактические результаты.

Запуск приведённой ниже команды авторизует controller работать только в выбранном репозитории: branches/worktrees, commits, issue/PR updates, push своих веток и merge прошедших gate PR. BOOT-01 может впервые опубликовать текущую историю в пустой `main`; force push, удаление чужих веток/данных и обход branch rules запрещены. Production deployment, платные покупки и mainnet signing не входят в запуск. Контролируемые testnet actions выполняются только выделенным signer с заданным бюджетом.

## 3. Роли: небольшой состав с точными обязанностями

**Coordinator — Project Shepherd + адаптированный Agents Orchestrator.** Читает issues и состояние, выбирает READY issue, составляет handoff и следит за зависимостями. Не изменяет критерии ради зелёного результата, не принимает собственную реализацию. Scheduler исполняется controller, а не исключительно рассуждением координатора.

**Architect — Software Architect.** Закрывает технические неопределённости внутри ТЗ, проектирует интерфейсы и failure cases. В BOOT-07/08 предлагает конкретное закрытие TEST-OPEN-01/02; независимый review обязателен. Если решение меняет пользовательскую гарантию, issue остаётся BLOCKED_DECISION.

**Native Engineer — Backend Architect, адаптированный отдельно для Geth/BSC/Bor.** Работает с закреплёнными upstream sources, hooks/state/receipts/finality и соответствующим профилем. Результат одной сети не закрывает другую. Solidity-экспертиза сама по себе не доказывает корректность native-клиента.

**Protocol Engineer — Backend Architect.** Владеет Protobuf/fragment codec, IDs, WAL, Gateway, ACK/recovery и bounded-memory путями. Использует точные контракты, не добавляет произвольные очереди, DLQ или drop policy.

**DevOps — DevOps Automator.** Готовит runner, reproducible builds, CI, pinned images, scripts/refresh-versions и test environment. Production-права и runtime secrets в модель/логи не передаются. Подготовка окружения учитывается отдельно от 30-минутного standard run.

**QA — Test Results Analyzer с backend-адаптацией.** Проверяет AC/profile/fork/suite matrix, independent expected, retries и evidence. Не заменяет native trace oracle одним receipt или количеством сообщений. Test Automation Engineer можно использовать только после удаления browser-only инструкций; Playwright не обязателен для этого продукта.

**Reviewer — Code Reviewer + адаптированный Reality Checker.** Отдельная сессия, конкретный diff и commit; ищет нарушение ТЗ, потери/дублирование, ошибки recovery и слабые тесты. Не пишет реализацию этой issue и не принимает вывод исполнителя без проверки. Штатные UI/screenshots/«три retry» правила agency не переносятся; действует наш контракт D83.

**Technical Writer.** Обновляет инструкции, решения, runbooks и оба ТЗ по реальным изменениям. На маленьких issues эту обязанность выполняет исполнитель; нормативный diff проверяется отдельно. Один worktree имеет одного writer.

Default concurrency: одна выполняемая продуктовая issue и один reviewer, максимум три одновременно активные model sessions с координатором. Параллельные разные issues разрешаются после проверки отсутствия общих writable files/stores; merge всегда сериализован. Нет обязанности запускать все роли на каждую задачу.

## 4. Подключение agency-agents

Исходный набор: [agency-agents](https://github.com/msitarzewski/agency-agents), проверенная ревизия `b859c515d813efcfc4755d3471dd9e7686c7706d`. BOOT-02 сохраняет source commit и SHA-256 выбранных Markdown; лицензия сохраняется. Обновление ролей — отдельная reviewable revision, не автозамена инструкций между попытками.

Роли берутся из `project-management/project-management-project-shepherd.md`, `specialized/agents-orchestrator.md`, `engineering/engineering-software-architect.md`, `engineering/engineering-backend-architect.md`, `engineering/engineering-devops-automator.md`, `engineering/engineering-code-reviewer.md`, `engineering/engineering-technical-writer.md`, `testing/testing-test-results-analyzer.md`, `testing/testing-reality-checker.md`. BOOT-02 проверяет наличие файлов на pin; отсутствие — явная ошибка.

BOOT-02 создаёт project-local адаптированные инструкции в `.project/agency/`, а при использовании Codex custom agents — `.codex/agents/*.toml` с name/description/developer_instructions. Не устанавливать весь roster глобально и не копировать штатный автономный pipeline без адаптации. Наши роли содержат scope, input/output, allowed paths, проверяемый результат и handoff; не маркетинговые обещания квалификации. Если custom-agent discovery недоступен в установленной версии Codex, controller запускает отдельные `codex exec` с явным role prompt — скрытого fallback на непроверенную роль нет.

## 5. Issue-first: до продуктового кода

Каждая issue содержит стабильный ID, этап, owner role/module, dependencies, profile applicability, ссылки REQ/AC, конкретный output, команды проверки/необходимые evidence и определение DONE. Первоначально все 84 issues открыты; «описано в ТЗ» не означает DONE.

Создание идемпотентно по marker `<!-- extractor-task:ID -->`: повторный запуск читает все страницы open/closed issues и сверяет marker/body revision. Потерянный ответ API не является поводом создать дубликат. Ни одна незнакомая/чужая issue не переименовывается и не закрывается. Новая обнаруженная работа сначала добавляется issue/dependency, затем исполняется. AC variants живут в CaseRegistry — не нужно создавать тысячи issues по одной на транзакцию.

BOOT-09 проверяет начальную полноту каталога и готовность автоматизации. E01 ждёт BOOT-09; каждый E02–E07 ждёт `G.E(previous)`, плюс собственные зависимости child task. Gate issue ждёт все child issues своего этапа и профильные evidence. Закрытие issue вручную без принятого commit/evidence не освобождает зависимость автоматически.

`verify-bootstrap` проверяет BOOT-01…08, каталог и сам controller; заранее закрытая BOOT-09 не является его prerequisite. BOOT-09 закрывается по успешному report этой проверки. Команда `run` требует уже принятую BOOT-09 и повторяет preflight — круговой зависимости между проверкой и её собственной gate issue нет.

Этапы: BOOT — организация и закрытие двух открытых критериев; E01 — native source и ранний harness; E02 — формат/IDs/валидация; E03 — durable delivery/NATS; E04 — полная семантика; E05 — lifecycle/recovery; E06 — комплект окружения; E07 — вся применимая приёмка. BASE/ARB/history не добавляются в backlog первого контура.

## 6. Контроллер: что должно работать до E01

BOOT-03 создаёт `.project/automation/controller.py` с командами `verify-bootstrap`, `run`, `resume`, `status`, `stop`. Это будущий результат issue, не уже существующая программа. Durable state хранится в `.project/automation/state/` (gitignored), artifacts/logs — в `.project/automation/runs/` (gitignored), переносимые manifests с hashes — в evidence. `resume` восстанавливается по явному run ID и GitHub/Git, не по глобальному `codex resume --last`.

State machine issue: PLANNED → READY → IMPLEMENTING → REVIEW → VERIFYING → MERGING → DONE. BLOCKED содержит код/причину/что нужно; FAILED содержит попытку и артефакты. READY вычисляет controller из DAG и доказательств, не из текста LLM. State update атомарный; singleton process lock, per-issue lease и run ID предотвращают двух писателей. После crash сверяются PR/head, CI/checks, branch и artifacts до любой повторной mutation. Не терять незакоммиченные изменения и не делать destructive reset при resume.

Каждый worker получает issue snapshot, base SHA, spec/role revision, dependencies evidence, allowed paths и output schema. Итог JSON содержит task ID, commit SHA, changed files, test commands/results/evidence refs, blockers, proposed status. Controller сверяет всё сам: exit 0 или валидный JSON не доказывает DONE. Любой новый commit после review инвалидирует review этого diff; CI должен относиться к фактическому merge candidate. PR не auto-close issue до проверки всех обязательных результатов.

Reviewer — отдельная сессия и read-only task; он не получает инструкцию «подтверди решение автора». При одном GitHub actor нельзя изображать review вторым человеком: отчёт сохраняется отдельным artifact/check, а branch policy настраивается на действительно доступный reviewer/check workflow. Запрещён admin bypass для обхода требуемого GitHub approval.

По умолчанию: один worker за раз; максимум три model sessions; до двух циклов исправления review (три реализации) на issue; общий запуск controller до 24 часов и 200 model invocations, затем resumable BUDGET_STOP; timeout одной worker-сессии настраивается (default 60 минут), без бесконечного ожидания. Это пределы оркестрации, не retry тестов: runtime FAIL допускает только один диагностический clean retry по D83, успешный retry не даёт зелёного gate. Инфраструктурные API ошибки имеют ограниченный backoff, затем BLOCKED.

Controller не хранит весь секретный environment в manifest. CLI auth/сетевые разрешения проверяются BOOT-04; отсутствие прав приводит к BLOCKED_ACCESS, без попытки отключить sandbox или изменить правила безопасности. Остановка controller завершает subprocess groups, сохраняет состояние и не портит native stores; graceful timeout и аварийное завершение отмечаются раздельно.

## 7. Как подготовить и запустить

Проверено локально 2026-11-06: `codex-cli 0.145.0`, `gh` авторизован, Docker CLI присутствует. Работоспособность Docker daemon/ресурсов и unattended git/network permissions ещё не проверена. GitHub репозиторий на начало подготовки пустой. API-модель и auth использует текущая конфигурация Codex; workflow не навязывает непроверенное имя модели.

Однократные prerequisites: доступ Codex, `gh` с issues/contents/PR/check permissions в выбранном repo, Git, Python 3, Docker и доступные ресурсы runner. Для public-smoke позже нужны подготовленные ноды и ограниченные testnet keys/funds. Рабочие secrets вводятся средствами окружения, не текстом issue. Для непрерывной работы предпочтителен выделенный runner, который не засыпает; ноутбук со sleep не даёт unattended uptime. BOOT-04 проверяет фактическую возможность сборки/сети/Git внутри выбранной sandbox; найденный запрет не обходится.

The first launch creates and verifies bootstrap automation, then exits. It does not start E01. Use the current PROJECT_STATUS before repeating a launch, and preserve D86's stage limit:

```bash
cd /Users/vadim/projects/bonpay/extractor
mkdir -p .project/automation/runs
nohup caffeinate -i -s bash -c '
  set -euo pipefail
  .project/cache/codex-0.167.0/node_modules/.bin/codex exec --sandbox workspace-write \
    -c approval_policy="\"never\"" \
    -c sandbox_workspace_write.network_access=true \
    --json -o .project/automation/runs/bootstrap-last.txt \
    - < .project/automation/BOOTSTRAP_PROMPT.md \
    > .project/automation/runs/bootstrap.jsonl \
    2> .project/automation/runs/bootstrap.stderr.log
  python3 .project/automation/controller.py verify-bootstrap --repo sigaev-pro/bc-indexer
' < /dev/null > .project/automation/runs/launcher.log 2>&1 &
```

This macOS launcher uses a process-scoped `caffeinate` guard. It does not change global power settings and does not protect against closing the laptop lid, shutdown, or loss of power. Use a dedicated runner for stronger uptime requirements. Linux runners need their own supervisor; do not copy the macOS guard there. The controller is created in BOOT-03. If bootstrap exits early, leaves a blocker, or fails verification, the launcher stops. Before repeating it, inspect saved state and logs and reconcile any existing process. Initial bootstrap recovery remains limited until the controller exists.

После BOOT-03 доступны:

```bash
python3 .project/automation/controller.py status
python3 .project/automation/controller.py stop --run-id RUN_ID
python3 .project/automation/controller.py resume --run-id RUN_ID
```

The controller implementation and exact recovery procedures are documented in [.project/automation/README.md](.project/automation/README.md). Public `run` requires accepted BOOT-09 plus current preflight; `verify-bootstrap` checks its BOOT-01 through BOOT-08 prerequisites. D86 still authorizes only BOOT. A later stage needs a new reviewed owner authorization record and declared task verification/acceptance configuration. Readiness alone cannot start E01. `resume` preserves the original authorization, task snapshots, operation identities, attempts, and consumed budget. A budget stop needs an explicit `--renew-budget` to receive one new bounded window; earlier usage remains recorded.

Service startup uses `resume --automatic` and preserves durable operator stops, budget stops, and blockers. Explicit operator resume can replace a stopped session only after authenticating its retained result and complete process-group cleanup. The replacement has a new linked operation ID and consumes the existing attempt and invocation budgets. A failed model session requires a separate explicit diagnostic `--retry-session`; failed task verification remains blocked, and D83 runtime failure rules are unchanged.

BOOT-09 configures only the initial E01-01.evidence artifact verifier and exact-task acceptance path. The trusted handoff and remaining verifier ownership are documented in [.project/automation/evidence-task-contract.md](.project/automation/evidence-task-contract.md). A future reviewed owner launch is still required; D86 cannot dispatch E01. Initial-task readiness does not claim that the other E01 recipes or native runtime verifiers exist. Later recipe additions require an explicit reviewed handoff to a fresh immutable snapshot without resetting consumed budgets or attempts. Generic CI/review evidence cannot accept a product artifact without its authenticated controller verification proof.

RUN_ID берётся из status/manifests, не угадывается. Длительный resume запускается под тем же supervisor/nohup. `stop` — явное намерение оператора, оно не считается FAIL продукта. Для рестарта машины BOOT-03 документирует установку service wrapper на выбранном runner; `nohup` переживает закрытие терминала, но не reboot. Автостарт после reboot включается только в заранее выбранном выделенном окружении, не глобально на личном компьютере.

Команды не используют устаревший `--full-auto` и не отключают sandbox. `approval_policy=never` не выдаёт дополнительные права: запрещённое действие не сможет спросить и будет блокером. Версия CLI/managed policy могут ограничивать возможности; preflight обязан это показать до автономного цикла.

## 8. Что видит владелец

На GitHub: исходный полный backlog, текущая issue/PR, evidence и gate issues. В status: этап, текущий task, последняя проверенная revision, завершённые/blocked issues, бюджет и точное действие для снятия блокера. Отчёт обновляется после issue/этапа и при остановке; постоянное участие владельца в обычных technical/review решениях не требуется.

DONE проекта означает закрытый G.E07 с полной применимой матрицей, принятыми commits и воспроизводимыми артефактами. Это не production deployment. Если open questions или внешний блокер остались, отчёт перечисляет их и не заявляет «всё готово».

## Проверенные основания

- [Agency README](https://github.com/msitarzewski/agency-agents/blob/b859c515d813efcfc4755d3471dd9e7686c7706d/README.md) и [handoff templates](https://github.com/msitarzewski/agency-agents/blob/b859c515d813efcfc4755d3471dd9e7686c7706d/strategy/coordination/handoff-templates.md) — роли/передача задач; собственный controller этим репозиторием не гарантируется.
- [Agency Codex integration](https://github.com/msitarzewski/agency-agents/blob/b859c515d813efcfc4755d3471dd9e7686c7706d/integrations/codex/README.md) — формат интеграции, требующий проектной адаптации.
- [Codex non-interactive mode](https://developers.openai.com/codex/noninteractive), [subagents](https://developers.openai.com/codex/subagents), [security](https://developers.openai.com/codex/security) — exec/JSON/schema, custom agents и реальные границы полномочий. Конкретные flags также сверены с локальным CLI help.
