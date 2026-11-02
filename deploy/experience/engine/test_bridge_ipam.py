"""Exact captured values; all bbolt/filesystem envelopes are synthetic.

The unchanged seven captured values have creator=2 and are accepted only inside
synthetic bbolt/filesystem envelopes. Creator=1 remains a compatibility vector.
"""
import ast
import base64
import copy
import hashlib
import ipaddress
import json
from pathlib import Path
import struct
import unittest

import store
from contract import DATA_ROOT, validate_store_inventory, validate_preflight, validate_preflight_v2
from test_contract import preflight, preflight_v2
from test_default_bridge_store import NETWORK_PATH, synthetic_bolt, scan_synthetic_default_store, update_record, saved_tree

FIXTURE = Path(__file__).parent / 'testdata/default-bridge-ipam-records.json'


def captured_tree():
    fixture = json.loads(FIXTURE.read_bytes())
    return {ast.literal_eval(bucket): {ast.literal_eval(key): base64.b64decode(record['base64'], validate=True)
            for key, record in records.items()} for bucket, records in fixture['tree'].items()}


def creator_one_counterfactual():
    """Counterfactual, not an actual observation: preserve every other byte."""
    tree = captured_tree()
    key = next(key for key in tree[b'libnetwork'] if b'/v1.0/bridge/' in key)
    original = tree[b'libnetwork'][key]
    assert original.count(b'"BridgeIfaceCreator":2') == 1
    tree[b'libnetwork'][key] = original.replace(b'"BridgeIfaceCreator":2', b'"BridgeIfaceCreator":1')
    return tree


def request(tree, change):
    def mutate(value):
        config = json.loads(value['ipamV4Config'])
        change(config)
        value['ipamV4Config'] = json.dumps(config)
    update_record(tree, 'network', mutate)


def allocation(tree, pool, gateway):
    def mutate(value):
        info = json.loads(value['ipamV4Info'])
        data = json.loads(info[0]['IPAMData'])
        data.update(Pool=pool, Gateway=gateway)
        info[0].update(IPAMData=json.dumps(data), PoolID='LocalDefault/' + pool)
        value['ipamV4Info'] = json.dumps(info)
    update_record(tree, 'network', mutate)
    update_record(tree, 'bridge', lambda value: value.update(AddressIPv4=gateway))


