#!/usr/bin/env python3
"""Validate evidence-artifacts-v1 bundles without third-party dependencies.

The checked-in JSON schemas define structure. Semantic checks bind documents to
the supplied registry, accepted rules, exact artifact bytes, and child outcomes.
Successful validation is artifact conformance, not acceptance or runtime proof.
"""

import datetime
from decimal import Decimal, DecimalException
from fractions import Fraction
import hashlib
import json
import math
from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parent
CONTRACT = "evidence-artifacts-v1"
IDENTITY_FIELDS = (
    "ac_id", "network", "profile", "profile_manifest_digest", "fork_variant",
    "fixture_kind", "suite", "variant_id",
)
RUN_AXES = IDENTITY_FIELDS[1:7]
CASE_BINDINGS = ("task_id", "fixture_id", "fixture_version", "applicability")
SCHEMAS = {
    "CaseRegistry": "case-registry.schema.json",
    "CaseResult": "case-result.schema.json",
    "RunManifest": "run-manifest.schema.json",
    "CompositeManifest": "composite-manifest.schema.json",
    "PrerequisitePlan": "prerequisite-plan.schema.json",
    "ReaderBoundaryManifest": "reader-boundary-manifest.schema.json",
}
SCHEMA_FILES = set(SCHEMAS.values()) | {"evidence-definitions.schema.json"}
READER_CONDITIONS = (
    "source_closure", "delivery_closure", "terminal_fence", "stream_coverage",
    "reconstruction", "semantic_comparison",
)
TIME_PATTERN = re.compile(
    r"[0-9]{4}-[0-9]{2}-[0-9]{2}T(?P<hour>[0-9]{2}):(?P<minute>[0-9]{2}):(?P<second>[0-9]{2})"
    r"(?:\.(?P<fraction>[0-9]+))?(?:Z|[+-](?P<offset_hour>[0-9]{2}):(?P<offset_minute>[0-9]{2}))\Z"
)


class InvalidEvidence(ValueError):
    """The bundle does not satisfy the evidence contract."""


def require(condition, message):
    if not condition:
        raise InvalidEvidence(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "JSON contains a duplicate object key")
        result[key] = value
    return result


def reject_constant(_value):
    raise InvalidEvidence("JSON contains a nonfinite number")


def canonical_float(value):
    """D91 uses Python JSON float spelling; unsupported decimal precision fails closed."""
    approximate = float(value)
    require(math.isfinite(approximate) and Decimal(str(approximate)) == value,
            "JSON number exceeds the lossless canonical precision boundary")
    return approximate


def parse_decimal(text):
    try:
        value = Decimal(text)
        canonical_float(value)
        return value
    except (DecimalException, OverflowError) as error:
        raise InvalidEvidence("JSON number exceeds the supported numeric range") from error


def parse_json(text):
    return json.loads(text, object_pairs_hook=unique_object, parse_constant=reject_constant, parse_float=parse_decimal)


def canonical_value(value):
    if type(value) is Decimal:
        return canonical_float(value)
    if type(value) is dict:
        return {key: canonical_value(item) for key, item in value.items()}
    if type(value) is list:
        return [canonical_value(item) for item in value]
    return value


def canonical(value):
    """Use D91's exact manifest and registry digest encoding."""
    return (json.dumps(canonical_value(value), sort_keys=True, separators=(",", ":"),
                       ensure_ascii=True, allow_nan=False) + "\n").encode("utf-8")


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def identity(value):
    return tuple(value[field] for field in IDENTITY_FIELDS)


def timestamp(value):
    match = TIME_PATTERN.fullmatch(value) if isinstance(value, str) else None
    require(match is not None,
            "timestamp must use the documented RFC 3339 profile")
    require(int(match["hour"]) < 24 and int(match["minute"]) < 60 and int(match["second"]) < 60,
            "timestamp has an invalid hour, minute, or second")
    require(match["offset_hour"] is None or (int(match["offset_hour"]) < 24 and int(match["offset_minute"]) < 60),
            "timestamp has an invalid offset hour or minute")
    try:
        parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
        epoch = datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
        whole = parsed.replace(microsecond=0) - epoch
        fraction = match["fraction"]
        # Preserve sub-microsecond ordering instead of truncating it in datetime.
        remainder = Fraction(int(fraction), 10 ** len(fraction)) if fraction else Fraction(0)
        return Fraction(whole.days * 86400 + whole.seconds) + remainder
    except ValueError as error:
        raise InvalidEvidence("timestamp has an invalid date, time, or offset") from error


