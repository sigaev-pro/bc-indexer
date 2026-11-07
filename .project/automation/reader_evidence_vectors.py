"""Independent synthetic D87 artifact inputs; none establish runtime closure."""

import copy
import hashlib
import json


IDENTITY = ("ac_id", "network", "profile", "profile_manifest_digest", "fork_variant",
            "fixture_kind", "suite", "variant_id")
AXES = IDENTITY[1:7]
CONDITIONS = ("source_closure", "delivery_closure", "terminal_fence", "stream_coverage",
              "reconstruction", "semantic_comparison")
PHASES = ("startup", "observation", "shutdown_drain", "stream_closure", "reconstruction", "comparison")
REQUIRED = {"schema_version", "boundary_id", "artifact_path", *AXES, "case_identities", "run_started",
            "run_id", "run_manifest_path", "run_plan_ref", "oracle_ref", "expectation", "source_boundaries",
            "delivery_boundaries", "streams", "logical_comparison", "phase_times", "conditions",
            "reader_complete", "comparison_passed"}


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n").encode()


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def artifact(context, path, meaning):
    content = canonical({"synthetic_inputs": True, "meaning": meaning}).decode()
    context["artifacts"][path] = content
    return {"path": path, "sha256": hashlib.sha256(content.encode()).hexdigest()}


def bind(document, context):
    """Refresh only the boundary output bytes; do not repair semantic mutations."""
    if document["run_started"] and document["run_manifest_path"] in context["manifests"]:
        content = canonical(document).decode()
        context["artifacts"][document["artifact_path"]] = content
        manifest = context["manifests"][document["run_manifest_path"]]
        manifest["actual_artifacts"] = [{"path": document["artifact_path"],
                                        "sha256": hashlib.sha256(content.encode()).hexdigest()}]


def fixture(seed, profile="ethereum-sepolia"):
    context = copy.deepcopy(seed["request"]["context"])
    case = context["registry"]["cases"][0]
    case.update(network=context["profiles"][profile], profile=profile,
                profile_manifest_digest=hashlib.sha256(profile.encode()).hexdigest(),
                fork_variant="synthetic-reader-fork", fixture_kind="local-e2e",
                fixture_id="synthetic-reader-input", variant_id="synthetic-reader-boundary")
    identity = {key: case[key] for key in IDENTITY}
    manifest = copy.deepcopy(seed["request"]["document"])
    manifest.update({key: case[key] for key in AXES})
    manifest.update(run_id="synthetic-reader-run", fixture_id=case["fixture_id"],
                    case_registry_digest=digest(context["registry"]), case_identities=[identity],
                    case_outcomes=[{"identity": identity, "verdict": "PASS"}],
                    native_commits={"execution": "e" * 40}, image_digests={"execution": "sha256:" + "e" * 64},
                    compiler_versions={"go": "synthetic-compiler"}, architecture="x86_64")
    manifest.pop("build_applicability", None)
    context["manifests"] = {"synthetic-reader-run.json": manifest}

    def ref(name):
        return artifact(context, "reader/" + name + ".json", "D87 synthetic " + name)

    condition = {"status": "SATISFIED", "evidence_refs": [ref("condition-evidence")],
                 "reason": "Synthetic conformance input; no runtime claim.", "missing_obligations": []}
    source = {"source_id": "synthetic-source", **{name: ref(name) for name in
              ("baseline_ref", "admission_set_ref", "terminal_frontier_ref", "transitions_ref")}}
    delivery = {"destination_id": "synthetic-destination", "binding_digest": "c" * 64,
                "episode_id": "synthetic-episode", "generation": 0, "manifest_refs": [ref("delivery-manifest")],
                **{name: ref(name) for name in
                   ("highwater_ref", "seal_ref", "puback_ref", "attempt_census_ref", "attempt_classifications_ref")}}
    stream = {"stream_id": "synthetic-stream", "destination_id": delivery["destination_id"], "generation": 0,
              "config_ref": ref("stream-config"), "lower_sequence": 4, "upper_sequence": 5,
              "seal_ref": ref("stream-seal"), "coverage_ref": ref("coverage"), "raw_message_refs": [ref("raw-messages")],
              "terminal_fence": {name: ref(name) for name in
                                 ("implementation_ref", "conformance_ref", "process_events_ref",
                                  "ingress_restrictions_ref", "recovered_frontiers_ref")}}
    document = {"schema_version": 1, "boundary_id": "synthetic-boundary", "artifact_path": "reader/boundary.json",
                **{key: case[key] for key in AXES}, "case_identities": [identity], "run_started": True,
                "run_id": manifest["run_id"], "run_manifest_path": "synthetic-reader-run.json",
                "run_plan_ref": ref("run-plan"), "oracle_ref": ref("independent-oracle"),
                "expectation": {"mode": "positive_delivery", "predicate_ref": None},
                "source_boundaries": [source], "delivery_boundaries": [delivery], "streams": [stream],
                "logical_comparison": {name: ref(name) for name in ("expected_ref", "actual_ref", "diff_ref")},
                "phase_times": {name: {"started_at": "2000-01-01T00:00:01Z", "finished_at": "2000-01-01T00:00:02Z",
                                       "deadline_seconds": 1} for name in PHASES},
                "conditions": {name: copy.deepcopy(condition) for name in CONDITIONS},
                "reader_complete": True, "comparison_passed": True}
    bind(document, context)
    return document, context


