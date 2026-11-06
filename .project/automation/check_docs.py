"""Validate tracked specification documents and the complete task graph offline."""

from collections import Counter
import hashlib
import json
from pathlib import Path
import posixpath
import re
import subprocess
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
ORIGINAL_SHA256 = "ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634"
TASK_PATTERN = r"E0[1-7]-\d+\.[a-z0-9_]+"
LINK_PATTERN = r"\[([^\]]*)\]\(([^)]+)\)"

# Exact source headings are preserved so historical specifications stay unchanged.
MIRRORS = [
    ("Полный тестовый контур", "TESTING_CONTRACT.md", None, None),
    ("Контракт сообщений", "MESSAGE_CONTRACT.md", "## 1. Типы", None),
    ("Реестр native типов", "TRANSACTION_TYPE_REGISTRY.md", "## Правила профиля", None),
    ("Точные данные и финальность текущих сетей", "CURRENT_SEMANTICS.md", "## Block, calls", None),
    ("Native capture и lifecycle recovery", "NATIVE_CAPTURE_PROTOCOL.md", None, None),
    ("Durable delivery и смена назначения", "DELIVERY_STATE_MACHINE.md", None, None),
    ("Идентификаторы и служебный wire protocol", "CONTROL_WIRE_CONTRACT.md", None, None),
    ("Live YAML и команды", "LIVE_CONFIG_SPEC.md", None, None),
    ("Состав и размещение нод", "NODE_DEPLOYMENT_SPEC.md", "## Состав и ответственность", "## Задачи пункта 2"),
    ("Bootstrap и окружение", "DEPLOYMENT_BOOTSTRAP_CONTRACT.md", None, None),
    ("Порты и экспонирование", "NETWORK_PORTS.md", None, None),
    ("Стек и границы сборок", "COMPONENT_STACK.md", "## Компоненты и интеграция", None),
    ("План доказательств и границы модулей", "IMPLEMENTATION_GATES.md", None, None),
    ("Требования и приёмка", "ACCEPTANCE_REQUIREMENTS.md", None, None),
    ("Атомарные сценарии приёмки", "ACCEPTANCE_CASES.md", None, None),
    ("Декомпозиция требований по результатам", "TASK_DECOMPOSITION.md", None, None),
]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def tracked_paths(root=ROOT):
    output = subprocess.run(
        ["git", "ls-files", "-z"], cwd=root, check=True, capture_output=True
    ).stdout
    return [root / name.decode("utf-8") for name in output.split(b"\0") if name]


def slug(text):
    return re.sub(r"[^\w\- ]", "", text.lower()).replace(" ", "-")


def prose_lines(text):
    fence = None
    for line in text.splitlines():
        match = re.match(r"^\s*(`{3,}|~{3,})", line)
        if match:
            token = match.group(1)
            if fence is None:
                fence = token
            elif token[0] == fence[0] and len(token) >= len(fence):
                fence = None
            yield ""
        else:
            yield "" if fence else line


def anchors(text):
    seen = Counter()
    result = set()
    for line in prose_lines(text):
        if re.match(r"^#{1,6} ", line):
            name = slug(re.sub(r"^#+ ", "", line).strip())
            count = seen[name]
            seen[name] += 1
            result.add(name + (f"-{count}" if count else ""))
    explicit = re.findall(r'<a\s+(?:id|name)="([^"]+)"', text)
    require(len(explicit) == len(set(explicit)), "duplicate explicit Markdown anchors")
    return result | set(explicit)


def check_markdown(paths, root=ROOT):
    tracked = {path.resolve() for path in paths}
    documents = {path: path.read_text() for path in paths if path.suffix == ".md"}
    anchor_cache = {path.resolve(): anchors(text) for path, text in documents.items()}
    links = 0
    for path, text in documents.items():
        expected_columns = None
        for number, line in enumerate(prose_lines(text), 1):
            if line.startswith("|"):
                columns = len(re.findall(r"(?<!\\)\|", line)) - 1
                if expected_columns is None:
                    expected_columns = columns
                require(columns == expected_columns, f"{path.relative_to(root)}:{number}: table column mismatch")
            else:
                expected_columns = None
            for target in re.findall(r"\]\(([^)]+)\)", line):
                target = unquote(target.strip("<>").split(' "')[0])
                parsed = urlsplit(target)
                if parsed.scheme or parsed.netloc:
                    continue
                destination = (path.parent / parsed.path).resolve() if parsed.path else path.resolve()
                require(destination in tracked, f"{path.relative_to(root)}:{number}: untracked or missing link {target}")
                if parsed.fragment and destination.suffix == ".md":
                    require(parsed.fragment in anchor_cache[destination], f"{path.relative_to(root)}:{number}: missing anchor {target}")
                links += 1
    return {"documents": len(documents), "local_links": links}


