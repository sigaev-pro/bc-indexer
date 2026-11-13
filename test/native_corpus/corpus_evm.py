"""A small reference executor for the E01-03.smoke_corpus contracts.

It is written from the EVM gas rules (EIP-150, EIP-158, EIP-2200, EIP-2929,
EIP-3529) and the yellow paper, not from a native client or a native output.
It runs only the opcodes that the corpus contracts use and refuses any other
opcode, so a contract change cannot silently leave the reference model.

Two rule sets exist:
- `ISTANBUL`: BSC blocks before Berlin (the private BSC fixture has Berlin
  and London at block 8);
- `LONDON`: the Ethereum fixture (London at genesis) and Bor block 1.

The executor models one transaction at a time on a plain dictionary state.
Fee payment is left to the caller (`corpus_oracle.py`), because each family pays
fees differently.
"""

import copy
from dataclasses import dataclass, field

WORD = 2 ** 256


@dataclass(frozen=True)
class Rules:
    name: str
    berlin: bool  # EIP-2929 access lists and EIP-2930 warm set
    london: bool  # EIP-3529 refunds
    refund_quotient: int


ISTANBUL = Rules("istanbul", berlin=False, london=False, refund_quotient=2)
LONDON = Rules("london", berlin=True, london=True, refund_quotient=5)

# Precompiles that are warm from the start of a Berlin transaction (1 to 9).
PRECOMPILES = tuple(f"{i:040x}" for i in range(1, 10))

OPCODES = {
    "STOP": 0x00, "ISZERO": 0x15, "CALLER": 0x33, "CALLVALUE": 0x34, "CALLDATALOAD": 0x35,
    "CALLDATASIZE": 0x36, "RETURNDATASIZE": 0x3D, "POP": 0x50, "MLOAD": 0x51, "MSTORE": 0x52,
    "SLOAD": 0x54, "SSTORE": 0x55, "GAS": 0x5A, "LOG0": 0xA0, "LOG1": 0xA1, "LOG2": 0xA2,
    "CALL": 0xF1, "RETURN": 0xF3, "REVERT": 0xFD,
}
NAMES = {value: key for key, value in OPCODES.items()}


class Unsupported(Exception):
    """The code uses an opcode or a case that this reference model does not cover."""


class Halt(Exception):
    """An exceptional halt: the frame consumes all its gas and reverts."""


def assemble(program):
    """Assemble a list of mnemonics and ("PUSHn", int) tuples into bytes."""
    out = bytearray()
    for item in program:
        if isinstance(item, tuple):
            name, value = item
            if not name.startswith("PUSH"):
                raise ValueError(f"only PUSH takes an argument: {name}")
            size = int(name[4:])
            if not 1 <= size <= 32 or value < 0 or value >= 256 ** size:
                raise ValueError(f"bad push {name} {value}")
            out.append(0x5F + size)
            out += value.to_bytes(size, "big")
        else:
            out.append(OPCODES[item])
    return bytes(out)


def disassemble(code):
    """The inverse of `assemble`, for the README and the review."""
    out, pc = [], 0
    while pc < len(code):
        op = code[pc]
        if 0x60 <= op <= 0x7F:
            size = op - 0x5F
            out.append(f"PUSH{size} 0x{code[pc + 1:pc + 1 + size].hex()}")
            pc += 1 + size
        else:
            if op not in NAMES:
                raise Unsupported(f"opcode 0x{op:02x} at {pc}")
            out.append(NAMES[op])
            pc += 1
    return out


@dataclass
class Account:
    balance: int = 0
    nonce: int = 0
    code: bytes = b""
    storage: dict = field(default_factory=dict)

    def empty(self):
        return self.balance == 0 and self.nonce == 0 and not self.code


def memory_cost(words):
    return 3 * words + words * words // 512


def to_word(data):
    return int.from_bytes(data.ljust(32, b"\0")[:32], "big")


def address_word(address):
    return int(address, 16)


def word_address(value):
    return f"{value % 2 ** 160:040x}"


