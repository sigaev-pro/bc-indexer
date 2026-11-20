"""Checked two-block seams over the accepted Polygon native helpers."""

import hashlib
import importlib.util
from pathlib import Path
import re

HERE = Path(__file__).resolve().parent
ACCEPTED = HERE.parent
PINS = {
    "poly_native.py": "1194a7a09d70f6b648f69e9622c0c2a23efd834008162c0b275d02b5c55fc3cb",
    "probe.go": "fa8bc85930121eb37bd3d4cefa7f6c36f96d5993cae33f6bee360f0703da2717",
    "poly_profile.py": "444cbdcb9a27af86a6e9eac39fd8a2536d6c6a9a530be52bb3c0b1f152abec4f",
    "poly_support.py": "3b9cd5c09f235de8aab62a901e239d9db6214beadefc57bc3cd8c4274f5ba100",
    "poly_codec.py": "928b7bb5dd9fae96a03256e1e13eeb554d22d3cd6d00e3573fb3e8630e60c823",
    "poly_judge.py": "67f25cf41b3ac6f423732ea82c8fcb7f09064ff3e587c6184b68f4beee023239",
}


def accepted(name):
    data = (ACCEPTED / name).read_bytes()
    if hashlib.sha256(data).hexdigest() != PINS[name]:
        raise ValueError("accepted helper identity changed: " + name)
    return data


def module(name):
    accepted(name + ".py")
    spec = importlib.util.spec_from_file_location("poly_activation_accepted_" + name, ACCEPTED / (name + ".py"))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def replace_once(text, before, after):
    if text.count(before) != 1:
        raise ValueError("non-unique accepted activation seam: " + before)
    return text.replace(before, after)


def render_support(corpus):
    shell = module("poly_support").render_support(corpus).decode()
    shell = replace_once(shell, "corpusBlocks         = 4", "corpusBlocks         = 2")
    current = accepted("probe.go").decode()
    current = replace_once(current, "func main() {", "func acceptedCurrentMain() {")
    current = replace_once(current, "func corpusTransactions(", "func acceptedCurrentTransactions(")
    imports, bodies = set(), []
    for source in (shell, current):
        matches = list(re.finditer(r"(?m)^import \(\n(.*?)\n\)", source, re.S))
        if len(matches) != 1:
            raise ValueError("expected one pinned Go import block")
        imports.update(line.strip() for line in matches[0][1].splitlines() if line.strip())
        bodies.append(source[matches[0].end():])
    return ("// Generated only from pinned accepted sources.\npackage main\n\nimport (\n" +
            "\n".join("\t" + item for item in sorted(imports)) + "\n)\n" + "\n".join(bodies)).encode()