def finite_number(value):
    return type(value) is int or (type(value) is float and math.isfinite(value)) or (type(value) is Decimal and value.is_finite())


def exact_number(value):
    """Use decimal JSON meaning, including for Python floats passed through the API."""
    if type(value) is float:
        return Fraction(str(value))
    return Fraction(value)


def json_equal(left, right):
    """JSON booleans are not numbers, even though Python bool subclasses int."""
    if isinstance(left, bool) != isinstance(right, bool):
        return False
    if finite_number(left) and finite_number(right):
        return exact_number(left) == exact_number(right)
    if type(left) is dict and type(right) is dict:
        return left.keys() == right.keys() and all(json_equal(left[k], right[k]) for k in left)
    if type(left) is list and type(right) is list:
        return len(left) == len(right) and all(json_equal(a, b) for a, b in zip(left, right))
    return left == right


def uniqueness_key(value):
    """Hash JSON values with integral numbers normalized to their exact integer value."""
    if finite_number(value) and exact_number(value).denominator == 1:
        return exact_number(value).numerator
    if type(value) is dict:
        return {key: uniqueness_key(item) for key, item in value.items()}
    if type(value) is list:
        return [uniqueness_key(item) for item in value]
    return value


class SchemaValidator:
    """Evaluate only the JSON Schema vocabulary used by the local artifacts.

    This is not a general JSON Schema library. Unknown keywords fail closed.
    References never fetch URLs or open bundle-controlled filesystem paths.
    """

    KEYWORDS = {
        "$schema", "$id", "$defs", "$ref", "title", "description", "type",
        "properties", "required", "additionalProperties", "items", "minItems",
        "maxItems", "uniqueItems", "minProperties", "maxProperties", "minLength",
        "maxLength", "pattern", "format", "const", "enum", "minimum", "maximum",
        "exclusiveMinimum", "exclusiveMaximum", "allOf", "anyOf", "oneOf", "not",
        "if", "then", "else",
    }

    def __init__(self):
        self.documents = {name: parse_json((ROOT / name).read_text(encoding="utf-8"))
                          for name in SCHEMA_FILES}
        for document in self.documents.values():
            self.check_vocabulary(document)

    def check_vocabulary(self, schema):
        if type(schema) is bool:
            return
        require(type(schema) is dict and set(schema) <= self.KEYWORDS,
                "schema uses an unsupported keyword")
        for key in ("properties", "$defs"):
            for child in schema.get(key, {}).values():
                self.check_vocabulary(child)
        for key in ("additionalProperties", "items", "not", "if", "then", "else"):
            if key in schema:
                self.check_vocabulary(schema[key])
        for key in ("allOf", "anyOf", "oneOf"):
            for child in schema.get(key, []):
                self.check_vocabulary(child)

    def validate(self, value, filename):
        self.check(value, self.documents[filename], filename, "document")

    def matches(self, value, schema, filename, location):
        try:
            self.check(value, schema, filename, location)
            return True
        except InvalidEvidence:
            return False

    def check(self, value, schema, filename, location):
        if type(schema) is bool:
            require(schema, location + ": value is forbidden")
            return
        if "$ref" in schema:
            target, _, fragment = schema["$ref"].partition("#")
            target = target or filename
            require(target in self.documents, "schema reference is not local")
            referenced = self.documents[target]
            if fragment:
                require(fragment.startswith("/"), "schema reference has an invalid fragment")
                for part in fragment[1:].split("/"):
                    referenced = referenced[part.replace("~1", "/").replace("~0", "~")]
            self.check(value, referenced, target, location)
        predicates = {
            "object": lambda v: type(v) is dict,
            "array": lambda v: type(v) is list,
            "string": lambda v: type(v) is str,
            "integer": lambda v: finite_number(v) and exact_number(v).denominator == 1,
            "number": finite_number,
            "boolean": lambda v: type(v) is bool,
            "null": lambda v: v is None,
        }
        if "type" in schema:
            kinds = schema["type"] if type(schema["type"]) is list else [schema["type"]]
            require(any(predicates[kind](value) for kind in kinds), location + ": wrong JSON type")
        if "const" in schema:
            require(json_equal(value, schema["const"]), location + ": wrong constant")
        if "enum" in schema:
            require(any(json_equal(value, item) for item in schema["enum"]), location + ": unknown value")
        for child in schema.get("allOf", []):
            self.check(value, child, filename, location)
        for keyword in ("anyOf", "oneOf"):
            if keyword in schema:
                count = sum(self.matches(value, child, filename, location) for child in schema[keyword])
                require(count >= 1 if keyword == "anyOf" else count == 1,
                        location + ": schema alternatives do not match")
        if "not" in schema:
            require(not self.matches(value, schema["not"], filename, location), location + ": forbidden field combination")
        if "if" in schema:
            branch = "then" if self.matches(value, schema["if"], filename, location) else "else"
            if branch in schema:
                self.check(value, schema[branch], filename, location)
        if type(value) is dict:
            require(set(schema.get("required", [])) <= value.keys(), location + ": missing required field")
            properties = schema.get("properties", {})
            for key, child in value.items():
                self.check(child, properties.get(key, schema.get("additionalProperties", True)),
                           filename, location + "." + (key if key in properties else "<additional property>"))
            require(len(value) >= schema.get("minProperties", 0), location + ": too few properties")
            require(len(value) <= schema.get("maxProperties", len(value)), location + ": too many properties")
        elif type(value) is list:
            require(len(value) >= schema.get("minItems", 0), location + ": too few items")
            require(len(value) <= schema.get("maxItems", len(value)), location + ": too many items")
            if schema.get("uniqueItems"):
                # Evidence arrays contain objects or strings. Canonical buckets
                # avoid a quadratic scan for large registries and artifact lists.
                keys = [canonical(uniqueness_key(item)) for item in value]
                require(len(set(keys)) == len(keys), location + ": duplicate item")
            for number, child in enumerate(value):
                self.check(child, schema.get("items", True), filename, location + f"[{number}]")
        elif type(value) is str:
            require(len(value) >= schema.get("minLength", 0), location + ": string is too short")
            require(len(value) <= schema.get("maxLength", len(value)), location + ": string is too long")
            if "pattern" in schema:
                require(re.search(schema["pattern"], value) is not None, location + ": string has an invalid format")
            if "format" in schema:
                require(schema["format"] == "date-time", "schema uses an unsupported format")
                timestamp(value)
        elif type(value) in (int, float, Decimal):
            require(finite_number(value), location + ": nonfinite number")
            for keyword, compare in (
                ("minimum", lambda a, b: a >= b), ("maximum", lambda a, b: a <= b),
                ("exclusiveMinimum", lambda a, b: a > b), ("exclusiveMaximum", lambda a, b: a < b),
            ):
                if keyword in schema:
                    require(compare(exact_number(value), exact_number(schema[keyword])), location + ": number is outside its bounds")