def check_dag(edges):
    require(all(dep in edges for deps in edges.values() for dep in deps), "unknown task dependency")
    visited, active = set(), set()

    def visit(task):
        require(task not in active, f"task dependency cycle at {task}")
        if task in visited:
            return
        active.add(task)
        for dependency in edges[task]:
            visit(dependency)
        active.remove(task)
        visited.add(task)

    for task in edges:
        visit(task)


def table_rows(text, prefix):
    return [[cell.strip() for cell in re.split(r"(?<!\\)\|", line)[1:-1]]
            for line in text.splitlines() if line.startswith(prefix)]


def check_requirements(root=ROOT):
    docs = root / ".project/docs"
    requirement_ids = re.findall(r"^\| (REQ-[A-Z]+-\d+) \|", (docs / "ACCEPTANCE_REQUIREMENTS.md").read_text(), re.M)
    require(len(requirement_ids) == len(set(requirement_ids)) == 63, "requirement IDs or count changed")
    cases = {}
    for row in table_rows((docs / "ACCEPTANCE_CASES.md").read_text(), "| AC-"):
        require(len(row) == 7 and row[0] not in cases, "invalid or duplicate acceptance case")
        cases[row[0]] = row
    require(len(cases) == 217 and {row[1] for row in cases.values()} == set(requirement_ids), "acceptance coverage changed")
    decomposition = re.sub(r"<a[^>]*></a>", "", (docs / "TASK_DECOMPOSITION.md").read_text())
    tasks = {}
    for row in table_rows(decomposition.split("## Отложенные и снятые parents", 1)[0], "| E0"):
        if not re.fullmatch(TASK_PATTERN, row[0]):
            continue
        require(len(row) == 5 and row[0] not in tasks, "invalid or duplicate product task")
        tasks[row[0]] = row
    require(len(tasks) == 68, "product task count changed")
    edges = {key: set(re.findall(TASK_PATTERN, row[3])) for key, row in tasks.items()}
    for stage in range(1, 8):
        gate = f"G.E0{stage}"
        edges[gate] = {key for key in tasks if key.startswith(f"E0{stage}-")}
        if stage > 1:
            for key in edges[gate]:
                edges[key].add(f"G.E0{stage - 1}")
    check_dag(edges)
    mapping = decomposition.split("## REQ/AC → ответственный task", 1)[1].split("## Шаблон карточки", 1)[0]
    assigned = set()
    for row in table_rows(mapping, "| REQ-"):
        require(len(row) == 4, "invalid requirement-to-task mapping row")
        requirement, expression, task_text, stage = row
        task_match = re.search(TASK_PATTERN, task_text)
        require(task_match is not None, "missing assigned product task")
        task = task_match.group()
        require(task in tasks and task.startswith(stage + "-"), "wrong assigned task or stage")
        for piece in expression.replace("`", "").split(","):
            bounds = piece.strip().split("…")
            ids = bounds
            if len(bounds) == 2:
                first_prefix, first = bounds[0].rsplit("-", 1)
                last_prefix, last = bounds[1].rsplit("-", 1)
                require(first_prefix == last_prefix and int(first) <= int(last), "invalid acceptance range")
                ids = [f"{first_prefix}-{number:02d}" for number in range(int(first), int(last) + 1)]
            for case in ids:
                require(case in cases and case not in assigned, f"missing or duplicate mapped case {case}")
                require(cases[case][1] == requirement and cases[case][6] == stage, f"wrong case mapping {case}")
                assigned.add(case)
    require(assigned == set(cases), "acceptance case has no assigned task")
    for filename in ["TZ_EXECUTOR.md", ".project/docs/DEVELOPMENT_PLAN.md", ".project/docs/IMPLEMENTATION_GATES.md"]:
        references = set(re.findall(TASK_PATTERN, (root / filename).read_text()))
        require(references <= set(tasks), f"unknown child task in {filename}")
    return {"requirements": len(requirement_ids), "cases": len(cases), "product_tasks": len(tasks)}


