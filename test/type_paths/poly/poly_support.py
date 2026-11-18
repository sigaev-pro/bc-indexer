"""Reuse the accepted native corpus shell with explicit, checked fixture seams."""

import hashlib


CORPUS_SHA256 = "fd73e2f4eb5965a29d53563099891e3d9dbe6badaf92f70f44f9cfffdfff7f91"


def render_support(source):
    """Keep accepted helpers, replace only entry point/cases/tracer selection.

    The result is compiled beside probe.go in the original corpus package path
    in a new owned native tree. No accepted source or installed adapter changes.
    """
    if hashlib.sha256(source).hexdigest() != CORPUS_SHA256:
        raise ValueError("accepted Polygon corpus source identity changed")
    text = source.decode("utf-8")
    edits = (
        ("func main() {", "func acceptedCorpusMain() {"),
        ("func corpusTransactions(config *params.ChainConfig, key *ecdsa.PrivateKey)",
         "func acceptedCorpusTransactions(config *params.ChainConfig, key *ecdsa.PrivateKey)"),
        ("cfg := ethconfig.Defaults\n", "cfg := ethconfig.Defaults\n\tcfg.VMTrace = typePathTracer\n"),
    )
    for before, after in edits:
        if text.count(before) != 1:
            raise ValueError("accepted Polygon corpus seam is not unique")
        text = text.replace(before, after)
    return text.encode("utf-8")
