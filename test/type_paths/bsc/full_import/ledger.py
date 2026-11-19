"""Absolute private-fixture balances/nonces, independent of observed snapshots."""


class BalanceLedger:
    def __init__(self, genesis, addresses, quantity):
        self.addresses = addresses
        self.quantity = quantity
        self.balances = {address: 0 for address in addresses["all"]}
        self.nonces = {address: 0 for address in addresses["all"]}
        for address, account in genesis["alloc"].items():
            address = "0x" + address.removeprefix("0x")
            self.balances[address] = quantity(account["balance"])
            self.nonces[address] = quantity(account.get("nonce", 0))

    def require_state(self, state, boundary):
        if set(state) != set(self.balances):
            raise ValueError(boundary + " account set differs")
        for address in self.balances:
            account = state[address]
            if self.quantity(account["balance"]) != self.balances[address]:
                raise ValueError(boundary + " absolute balance differs: " + address)
            if type(account["nonce"]) is not int or account["nonce"] != self.nonces[address]:
                raise ValueError(boundary + " absolute nonce differs: " + address)

    def move(self, sender, recipient, value):
        if value < 0 or self.balances[sender] < value:
            raise ValueError("impossible expected transfer")
        self.balances[sender] -= value
        self.balances[recipient] += value

    def apply(self, row, phase):
        a = self.addresses
        selector = row["data"][:8]
        system = row["system"]
        if system and selector == "f340fa01":
            # Native Parlia moves accumulated fees immediately before the
            # deposit transaction's OnTxStart boundary, outside that tx.
            self.move(a["system"], a["validator"], self.balances[a["system"]])
        self.require_state(row["before"], "before")
        if system:
            self.nonces[a["validator"]] += 1
            if selector in ("e1c7392a", "8129fc1c"):
                pass
            elif selector == "f340fa01":
                value = self.quantity(row["value"])
                if value != self.balances[a["validator"]]:
                    raise ValueError("deposit does not consume the expected fee balance")
                reward, burn = value * 625 // 10000, value * 1000 // 10000
                self.move(a["validator"], a["reward"], reward)
                self.move(a["validator"], a["burn"], burn)
                self.move(a["validator"], a["validator_contract"], value - reward - burn)
            elif selector == "1e4c1524":
                if self.nonces[a["stake_hub"]] != 1:
                    raise ValueError("election precedes expected registered stake")
                self.move(a["validator_contract"], a["reward"], self.balances[a["validator_contract"]])
            else:
                raise ValueError("unmodelled ledger system transaction")
        else:
            kind = row["type"]
            if type(kind) is not int or kind not in range(5):
                raise ValueError("invalid fixture user type")
            price = 2000000000 if kind <= 1 else 1000000000
            fee = self.quantity(row["receipt"]["gasUsed"]) * price + (131072 if kind == 3 else 0)
            self.move(a["sender"], a["system"], fee)
            self.nonces[a["sender"]] += 1
            if phase == "registration":
                self.move(a["sender"], a["credit"], 2001 * 10**18)
                self.nonces[a["stake_hub"]] += 1
                self.nonces[a["credit"]] = 1
            elif phase == "bootstrap-undelegate":
                pass
            elif phase == "bootstrap-delegate":
                self.move(a["sender"], a["credit"], 10**18)
            elif phase in ("success", "revert"):
                if kind == 4:
                    self.nonces[a["authority"]] += 1
                if phase == "success":
                    recipient = a["authority"] if kind == 4 else a["success"]
                    self.move(a["sender"], recipient, 1)
            else:
                raise ValueError("unmodelled ledger user phase")
        self.require_state(row["after"], "after")
