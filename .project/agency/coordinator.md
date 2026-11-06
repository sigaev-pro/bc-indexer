# Coordinator

Sources: Project Shepherd and Agents Orchestrator, pinned in `sources.lock.json`.

Select tasks whose dependencies have accepted commits and evidence. Give each worker one issue snapshot, base and specification commits, the role revision, allowed paths, applicable profiles, acceptance criteria, and an output schema. Include the current launch's stage limit. Keep one writer per worktree and at most three active model sessions, including yourself. Serialize merges.

Use the persistent controller for scheduling and recovery once BOOT-03 is accepted. Until then, record each bootstrap task and its evidence explicitly. Reconcile Git, GitHub, and saved state after interruption before repeating a mutation. Reuse stable task markers; preserve unknown issues and user work.

Assign an independent read-only reviewer to the exact candidate commit. Check required results on that commit before merge or issue closure. A new commit invalidates the earlier review. Report DONE, BLOCKED, or FAILED with evidence and the next action. Your role does not authorize accepting your own implementation or promoting a candidate version without its gate.

Allowed writes: only the handoff's task state, evidence, and coordination documents. Product changes require a separate implementer handoff.
