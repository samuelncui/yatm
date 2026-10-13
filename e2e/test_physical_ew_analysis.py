"""Synthetic offline decoder and stream tests; no hardware or service access."""

import csv
import importlib.util
import json
from pathlib import Path
import tempfile
import tracemalloc
import unittest

import physical_ew_analysis as analysis

NS = 10**9
BASE = 1790000000000000000


def log_page(number, values):
    body = b''
    for code, value in values.items():
        width = 8 if number == 12 and code < 4 else 4
        body += code.to_bytes(2, 'big') + bytes((0x60, width)) + value.to_bytes(width, 'big')
    return (bytes((number, 0)) + len(body).to_bytes(2, 'big') + body).hex()


def position(partition=1, flags=0x30, block=17):
    data = bytearray(20)
    data[0], data[1] = flags, partition
    data[4:8] = block.to_bytes(4, 'big')
    data[8:12] = (block + 2).to_bytes(4, 'big')
    data[13:16] = (3).to_bytes(3, 'big')
    data[16:20] = (4096).to_bytes(4, 'big')
    return data.hex()


def sense(key=0, residual=0, valid=True, deferred=False, eom=True, ascq=2):
    data = bytearray(18)
    data[0] = (0x80 if valid else 0) | (0x71 if deferred else 0x70)
    data[2], data[7], data[13] = key | (0x40 if eom else 0), 10, ascq
    data[3:7] = residual.to_bytes(4, 'big')
    return data.hex()


def command(seconds, opcode=0x0a, data='', duration=.1, **extra):
    cdb = bytearray(10 if opcode in (0x34, 0x4d, 0x2b) else 6)
    cdb[0] = opcode
    if opcode == 0x4d:
        cdb[2] = 0x40 | bytes.fromhex(data)[0]
    row = dict(request_start_ns=BASE + round(seconds * NS), request_end_ns=BASE + round((seconds + duration) * NS),
               monotonic_start_ns=round(seconds * NS), monotonic_end_ns=round((seconds + duration) * NS),
               cdb=cdb.hex(), rc=0, errno=0, status=0, host_status=0, driver_status=0,
               resid=0, dxfer_len=len(bytes.fromhex(data)) if data else 1024,
               direction=-3 if data else -2, sense='', data=data, injected=False, pid=91, tid=92)
    row.update(masked_status=0, msg_status=0, sb_len_wr=0, info=0, outputs_valid=True, data_truncated=False)
    row.update(extra)
    return row


def ew(seconds=10, **extra):
    return command(seconds, sense=sense(), status=2, driver_status=8, **extra)


class EWAnalysisTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.prefix = self.root / 'result'
        self.sg = self.root / 'sg.jsonl'
        self.metadata = self.root / 'metadata.json'
        self.metadata.write_text(json.dumps({'attributes': {'partitionMap': 'I:a,D:b'}}))

    def write_rows(self, name, rows):
        path = self.root / name
        with path.open('w') as output:
            for row in rows:
                output.write(json.dumps(row) + '\n')
        return path

    def run_analysis(self, commands, samples=(), events=(), plot=False):
        self.write_rows('sg.jsonl', [{'event': 'init', 'ready': True}, *commands])
        return analysis.analyze(self.sg, self.write_rows('samples.jsonl', samples),
                                self.write_rows('events.jsonl', events), self.metadata, self.prefix, plot=plot)

    def rows(self, name):
        with Path(str(self.prefix) + '-' + name + '.csv').open() as handle:
            return list(csv.DictReader(handle))

    def test_current_write_ew_signal_separate_from_completion(self):
        cases = [
            (ew(), 'hardware-ew'),
            (ew(errno=28), 'hardware-ew'),  # errno can be stale on rc=0
            (command(10, sense=sense(key=13, residual=1024), status=2), 'hard-eom'),
            (command(10, sense=sense(deferred=True), status=2), 'unconfirmed-boundary'),
            (command(10, sense=sense(valid=False), status=2), 'hardware-ew'),
            (command(10, sense=sense(residual=1), status=2), 'hardware-ew'),
            (command(10, sense=sense(), status=0), ''),
            (ew(rc=-1, errno=28), 'sg-error'),
            (ew(host_status=7), 'sg-error'),
            (ew(injected=True), 'injected-error'),
            (command(10, opcode=0x34, sense=sense(), status=2), 'unconfirmed-boundary'),
            (command(10, sense=sense(ascq=7), status=2), 'sg-error'),
            (command(10, sense=sense(eom=False), status=2), 'sg-error'),
        ]
        for row, expected in cases:
            with self.subTest(expected=expected, row=row):
                self.assertEqual(analysis.classify(row, bytes.fromhex(row['cdb']))[0], expected)

    def test_ew_completion_and_injected_boundary_are_independent(self):
        for info, valid, resid, known, complete in ((0, True, 0, True, True),
                                                   (0, False, 0, False, False),
                                                   (5, True, 0, True, False),
                                                   (0, True, 5, True, False)):
            with self.subTest(info=info, valid=valid, resid=resid):
                row = command(10, sense=sense(residual=info, valid=valid), status=2, resid=resid)
                result = self.run_analysis([row])
                self.assertEqual(result['ew_status'], 'observed')
                self.assertEqual(result['first_ew']['completion_known'], known)
                self.assertEqual(result['first_ew']['complete'], complete)
        observer = command(1, 0x34, sense=sense(valid=False, deferred=True), status=2, injected=True)
        result = self.run_analysis([observer, ew(2)])
        self.assertEqual(result['first_observer_boundary']['start_ns'], BASE+NS)
        self.assertEqual(result['first_observer_error']['start_ns'], BASE+NS)
        self.assertEqual(result['first_ew']['start_ns'], BASE+2*NS)
        self.assertEqual(result['capture_issues']['injected_errors'], 1)

    def test_descriptor_sense_and_truncation(self):
        descriptor = bytes((0, 10, 0x80, 0)) + bytes(8) + bytes((4, 2, 0, 0x40))
        raw = (bytes((0x72, 0, 0, 2, 0, 0, 0, len(descriptor))) + descriptor).hex()
        row = command(1, status=2, sense=raw)
        self.assertEqual(analysis.classify(row, bytes.fromhex(row['cdb']))[0], 'hardware-ew')
        for raw in ('', 'zz', sense()[:20], raw[:-2]):
            self.assertEqual(analysis.sense_fields(raw), {})

    def test_read_position_flags_and_logical_objects(self):
        unknown = analysis.read_position(position(flags=0xf0))
        self.assertTrue(unknown['bop'])
        self.assertTrue(unknown['eop'])
        self.assertEqual(unknown['first_block'], 17)
        self.assertEqual(unknown['last_block'], 19)
        self.assertIsNone(unknown['buffer_bytes'])
        self.assertIsNone(unknown['buffer_blocks'])
        known = analysis.read_position(position(flags=0))
        self.assertEqual((known['buffer_blocks'], known['buffer_bytes']), (3, 4096))
        for flags in (4, 2):
            self.assertIsNone(analysis.read_position(position(flags=flags))['partition'])
        with self.assertRaises(ValueError):
            analysis.read_position('00' * 19)

    def test_long_position_preserves_64bit_objects_and_validity(self):
        data = bytearray(32)
        data[0] = 0xc0
        data[4:8] = (1).to_bytes(4, 'big')
        data[8:16] = (2**40+7).to_bytes(8, 'big')
        data[16:24] = (99).to_bytes(8, 'big')
        pos = analysis.read_position(data.hex(), 6)
        self.assertEqual(pos['first_block'], 2**40+7)
        self.assertEqual(pos['file_number'], 99)
        self.assertEqual(pos['partition'], 1)
        self.assertTrue(pos['eop'])
        self.assertTrue(pos['bop'])
        self.assertIsNone(pos['buffer_blocks'])
        self.assertIsNone(pos['buffer_bytes'])
        row = command(0, 0x34, data.hex(), cdb='34060000000000000000')
        result = self.run_analysis([row, ew(1)])
        self.assertEqual(result['first_ew']['partition'], 1)
        self.assertEqual(self.rows('positions')[0]['format'], 'long')
        data[0] |= 12
        unknown = analysis.read_position(data.hex(), 6)
        self.assertIsNone(unknown['partition'])
        self.assertIsNone(unknown['first_block'])
        self.assertIsNone(unknown['file_number'])
        with self.assertRaises(ValueError):
            analysis.read_position(data[:-1].hex(), 6)

    def test_live_short_positions_keep_both_locations_without_buffer_inference(self):
        raw = ('300100000000303000002ea80000000000000000',
               '300100000000325c000030d40000000000000000')
        expected = ((12336, 11944), (12892, 12500))
        result = self.run_analysis([command(index, 0x34, data, injected=True)
                                    for index, data in enumerate(raw)])
        positions = self.rows('positions')
        for row, (first, last) in zip(positions, expected):
            self.assertEqual((int(row['first_block']), int(row['last_block'])), (first, last))
            self.assertEqual(row['locu'], 'True')
            self.assertEqual(row['bycu'], 'True')
            self.assertEqual(row['buffer_blocks'], '')
            self.assertEqual(row['buffer_bytes'], '')
        observations = self.rows('observations')
        self.assertEqual([int(r['value']) for r in observations if r['metric'] == 'logical_position'], [12336, 12892])
        self.assertEqual([int(r['value']) for r in observations if r['metric'] == 'last_logical_position'], [11944, 12500])
        self.assertEqual(result['nearest']['sg.buffer_bytes.p1']['observed'], 0)
        self.assertEqual(result['nearest']['sg.last_logical_position.p1']['observed'], 2)

    def test_units_negative_distance_and_strict_neighbors(self):
        p31 = log_page(49, {1: 7, 2: 10, 3: 20, 4: 30})
        before = log_page(12, {0: 200, 1: 100, 4: 19, 5: 20, 6: 2, 7: 15, 8: 128})
        after = log_page(12, {4: 21, 5: 20})
        result = self.run_analysis([
            command(0, 0x34, position(), injected=True), command(1, 0x4d, p31),
            command(8, 0x4d, before), command(9, 0x4d, p31, duration=.2),
            ew(), command(11, 0x34, position(flags=0x70)), command(12, 0x4d, after),
        ])
        self.assertEqual(result['first_ew']['partition'], 1)
        self.assertEqual(result['nearest']['sg.31.remaining.p1']['before']['value'], 10 * 2**20)
        self.assertEqual(result['nearest']['sg.31.remaining.p1']['overlapping'], 0)
        self.assertEqual(result['nearest']['sg.0c.ew_remaining.p1']['before']['value'], 10**6)
        self.assertEqual(result['nearest']['sg.0c.ew_remaining.p1']['after']['value'], -10**6)
        self.assertEqual(self.rows('positions')[0]['injected'], 'True')
        self.assertEqual(result['sg_events'].get('injected-error', 0), 0)

    def test_partition_switch_gap_and_unknown_page(self):
        page = log_page(12, {4: 10, 5: 20})
        result = self.run_analysis([
            command(0, 0x34, position()), command(1, 0x4d, page),
            command(2, 0x2b), command(3, 0x4d, page), command(4, 0x34, position(0)),
            command(5, 0x4d, page), command(6, 0x34, position(1)), command(7, 0x4d, page),
            command(20, 0x4d, page), ew(21),
        ])
        self.assertEqual(result['first_ew']['partition'], 1)
        rows = [r for r in self.rows('observations') if r['metric'] == '0c.ew_remaining']
        self.assertEqual(rows[1]['status'], 'unknown-partition')
        self.assertEqual(rows[2]['partition'], '0')
        self.assertEqual(rows[3]['continuity'], 'partition-or-owner-change')
        self.assertEqual(rows[4]['continuity'], 'sample-gap')
        self.assertTrue(result['nearest']['sg.0c.ew_remaining.p1']['before']['same_ew_partition'])

    def test_all_ones_wrong_width_and_bounded_transfer(self):
        page = log_page(49, {1: 0xffffffff, 2: 4, 3: 10, 4: 20})
        self.run_analysis([command(1, 0x4d, page), command(2, 0x4d, page, resid=5)])
        values = self.rows('observations')
        self.assertEqual(values[0]['status'], 'all-ones-unknown')
        self.assertTrue(all(row['status'] == 'missing-or-wrong-width' or row['status'] == 'invalid-page' for row in values[4:]))

    def test_enospc_and_eop_flag_do_not_establish_hardware_ew(self):
        event = dict(event='write', request_start_ns=BASE, request_end_ns=BASE+1,
                     requested_bytes=200, total_accepted_bytes=100, errno=28, error='No space')
        result = self.run_analysis([command(1, 0x34, position(flags=0x70))], events=[event])
        self.assertIsNone(result['first_ew'])
        self.assertEqual(result['ew_status'], 'not reached in captured evidence')
        self.assertEqual(result['application']['error_events'], 1)
        self.assertEqual(result['application']['first_error']['start_ns'], BASE)
        self.assertEqual(result['application']['last_error']['end_ns'], BASE+1)
        self.assertEqual(result['nearest']['sg.eop.p1']['observed'], 1)

    def test_snapshot_wrapper_direct_and_unmounted_host_fs(self):
        page = log_page(49, {1: 2, 2: 3})
        snap = dict(status='mounted', pid=91, mount='/test', pages={'31': {'raw': page}},
                    request_start_ns=BASE+NS, request_end_ns=BASE+NS+10,
                    statvfs={'f_bavail': 4, 'f_frsize': 4096})
        wrapped = dict(source='drive', sample=snap, request_start_ns=BASE+2*NS, request_end_ns=BASE+2*NS+10)
        bad = {**snap, 'pages': {}, 'request_start_ns': BASE+3*NS, 'request_end_ns': BASE+3*NS+10}
        result = self.run_analysis([ew(4)], samples=[snap, wrapped, bad])
        entry = result['nearest']['samples.filesystem_available.p1']
        self.assertEqual(entry['before']['value'], 4 * 4096)
        self.assertEqual(entry['before']['clock_basis'], 'recorded-wall-time')
        self.assertEqual(entry['before']['alignment'], 'unverified')
        self.assertEqual(entry['alignment'], 'unverified')
        self.assertIsNone(entry['after'])
        self.assertEqual(entry['uncertain'], 2)
        self.assertEqual(entry['observed'], 2)
        self.assertEqual(entry['unknown'], 1)

    def test_application_prewrite_total_and_final_timestamp(self):
        events = [dict(event='write', request_start_ns=BASE, request_end_ns=BASE+10,
                       requested_bytes=100, returned_bytes=70, total_accepted_bytes=0),
                  dict(event='write', request_start_ns=BASE+20, request_end_ns=BASE+30,
                       requested_bytes=30, returned_bytes=30, total_accepted_bytes=70),
                  dict(event='final', timestamp_ns=BASE+40, total_accepted_bytes=100)]
        result = self.run_analysis([], events=events)
        self.assertEqual(result['application']['sum_write_returned_bytes'], 100)
        self.assertEqual(result['application']['total_accepted_bytes'], 100)
        self.assertTrue(result['application']['final_seen'])
        self.assertEqual(result['nearest']['app.write_returned_total.p1']['observed'], 2)
        rows = [r for r in self.rows('observations') if r['metric'] == 'accepted_bytes']
        self.assertEqual([int(r['value']) for r in rows], [70, 100, 100])

    def test_wall_neighbors_keep_labels_and_overlapping_intervals(self):
        events = [dict(event='sample', request_start_ns=BASE+start*NS,
                       request_end_ns=BASE+end*NS, total_accepted_bytes=value)
                  for start, end, value in ((7, 8, 1), (8, 9, 2), (9, 11, 3), (10, 10, 4),
                                            (12, 13, 5), (14, 15, 6), (16, 15, 7))]
        result = self.run_analysis([ew(10)], events=events)
        entry = result['nearest']['app.accepted_bytes.p1']
        self.assertEqual(entry['before']['value'], 2)
        self.assertEqual(entry['before']['distance_ns'], NS)
        self.assertEqual(entry['after']['value'], 5)
        self.assertEqual(entry['after']['distance_ns'], 1900000000)
        for point in (entry['before'], entry['after']):
            self.assertEqual(point['clock_basis'], 'recorded-wall-time')
            self.assertEqual(point['alignment'], 'unverified')
            self.assertTrue(point['within_gap_limit'])
        self.assertEqual(entry['overlapping'], 2)
        self.assertEqual(entry['unknown'], 1)
        self.assertEqual(entry['uncertain'], 6)

    def test_external_clock_discontinuity_suppresses_wall_neighbors(self):
        events = [dict(event='sample', request_start_ns=BASE+second*NS,
                       request_end_ns=BASE+second*NS+NS//10,
                       monotonic_start_ns=mono*NS, monotonic_end_ns=mono*NS+NS//10,
                       total_accepted_bytes=number)
                  for number, (second, mono) in enumerate(((8, 100), (12, 101)))]
        result = self.run_analysis([ew(10)], events=events)
        self.assertGreater(result['capture_issues']['clock_discontinuities'], 0)
        entry = result['nearest']['app.accepted_bytes.p1']
        self.assertIsNone(entry['before'])
        self.assertIsNone(entry['after'])
        self.assertEqual(entry['uncertain'], 2)
        self.assertIn('discontinuity', entry['alignment'])

    def test_complete_snapshots_do_not_inherit_sg_clock_domain(self):
        p31 = log_page(49, {1: 3, 2: 10, 3: 20, 4: 30})
        p0c = log_page(12, {4: 19, 5: 20, 6: 2, 7: 15, 8: 128})
        def snapshot(seconds):
            return dict(source='drive', request_start_ns=BASE+seconds*NS,
                        request_end_ns=BASE+seconds*NS+NS//10,
                        sample=dict(status='mounted', pid=91, mount='/test',
                                    pages={'31': {'raw': p31}, '0C': {'raw': p0c}},
                                    statvfs={'available_bytes': 1024}))
        samples = [snapshot(9), snapshot(10), snapshot(11), snapshot(12)]
        samples[-1]['sample']['pages']['0C']['error'] = 'failed'
        result = self.run_analysis([ew()], samples=samples)
        self.assertEqual(result['snapshots']['first_full']['line'], 1)
        self.assertIsNone(result['snapshots']['last_full_after_ew'])
        self.assertEqual(result['snapshots']['last_full']['line'], 3)

    def test_trace_init_invalid_tail_and_checkpoint(self):
        self.sg.write_text(json.dumps({'event': 'init'}) + '\nnot json\n' + json.dumps(ew()) + '\n{"partial":')
        result = analysis.analyze(self.sg, None, None, self.metadata, self.prefix, plot=False)
        self.assertEqual(result['inputs']['sg']['trace_init'], 1)
        self.assertEqual(result['inputs']['sg']['invalid_rows'], 1)
        self.assertEqual(result['inputs']['sg']['partial_tail'], 1)
        self.assertEqual(result['ew_status'], 'observed')
        self.assertEqual(json.loads(Path(str(self.prefix)+'-summary.json').read_text())['status'], 'complete-with-issues')
        self.assertTrue(result['capture_issues']['trace_initialization'])

    def test_injected_incomplete_or_unclean_reply_is_an_observer_failure(self):
        for changes in ({'resid': 1}, {'driver_status': 8}, {'masked_status': 1},
                        {'msg_status': 1}, {'sb_len_wr': 1}, {'info': 1},
                        {'outputs_valid': False}, {'data_truncated': True},
                        {'data': position()[:-2]}, {'sense': sense()}):
            with self.subTest(changes=changes):
                row = command(1, 0x34, position(), injected=True)
                row.update(changes)
                result = self.run_analysis([row, ew(2)])
                self.assertEqual(result['capture_issues']['injected_errors'], 1)
                self.assertEqual(result['first_observer_error']['start_ns'], BASE + NS)
                self.assertEqual(result['ew_status'], 'observed')
                self.assertEqual(result['status'], 'complete-with-issues')
                self.assertFalse(any(r['status'] == 'observed' for r in self.rows('positions')))

    def test_trace_marker_and_initialization_are_required_for_clean_analysis(self):
        marker = Path(str(self.sg) + '.error')
        for init, error, clean in ((None, False, False), (False, False, False),
                                   (True, True, False), (True, False, True)):
            with self.subTest(init=init, error=error):
                rows = [] if init is None else [{'event': 'init', 'ready': init}]
                self.write_rows('sg.jsonl', [*rows, command(1)])
                if error:
                    marker.write_text('injected READ POSITION failed\n')
                else:
                    marker.unlink(missing_ok=True)
                result = analysis.analyze(self.sg, None, None, None, self.prefix, plot=False)
                self.assertEqual(result['status'], 'complete' if clean else 'complete-with-issues')
                self.assertEqual(result['capture_issues']['trace_error_marker'], error)
                self.assertEqual(result['ew_status'], 'not reached in captured evidence' if clean
                                 else 'not established from incomplete evidence')
                if error:
                    self.assertEqual(marker.read_text(), 'injected READ POSITION failed\n')

    def test_backward_wall_steps_preserve_same_owner_ew_and_neighbor_order(self):
        page = log_page(49, {1: 1, 2: 2, 3: 3, 4: 4})
        rows = [command(0, 0x34, position()),
                command(2, 0x4d, page, request_start_ns=BASE+20*NS, request_end_ns=BASE+21*NS),
                ew(3, request_start_ns=BASE+10*NS, request_end_ns=BASE+11*NS),
                ew(4, request_start_ns=BASE+5*NS, request_end_ns=BASE+6*NS),
                command(5, 0x4d, page, request_start_ns=BASE+6*NS, request_end_ns=BASE+7*NS)]
        events = [dict(event='sample', request_start_ns=BASE+second*NS,
                       request_end_ns=BASE+second*NS+NS//10, total_accepted_bytes=1)
                  for second in (9, 12)]
        result = self.run_analysis(rows, events=events, plot=True)
        self.assertEqual(result['first_ew']['mono_start_ns'], 3*NS)
        neighbors = result['nearest']['sg.31.remaining.p1']
        self.assertEqual(int(neighbors['before']['mono_start_ns']), 2*NS)
        self.assertEqual(int(neighbors['after']['mono_start_ns']), 5*NS)
        self.assertEqual(neighbors['before']['clock_basis'], 'same-owner-monotonic')
        self.assertGreater(result['capture_issues']['clock_discontinuities'], 0)
        self.assertIn('clock discontinuity', result['plot'])
        self.assertIn('clock discontinuity', result['boundary_plot'])
        wall = result['nearest']['app.accepted_bytes.p1']
        self.assertIsNone(wall['before'])
        self.assertIsNone(wall['after'])
        self.assertEqual(wall['uncertain'], 2)

    def test_other_owner_and_external_monotonic_values_do_not_establish_order(self):
        sample = dict(status='mounted', pid=91, mount='/test',
                      request_start_ns=BASE+NS, request_end_ns=BASE+2*NS,
                      monotonic_start_ns=100*NS, monotonic_end_ns=101*NS,
                      pages={'31': {'raw': log_page(49, {1: 1, 2: 2, 3: 3, 4: 4})}},
                      statvfs={'available_bytes': 4096})
        result = self.run_analysis([ew(10), ew(2, pid=92)], samples=[sample])
        self.assertEqual(result['first_ew']['pid'], 91)
        self.assertTrue(result['capture_issues']['boundary_order_uncertain'])
        neighbors = result['nearest']['samples.filesystem_available.p1']
        self.assertEqual(neighbors['before']['start_ns'], BASE+NS)
        self.assertEqual(neighbors['before']['clock_basis'], 'recorded-wall-time')
        self.assertEqual(neighbors['before']['alignment'], 'unverified')
        self.assertIsNone(neighbors['after'])
        self.assertEqual(neighbors['uncertain'], 1)

    def test_reversed_monotonic_or_invalid_wall_time_cannot_report_clean_capture(self):
        result = self.run_analysis([ew(10), ew(2)])
        self.assertEqual(result['first_ew']['mono_start_ns'], 10*NS)
        self.assertTrue(result['capture_issues']['boundary_order_uncertain'])
        self.assertEqual(result['status'], 'complete-with-issues')
        result = self.run_analysis([ew(2, request_end_ns=BASE+NS)])
        self.assertEqual(result['ew_status'], 'observed-with-uncertain-time')
        self.assertIsNone(result['first_ew'])
        self.assertEqual(result['capture_issues']['invalid_command_times'], 1)
        self.assertEqual(result['status'], 'complete-with-issues')

    def test_overlap_and_missing_monotonic_time_leave_alignment_uncertain(self):
        page = log_page(49, {1: 1, 2: 2, 3: 3, 4: 4})
        for changes in ({'duration': 2}, {'monotonic_start_ns': None, 'monotonic_end_ns': None}):
            with self.subTest(changes=changes):
                result = self.run_analysis([command(0, 0x34, position()),
                                           command(9, 0x4d, page, **changes), ew(10)])
                neighbors = result['nearest']['sg.31.remaining.p1']
                self.assertIsNone(neighbors['before'])
                self.assertIsNone(neighbors['after'])
                self.assertEqual(neighbors['uncertain'], 1)

    def test_reset_and_clock_discontinuity(self):
        page = lambda n: log_page(12, {0: n, 1: n})
        rows = [command(0, 0x4d, page(10)), command(1, 0x4d, page(8)),
                command(2, 0x4d, page(12), monotonic_start_ns=3*NS, monotonic_end_ns=3*NS+100000000)]
        self.run_analysis(rows)
        points = [r for r in self.rows('observations') if r['metric'] == '0c.received']
        self.assertEqual(points[1]['continuity'], 'counter-reset')
        self.assertEqual(points[2]['continuity'], 'clock-discontinuity')

    def test_output_cannot_overwrite_input(self):
        self.sg.write_text('')
        with self.assertRaisesRegex(ValueError, 'overwrite'):
            analysis.analyze(self.sg, self.root / 'result-events.csv', None, None, self.prefix, plot=False)

    def test_successful_warning_is_separate_from_command_errors(self):
        self.run_analysis([ew(1), command(2, rc=-1, errno=5)])
        rows = self.rows('commands')
        self.assertEqual(sum(int(row['errors']) for row in rows), 1)
        self.assertEqual(sum(int(row['ew_warnings']) for row in rows), 1)

    def test_statfs_subrequest_does_not_borrow_snapshot_monotonic_bounds(self):
        samples = []
        for second, delay in ((0, .2), (2, .8)):
            samples.append(dict(request_start_ns=BASE+second*NS,
                                request_end_ns=BASE+(second+1)*NS,
                                monotonic_start_ns=second*NS,
                                monotonic_end_ns=(second+1)*NS,
                                sample=dict(status='mounted',
                                    statvfs_request_start_ns=BASE+round((second+delay)*NS),
                                    statvfs_request_end_ns=BASE+(second+1)*NS,
                                    statvfs={'available_bytes': 100},
                                    pages={'31': {'raw': log_page(49, {1: 1, 2: 2, 3: 3, 4: 4})}})))
        self.run_analysis([], samples=samples)
        rows = [row for row in self.rows('observations') if row['metric'] == 'filesystem_available']
        self.assertEqual(rows[1]['continuity'], '')
        self.assertEqual(rows[1]['mono_start_ns'], '')
        self.assertEqual(int(rows[1]['start_ns']), BASE+round(2.8*NS))

    def test_partition_map_requires_distinct_known_roles(self):
        self.assertEqual(analysis.partition_map({'partitionMap': 'I:b,D:a'}), {'index': 1, 'data': 0})
        for value in ('', 'I:a,D:a', 'I:x,D:b'):
            self.assertIsNone(analysis.partition_map({'partitionMap': value})['data'])

    def test_stream_memory_does_not_grow_with_command_count(self):
        # No capacity queries: command volume grows, per-second output streams.
        # A material regression to read-all/list(rows) would exceed this allowance.
        peaks = []
        for count in (200, 5000):
            self.write_rows('sg.jsonl', (command(i / 100) for i in range(count)))
            tracemalloc.start()
            try:
                analysis.analyze(self.sg, None, None, None, self.prefix, plot=False)
                peaks.append(tracemalloc.get_traced_memory()[1])
            finally:
                tracemalloc.stop()
        self.assertLess(peaks[1], peaks[0] + 1024 * 1024)
        self.assertEqual(sum(int(r['commands']) for r in self.rows('commands')), 5000)

    @unittest.skipUnless(importlib.util.find_spec('matplotlib'), 'optional matplotlib unavailable')
    def test_plot_and_empty_capture(self):
        result = self.run_analysis([], plot=True)
        self.assertEqual(result['plot'], 'no timed observations')
        result = self.run_analysis([command(0, 0x34, position()),
                                   command(1, 0x4d, log_page(49, {1: 1, 2: 2})), ew(2)], plot=True)
        self.assertEqual(result['plot'], 'written')
        self.assertGreater(Path(result['artifacts']['plot']).stat().st_size, 1000)
        self.assertEqual(result['boundary_plot'], 'written')
        self.assertGreater(Path(result['artifacts']['boundary_plot']).stat().st_size, 1000)
        result = self.run_analysis([command(0, 0x34, position())], plot=True)
        self.assertEqual(result['boundary_plot'], 'no hardware EW observed')


if __name__ == '__main__':
    unittest.main()
