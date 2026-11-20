"""Portable genesis, receipt and durable capture boundary controls."""
import copy
import hashlib
import importlib.util
from pathlib import Path
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('bsc_full_import_boundary_controls', HERE / 'judge.py')
j = importlib.util.module_from_spec(spec); spec.loader.exec_module(j)


def genesis_fixture():
    contracts = [{'address': '0x' + '1000'.zfill(40), 'bytecode_sha256': hashlib.sha256(b'\x00').hexdigest()}]
    config = {'chainId': 714, 'jennerTime': j.GENESIS_TIME + 864000, 'keplerTime': 0, 'shanghaiTime': 0, 'homesteadBlock': 0}
    config.update({key: j.GENESIS_TIME + 6 for key in j.CURRENT_TIMES})
    alloc = {contracts[0]['address'][2:]: {'balance': '0x0', 'code': '0x00'},
             j.SENDER[2:]: {'balance': str(100000 * 10**18)}, j.SUCCESS[2:]: {'balance': '0', 'code': '0x600160005500'},
             j.REVERT[2:]: {'balance': '0', 'code': '0x600260005560006000fd'}}
    extra = bytes(32) + b'\x01' + bytes.fromhex(j.VALIDATOR[2:]) + bytes(range(48)) + bytes(65)
    return {'config': config, 'alloc': alloc, 'timestamp': j.GENESIS_TIME, 'gasLimit': 40000000, 'difficulty': 1, 'extraData': '0x' + extra.hex()}, contracts


def receipt_fixture(kind=2):
    address, topic, data = bytes.fromhex(j.SUCCESS[2:]), bytes(range(32)), b'hello'
    fields = [1, 45000, bytes(256), [[address, [topic], data]]]
    raw = (bytes([kind]) if kind else b'') + j.wire.encode(fields)
    block = {'number': 7, 'hash': '0x' + '3' * 64}
    receipt = {'status': '0x1', 'cumulativeGasUsed': hex(45000), 'gasUsed': hex(30000), 'logsBloom': '0x' + '00' * 256,
               'logs': [{'address': j.SUCCESS, 'topics': ['0x' + topic.hex()], 'data': '0x' + data.hex()}],
               'transactionIndex': '0x1', 'blockNumber': '0x7', 'blockHash': block['hash']}
    return {'type': kind, 'receipt': receipt, 'stored_receipt_wire': raw.hex(), 'observed_receipt_wire': raw.hex()}, block, fields


def bridge_fixture():
    blocks = [{'hash': '0x' + str(i) * 64, 'root': '0x' + str(i + 2) * 64} for i in (1, 2)]
    genesis = {'hash': '0x' + '0' * 64}; previous = {'number': 0, 'hash': '0' * 64}
    records, obligations = {'counter': {'next': 2}}, []
    for index, block in enumerate(blocks):
        head = {'number': index + 1, 'hash': block['hash'][2:]}
        intent = {'native_store_id': '0123456789abcdef0123456789abcdef', 'producer': 'bsc-probe.run-1', 'sequence': index,
                  'kind': 'IMPORT', 'old_head': previous, 'new_head': head, 'evidence_digest': 'a' * 64, 'events': True,
                  'members': [{'observation': f'bsc-probe.run-1.{index:016x}', 'block': head['hash'], 'exec_ready_digest': 'b' * 64, 'child_root': block['root'][2:]}]}
        intent['digest'] = j.encoding.intent_digest(intent)
        decided = {'sequence': index, 'intent_digest': intent['digest'], 'delivery': 'BATCH', 'digest': j.encoding.decided_digest(intent['digest'], 'BATCH')}
        records[f'{index:016x}/intent'], records[f'{index:016x}/decided'] = intent, decided
        obligations.append({'sequence': index, 'intent_digest': intent['digest'], 'decided_digest': decided['digest']})
        previous = head
    return {'mode': 'LIVE', 'head_number': 2, 'head_hash': blocks[-1]['hash'][2:], 'counters': [2, 2], 'open_observations': [],
            'admission_records': {'counter': {'next': 2}, 'lowwater': {'sequence': 2}, 'purged': {'sequence': 2}},
            'decision_records': records, 'obligations': obligations}, blocks, genesis


