"""Checked block-selection seams over immutable Polygon fixture helpers."""

import hashlib
import importlib.util
from pathlib import Path
import re

HERE = Path(__file__).resolve().parent
ACTIVATION = HERE.parent / "activation"
ACTIVATION_PINS = {
    "activation_support.py": "ed3a1c6db42bf898aeeb4bb8e83872ff4be891f9a8f3846ecb136a82f0090895",
    "probe.go": "11b35a1c9e5a9ae5c095a7dc40779379cf33ea3ac5b20fe4905b1f0070c17805",
}


def activation_source(name):
    data = (ACTIVATION / name).read_bytes()
    if hashlib.sha256(data).hexdigest() != ACTIVATION_PINS[name]:
        raise ValueError("activation helper identity changed: " + name)
    return data


activation_source("activation_support.py")
spec = importlib.util.spec_from_file_location("crossing_activation_support", ACTIVATION / "activation_support.py")
parent = importlib.util.module_from_spec(spec)
spec.loader.exec_module(parent)
module, replace_once = parent.module, parent.replace_once


def render_support(corpus):
    shell = parent.render_support(corpus).decode()
    shell = replace_once(shell, "\ttxs, err := corpusTransactions(config, sender)\n\tif err != nil {\n\t\treturn err\n\t}\n", "")
    shell = replace_once(shell, "\t\tif number == 1 {", "\t\ttxs, err := crossingTransactions(config, sender, number)\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif len(txs) != 0 {")
    source = activation_source("probe.go").decode()
    marker = "func activationCase("
    if source.count(marker) != 1:
        raise ValueError("non-unique activation helper boundary")
    helpers = source[:source.index(marker)]
    helpers = replace_once(helpers, "func historical(input, output string, ethPrefix, borPrefix int)",
                           "func crossingInputs(input, output string, ethPrefix, borPrefix int, gate, borGate string)")
    helpers = replace_once(helpers, "\tif ethPrefix < 2 {", "\tconfig[gate] = 2\n\tif borGate != \"\" {\n\t\tengine[borGate] = 2\n\t}\n\tif ethPrefix < 2 {")
    helpers = replace_once(helpers, "func activationTransaction(config *params.ChainConfig, kind byte)",
                           "func crossingTransaction(config *params.ChainConfig, kind byte, nonce uint64)")
    if helpers.count("Nonce: 1,") != 3:
        raise ValueError("unexpected typed transaction nonce seams")
    helpers = helpers.replace("Nonce: 1,", "Nonce: nonce,")
    imports, bodies = set(), []
    for source in (shell, helpers):
        matches = list(re.finditer(r"(?m)^import \(\n(.*?)\n\)", source, re.S))
        if len(matches) != 1:
            raise ValueError("expected one pinned Go import block")
        imports.update(line.strip() for line in matches[0][1].splitlines() if line.strip())
        bodies.append(source[matches[0].end():])
    return ("// Generated from pinned corpus, current-fork and activation helpers.\npackage main\n\nimport (\n" +
            "\n".join("\t" + item for item in sorted(imports)) + "\n)\n" + "\n".join(bodies)).encode()