class Transaction:
    """The journaled state of one transaction.

    `state` maps a lowercase 40-hex address to an `Account`. `on_transfer`,
    when set, is called after every value transfer inside the transfer's own
    snapshot (Bor adds its transfer log there).
    """

    def __init__(self, rules, state, origin, on_transfer=None):
        self.rules = rules
        self.state = state
        self.origin = origin
        self.on_transfer = on_transfer
        self.logs = []
        self.refund = 0
        self.warm_accounts = set()
        self.warm_slots = set()
        self.original = {}

    # ---- journal ----

    def snapshot(self):
        return (copy.deepcopy(self.state), list(self.logs), self.refund, set(self.warm_accounts),
                set(self.warm_slots))

    def revert(self, snap):
        state, logs, refund, accounts, slots = snap
        self.state.clear()
        self.state.update(state)
        self.logs[:] = logs
        self.refund = refund
        self.warm_accounts = accounts
        self.warm_slots = slots

    def account(self, address):
        return self.state.setdefault(address, Account())

    def exists(self, address):
        return address in self.state

    def transfer(self, sender, recipient, value):
        if self.account(sender).balance < value:
            raise Unsupported("insufficient balance for a value transfer")
        self.account(sender).balance -= value
        self.account(recipient).balance += value
        if self.on_transfer is not None:
            self.on_transfer(self, sender, recipient, value)

    def add_log(self, address, topics, data):
        self.logs.append({"address": address, "topics": [f"{t:064x}" for t in topics], "data": data.hex()})

    # ---- calls ----

    def call(self, caller, to, value, data, gas, depth=0):
        """Run a CALL. Return (success, gas_left, output, frame)."""
        frame = {"type": "CALL", "from": caller, "to": to, "value": value, "input": data.hex(), "calls": []}
        if depth > 1024:
            raise Unsupported("call depth")
        if value and self.account(caller).balance < value:
            raise Unsupported("insufficient balance for CALL")
        snap = self.snapshot()
        if not self.exists(to) and value == 0:
            frame.update(output="", error=None)
            return True, gas, b"", frame
        self.account(to)
        self.transfer(caller, to, value)
        code = self.state[to].code
        if not code:
            frame.update(output="", error=None)
            return True, gas, b"", frame
        try:
            output, gas_left, reverted = self.run(code, caller, to, value, data, gas, depth, frame)
        except Halt as halt:
            self.revert(snap)
            frame.update(output="", error=str(halt))
            return False, 0, b"", frame
        if reverted:
            self.revert(snap)
            frame.update(output=output.hex(), error="execution reverted")
            return False, gas_left, output, frame
        frame.update(output=output.hex(), error=None)
        return True, gas_left, output, frame

    def run(self, code, caller, address, value, data, gas, depth, frame):
        rules = self.rules
        stack, memory, pc = [], bytearray(), 0
        return_data = b""

        def use(amount):
            nonlocal gas
            if amount > gas:
                raise Halt("out of gas")
            gas -= amount

        def pop():
            if not stack:
                raise Halt("stack underflow")
            return stack.pop()

        def push(item):
            if len(stack) >= 1024:
                raise Halt("stack limit reached 1024 (1023)")
            stack.append(item % WORD)

        def expand(offset, size):
            if size == 0:
                return 0
            end = offset + size
            if end > 2 ** 32:
                raise Unsupported("memory offset")
            old = (len(memory) + 31) // 32
            new = (end + 31) // 32
            if new <= old:
                return 0
            return memory_cost(new) - memory_cost(old)

        def grow(offset, size):
            if size and offset + size > len(memory):
                memory.extend(b"\0" * ((offset + size + 31) // 32 * 32 - len(memory)))

        while True:
            if pc >= len(code):
                return b"", gas, False
            op = code[pc]
            if 0x60 <= op <= 0x7F:
                size = op - 0x5F
                use(3)
                push(int.from_bytes(code[pc + 1:pc + 1 + size].ljust(size, b"\0"), "big"))
                pc += 1 + size
                continue
            name = NAMES.get(op)
            if name is None:
                raise Unsupported(f"opcode 0x{op:02x} at {pc}")
            pc += 1
            if name == "STOP":
                return b"", gas, False
            if name == "ISZERO":
                use(3)
                push(1 if pop() == 0 else 0)
            elif name == "CALLER":
                use(2)
                push(address_word(caller))
            elif name == "CALLVALUE":
                use(2)
                push(value)
            elif name == "CALLDATALOAD":
                use(3)
                offset = pop()
                push(to_word(data[offset:offset + 32]) if offset < len(data) else 0)
            elif name == "CALLDATASIZE":
                use(2)
                push(len(data))
            elif name == "RETURNDATASIZE":
                use(2)
                push(len(return_data))
            elif name == "POP":
                use(2)
                pop()
            elif name == "GAS":
                use(2)
                push(gas)
            elif name == "MLOAD":
                offset = pop()
                use(3 + expand(offset, 32))
                grow(offset, 32)
                push(int.from_bytes(memory[offset:offset + 32], "big"))
            elif name == "MSTORE":
                offset, item = pop(), pop()
                use(3 + expand(offset, 32))
                grow(offset, 32)
                memory[offset:offset + 32] = item.to_bytes(32, "big")
            elif name == "SLOAD":
                slot = pop()
                key = (address, slot)
                if rules.berlin:
                    use(100 if key in self.warm_slots else 2100)
                    self.warm_slots.add(key)
                else:
                    use(800)
                push(self.account(address).storage.get(slot, 0))
            elif name == "SSTORE":
                slot, new = pop(), pop()
                use(self.sstore_cost(address, slot, new, gas))
                if new:
                    self.account(address).storage[slot] = new
                else:
                    self.account(address).storage.pop(slot, None)
            elif name in ("LOG0", "LOG1", "LOG2"):
                count = int(name[3])
                offset, size = pop(), pop()
                topics = [pop() for _ in range(count)]
                use(375 + 375 * count + 8 * size + expand(offset, size))
                grow(offset, size)
                self.add_log(address, topics, bytes(memory[offset:offset + size]))
            elif name in ("RETURN", "REVERT"):
                offset, size = pop(), pop()
                use(expand(offset, size))
                grow(offset, size)
                return bytes(memory[offset:offset + size]), gas, name == "REVERT"
            elif name == "CALL":
                requested, target_word, call_value = pop(), pop(), pop()
                in_offset, in_size, out_offset, out_size = pop(), pop(), pop(), pop()
                target = word_address(target_word)
                use(100 if rules.berlin else 700)
                if rules.berlin and target not in self.warm_accounts:
                    use(2500)
                    self.warm_accounts.add(target)
                base = expand(in_offset, in_size)
                grow(in_offset, in_size)
                base += expand(out_offset, out_size)
                grow(out_offset, out_size)
                if call_value:
                    base += 9000
                    if not self.exists(target) or self.state[target].empty():
                        base += 25000
                use(base)
                available = gas - gas // 64
                call_gas = min(requested, available)
                use(call_gas)
                if call_value:
                    call_gas += 2300
                payload = bytes(memory[in_offset:in_offset + in_size])
                success, left, output, child = self.call(address, target, call_value, payload, call_gas, depth + 1)
                frame["calls"].append(child)
                gas += left
                return_data = output
                if success or child["error"] == "execution reverted":
                    copied = output[:out_size]
                    memory[out_offset:out_offset + len(copied)] = copied
                push(1 if success else 0)
            else:  # pragma: no cover - every name above is handled
                raise Unsupported(name)

    def sstore_cost(self, address, slot, new, gas_left):
        """EIP-2200 (Istanbul) and EIP-2929 with EIP-3529 (London)."""
        rules = self.rules
        if gas_left <= 2300:
            raise Halt("out of gas")
        storage = self.account(address).storage
        current = storage.get(slot, 0)
        original = self.original.get((address, slot), current)
        self.original.setdefault((address, slot), current)
        cold = 0
        if rules.berlin:
            if (address, slot) not in self.warm_slots:
                cold = 2100
                self.warm_slots.add((address, slot))
            read, reset, clears = 100, 5000 - 2100, (4800 if rules.london else 15000)
        else:
            read, reset, clears = 800, 5000, 15000
        if current == new:
            return cold + read
        if original == current:
            if original == 0:
                return cold + 20000
            if new == 0:
                self.refund += clears
            return cold + reset
        if original != 0:
            if current == 0:
                self.refund -= clears
            elif new == 0:
                self.refund += clears
        if original == new:
            if original == 0:
                self.refund += 20000 - read
            else:
                self.refund += reset - read
        return cold + read


def intrinsic_gas(data):
    return 21000 + sum(16 if byte else 4 for byte in data)


def execute(rules, state, sender, to, value, data, gas_limit, on_transfer=None):
    """Execute a top-level call transaction without paying fees.

    The caller has already charged the gas purchase and incremented the
    sender nonce. Returns a dictionary with status, gas used (after the
    refund), logs and the call frame. `state` is changed in place.
    """
    tx = Transaction(rules, state, sender, on_transfer)
    intrinsic = intrinsic_gas(data)
    if gas_limit < intrinsic:
        raise Unsupported("gas limit below intrinsic gas")
    if rules.berlin:
        tx.warm_accounts.update((sender, to, *PRECOMPILES))
    success, left, output, frame = tx.call(sender, to, value, data, gas_limit - intrinsic)
    used = gas_limit - left
    refund = min(tx.refund, used // rules.refund_quotient)
    used -= refund
    return {"status": 1 if success else 0, "gas_used": used, "logs": tx.logs, "frame": frame,
            "refund": refund}