class BoundaryControls(unittest.TestCase):
    def test_hash_shape_and_normalization(self):
        self.assertEqual(j.normalized('0x' + 'A' * 64), 'a' * 64)
        for value in (None, 123, '', '0x' + 'a' * 63, 'g' * 64):
            with self.subTest(value=value), self.assertRaises(ValueError): j.normalized(value)

    def test_genesis_all_fork_and_allocation_guards(self):
        genesis, contracts = genesis_fixture()
        with mock.patch.object(j, 'provenance', return_value=contracts):
            j.check_genesis(genesis, genesis['config'])
            cases = []
            for key in genesis['config']:
                bad = copy.deepcopy(genesis); bad['config'][key] += 1; cases.append(bad)
            for key in ('timestamp', 'gasLimit', 'difficulty'):
                bad = copy.deepcopy(genesis); bad[key] += 1; cases.append(bad)
            for offset in (0, 32, 33, 101):
                bad = copy.deepcopy(genesis); raw = bytearray.fromhex(bad['extraData'][2:]); raw[offset] ^= 1; bad['extraData'] = raw.hex(); cases.append(bad)
            for address in genesis['alloc']:
                for field, value in (('balance', '1'), ('nonce', '1'), ('storage', {'0x00': '0x01'}), ('code', '0xff')):
                    bad = copy.deepcopy(genesis); bad['alloc'][address][field] = value; cases.append(bad)
            bad = copy.deepcopy(genesis); bad['alloc']['f' * 40] = {'balance': '0'}; cases.append(bad)
            for index, bad in enumerate(cases):
                with self.subTest(index=index), self.assertRaises(ValueError): j.check_genesis(bad, bad['config'])
            bad_config = copy.deepcopy(genesis['config']); bad_config['chainId'] = 1
            with self.assertRaises(ValueError): j.check_genesis(genesis, bad_config)

    def test_receipt_topics_must_be_a_list_even_when_empty(self):
        for kind in range(5):
            row, block, fields = receipt_fixture(kind)
            fields[3][0][1] = []
            row['receipt']['logs'][0]['topics'] = []
            prefix = bytes([kind]) if kind else b''
            row['stored_receipt_wire'] = row['observed_receipt_wire'] = (prefix + j.wire.encode(fields)).hex()
            self.assertEqual(j.check_receipt(row, 15000, 1, block), 45000)
            # RLP empty bytes and empty list differ although both iterate empty.
            fields[3][0][1] = b''
            row['stored_receipt_wire'] = row['observed_receipt_wire'] = (prefix + j.wire.encode(fields)).hex()
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                j.check_receipt(row, 15000, 1, block)

    def test_receipt_context_status_logs_and_canonical_wire(self):
        for kind in range(5):
            row, block, fields = receipt_fixture(kind)
            self.assertEqual(j.check_receipt(row, 15000, 1, block), 45000)
            changes = [('status', 0), ('cumulativeGasUsed', 45001), ('gasUsed', 29999), ('logsBloom', '0x' + '11' * 256),
                       ('logs', []), ('transactionIndex', 0), ('blockNumber', 8), ('blockHash', '0x' + '4' * 64)]
            for key, value in changes:
                bad = copy.deepcopy(row); bad['receipt'][key] = value
                with self.subTest(kind=kind, key=key), self.assertRaises(ValueError): j.check_receipt(bad, 15000, 1, block)
            for key, value in (('address', j.BURN), ('topics', []), ('data', '0xff')):
                bad = copy.deepcopy(row); bad['receipt']['logs'][0][key] = value
                with self.subTest(kind=kind, key=key), self.assertRaises(ValueError): j.check_receipt(bad, 15000, 1, block)
            malformed = [fields[:3], fields + [b''], [1, 45000, bytes(255), fields[3]], [1, 45000, bytes(256), [[bytes(20), [], b'', b'']]],
                         [1, 45000, bytes(256), [[bytes(20), [bytes(31)], b'']]]]
            for changed in malformed:
                bad = copy.deepcopy(row); raw = (bytes([kind]) if kind else b'') + j.wire.encode(changed)
                bad['stored_receipt_wire'] = bad['observed_receipt_wire'] = raw.hex()
                with self.assertRaises(ValueError): j.check_receipt(bad, 15000, 1, block)
            bad = copy.deepcopy(row); bad['observed_receipt_wire'] += '00'
            with self.assertRaises(ValueError): j.check_receipt(bad, 15000, 1, block)
            if kind:
                bad = copy.deepcopy(row); raw = bytes([5]) + bytes.fromhex(bad['stored_receipt_wire'])[1:]
                bad['stored_receipt_wire'] = bad['observed_receipt_wire'] = raw.hex()
                with self.assertRaises(ValueError): j.check_receipt(bad, 15000, 1, block)

    def test_bridge_frontiers_digests_and_members(self):
        bridge, blocks, genesis = bridge_fixture(); j.check_bridge(bridge, blocks, genesis)
        cases = []
        for key, value in (('mode', 'REPLAY'), ('head_number', 1), ('head_hash', '0' * 64), ('counters', [1, 2]),
                           ('open_observations', ['pending']), ('admission_records', {}), ('obligations', [])):
            bad = copy.deepcopy(bridge); bad[key] = value; cases.append(bad)
        for index in range(2):
            key = f'{index:016x}/intent'
            for field, value in (('native_store_id', 'f' * 32), ('producer', 'other'), ('sequence', True), ('kind', 'REORG'),
                                 ('events', False), ('old_head', {}), ('new_head', {}), ('digest', '0' * 64), ('members', [])):
                bad = copy.deepcopy(bridge); bad['decision_records'][key][field] = value; cases.append(bad)
            for field, value in (('observation', 'wrong'), ('block', '0' * 64), ('child_root', '0' * 64)):
                bad = copy.deepcopy(bridge); bad['decision_records'][key]['members'][0][field] = value; cases.append(bad)
            bad = copy.deepcopy(bridge); bad['decision_records'][key]['unexpected'] = 1; cases.append(bad)
            bad = copy.deepcopy(bridge); bad['decision_records'][key]['members'][0]['unexpected'] = 1; cases.append(bad)
            bad = copy.deepcopy(bridge); bad['decision_records'][f'{index:016x}/decided']['digest'] = '0' * 64; cases.append(bad)
        bad = copy.deepcopy(bridge); bad['decision_records']['counter']['next'] = 3; cases.append(bad)
        bad = copy.deepcopy(bridge); bad['decision_records']['unexpected'] = {}; cases.append(bad)
        # Keep dependent digests/frontiers coherent while substituting identity
        # or member data, so rejection cannot be credited to stale hashes.
        for field, value in (('native_store_id', 'f' * 32), ('producer', 'other'), ('sequence', 7), ('kind', 'REORG'), ('events', False)):
            bad = copy.deepcopy(bridge); intent = bad['decision_records']['0000000000000000/intent']; intent[field] = value
            intent['digest'] = j.encoding.intent_digest(intent)
            decided = {'sequence': 0, 'intent_digest': intent['digest'], 'delivery': 'BATCH', 'digest': j.encoding.decided_digest(intent['digest'], 'BATCH')}
            bad['decision_records']['0000000000000000/decided'] = decided
            bad['obligations'][0] = {'sequence': 0, 'intent_digest': intent['digest'], 'decided_digest': decided['digest']}
            cases.append(bad)
        for field, value in (('observation', 'wrong'), ('block', '0' * 64), ('child_root', '0' * 64)):
            bad = copy.deepcopy(bridge); intent = bad['decision_records']['0000000000000000/intent']; intent['members'][0][field] = value
            intent['digest'] = j.encoding.intent_digest(intent)
            decided = {'sequence': 0, 'intent_digest': intent['digest'], 'delivery': 'BATCH', 'digest': j.encoding.decided_digest(intent['digest'], 'BATCH')}
            bad['decision_records']['0000000000000000/decided'] = decided
            bad['obligations'][0] = {'sequence': 0, 'intent_digest': intent['digest'], 'decided_digest': decided['digest']}
            cases.append(bad)
        bad = copy.deepcopy(bridge); intent = bad['decision_records']['0000000000000000/intent']; intent['digest'] = '0' * 64
        decided = {'sequence': 0, 'intent_digest': intent['digest'], 'delivery': 'BATCH', 'digest': j.encoding.decided_digest(intent['digest'], 'BATCH')}
        bad['decision_records']['0000000000000000/decided'] = decided
        bad['obligations'][0] = {'sequence': 0, 'intent_digest': intent['digest'], 'decided_digest': decided['digest']}; cases.append(bad)
        bad = copy.deepcopy(bridge); bad['decision_records']['0000000000000000/decided']['digest'] = '0' * 64
        bad['obligations'][0]['decided_digest'] = '0' * 64; cases.append(bad)
        for index, bad in enumerate(cases):
            with self.subTest(index=index), self.assertRaises(ValueError): j.check_bridge(bad, blocks, genesis)