def check_catalog(root=ROOT):
    catalog = (root / ".project/docs/ISSUE_CATALOG.md").read_text()
    edges = {}
    for block in re.split(r"(?=^## )", catalog, flags=re.M):
        marker = re.search(r"<!-- extractor-task:([^ ]+) -->", block)
        if not marker:
            continue
        task = marker.group(1)
        dependency = re.search(r"^Dependencies: (.+)\.$", block, re.M)
        require(task not in edges and dependency is not None, "invalid catalog task")
        edges[task] = set() if dependency.group(1) == "none" else set(dependency.group(1).split(", "))
    require(len(edges) == 84, "initial issue catalog count changed")
    check_dag(edges)
    return {"issues": len(edges)}


def check_mirrors(root=ROOT):
    project = root / ".project"
    canonical = (project / "evm-transaction-extractor-tz-v3-codex.md").read_text()
    names = {"docs/" + name: title for title, name, _, _ in MIRRORS}
    names["docs/DEVELOPMENT_PLAN.md"] = "Задачи разработки E01–E07"

    def transform(text, name):
        def link(match):
            target = match.group(2)
            if target.startswith(("http:", "https:", "#", "/", "mailto:")):
                return match.group(0)
            path, separator, fragment = target.partition("#")
            target = posixpath.normpath(posixpath.join(posixpath.dirname(name), path))
            target += separator + fragment if separator else ""
            return f"[{match.group(1)}](" + ("#" + slug(names[target]) if target in names else target) + ")"
        text = re.sub(LINK_PATTERN, link, text)
        return "\n".join("##" + line if prose and re.match(r"^#{1,6} ", line) else line
                         for line, prose in zip(text.splitlines(), prose_lines(text))).strip()

    for title, name, start, end in MIRRORS:
        text = (project / "docs" / name).read_text()
        if start:
            index = text.index(start)
            text = text[index:text.index(end, index) if end else len(text)]
        else:
            text = text.split("\n", 1)[1]
        text = "\n".join(line for line in text.splitlines() if not line.startswith("Тематическое представление "))
        lines = canonical.splitlines()
        begin = lines.index("### " + title) + 1
        length = next((index for index, line in enumerate(prose_lines("\n".join(lines[begin:])))
                       if re.match(r"^#{1,3} ", line)), len(lines) - begin)
        actual = "\n".join(lines[begin:begin + length]).strip()
        require(transform(text, "docs/" + name) == actual, f"normative mirror differs: {name}")
    body = canonical[canonical.index("## 1. Назначение и границы"):].strip()
    def executor_link(match):
        target = match.group(2)
        return match.group(0) if target.startswith(("http:", "https:", "#", "/", "mailto:")) else f"[{match.group(1)}](.project/{target})"
    executor = (root / "TZ_EXECUTOR.md").read_text().split("<!-- BEGIN AGREED CONTRACT -->")[1].split("<!-- END AGREED CONTRACT -->")[0].strip()
    require(re.sub(LINK_PATTERN, executor_link, body) == executor, "executor specification differs")
    original = (project / "evm-transaction-extractor-tz-v2.md").read_bytes()
    require(hashlib.sha256(original).hexdigest() == ORIGINAL_SHA256, "immutable v2 specification changed")
    return {"normative_mirrors": len(MIRRORS), "original_v2": "unchanged"}


def validate(root=ROOT):
    result, errors = {}, []
    paths = tracked_paths(root)
    required = {root / ".project/docs" / name for _, name, _, _ in MIRRORS}
    required.update(root / name for name in [
        ".project/evm-transaction-extractor-tz-v2.md", ".project/evm-transaction-extractor-tz-v3-codex.md",
        ".project/docs/DEVELOPMENT_PLAN.md", ".project/docs/ISSUE_CATALOG.md", "TZ_EXECUTOR.md",
    ])
    missing = sorted(str(path.relative_to(root)) for path in required - set(paths))
    if missing:
        return {"status": "FAIL", "checks": {}, "errors": ["required documents are not tracked: " + ", ".join(missing)]}
    for name, check in [("markdown", lambda: check_markdown(paths, root)),
                        ("requirements", lambda: check_requirements(root)),
                        ("catalog", lambda: check_catalog(root)),
                        ("mirrors", lambda: check_mirrors(root))]:
        try:
            result[name] = check()
        except (OSError, ValueError, KeyError, IndexError) as error:
            errors.append(f"{name}: {error}")
    return {"status": "FAIL" if errors else "PASS", "checks": result, "errors": errors}


if __name__ == "__main__":
    report = validate()
    print(json.dumps(report, indent=2))
    raise SystemExit(bool(report["errors"]))
