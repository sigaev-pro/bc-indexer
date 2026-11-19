"""Independent user state/fee and registration ABI controls, not execution credit."""
import copy
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('bsc_full_import_user_controls', HERE / 'judge.py')
j = importlib.util.module_from_spec(spec)
spec.loader.exec_module(j)
VOTE = bytes(range(48))


def registration_data():
    data = bytearray(768)
    def word(offset, value): data[offset:offset + 32] = value.to_bytes(32, 'big')
    for index, value in enumerate((int(j.VALIDATOR, 16), 224, 320, 100, 1000, 100, 448)): word(index * 32, value)
    word(224, 48); data[256:304] = VOTE
    word(320, 96); data[352:448] = bytes(range(96))
    for index, value in enumerate((128, 192, 224, 256)): word(448 + index * 32, value)
    for offset, value in ((576, b'Extractor'), (640, b''), (672, b''), (704, b'bounded fixture')):
        word(offset, len(value)); data[offset + 32:offset + 32 + len(value)] = value
    return j.REGISTER + data.hex()


def user(kind, phase):
    index = 3 + kind + (5 if phase == 'revert' else 0)
    status = int(phase != 'revert'); price = 2_000_000_000 if kind < 2 else 1_000_000_000
    before = {a: {'balance': '0', 'nonce': 0, 'storage': {'0': '0'}, 'code_hash': '0x' + '0' * 64} for a in j.ledger_addresses()['all']}
    before[j.SENDER].update(balance=str(100000 * 10**18), nonce=index)
    before[j.AUTHORITY]['nonce'] = int(phase == 'revert')
    before[j.AUTHORITY]['storage']['0'] = '1' if phase == 'revert' else '0'
    after = copy.deepcopy(before)
    fee = 30000 * price + (131072 if kind == 3 else 0)
    after[j.SENDER].update(balance=str(int(before[j.SENDER]['balance']) - fee - status), nonce=index + 1)
    after[j.SYSTEM]['balance'] = str(fee)
    target = j.SUCCESS if phase == 'success' else j.REVERT
    recipient = j.AUTHORITY if kind == 4 else target
    after[recipient]['balance'] = str(status)
    if status: after[recipient]['storage']['0'] = '1'
    fields = {'chainId': 714, 'gas': 150000, 'input': '0x', 'gasPrice': 2_000_000_000,
              'maxFeePerGas': 2_000_000_000, 'maxPriorityFeePerGas': 1_000_000_000,
              'accessList': [{'address': target, 'storageKeys': ['0x' + '0' * 64] * 2}, {'address': target, 'storageKeys': []}]}
    receipt = {'status': status, 'gasUsed': 30000}
    if kind == 3:
        fields.update(blobVersionedHashes=['0x010657f37554c781402a22917dee2f75def7ab966d7b770905398eba3c444014'], maxFeePerBlobGas=10**9)
        receipt.update(blobGasUsed=131072, blobGasPrice=1)
    if kind == 4:
        fields['authorizationList'] = [{'chainId': 714, 'nonce': int(phase == 'revert'), 'address': target}]
        after[j.AUTHORITY]['nonce'] += 1
        after[j.AUTHORITY]['code_hash'] = '0x' + j.encoding.keccak256(bytes.fromhex('ef0100' + target[2:])).hex()
    return {'type': kind, 'from': j.SENDER, 'to': recipient, 'nonce': index, 'value': '1', 'time': j.GENESIS_TIME + 20,
            'gas_price': str(price), 'receipt': receipt, 'error': None, 'fields': fields, 'before': before, 'after': after}


def registration():
    row = user(0, 'success')
    value = 2001 * 10**18
    row.update(to=j.STAKE_HUB, nonce=0, value=str(value), data=registration_data())
    row['fields'].update(gas=8000000)
    row['before'][j.SENDER]['nonce'] = 0; row['after'][j.SENDER]['nonce'] = 1
    row['after'][j.SENDER]['balance'] = str(int(row['before'][j.SENDER]['balance']) - 30000 * 2_000_000_000 - value)
    row['after'][j.CREDIT].update(balance=str(value), nonce=1)
    row['after'][j.STAKE_HUB]['nonce'] = 1
    row['after'][j.SUCCESS]['balance'] = '0'
    hub = {'63': 1, 'operator_index': 1, 'consensus_operator': int(j.SENDER, 16),
           'validator_consensus': int(j.VALIDATOR, 16), 'validator_operator': int(j.SENDER, 16),
           'validator_credit': int(j.CREDIT, 16), 'validator_created': row['time'], 'validator_updated': row['time'],
           'validator_vote': 97, 'validator_commission': 100 | (1000 << 64) | (100 << 128),
           'validator_jail_until': 0, 'validator_identity': 0, 'validator_website': 0,
           'validator_moniker': int.from_bytes(b'Extractor'.ljust(31, b'\0') + b'\x12', 'big'),
           'validator_details': int.from_bytes(b'bounded fixture'.ljust(31, b'\0') + b'\x1e', 'big')}
    credit = {'0': 1, '1': 1, '103': value, '151': int(j.SENDER, 16), '152': value,
              'operator_shares': 2000 * 10**18, 'locked_shares': 10**18, 'implementation': 0x2003, 'admin': 0xdead}
    gov = {'gov_supply': 2000 * 10**18, 'gov_operator': 2000 * 10**18, 'gov_dead': 0}
    for address, slots in ((j.STAKE_HUB, hub), (j.CREDIT, credit), ('0x' + '2005'.zfill(40), gov)):
        row['before'][address]['storage'] = {key: '0' for key in slots}
        row['after'][address]['storage'] = {key: str(value) for key, value in slots.items()}
    return row


