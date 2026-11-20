"""Explicit private Bor fork profile; never a public-network activation claim."""

import copy


SOURCE_COMMIT = "3a018e38467594508ca31ebeaf804837fb92834a"
SOURCE_ARCHIVE_SHA256 = "a313bc56a136b054d44ef0a319bcbc0fcf0eddba046035ed2b89b83c83847e38"

# BorMainnetChainConfig at SOURCE_COMMIT. Keep scheduled and unscheduled forks
# distinct: native MergedTestChainConfig activates additional, unscheduled rules.
ETHEREUM_SCHEDULE = {
    "berlinBlock": 14750000, "londonBlock": 23850000,
    "shanghaiBlock": 50523000, "cancunBlock": 54876000,
    "pragueBlock": 73440256,
}
BOR_SCHEDULE = {
    "jaipurBlock": 23850000, "delhiBlock": 38189056,
    "indoreBlock": 44934656, "ahmedabadBlock": 62278656,
    "bhilaiBlock": 73440256, "rioBlock": 77414656,
    "madhugiriBlock": 80084800, "madhugiriProBlock": 80084800,
    "dandeliBlock": 81424000, "lisovoBlock": 83756500,
    "lisovoProBlock": 83756500, "giuglianoBlock": 85268500,
    "chicagoBlock": 87218600, "valenciaBlock": 89531000,
    "austinBlock": 91949700, "uburuBlock": 96787900,
}


def private_genesis(source_genesis):
    """Derive rules only; retain native test accounts and contract bytecode.

    This output still needs native genesis/configuration/import validation. It
    is not a declaration that a transaction type is executable on Polygon.
    """
    genesis = copy.deepcopy(source_genesis)
    config = genesis.get("config", {})
    bor = config.get("bor")
    if config.get("chainId") != 15001 or not isinstance(bor, dict):
        raise ValueError("expected the pinned private Bor genesis")
    if bor.get("hampiBlock") is not None:
        raise ValueError("Hampi is unscheduled in the pinned mainnet source")
    for field in ("osakaTime", "osakaBlock", "terminalTotalDifficulty"):
        if config.get(field) is not None:
            raise ValueError(f"unexpected non-Bor schedule: {field}")
    for field in ETHEREUM_SCHEDULE:
        config[field] = 0
    for field in BOR_SCHEDULE:
        bor[field] = 0
    bor["hampiBlock"] = None
    bor["sprint"] = {"0": 4}
    bor["stateSyncConfirmationDelay"] = {"0": 0}
    # Rio requires a configured beneficiary, independent of the signing key.
    bor["coinbase"] = {"0": "0x0000000000000000000000000000000000000000"}
    return genesis
