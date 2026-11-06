# Bootstrap разработки bc-indexer

Пользователь D84 разрешил переход к разработке. Ты — координатор подготовки. Прочитай DEVELOPMENT_AUTOPILOT.md, .project/AGENTS.md, .project/PROJECT_STATUS.md, .project/docs/DECISIONS.md и .project/docs/ISSUE_CATALOG.md. Только репозиторий sigaev-pro/bc-indexer. Соблюдай продуктовый scope TZ_EXECUTOR.md и существующие REQ/AC/task IDs. Старый запрет D64 superseded D84.

Твой результат — закрытые доказательствами BOOT-01…BOOT-09 и проверяемый controller, а не реализация E01–E07 внутри одной сессии. Launcher запустит продуктовый цикл только после verify-bootstrap. Выполняй подготовительные issues по их зависимостям. До первого изменения кода сверяй полный реестр 84 issues: публикуй отсутствующие по marker, не создавай дубликатов. Не закрывай issue на основании наличия текста в ТЗ.

Разрешены адаптация ролей agency-agents, отдельные worker/reviewer sessions, создание orchestration/CI кода и его meaningful tests, локальные commits, обновление issues/PR, push своих веток и merge после проверки. Initial publish в main допустим только после подтверждения, что выбранный remote пустой; никакого force push. Для существующего main сохраняй его историю и создавай обычный PR. Каждая законченная issue получает проверку и commit до следующей.

BOOT-03: реализуй .project/automation/controller.py по runbook, включая CLI, durable state/leases, восстановление, task DAG и независимый review. Докажи на изолированном fixture backend отсутствие ложного DONE после exit 0/пустого JSON, duplicate selection, crash после GitHub mutation, stale review/CI и небезопасной повторной записи. Проверки не должны создавать лишние настоящие issues или запускать production. Custom agents необязательны для scheduling: отдельные codex exec с явными role instructions допустимы. Сам controller не подменяй одним бесконечным prompt loop.

BOOT-07/08: проработай открытые критерии окончания потока и independent corpus с отдельным review. Внутри принятых гарантий выбери рациональное решение самостоятельно и запиши ADR/DECISIONS; при необходимости изменить пользовательский scope сохрани BLOCKED_DECISION. Нельзя выдать отсутствие решения за runtime PASS.

Если credentials/managed sandbox/ресурсы мешают — сохрани точный blocker, не отключай ограничения и не заявляй готовность. Не расходуй mainnet funds, не выполняй production deployment и платные provisioning операции. Testnet signing появляется только после подготовленного signer/budget и профильной проверки.

Перед выходом выполни controller verify-bootstrap; положительный результат должен опираться на реальные BOOT issues, commits, tests и catalog digest. Сохрани статус и report; если результат отрицательный, явно перечисли blockers. Не запускай product controller фоном из tool subprocess: этим владеет внешний launcher. Не подменяй инструкции запуском глобального resume --last. Повторный bootstrap идемпотентен, использует реальные Git/GitHub/evidence состояния и не теряет чужие файлы.