class BridgeIPAMTests(unittest.TestCase):
    def assert_rejected(self, tree, reason=None):
        for parse in (lambda: store._network_metadata_v2(tree),
                      lambda: store._classify_v4(NETWORK_PATH, synthetic_bolt({}, tree)),
                      lambda: scan_synthetic_default_store(tree)):
            with self.assertRaisesRegex(ValueError, reason or '.'):
                parse()

    def test_capture_provenance_all_seven_hashes_and_exact_creator(self):
        fixture = json.loads(FIXTURE.read_bytes())
        provenance = fixture['provenance']
        self.assertEqual(provenance['schema'], 'switchback.bridge_ipam.captured_tree.v1')
        wire = (json.dumps(fixture['tree'], sort_keys=True, separators=(',', ':')) + '\n').encode()
        self.assertEqual(hashlib.sha256(wire).hexdigest(), provenance['wire_sha256'])
        tree = captured_tree()
        self.assertEqual(len(tree[b'libnetwork']), 7)
        self.assertEqual(len(provenance['records']), 7)
        expected = {base64.b64decode(item['key_base64'], validate=True): item for item in provenance['records']}
        self.assertEqual(set(expected), set(tree[b'libnetwork']))
        for key, raw in tree[b'libnetwork'].items():
            self.assertEqual(len(raw), expected[key]['bytes'])
            self.assertEqual(hashlib.sha256(raw).hexdigest(), expected[key]['sha256'])
            record = fixture['tree']["b'libnetwork'"][repr(key)]
            self.assertEqual(record['bytes'], len(raw))
            self.assertEqual(record['sha256'], 'sha256:' + hashlib.sha256(raw).hexdigest())
        driver = json.loads(next(raw for key, raw in tree[b'libnetwork'].items() if b'/bridge/' in key)[8:])
        self.assertIs(type(driver['BridgeIfaceCreator']), int)
        self.assertEqual(driver['BridgeIfaceCreator'], 2)
        transformed = creator_one_counterfactual()
        changed = [key for key in expected if tree[b'libnetwork'][key] != transformed[b'libnetwork'][key]]
        self.assertEqual(len(changed), 1)
        self.assertEqual(sum(a != b for a, b in zip(tree[b'libnetwork'][changed[0]], transformed[b'libnetwork'][changed[0]])), 1)

    def test_creator_one_compatibility_accepts_matching_explicit_request(self):
        tree = creator_one_counterfactual()
        self.assertIs(store._network_metadata_v2(tree), True)
        self.assertEqual(store._decode_bbolt(synthetic_bolt({}, tree)), tree)
        self.assertEqual(store._classify_v4(NETWORK_PATH, synthetic_bolt({}, tree)),
                         ('docker_default_bridge_metadata', 'docker-29.9.0-network-bbolt-v2'))

    def test_unchanged_capture_accepts_all_seven_raw_values(self):
        tree = captured_tree()
        self.assertIs(store._network_metadata_v2(tree), True)
        self.assertEqual(store._decode_bbolt(synthetic_bolt({}, tree)), tree)
        self.assertEqual(store._classify_v4(NETWORK_PATH, synthetic_bolt({}, tree)),
                         ('docker_default_bridge_metadata', 'docker-29.9.0-network-bbolt-v2'))

    def test_explicit_and_legacy_requests_across_all_private_ranges(self):
        for pool in ('10.0.0.0/8', '10.20.0.0/16', '172.16.0.0/12', '172.17.0.0/16',
                     '192.168.0.0/16', '192.168.5.0/24'):
            network = ipaddress.IPv4Network(pool)
            gateway = str(network.network_address + 1)
            for explicit in (False, True):
                tree = creator_one_counterfactual()
                allocation(tree, pool, gateway + '/' + str(network.prefixlen))
                request(tree, lambda config: config[0].update(PreferredPool=pool if explicit else '', Gateway=gateway if explicit else ''))
                with self.subTest(pool=pool, explicit=explicit):
                    self.assertIs(store._network_metadata_v2(tree), True)
                    self.assertEqual(store._classify_v4(NETWORK_PATH, synthetic_bolt(tree))[0], 'docker_default_bridge_metadata')
        self.assertIs(store._network_metadata_v2(saved_tree()), True)
        with self.assertRaises(ValueError): store._network_metadata(creator_one_counterfactual())

    def test_half_empty_mismatch_noncanonical_and_nonprivate_requests_reject(self):
        changes = [{'PreferredPool': ''}, {'Gateway': ''}, {'PreferredPool': '172.18.0.0/16'},
            {'Gateway': '172.17.0.2'}, {'PreferredPool': '172.17.0.1/16'},
            {'PreferredPool': '172.17.0.0/255.255.0.0'}, {'Gateway': '172.17.0.1/16'},
            {'Gateway': '172.017.0.1'}, {'PreferredPool': '8.8.0.0/16'},
            {'PreferredPool': 'fd00::/64'}, {'Gateway': '::1'},
            {'SubPool': '172.17.1.0/24'}, {'AuxAddresses': {'x': '172.17.0.2'}}]
        for fields in changes:
            tree = creator_one_counterfactual(); request(tree, lambda config: config[0].update(fields))
            with self.subTest(fields=fields): self.assert_rejected(tree, 'unsupported default bridge IPAM config')

    def test_request_schema_missing_extra_wrong_types_and_duplicate_json_reject(self):
        for field in ('PreferredPool', 'Gateway', 'SubPool', 'AuxAddresses'):
            for invalid in (None, False, True, 0, 1, [], {}, 'x'):
                if field == 'AuxAddresses' and invalid == {}: continue
                tree = creator_one_counterfactual(); request(tree, lambda config: config[0].update({field: invalid}))
                with self.subTest(field=field, invalid=invalid): self.assert_rejected(tree)
            tree = creator_one_counterfactual(); request(tree, lambda config: config[0].pop(field))
            self.assert_rejected(tree)
        for payload in ('null', '{}', '[]', '[{},{}]', '[null]',
                        '[{"PreferredPool":"172.17.0.0/16","SubPool":"","Gateway":"172.17.0.1","AuxAddresses":{},"Gateway":"172.17.0.1"}]'):
            tree = creator_one_counterfactual(); update_record(tree, 'network', lambda value: value.update(ipamV4Config=payload))
            self.assert_rejected(tree)
        tree = creator_one_counterfactual(); request(tree, lambda config: config[0].update(extra=''))
        self.assert_rejected(tree)

    def test_consistent_requests_cannot_bypass_allocation_validation(self):
        for pool, gateway in (('8.8.0.0/16', '8.8.0.1/16'), ('100.64.0.0/10', '100.64.0.1/10'),
                ('fd00::/64', 'fd00::1/64'), ('172.17.0.1/16', '172.17.0.1/16'),
                ('172.17.0.0/255.255.0.0', '172.17.0.1/16'), ('172.17.0.0/16', '172.17.0.1/255.255.0.0'),
                ('172.17.0.0/16', '172.18.0.1/16'), ('172.17.0.0/16', '172.17.0.0/16'),
                ('172.17.0.0/16', '172.17.255.255/16')):
            tree = creator_one_counterfactual(); allocation(tree, pool, gateway)
            request(tree, lambda config: config[0].update(PreferredPool=pool, Gateway=gateway.split('/')[0]))
            with self.subTest(pool=pool, gateway=gateway): self.assert_rejected(tree)

    def test_record_completeness_counts_hidden_endpoints_and_driver_stay_strict(self):
        original = creator_one_counterfactual()
        for key in original[b'libnetwork']:
            tree = copy.deepcopy(original); del tree[b'libnetwork'][key]
            with self.subTest(missing=key): self.assert_rejected(tree)
        for count in (1, -1, False, 0.0, '0'):
            tree = copy.deepcopy(original); key = next(key for key in tree[b'libnetwork'] if b'/endpoint_count/' in key)
            tree[b'libnetwork'][key] = struct.pack('<Q', 1) + json.dumps({'Count': count}).encode()
            self.assert_rejected(tree)
        tree = copy.deepcopy(original)
        tree[b'libnetwork'][b'docker/network/v1.0/endpoint/' + b'a' * 64 + b'/'] = struct.pack('<Q', 1) + b'{}'
        self.assert_rejected(tree)
        for fields in ({'BridgeIfaceCreator': value} for value in (0, -1, 3, True, False, 1.0, 2.0, None, '1', '2', [], {})):
            tree = copy.deepcopy(original); update_record(tree, 'bridge', lambda value: value.update(fields))
            with self.subTest(fields=fields): self.assert_rejected(tree)
        for fields in (
                       {'BridgeName': 'custom0'}, {'DefaultBridge': False}, {'Mtu': True}, {'extra': False}):
            tree = copy.deepcopy(original); update_record(tree, 'bridge', lambda value: value.update(fields))
            with self.subTest(fields=fields): self.assert_rejected(tree)

    def test_creator_two_cannot_bypass_custom_incomplete_or_identity_checks(self):
        original = captured_tree()
        raw = next(raw for key, raw in original[b'libnetwork'].items() if b'/bridge/' in key)
        for field in json.loads(raw[8:]):
            tree = copy.deepcopy(original); update_record(tree, 'bridge', lambda value: value.pop(field))
            with self.subTest(missing_driver_field=field): self.assert_rejected(tree)
        for role, fields in (('network', {'name': 'custom'}), ('bridge', {'ID': 'a' * 64}),
                             ('bridge', {'BridgeName': 'custom0'}), ('bridge', {'DefaultBridge': False})):
            tree = copy.deepcopy(original); update_record(tree, role, lambda value: value.update(fields))
            with self.subTest(role=role, fields=fields): self.assert_rejected(tree)

    def test_check_interrupts_every_explicit_parse_checkpoint(self):
        tree = captured_tree(); checkpoints = []
        store._network_metadata_v2(tree, lambda: checkpoints.append(None))
        self.assertGreater(len(checkpoints), 10)
        for stop in range(1, len(checkpoints) + 1):
            calls = 0
            def check():
                nonlocal calls
                calls += 1
                if calls == stop: raise TimeoutError('explicit IPAM bounded deadline')
            with self.subTest(stop=stop), self.assertRaisesRegex(TimeoutError, 'bounded deadline'):
                store._network_metadata_v2(tree, check)

    def test_synthetic_scanner_dto_both_preflights_and_old_inventory_rejection(self):
        tree = captured_tree(); observations = []
        value = scan_synthetic_default_store(tree, observe=lambda *args: observations.append(args))
        validate_store_inventory(value)
        self.assertEqual(value['schema'], 'switchback.engine.store_inventory.v4')
        self.assertEqual([data['tree'] for kind, data in observations
            if kind == 'store-decoded-current-tree' and data['path'] == NETWORK_PATH], [tree])
        entry = next(item for item in value['entries'] if item['path'] == DATA_ROOT + '/' + NETWORK_PATH)
        self.assertEqual((entry['classification'], entry['format']),
                         ('docker_default_bridge_metadata', 'docker-29.9.0-network-bbolt-v2'))
        for factory, validator in ((preflight, validate_preflight), (preflight_v2, validate_preflight_v2)):
            facts = factory(); facts.update(store=copy.deepcopy(value), filesystem=copy.deepcopy(value['backing_filesystem']))
            validator(facts)
            facts['filesystem']['uuid'] = '00000000-0000-4000-8000-000000000005'
            with self.assertRaisesRegex(ValueError, 'backing filesystem'): validator(facts)
        paths = {DATA_ROOT, DATA_ROOT + '/network', DATA_ROOT + '/network/files', DATA_ROOT + '/' + NETWORK_PATH}
        for version in (1, 2, 3):
            legacy = copy.deepcopy(value); legacy['schema'] = 'switchback.engine.store_inventory.v%d' % version
            legacy['entries'] = [item for item in legacy['entries'] if item['path'] in paths]
            if version == 1: legacy.pop('backing_filesystem')
            for item in legacy['entries']:
                item['rule_revision'] = 'm1-engine-store-v%d' % version
                if item['classification'] == 'docker_default_bridge_metadata': item['format'] = 'docker-29.9.0-network-bbolt-v1'
            with self.subTest(version=version), self.assertRaises(ValueError): validate_store_inventory(legacy)
