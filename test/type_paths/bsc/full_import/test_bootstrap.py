"""Source-derived two-call bootstrap state controls; no native acceptance."""
import copy
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('bsc_full_import_bootstrap_controls', HERE / 'judge.py')
j = importlib.util.module_from_spec(spec); spec.loader.exec_module(j)
GOV = '0x' + '2005'.zfill(40)


def fixture(restore):
    before = {a: {'balance': '0', 'nonce': 0, 'storage': {}} for a in j.ledger_addresses()['all']}
    after = copy.deepcopy(before)
    for state, pooled, own in ((before, 2000 if restore else 2001, 1999 if restore else 2000), (after, 2001 if restore else 2000, 2000 if restore else 1999)):
        state[j.CREDIT]['storage'] = {'103': str(pooled * 10**18), '152': str(pooled * 10**18), 'operator_shares': str(own * 10**18), 'locked_shares': str(10**18)}
        state[GOV]['storage'] = {'gov_supply': str(own * 10**18), 'gov_operator': str(own * 10**18), 'gov_dead': '0'}
        state[j.VALIDATOR_CONTRACT]['storage'] = {'11': '21', '1': '21', 'validator_index': '0'}
        state[j.STAKE_HUB]['storage'] = {'validator_jail_until': '0', '73': '0', '57': '604800'}
    before[j.VALIDATOR_CONTRACT]['storage']['11'] = '21' if restore else '0'
    before[j.CREDIT]['balance'] = str(2001 * 10**18); after[j.CREDIT]['balance'] = str((2002 if restore else 2001) * 10**18)
    pending = {'unbond_shares': 10**18, 'unbond_amount': 10**18, 'unbond_sequence': 1, 'unbond_queue': 1 << 128,
               'unbond_first': int.from_bytes(j.encoding.keccak256(bytes.fromhex(j.SENDER[2:]) + bytes(32)), 'big'), 'unbond_unlock': j.GENESIS_TIME + 7 + 604800}
    for key, value in pending.items():
        before[j.CREDIT]['storage'][key] = str(value if restore else 0); after[j.CREDIT]['storage'][key] = str(value)
    return {'type': 0, 'to': j.STAKE_HUB, 'nonce': 2 if restore else 1, 'time': j.GENESIS_TIME + 7,
            'fields': {'chainId': 714, 'gas': 2000000, 'gasPrice': 2000000000},
            'data': ('982ef0a7' if restore else '4d99dd16') + j.SENDER[2:].zfill(64) + (0 if restore else 10**18).to_bytes(32, 'big').hex(),
            'before': before, 'after': after}


class BootstrapControls(unittest.TestCase):
    def test_both_source_derived_transitions(self):
        for restore in (False, True): j.check_bootstrap(fixture(restore), 'bootstrap-delegate' if restore else 'bootstrap-undelegate')

    def test_each_selected_storage_and_balance_boundary(self):
        for restore in (False, True):
            row = fixture(restore); phase = 'bootstrap-delegate' if restore else 'bootstrap-undelegate'
            for boundary in ('before', 'after'):
                for address in (j.CREDIT, GOV, j.VALIDATOR_CONTRACT, j.STAKE_HUB):
                    for key in row[boundary][address]['storage']:
                        bad = copy.deepcopy(row); bad[boundary][address]['storage'][key] = str(int(bad[boundary][address]['storage'][key]) + 1)
                        with self.subTest(restore=restore, boundary=boundary, address=address, key=key), self.assertRaises(ValueError): j.check_bootstrap(bad, phase)
                bad = copy.deepcopy(row); bad[boundary][j.CREDIT]['balance'] = '0'
                with self.assertRaises(ValueError): j.check_bootstrap(bad, phase)

    def test_call_context_and_abi(self):
        for restore in (False, True):
            row = fixture(restore); phase = 'bootstrap-delegate' if restore else 'bootstrap-undelegate'
            for field, value in (('type', 1), ('to', j.BURN), ('nonce', 99), ('data', row['data'][:-2] + '01')):
                bad = copy.deepcopy(row); bad[field] = value
                with self.subTest(field=field), self.assertRaises(ValueError): j.check_bootstrap(bad, phase)
            for field in row['fields']:
                bad = copy.deepcopy(row); bad['fields'][field] = 1
                with self.subTest(field=field), self.assertRaises(ValueError): j.check_bootstrap(bad, phase)


if __name__ == '__main__': unittest.main()