class ImportControlTests(unittest.TestCase):
    def report(self):
        return {'config': {'cancunTime': j.GENESIS_TIME + 6},
                'activation_import_controls': {'number': 1, 'time': j.GENESIS_TIME + 3, 'tx_type': 3,
                                               'wire': '7fc0', 'error': 'data blobs present in block body',
                                               'unsupported': False, 'head_after': 0},
                'unknown_import_control': {'wire': '7fc0', 'error': 'transaction type not supported',
                                           'unsupported': True}}

    def test_the_two_refusals_pass(self):
        j.check_import_controls(self.report())

    def test_a_typed_refusal_with_the_flag_also_passes(self):
        report = self.report()
        report['activation_import_controls'].update(unsupported=True, error='transaction type not supported')
        j.check_import_controls(report)

    def refusing(self, change):
        report = self.report()
        change(report)
        with self.assertRaises((ValueError, KeyError)):
            j.check_import_controls(report)

    def test_the_activation_refusal_is_bound(self):
        self.refusing(lambda r: r['activation_import_controls'].update(error=''))
        self.refusing(lambda r: r['activation_import_controls'].update(error=None))
        self.refusing(lambda r: r['activation_import_controls'].update(error='some other refusal'))
        self.refusing(lambda r: r['activation_import_controls'].update(head_after=1))
        self.refusing(lambda r: r['activation_import_controls'].update(tx_type=2))
        self.refusing(lambda r: r['activation_import_controls'].update(number=2))

    def test_the_control_window_is_before_the_activation(self):
        self.refusing(lambda r: r['activation_import_controls'].update(time=j.GENESIS_TIME + 6))
        self.refusing(lambda r: r['config'].update(cancunTime=j.GENESIS_TIME + 3))

    def test_the_unknown_refusal_is_bound(self):
        self.refusing(lambda r: r['unknown_import_control'].update(unsupported=False))
        self.refusing(lambda r: r['unknown_import_control'].update(error=''))
        self.refusing(lambda r: r['unknown_import_control'].update(wire='02c0'))

    def test_missing_or_string_controls_refuse(self):
        self.refusing(lambda r: r.update(activation_import_controls='NOT_RUN'))
        self.refusing(lambda r: r.update(unknown_import_control='NOT_RUN'))
        self.refusing(lambda r: r.pop('activation_import_controls'))


if __name__ == '__main__': unittest.main()