class UserControls(unittest.TestCase):
    def test_ten_type_and_outcome_cases(self):
        for phase in ('success', 'revert'):
            for kind in range(5):
                row = user(kind, phase)
                j.check_user(row, phase, row['nonce'], VOTE)

    def test_envelope_fee_status_and_state_predicates(self):
        for phase in ('success', 'revert'):
            for kind in range(5):
                row = user(kind, phase)
                changes = [(['from'], j.AUTHORITY), (['nonce'], 99), (['gas_price'], '1'), (['value'], '2'),
                           (['to'], j.BURN), (['time'], 0), (['receipt', 'status'], 1 - row['receipt']['status']),
                           (['receipt', 'gasUsed'], 1), (['error'], 'execution error'),
                           (['fields', 'chainId'], 1), (['fields', 'gas'], 1), (['fields', 'input'], '0xff'),
                           (['after', j.SENDER, 'balance'], '0'), (['after', j.SENDER, 'nonce'], 0),
                           (['before', j.SENDER, 'nonce'], 99), (['after', j.SYSTEM, 'balance'], '0'),
                           (['after', row['to'], 'balance'], '10'), (['after', j.REVERT, 'storage', '0'], '1')]
                changes += [(['fields', 'gasPrice'], 1)] if kind < 2 else [(['fields', 'maxFeePerGas'], 1), (['fields', 'maxPriorityFeePerGas'], 1)]
                if kind: changes += [(['fields', 'accessList'], [])]
                if phase == 'success': changes += [(['after', row['to'], 'storage', '0'], '0')]
                if kind == 3: changes += [(['fields', 'blobVersionedHashes'], []), (['fields', 'maxFeePerBlobGas'], 1), (['receipt', 'blobGasUsed'], 1), (['receipt', 'blobGasPrice'], 2)]
                if kind == 4: changes += [(['fields', 'authorizationList'], []), (['fields', 'authorizationList', 0, 'chainId'], 1), (['fields', 'authorizationList', 0, 'nonce'], 99), (['fields', 'authorizationList', 0, 'address'], j.BURN), (['after', j.AUTHORITY, 'nonce'], 99), (['after', j.AUTHORITY, 'code_hash'], '0x' + '0' * 64), (['after', j.AUTHORITY, 'storage', '0'], '0')]
                for path, value in changes:
                    bad = copy.deepcopy(row); cursor = bad
                    for key in path[:-1]: cursor = cursor[key]
                    cursor[path[-1]] = value
                    with self.subTest(phase=phase, kind=kind, path=path), self.assertRaises(ValueError):
                        j.check_user(bad, phase, row['nonce'], VOTE)

    def test_registration_state_and_every_selected_slot(self):
        row = registration()
        j.check_user(row, 'registration', 0, VOTE)
        for boundary in ('before', 'after'):
            for address in (j.STAKE_HUB, j.CREDIT, '0x' + '2005'.zfill(40)):
                for key in row[boundary][address]['storage']:
                    if boundary == 'before' and key == 'gov_dead': continue
                    bad = copy.deepcopy(row)
                    bad[boundary][address]['storage'][key] = str(int(bad[boundary][address]['storage'][key]) + 1)
                    with self.subTest(boundary=boundary, address=address, key=key), self.assertRaises(ValueError):
                        j.check_user(bad, 'registration', 0, VOTE)
            for address in (j.STAKE_HUB, j.CREDIT):
                for key in ('balance', 'nonce'):
                    bad = copy.deepcopy(row); bad[boundary][address][key] = int(bad[boundary][address][key]) + 1
                    with self.subTest(boundary=boundary, address=address, key=key), self.assertRaises(ValueError):
                        j.check_user(bad, 'registration', 0, VOTE)
        for key, value in (('type', 1), ('to', j.SUCCESS), ('value', '1')):
            bad = copy.deepcopy(row); bad[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError): j.check_user(bad, 'registration', 0, VOTE)
        for boundary in ('before', 'after'):
            bad = copy.deepcopy(row); bad[boundary][j.BURN]['balance'] = '1'
            with self.assertRaises(ValueError): j.check_user(bad, 'registration', 0, VOTE)

    def test_registration_abi_fields_and_padding(self):
        row = {'fields': {'chainId': 714, 'gas': 8000000, 'gasPrice': 2000000000}, 'data': registration_data()}
        j.check_registration_input(row, VOTE)
        for field in row['fields']:
            bad = copy.deepcopy(row); bad['fields'][field] = 1
            with self.subTest(field=field), self.assertRaises(ValueError): j.check_registration_input(bad, VOTE)
        # Every fixed ABI byte must bind, except BLS proof contents verified natively.
        for offset in list(range(4 + 352)) + list(range(4 + 448, 772)):
            raw = bytearray.fromhex(row['data']); raw[offset] ^= 1
            bad = dict(row, data=raw.hex())
            with self.subTest(offset=offset), self.assertRaises(ValueError): j.check_registration_input(bad, VOTE)
        for data in (row['data'][:-2], row['data'] + '00'):
            with self.assertRaises(ValueError): j.check_registration_input(dict(row, data=data), VOTE)
        raw = bytearray.fromhex(row['data']); raw[356:452] = bytes(96)
        with self.assertRaises(ValueError): j.check_registration_input(dict(row, data=raw.hex()), VOTE)


if __name__ == '__main__': unittest.main()