class EvidenceValidator:
    def __init__(self, context):
        require(type(context) is dict, "context must be an object")
        self.context = context
        self.schemas = SchemaValidator()
        self.profiles = parse_json((ROOT / "profile-registry.json").read_text(encoding="utf-8"))["profiles"]
        if "profiles" in context:
            require(context["profiles"] == self.profiles, "context profiles differ from the accepted registry")
        self.registry = None
        self.cases = None

    def object_map(self, name):
        value = self.context.get(name)
        require(type(value) is dict and all(type(key) is str for key in value),
                "context is missing the " + name + " map")
        return value

    def check_case(self, case):
        tasks = self.context.get("task_ids")
        require(type(tasks) is list and tasks and all(type(task) is str for task in tasks)
                and len(set(tasks)) == len(tasks), "context must declare distinct allowed task IDs")
        require(case["task_id"] in tasks, "case task is not allowed")
        owners = self.object_map("case_owners")
        require(owners.get(case["ac_id"]) == case["task_id"], "case task does not match its assigned owner")
        if case["applicability"] != "APPLICABLE":
            rules = self.object_map("rules")
            rule = rules.get(case["rule_ref"])
            require(type(rule) is dict and rule.get("accepted") is True,
                    "case exception needs an accepted applicability rule")
            require(rule.get("applicability") == case["applicability"], "rule applicability differs from the case")
            if case["applicability"] == "NOT_ENABLED":
                require(rule.get("optional_extension") is True, "NOT_ENABLED requires an optional extension")
            # Accepted rules may be broad or explicitly scoped. A supplied
            # selector is binding, not an advisory description.
            for key in IDENTITY_FIELDS + ("task_id", "fixture_id", "fixture_version"):
                if key in rule:
                    require(rule[key] == case[key], "applicability rule does not cover this case")
            if "case_identities" in rule:
                require(type(rule["case_identities"]) is list
                        and {key: case[key] for key in IDENTITY_FIELDS} in rule["case_identities"],
                        "applicability rule does not include the case identity")

    def validate_registry(self, document):
        self.schemas.validate(document, SCHEMAS["CaseRegistry"])
        cases = {}
        for case in document["cases"]:
            key = identity(case)
            require(key not in cases, "registry contains a duplicate case identity")
            self.check_case(case)
            cases[key] = case
        return cases

    def load_registry(self):
        if self.registry is None:
            self.registry = self.context.get("registry")
            self.cases = self.validate_registry(self.registry)
        return self.registry

    def registry_case(self, case_identity):
        self.load_registry()
        key = identity(case_identity)
        require(key in self.cases, "case identity is absent from the current registry")
        return self.cases[key]

    def validate_result(self, document):
        self.schemas.validate(document, SCHEMAS["CaseResult"])
        case = self.registry_case(document)
        for field in CASE_BINDINGS:
            require(document[field] == case[field], "result differs from its registry binding")
        require(document.get("rule_ref") == case.get("rule_ref"), "result changed its registry rule")
        self.check_case(document)
        if document.get("verdict") in ("PASS", "FAIL"):
            manifest = self.resolve_manifest(document["run_manifest_ref"], "manifests")
            outcomes = self.validate_manifest(manifest)
            require(identity(document) in outcomes, "run omits the result identity")
            require(outcomes[identity(document)] == document["verdict"], "result and run outcomes differ")

    def resolve_manifest(self, reference, map_name):
        manifests = self.object_map(map_name)
        require(reference["path"] in manifests, "referenced manifest is unavailable")
        manifest = manifests[reference["path"]]
        require(type(manifest) is dict, "referenced manifest must be an object")
        require(digest(manifest) == reference["sha256"], "referenced manifest digest differs")
        require(manifest.get("run_id") == reference["run_id"], "referenced run ID differs")
        return manifest

    def check_artifacts(self, document):
        for name in ("input_artifacts", "expected_artifacts", "actual_artifacts", "logs"):
            for reference in document[name]:
                self.check_artifact_reference(reference)

    def check_artifact_reference(self, reference):
        if reference is None:
            return
        content = self.object_map("artifacts").get(reference["path"])
        require(type(content) is str, "referenced artifact text is unavailable")
        actual = hashlib.sha256(content.encode("utf-8")).hexdigest()
        require(actual == reference["sha256"], "artifact digest differs from its exact UTF-8 bytes")

    def check_phases(self, document, verdict):
        started, finished = timestamp(document["started_at"]), timestamp(document["finished_at"])
        require(started <= finished, "run timestamps are reversed")
        phases = {}
        for name, phase in document["phases"].items():
            begin, end = timestamp(phase["started_at"]), timestamp(phase["finished_at"])
            require(started <= begin <= end <= finished, "phase falls outside the run or has reversed timestamps")
            elapsed = end - begin
            if "duration_seconds" in phase:
                require(elapsed == exact_number(phase["duration_seconds"]),
                        "phase duration differs from its timestamps")
            require(elapsed <= exact_number(phase["deadline_seconds"]) or verdict == "FAIL",
                    "a run that exceeded a phase deadline cannot report PASS")
            phases[name] = (begin, end)
        require(phases["preparation"][1] <= phases["execution"][0]
                and phases["execution"][1] <= phases["teardown"][0], "required phases overlap or are out of order")
        if document.get("run_mode", "standard") == "standard":
            require(exact_number(document["phases"]["execution"]["deadline_seconds"]) <= 1800,
                    "standard execution deadline exceeds 1800 seconds")
        return phases

    def validate_manifest(self, document):
        self.schemas.validate(document, SCHEMAS["RunManifest"])
        registry = self.load_registry()
        require(document["case_registry_version"] == registry["version"]
                and document["case_registry_digest"] == digest(registry), "run uses a stale or different registry")
        declared = set()
        for case_identity in document["case_identities"]:
            key = identity(case_identity)
            require(key not in declared, "run repeats a case identity")
            declared.add(key)
            case = self.registry_case(case_identity)
            require(case["applicability"] == "APPLICABLE", "run cannot execute an excluded registry case")
            for field in RUN_AXES + ("task_id", "fixture_id", "fixture_version"):
                require(document[field] == case[field], "run mixes identities or changes a fixture binding")
            requirement = "REQ-" + "-".join(case["ac_id"].split("-")[1:3])
            require(requirement in document["requirement_ids"], "run omits a case requirement ID")
        outcomes = {}
        for outcome in document["case_outcomes"]:
            key = identity(outcome["identity"])
            require(key not in outcomes, "run repeats a case outcome")
            outcomes[key] = outcome["verdict"]
        require(set(outcomes) == declared, "run outcomes must exactly cover its declared identities")
        require(document["parent_integration_run"] != document["run_id"], "run cannot be its own parent")
        if not document["image_digests"]:
            require("build_applicability" in document, "missing images need an explicit build exclusion")
        self.check_artifacts(document)
        verdict = aggregate(outcomes.values())
        self.check_phases(document, verdict)
        if document.get("attempt_index") == 2:
            require(document["first_run_id"] != document["run_id"], "retry must use a new run ID")
        return outcomes

    def validate_composite(self, document):
        self.schemas.validate(document, SCHEMAS["CompositeManifest"])
        profiles, paths, runs = set(), set(), set()
        verdicts = [document["integration_outcome"]["verdict"]]
        intervals = []
        for reference in document["children"]:
            require(reference["profile"] not in profiles and reference["path"] not in paths
                    and reference["run_id"] not in runs, "composite repeats a child profile, path, or run")
            profiles.add(reference["profile"])
            paths.add(reference["path"])
            runs.add(reference["run_id"])
            child = self.resolve_manifest(reference, "child_manifests")
            require(child.get("profile") == reference["profile"], "child reference changes its profile")
            require(child.get("parent_integration_run") == document["run_id"], "child does not name this parent")
            if "correlation_id" in child:
                require(child["correlation_id"] == document["correlation_id"], "child correlation ID differs")
            outcomes = self.validate_manifest(child)
            verdict = aggregate(outcomes.values())
            require(verdict == reference["verdict"], "child reference hides its individual outcomes")
            verdicts.append(verdict)
            phase = child["phases"]["execution"]
            intervals.append((timestamp(phase["started_at"]), timestamp(phase["finished_at"])))
        require(profiles == set(document["required_profiles"]), "composite omits or adds a required profile")
        require(document["run_id"] not in runs, "composite cannot contain itself")
        require(document["verdict"] == aggregate(verdicts), "aggregate verdict differs from child or integration results")
        if "phases" in document:
            phases = self.check_phases(document, document["verdict"])
            require(all(phases["execution"][0] <= start <= end <= phases["execution"][1]
                        for start, end in intervals), "parent execution does not contain every child execution")
        if document.get("run_mode", "standard") == "standard":
            elapsed = max(end for _, end in intervals) - min(start for start, _ in intervals)
            require(elapsed <= 1800 or document["verdict"] == "FAIL",
                    "standard parent cannot hide more than 1800 seconds of child execution")

    def validate_prerequisites(self, document):
        self.schemas.validate(document, SCHEMAS["PrerequisitePlan"])
        stages = {row["stage"]: row for row in document["stages"]}
        require(len(stages) == len(document["stages"]), "prerequisite plan repeats a stage")
        obligations = {"E01": "native-seam", "E03": "delivery", "E05": "composed"}
        for stage, row in stages.items():
            for dependency in row["dependencies"]:
                require(dependency in stages, "prerequisite is absent from the plan")
                # Strictly earlier edges reject cycles as well as acyclic forward edges.
                require(dependency < stage, "prerequisite must come from an earlier stage")
            if stage in obligations:
                require(obligations[stage] in row["evidence"], "stage omits its required early evidence")

    def validate_reader_boundary(self, document):
        """Check D87 record consistency without claiming native or stream proof."""
        self.schemas.validate(document, SCHEMAS["ReaderBoundaryManifest"])
        case_keys = set()
        for case_identity in document["case_identities"]:
            case = self.registry_case(case_identity)
            require(case["applicability"] == "APPLICABLE", "reader boundary includes an excluded case")
            require(all(case[field] == document[field] for field in RUN_AXES),
                    "reader boundary mixes profile, fork, fixture, or suite axes")
            key = identity(case_identity)
            require(key not in case_keys, "reader boundary repeats a case identity")
            case_keys.add(key)

        statuses = {name: document["conditions"][name]["status"] for name in READER_CONDITIONS}
        reader_complete = all(statuses[name] == "SATISFIED" for name in READER_CONDITIONS[:5])
        require(document["reader_complete"] is reader_complete, "reader completion flag differs from closure conditions")
        comparison = {"SATISFIED": True, "FAILED": False, "NOT_RUN": None, "INCOMPLETE": None}
        require(document["comparison_passed"] is comparison[statuses["semantic_comparison"]],
                "comparison flag differs from the semantic comparison condition")

        references = [document["run_plan_ref"], document["oracle_ref"], document["expectation"]["predicate_ref"]]
        sources = document["source_boundaries"]
        deliveries = document["delivery_boundaries"]
        streams = document["streams"]
        source_fields = ("baseline_ref", "admission_set_ref", "terminal_frontier_ref", "transitions_ref")
        delivery_fields = ("highwater_ref", "seal_ref", "puback_ref", "attempt_census_ref", "attempt_classifications_ref")
        stream_fields = ("config_ref", "seal_ref", "coverage_ref")
        source_ids, delivery_ids, stream_ids = set(), set(), set()
        for source in sources:
            require(source["source_id"] not in source_ids, "reader boundary repeats a source ID")
            source_ids.add(source["source_id"])
            references.extend(source[field] for field in source_fields)
        for delivery in deliveries:
            key = tuple(delivery[field] for field in ("destination_id", "binding_digest", "episode_id")) + (exact_number(delivery["generation"]),)
            require(key not in delivery_ids, "reader boundary repeats a delivery episode")
            delivery_ids.add(key)
            references.extend(delivery["manifest_refs"])
            references.extend(delivery[field] for field in delivery_fields)
        for stream in streams:
            key = (stream["stream_id"], stream["destination_id"], exact_number(stream["generation"]))
            require(key not in stream_ids, "reader boundary repeats a stream identity")
            stream_ids.add(key)
            require(stream["upper_sequence"] is None or exact_number(stream["lower_sequence"]) <= exact_number(stream["upper_sequence"]),
                    "stream upper sequence precedes its lower sequence")
            references.extend(stream[field] for field in stream_fields)
            references.extend(stream["raw_message_refs"])
            references.extend(stream["terminal_fence"].values())
        references.extend(document["logical_comparison"].values())
        for condition in document["conditions"].values():
            references.extend(condition["evidence_refs"])
        for reference in references:
            self.check_artifact_reference(reference)

        if statuses["source_closure"] == "SATISFIED":
            require(sources and all(all(source[field] is not None for field in source_fields) for source in sources),
                    "source closure lacks baseline, obligations, frontier, or transition evidence")
        if statuses["delivery_closure"] == "SATISFIED":
            require(deliveries and all(delivery["manifest_refs"] and all(delivery[field] is not None for field in delivery_fields)
                                       for delivery in deliveries), "delivery closure lacks required delivery or publication evidence")
        if statuses["terminal_fence"] == "SATISFIED":
            require(streams and all(all(reference is not None for reference in stream["terminal_fence"].values())
                                   for stream in streams), "terminal fence lacks its required evidence references")
        if statuses["stream_coverage"] == "SATISFIED":
            require(streams and all(stream["upper_sequence"] is not None
                                   and all(stream[field] is not None for field in stream_fields)
                                   and (exact_number(stream["upper_sequence"]) == exact_number(stream["lower_sequence"]) or stream["raw_message_refs"])
                                   for stream in streams), "stream coverage lacks bounds, configuration, seal, coverage, or raw bytes")
        if statuses["semantic_comparison"] == "SATISFIED":
            require(all(reference is not None for reference in document["logical_comparison"].values()),
                    "semantic comparison lacks expected, actual, or diff evidence")

        if not document["run_started"]:
            # The schema rejects fabricated actual fields in this preparation state.
            return
        manifests = self.object_map("manifests")
        require(document["run_manifest_path"] in manifests, "reader boundary run manifest is unavailable")
        manifest = manifests[document["run_manifest_path"]]
        outcomes = self.validate_manifest(manifest)
        require(manifest["run_id"] == document["run_id"], "reader boundary run ID differs")
        require(all(manifest[field] == document[field] for field in RUN_AXES), "reader boundary run axes differ")
        require(set(outcomes) == case_keys, "reader boundary does not cover the exact run case set")
        matches = [item for item in manifest["actual_artifacts"] if item["path"] == document["artifact_path"]]
        require(len(matches) == 1 and matches[0]["sha256"] == digest(document),
                "run must reference the canonical boundary artifact exactly once")
        require(self.object_map("artifacts").get(document["artifact_path"]) == canonical(document).decode("ascii"),
                "reader boundary artifact bytes are not the exact canonical JSON")

        execution = manifest["phases"]["execution"]
        run_start, run_end = timestamp(execution["started_at"]), timestamp(execution["finished_at"])
        positive_pass = document["expectation"]["mode"] == "positive_delivery" and aggregate(outcomes.values()) == "PASS"
        if positive_pass:
            require(all(status == "SATISFIED" for status in statuses.values()),
                    "positive delivery PASS requires all six reader conditions")
        for phase in document["phase_times"].values():
            start = timestamp(phase["started_at"]) if phase["started_at"] is not None else None
            finish = timestamp(phase["finished_at"]) if phase["finished_at"] is not None else None
            require(finish is None or start is not None, "reader phase finish requires a start")
            require(start is None or run_start <= start <= run_end, "reader phase start is outside execution")
            require(finish is None or start <= finish <= run_end, "reader phase finish is reversed or outside execution")
            if positive_pass:
                require(start is not None and finish is not None, "positive reader completion requires every phase timestamp")
                require(finish - start <= exact_number(phase["deadline_seconds"]),
                        "positive reader completion exceeded a phase deadline")

    def validate(self, kind, document):
        handlers = {
            "CaseRegistry": self.validate_registry,
            "CaseResult": self.validate_result,
            "RunManifest": self.validate_manifest,
            "CompositeManifest": self.validate_composite,
            "PrerequisitePlan": self.validate_prerequisites,
            "ReaderBoundaryManifest": self.validate_reader_boundary,
        }
        require(kind in handlers, "unknown evidence document kind")
        handlers[kind](document)