def vectors(seed):
    document, context = fixture(seed)
    rows = []

    def add(name, doc=None, ctx=None, valid=False, rebind=True):
        doc = copy.deepcopy(document if doc is None else doc)
        ctx = copy.deepcopy(context if ctx is None else ctx)
        if rebind:
            bind(doc, ctx)
        rows.append({"id": "reader-" + name, "valid": valid,
                     "request": {"contract": "evidence-artifacts-v1", "synthetic": True,
                                 "kind": "ReaderBoundaryManifest", "document": doc, "context": ctx}})

    for profile in context["profiles"]:
        doc, ctx = fixture(seed, profile)
        add("positive-" + profile, doc, ctx, valid=True)

    doc = copy.deepcopy(document)
    doc["streams"][0].update(upper_sequence=4, raw_message_refs=[])
    add("empty-interval", doc, valid=True)

    failed = copy.deepcopy(context)
    failed["manifests"][document["run_manifest_path"]]["case_outcomes"][0]["verdict"] = "FAIL"
    doc = copy.deepcopy(document)
    doc["conditions"]["delivery_closure"].update(status="INCOMPLETE", evidence_refs=[],
        reason="Synthetic missing producer acknowledgment.", missing_obligations=["producer acknowledgment"])
    doc["reader_complete"] = False
    add("incomplete-delivery-fail", doc, failed, valid=True)
    add("incomplete-delivery-cannot-pass", doc)
    doc["reader_complete"] = True
    add("false-reader-completion", doc, failed)

    doc = copy.deepcopy(document)
    doc["conditions"]["semantic_comparison"].update(status="FAILED", reason="Synthetic logical mismatch.")
    doc["comparison_passed"] = False
    add("completed-reader-semantic-fail", doc, failed, valid=True)
    add("semantic-failure-cannot-pass", doc)
    doc["comparison_passed"] = True
    add("false-semantic-flag", doc, failed)

    doc = copy.deepcopy(document)
    doc.update(run_started=False, run_id=None, run_manifest_path=None, run_plan_ref=None, oracle_ref=None,
               source_boundaries=[], delivery_boundaries=[], streams=[], reader_complete=False, comparison_passed=None,
               logical_comparison={name: None for name in ("expected_ref", "actual_ref", "diff_ref")})
    doc["conditions"] = {name: {"status": "NOT_RUN", "evidence_refs": [], "reason": "Synthetic preparation failure.",
                                "missing_obligations": []} for name in CONDITIONS}
    for phase in doc["phase_times"].values():
        phase.update(started_at=None, finished_at=None)
    not_run_context = copy.deepcopy(context)
    not_run_context["manifests"] = {}
    not_run_context["artifacts"].pop(document["artifact_path"])
    add("not-run", doc, not_run_context, valid=True)
    invented = copy.deepcopy(doc)
    invented["phase_times"]["startup"]["started_at"] = "2000-01-01T00:00:01Z"
    add("not-run-invented-time", invented, not_run_context)
    invented = copy.deepcopy(doc)
    invented["run_id"] = "invented-run"
    add("not-run-invented-run", invented, not_run_context)
    invented = copy.deepcopy(doc)
    invented["source_boundaries"] = copy.deepcopy(document["source_boundaries"])
    add("not-run-invented-source", invented, not_run_context)

    doc, ctx = copy.deepcopy(document), copy.deepcopy(context)
    doc["expectation"] = {"mode": "declared_negative", "predicate_ref": artifact(ctx, "reader/predicate.json", "Predeclared synthetic fence rejection.")}
    doc["conditions"]["terminal_fence"].update(status="FAILED", reason="The declared synthetic fence rejection occurred.")
    doc["reader_complete"] = False
    add("declared-negative", doc, ctx, valid=True)
    doc["expectation"]["predicate_ref"] = None
    add("negative-without-predicate", doc, ctx)

    for name in CONDITIONS:
        doc = copy.deepcopy(document)
        del doc["conditions"][name]
        add("missing-condition-" + name, doc)
        doc = copy.deepcopy(document)
        doc["conditions"][name]["evidence_refs"] = []
        add("satisfied-without-evidence-" + name, doc)
    for field in ("run_plan_ref", "oracle_ref"):
        doc = copy.deepcopy(document)
        doc[field] = None
        add("missing-" + field, doc)
    for field in ("baseline_ref", "admission_set_ref", "terminal_frontier_ref", "transitions_ref"):
        doc = copy.deepcopy(document)
        doc["source_boundaries"][0][field] = None
        add("missing-source-" + field, doc)
    for field in ("highwater_ref", "seal_ref", "puback_ref", "attempt_census_ref", "attempt_classifications_ref"):
        doc = copy.deepcopy(document)
        doc["delivery_boundaries"][0][field] = None
        add("missing-delivery-" + field, doc)
    for field in document["streams"][0]["terminal_fence"]:
        doc = copy.deepcopy(document)
        doc["streams"][0]["terminal_fence"][field] = None
        add("missing-fence-" + field, doc)
    for field in ("config_ref", "seal_ref", "coverage_ref"):
        doc = copy.deepcopy(document)
        doc["streams"][0][field] = None
        add("missing-stream-" + field, doc)
    for field in document["logical_comparison"]:
        doc = copy.deepcopy(document)
        doc["logical_comparison"][field] = None
        add("missing-comparison-" + field, doc)
    for field in ("source_boundaries", "delivery_boundaries", "streams"):
        doc = copy.deepcopy(document)
        doc[field] = []
        add("missing-" + field, doc)
        doc = copy.deepcopy(document)
        doc[field].append(copy.deepcopy(doc[field][0]))
        add("duplicate-" + field, doc)
    for field, value in (("schema_version", 3), ("unexpected", True), ("network", "BSC"),
                         ("run_id", "wrong-run"), ("profile_manifest_digest", "f" * 64)):
        doc = copy.deepcopy(document)
        doc[field] = value
        add("wrong-" + field, doc)
    for field, value in (("upper_sequence", 3), ("raw_message_refs", [])):
        doc = copy.deepcopy(document)
        doc["streams"][0][field] = value
        add("wrong-stream-" + field, doc)
    doc = copy.deepcopy(document)
    doc["delivery_boundaries"][0]["manifest_refs"] = []
    add("missing-delivery-manifests", doc)
    doc = copy.deepcopy(document)
    doc["conditions"]["source_closure"]["status"] = "PASS"
    add("case-verdict-is-not-condition", doc)
    doc = copy.deepcopy(document)
    doc["conditions"]["source_closure"]["missing_obligations"] = ["unresolved obligation"]
    add("satisfied-with-missing-obligations", doc)
    for field, value in (("started_at", "1999-12-31T23:59:59Z"), ("finished_at", "2000-01-01T00:00:00Z"),
                         ("deadline_seconds", 0), ("deadline_seconds", 0.5)):
        doc = copy.deepcopy(document)
        doc["phase_times"]["startup"][field] = value
        add("phase-" + field + "-" + str(value), doc)
    ctx = copy.deepcopy(context)
    ctx["artifacts"][document["source_boundaries"][0]["admission_set_ref"]["path"]] = "Changed chunk bytes.\n"
    add("changed-source-chunk", ctx=ctx)
    ctx = copy.deepcopy(context)
    del ctx["artifacts"][document["streams"][0]["raw_message_refs"][0]["path"]]
    add("missing-raw-chunk", ctx=ctx)
    ctx = copy.deepcopy(context)
    ctx["manifests"] = {}
    add("missing-run-manifest", ctx=ctx)
    ctx = copy.deepcopy(context)
    ctx["manifests"][document["run_manifest_path"]]["actual_artifacts"][0]["sha256"] = "f" * 64
    add("wrong-boundary-output-hash", ctx=ctx, rebind=False)
    ctx = copy.deepcopy(context)
    ctx["manifests"][document["run_manifest_path"]]["actual_artifacts"].append(
        copy.deepcopy(ctx["manifests"][document["run_manifest_path"]]["actual_artifacts"][0]))
    add("duplicate-boundary-output-reference", ctx=ctx, rebind=False)
    doc = copy.deepcopy(document)
    doc["case_identities"][0]["variant_id"] = "another-variant"
    add("wrong-case-identity", doc)
    return rows