def aggregate(verdicts):
    return "FAIL" if "FAIL" in verdicts else "PASS"


def validate_bundle(bundle):
    require(type(bundle) is dict and bundle.get("contract") == CONTRACT, "unknown evidence bundle contract")
    kind = bundle.get("kind")
    require(type(kind) is str, "bundle kind must be a string")
    if kind == "ExpandProfiles":
        require(set(bundle) <= {"contract", "kind", "scope", "synthetic"}, "unknown expansion bundle field")
        scope = bundle.get("scope")
        require(scope in ("ALL", "ETH", "BSC", "POLY"), "unknown profile expansion scope")
        profiles = parse_json((ROOT / "profile-registry.json").read_text(encoding="utf-8"))["profiles"]
        return sorted(profile for profile, family in profiles.items() if scope in ("ALL", family))
    require(set(bundle) <= {"contract", "kind", "document", "context", "synthetic"}, "unknown validation bundle field")
    require("document" in bundle and "context" in bundle, "bundle must supply document and context")
    EvidenceValidator(bundle["context"]).validate(kind, bundle["document"])
    return None


def main():
    if sys.argv[1:] == ["--help"]:
        print("Read one evidence-artifacts-v1 JSON bundle from stdin. Exit 0: valid; exit 2: invalid.")
        print("Kinds: CaseRegistry, CaseResult, RunManifest, CompositeManifest, PrerequisitePlan, ReaderBoundaryManifest, ExpandProfiles.")
        print("ExpandProfiles prints a sorted JSON array. See test/evidence/README.md for the context contract.")
        return 0
    try:
        require(len(sys.argv) == 1, "unexpected command arguments")
        response = validate_bundle(parse_json(sys.stdin.buffer.read().decode("utf-8")))
        if response is not None:
            print(json.dumps(response))
        return 0
    except (InvalidEvidence, ValueError, TypeError, KeyError, OSError, RecursionError, OverflowError) as error:
        # Never echo payloads, artifact bytes, commands, or tracebacks to the log.
        message = str(error) if isinstance(error, InvalidEvidence) else "invalid JSON, evidence structure, or local artifact"
        print("Invalid evidence: " + message[:400].replace("\n", " ").replace("\r", " "), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
