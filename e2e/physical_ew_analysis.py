"""Offline EW evidence analysis, with bounded memory and no device access.

Inputs are the SG trace, snapshot samples, application events and metadata JSON.
Outputs: observations/positions/events/commands CSV, summary JSON, optional PNG.
Each input is read to its size at invocation; an incomplete final JSONL record is
ignored and reported, allowing analysis while the producer is still running.
HP LTO-5 Host Interface Guide pp.148-150,225 defines position and EW semantics:
https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf
HP LTO-6 pp.64-65 defines the observed 0C layout (not a Z6ED accuracy guarantee):
https://docs.oracle.com/cd/E38452_01/en/LTO6_Vol3_E1_D20/LTO6_Vol3_E1_D20.pdf
"""

import argparse
from contextlib import ExitStack
import csv
import json
import math
from pathlib import Path
import re
import time

from physical_capacity_analysis import integer, parameters

NS = 10**9
OBS_FIELDS = ('source', 'line', 'metric', 'partition', 'value', 'status', 'start_ns', 'end_ns',
              'mono_start_ns', 'mono_end_ns', 'epoch', 'injected', 'continuity')
POSITION_FIELDS = ('line', 'start_ns', 'end_ns', 'format', 'partition', 'bop', 'eop', 'locu', 'bycu',
                   'lolu', 'perr', 'lonu', 'mpu', 'first_block', 'last_block', 'file_number', 'set_number',
                   'buffer_blocks', 'buffer_bytes', 'status', 'injected')
EVENT_FIELDS = ('source', 'line', 'start_ns', 'end_ns', 'kind', 'partition', 'detail')
COMMAND_FIELDS = ('second', 'commands', 'writes', 'requested_bytes', 'host_transferred_bytes',
                  'transfer_unknown', 'errors', 'ew_warnings', 'injected')
METRICS = {
    0x31: {1: ('remaining', 0, 2**20, 4), 2: ('remaining', 1, 2**20, 4),
           3: ('maximum', 0, 2**20, 4), 4: ('maximum', 1, 2**20, 4)},
    0x0C: {0: ('received', None, 1, 8), 1: ('written', None, 1, 8),
           4: ('eod', 'current', 10**6, 4), 5: ('ew', 'current', 10**6, 4),
           6: ('ew_to_eop_minimum', 'current', 10**6, 4),
           7: ('head', 'current', 10**6, 4), 8: ('buffer_allowed', 'current', 10**6, 4)},
}
COUNTERS = {'0c.received', '0c.written', 'accepted_bytes', 'reported_accepted_bytes', 'write_returned_total'}


def records(path, stats):
    if path is None:
        return
    with Path(path).open('rb') as handle:
        limit = stats.setdefault('size_bytes', handle.seek(0, 2))
        handle.seek(0)
        number = 0
        while handle.tell() < limit:
            raw = handle.readline(limit - handle.tell())
            number += 1
            if not raw.strip():
                continue
            try:
                row = json.loads(raw)
                if not isinstance(row, dict):
                    raise ValueError('Expected an object')
            except (ValueError, UnicodeDecodeError):
                key = 'partial_tail' if handle.tell() == limit and not raw.endswith(b'\n') else 'invalid_rows'
                stats[key] = stats.get(key, 0) + 1
                continue
            stats['rows'] = stats.get('rows', 0) + 1
            yield number, row


def times(row):
    return dict(start_ns=integer(row.get('request_start_ns', row.get('request_start_wall_ns'))),
                end_ns=integer(row.get('request_end_ns', row.get('request_end_wall_ns'))),
                mono_start_ns=integer(row.get('monotonic_start_ns', row.get('request_start_mono_ns'))),
                mono_end_ns=integer(row.get('monotonic_end_ns', row.get('request_end_mono_ns'))))


def valid_time(t):
    return t['start_ns'] is not None and t['end_ns'] is not None and t['end_ns'] >= t['start_ns']


def valid_monotonic(t):
    return (t['mono_start_ns'] is not None and t['mono_end_ns'] is not None
            and 0 <= t['mono_start_ns'] <= t['mono_end_ns'])


def first_boundary(summary, name, candidate):
    # Only one traced process establishes a shared monotonic domain. Otherwise
    # retain capture order and expose the uncertain ordering, not a wall-time guess.
    old = summary[name]
    if candidate['pid'] is None or not valid_monotonic(candidate):
        summary['inputs']['sg']['boundary_order_uncertain'] = True
    if old is None:
        summary[name] = candidate
    elif candidate['pid'] is not None and candidate['pid'] == old['pid'] and all(
            valid_monotonic(point) for point in (old, candidate)):
        if candidate['mono_end_ns'] < old['mono_end_ns']:
            summary['inputs']['sg']['boundary_order_uncertain'] = True
    else:
        summary['inputs']['sg']['boundary_order_uncertain'] = True


def unhex(value):
    try:
        return bytes.fromhex(value or '')
    except (ValueError, TypeError):
        return b''


def sense_fields(raw):
    """Separate current/deferred sense and residual validity from the EW signal."""
    data = unhex(raw)
    if len(data) < 8:
        return {}
    response = data[0] & 0x7f
    size = 8 + data[7]
    if len(data) < size:
        return {}
    if response in (0x70, 0x71) and size >= 14:
        return dict(response=response, deferred=response == 0x71, key=data[2] & 15,
                    asc=data[12], ascq=data[13], eom=bool(data[2] & 0x40),
                    information=int.from_bytes(data[3:7], 'big') if data[0] & 0x80 else None)
    if response not in (0x72, 0x73):
        return {}
    result = dict(response=response, deferred=response == 0x73, key=data[1] & 15,
                  asc=data[2], ascq=data[3], eom=False, information=None)
    offset = 8
    while offset + 2 <= size:
        length = data[offset + 1] + 2
        if offset + length > size:
            return {}
        descriptor = data[offset:offset + length]
        if descriptor[0] == 0 and length == 12 and descriptor[2] & 0x80:
            result['information'] = int.from_bytes(descriptor[4:12], 'big')
        if descriptor[0] == 4 and length == 4:
            result['eom'] = bool(descriptor[3] & 0x40)
        offset += length
    return result if offset == size else {}


def classify(row, cdb):
    sense = sense_fields(row.get('sense'))
    transport_ok = row.get('rc') == 0 and row.get('host_status') == 0 and row.get('driver_status') in (0, 8)
    # errno after a successful ioctl may be stale; rc is authoritative.
    check = row.get('status') == 2 and transport_ok
    boundary = check and sense.get('eom') and (sense.get('asc'), sense.get('ascq')) == (0, 2)
    writing = bool(cdb) and ((cdb[0] in (0x0a, 0x10) and len(cdb) >= 6)
                             or (cdb[0] in (0x8a, 0x80) and len(cdb) >= 16))
    if row.get('injected'):
        # Match the C observer's clean result and its complete short-position reply.
        clean = all(row.get(field) == 0 for field in (
            'rc', 'status', 'masked_status', 'msg_status', 'host_status', 'driver_status', 'sb_len_wr', 'resid'))
        info = integer(row.get('info'))
        complete = (info is not None and info & 1 == 0
                    and row.get('outputs_valid') is True and row.get('data_truncated') is False
                    and not row.get('sense') and row.get('direction') == -3
                    and row.get('dxfer_len') == 20 and len(unhex(row.get('data'))) == 20
                    and len(cdb) == 10 and cdb[:2] == b'\x34\x00')
        if not clean or not complete:
            return 'injected-error', sense
    if boundary and writing and not row.get('injected') and sense.get('key') == 0 and not sense.get('deferred'):
        information, resid = sense.get('information'), integer(row.get('resid'))
        # A clear warning remains a warning even when completion cannot be
        # established. "complete" describes logical command completion only.
        incomplete = (information is not None and information != 0) or (resid is not None and resid > 0)
        complete = information == 0 and resid == 0
        sense.update(completion_known=incomplete or complete, complete=complete)
        return 'hardware-ew', sense
    if boundary and writing and sense.get('key') == 13:
        return 'hard-eom', sense
    if boundary:
        return 'unconfirmed-boundary', sense
    if row.get('rc') != 0 or row.get('status') != 0 or row.get('host_status') != 0 or row.get('driver_status') != 0:
        return 'sg-error', sense
    return '', sense


def read_position(raw, service_action=0):
    """HP pp.149-150: positions count objects/filemarks, never physical bytes."""
    data = unhex(raw)
    size = {0: 20, 6: 32}.get(service_action)
    if size is None or len(data) < size:
        raise ValueError('unsupported-or-truncated-read-position')
    flags = data[0]
    if service_action == 6:
        lonu, mpu = bool(flags & 4), bool(flags & 8)
        return dict(format='long', bop=bool(flags & 128), eop=bool(flags & 64), lonu=lonu, mpu=mpu,
                    locu=None, bycu=None, lolu=None, perr=None,
                    partition=None if lonu else int.from_bytes(data[4:8], 'big'),
                    first_block=None if lonu else int.from_bytes(data[8:16], 'big'), last_block=None,
                    file_number=None if mpu else int.from_bytes(data[16:24], 'big'),
                    set_number=None if mpu else int.from_bytes(data[24:32], 'big'),
                    buffer_blocks=None, buffer_bytes=None)
    result = {name: bool(flags & bit) for name, bit in
              (('bop', 128), ('eop', 64), ('locu', 32), ('bycu', 16), ('lolu', 4), ('perr', 2))}
    known = not result['lolu'] and not result['perr']
    result.update(format='short', partition=data[1] if known else None,
                  first_block=int.from_bytes(data[4:8], 'big') if known else None,
                  last_block=int.from_bytes(data[8:12], 'big') if known else None,
                  buffer_blocks=None if result['locu'] else int.from_bytes(data[13:16], 'big'),
                  buffer_bytes=None if result['bycu'] else int.from_bytes(data[16:20], 'big'))
    return result


def partition_map(metadata):
    attrs = metadata.get('attributes') or {}
    raw = attrs.get('partitionMap', '') if isinstance(attrs, dict) else ''
    raw = metadata.get('partitionMap', raw)
    match = re.fullmatch(r'I:([ab01]),D:([ab01])', str(raw))
    if not match:
        return {'index': None, 'data': None}
    index, data = [int(value) if value.isdigit() else ord(value) - ord('a') for value in match.groups()]
    return {'index': index, 'data': data} if index != data else {'index': None, 'data': None}


class Observations:
    def __init__(self, writer, gap_seconds):
        self.writer, self.gap_ns, self.previous = writer, gap_seconds * NS, {}
        self.count, self.first_ns, self.last_ns = 0, None, None
        self.clock_discontinuities = 0

    def add(self, source, line, metric, value, t, partition=None, status='', epoch=0, injected=False):
        if partition not in (None, 0, 1):
            partition, status = None, status or 'unsupported-partition'
        status = status or ('' if value is not None else 'unknown')
        if not valid_time(t):
            status = 'invalid-time'
        point = dict(source=source, line=line, metric=metric, partition=partition, value=value,
                     status=status or 'observed', epoch=epoch, injected=injected, continuity='', **t)
        # Keys are bounded by the known metrics and the one-byte partition field.
        key = (source, metric, partition)
        old = self.previous.get(key)
        reason = 'first' if old is None else ''
        if old is not None:
            if old['epoch'] != epoch:
                reason = 'partition-or-owner-change'
            elif old['status'] != 'observed' or point['status'] != 'observed':
                reason = 'unknown-observation'
            elif t['start_ns'] <= old['end_ns']:
                reason = 'overlap-or-reversed-time'
            elif t['start_ns'] - old['end_ns'] > self.gap_ns:
                reason = 'sample-gap'
            elif metric in COUNTERS and value < old['value']:
                reason = 'counter-reset'
            if old['epoch'] == epoch and all(valid_monotonic(p) for p in (old, point)):
                wall = t['start_ns'] - old['start_ns'] if valid_time(t) and valid_time(old) else 0
                mono = t['mono_start_ns'] - old['mono_start_ns']
                if mono <= 0 or abs(wall - mono) > 1_000_000:
                    reason = 'clock-discontinuity'
                    self.clock_discontinuities += 1
        point['continuity'] = reason
        self.previous[key] = point
        self.writer.writerow(point)
        self.count += 1
        if valid_time(t):
            self.first_ns = t['start_ns'] if self.first_ns is None else min(self.first_ns, t['start_ns'])
            self.last_ns = t['end_ns'] if self.last_ns is None else max(self.last_ns, t['end_ns'])

    def page(self, source, line, page, raw, t, partition, epoch=0, status='', injected=False):
        try:
            data = unhex(raw)
            # SG allocation can exceed a page's declared size; ignore padding,
            # but retain the framing helper's truncated-page rejection.
            size = 4 + int.from_bytes(data[2:4], 'big') if len(data) >= 4 else 0
            decoded = parameters(data[:size].hex(), page)
        except (ValueError, TypeError):
            decoded, status = {}, status or 'invalid-page'
        values = {}
        for code, (metric, owner, scale, width) in METRICS[page].items():
            data = unhex(decoded.get(code, {}).get('raw'))
            reason = status
            value = None
            if len(data) != width:
                reason = reason or 'missing-or-wrong-width'
            elif all(byte == 255 for byte in data):
                reason = reason or 'all-ones-unknown'
            else:
                value = int.from_bytes(data, 'big') * scale
            part = partition if owner == 'current' else owner
            if owner == 'current' and part is None:
                reason = reason or 'unknown-partition'
            values[metric] = (value, reason)
            self.add(source, line, f'{page:02x}.{metric}', value, t, part, reason, epoch, injected)
        if page == 0x0c:
            eod, er = values['eod']
            ew, wr = values['ew']
            self.add(source, line, '0c.ew_remaining', ew - eod if ew is not None and eod is not None else None,
                     t, partition, er or wr, epoch, injected)


def event(writer, source, line, t, kind, detail, partition=None):
    writer.writerow(dict(source=source, line=line, start_ns=t['start_ns'], end_ns=t['end_ns'],
                         kind=kind, partition=partition, detail=json.dumps(detail, separators=(',', ':'))))


def sg_scan(path, obs, writers, summary, checkpoint):
    partition, epoch, pid, last_end, previous_time = None, 0, None, None, None
    last_mono_end = None
    aggregate = None
    stats = summary['inputs']['sg']
    for line, row in records(path, stats):
        if 'cdb' not in row:
            name = 'trace_init' if row.get('event') == 'init' else 'non_command_rows'
            stats[name] = stats.get(name, 0) + 1
            if name == 'trace_init' and row.get('ready') is not True:
                stats['failed_init'] = stats.get('failed_init', 0) + 1
            continue
        t, cdb = times(row), unhex(row.get('cdb'))
        kind, sense = classify(row, cdb)
        if not valid_time(t):
            stats['invalid_command_times'] = stats.get('invalid_command_times', 0) + 1
        start, end = t['start_ns'], t['end_ns']
        current_pid = row.get('pid')
        same_clock = current_pid is not None and pid == current_pid and previous_time is not None
        monotonic = same_clock and valid_monotonic(t) and valid_monotonic(previous_time)
        if monotonic and valid_time(t) and valid_time(previous_time):
            wall = start - previous_time['start_ns']
            mono = t['mono_start_ns'] - previous_time['mono_start_ns']
            if abs(wall - mono) > 1_000_000:
                stats['clock_discontinuities'] = stats.get('clock_discontinuities', 0) + 1
        # Do not let a wall-clock step manufacture overlapping SG operations.
        overlap = monotonic and last_mono_end is not None and t['mono_start_ns'] < last_mono_end
        if not monotonic:
            overlap = same_clock and valid_time(t) and last_end is not None and start < last_end
        if pid != current_pid or overlap or not valid_time(t):
            partition, epoch = None, epoch + 1
        if pid != current_pid:
            last_end, last_mono_end = None, None
        pid = current_pid
        if valid_time(t):
            last_end = end if last_end is None else max(last_end, end)
        if valid_monotonic(t):
            last_mono_end = t['mono_end_ns'] if last_mono_end is None else max(last_mono_end, t['mono_end_ns'])
        previous_time = t
        if kind:
            event(writers['events'], 'sg', line, t, kind, {**row, 'decoded_sense': sense}, partition)
            counts = summary['sg_events']
            counts[kind] = counts.get(kind, 0) + 1
        if kind == 'hardware-ew' and valid_time(t):
            candidate = dict(line=line, partition=partition, epoch=epoch, pid=pid, **t, sense=sense, cdb=cdb.hex(),
                             completion_known=sense['completion_known'], complete=sense['complete'])
            first_boundary(summary, 'first_ew', candidate)
        if kind == 'hard-eom' and valid_time(t):
            first_boundary(summary, 'first_hard_eom', dict(line=line, partition=partition, pid=pid, **t, sense=sense))
        if kind == 'injected-error' and valid_time(t):
            candidate = dict(line=line, partition=partition, pid=pid, **t, sense=sense, cdb=cdb.hex())
            for name in ('first_observer_error', 'first_observer_boundary'):
                if name == 'first_observer_boundary' and not (sense.get('eom') and (sense.get('asc'), sense.get('ascq')) == (0, 2)):
                    continue
                first_boundary(summary, name, candidate)
        injected = bool(row.get('injected'))
        ok = not kind
        transfer = integer(row.get('dxfer_len'))
        resid = integer(row.get('resid'))
        payload = unhex(row.get('data'))
        if transfer is not None and resid is not None and 0 <= resid <= transfer:
            payload = payload[:transfer - resid]
        else:
            payload = b''
        if cdb and cdb[0] == 0x34 and len(cdb) >= 10 and cdb[1] & 31 in (0, 6):
            try:
                pos = read_position(payload.hex(), cdb[1] & 31)
                reason = '' if ok else 'sg-error'
                if overlap:
                    reason = 'overlapping-commands'
                if not reason:
                    if partition != pos['partition']:
                        epoch += 1
                    partition = pos['partition']
                writers['positions'].writerow(dict(line=line, start_ns=start, end_ns=end, **pos,
                                                   status=reason or 'observed', injected=injected))
                for metric, field in (('logical_position', 'first_block'), ('last_logical_position', 'last_block'), ('eop', 'eop'),
                                      ('buffer_bytes', 'buffer_bytes'), ('buffer_blocks', 'buffer_blocks')):
                    value = pos[field]
                    obs.add('sg', line, metric, int(value) if value is not None else None, t,
                            pos['partition'], reason, epoch, injected)
            except ValueError:
                partition, epoch = None, epoch + 1
                event(writers['events'], 'sg', line, t, 'invalid-position', row)
        elif cdb and cdb[0] == 0x4d and len(cdb) >= 10 and cdb[2] & 63 in METRICS:
            page = cdb[2] & 63
            # Only current cumulative values, not thresholds/defaults/subpages.
            reason = '' if ok else 'sg-error'
            if cdb[2] >> 6 != 1 or cdb[3] != 0:
                reason = 'unsupported-page-control'
            obs.page('sg', line, page, payload.hex(), t, partition, epoch, reason, injected)
        elif cdb and cdb[0] not in (0x00, 0x03, 0x0a, 0x10, 0x12, 0x1a, 0x4d, 0x5a, 0x8a, 0x80):
            # LOCATE, LOAD, REWIND, SPACE, MODE SELECT, reset-like or unknown commands
            # invalidate attribution until another successful READ POSITION.
            partition, epoch = None, epoch + 1
        if not ok and kind not in ('hardware-ew', 'hard-eom'):
            partition, epoch = None, epoch + 1
        if valid_time(t):
            second = start // NS
            if aggregate is None or second != aggregate['second']:
                if aggregate is not None:
                    writers['commands'].writerow(aggregate)
                aggregate = dict.fromkeys(COMMAND_FIELDS, 0)
                aggregate['second'] = second
            aggregate['commands'] += 1
            aggregate['injected'] += int(injected)
            aggregate['errors'] += int(bool(kind) and kind != 'hardware-ew')
            aggregate['ew_warnings'] += int(kind == 'hardware-ew')
            if cdb and cdb[0] in (0x0a, 0x8a) and not injected:
                aggregate['writes'] += 1
                aggregate['requested_bytes'] += max(0, transfer or 0)
                if row.get('rc') == 0 and row.get('host_status') == 0 and transfer is not None and resid is not None and 0 <= resid <= transfer:
                    aggregate['host_transferred_bytes'] += transfer - resid
                else:
                    aggregate['transfer_unknown'] += 1
        if line % 100000 == 0:
            checkpoint()
    if aggregate is not None:
        writers['commands'].writerow(aggregate)


def sample_scan(path, obs, summary):
    epoch, owner = 0, None
    for line, row in records(path, summary['inputs']['samples']):
        sample = row.get('sample', row.get('response', row)) or {}
        t = times(row)
        if not valid_time(t):
            t = times(sample)
        current_owner = (sample.get('pid'), sample.get('mount'))
        if current_owner != owner:
            epoch += 1
        owner = current_owner
        status = '' if sample.get('status') == 'mounted' else 'not-mounted'
        if row.get('error') or row.get('timed_out') or row.get('exit_code', 0) != 0:
            status = 'sample-error'
        # A snapshot does not prove its sequential page reads used one partition.
        # The raw SG stream supplies partition-attributed page observations.
        pages = sample.get('pages') or {}
        for name, page in (('31', 0x31), ('0C', 0x0c)):
            reading = pages.get(name, pages.get(name.lower(), {})) or {}
            rt = times(reading)
            if not valid_time(rt):
                rt = t
            if reading:
                obs.page('samples', line, page, reading.get('raw', ''), rt, None, epoch,
                         status or ('read-error' if reading.get('error') else ''))
        space = sample.get('statvfs', sample.get('statfs', {})) or {}
        if not space:
            continue
        st = times(space)
        if not valid_time(st):
            # These bounds cover statvfs alone, not the enclosing snapshot.
            # That snapshot's monotonic bounds cannot diagnose this call's clock.
            st = dict(start_ns=integer(sample.get('statvfs_request_start_ns')),
                      end_ns=integer(sample.get('statvfs_request_end_ns')),
                      mono_start_ns=None, mono_end_ns=None)
        if not valid_time(st):
            st = t
        value = integer(space.get('available_bytes'))
        if value is None:
            block, available = integer(space.get('f_frsize')), integer(space.get('f_bavail'))
            value = block * available if block is not None and available is not None else None
        # A matched process can survive unmount; require evidence of successful
        # Tape page reads in this snapshot before attributing statfs to Tape.
        tape_read = any(reading.get('raw') and not reading.get('error') for reading in pages.values())
        reason = status or ('statfs-error' if space.get('error') else '') or ('' if tape_read else 'mount-unconfirmed')
        obs.add('samples', line, 'filesystem_available', value, st, summary['partition_map']['data'], reason, epoch)
        complete = not reason and value is not None and valid_time(t)
        for name, page in (('31', 0x31), ('0C', 0x0c)):
            reading = pages.get(name, pages.get(name.lower(), {})) or {}
            try:
                decoded = parameters(reading.get('raw', ''), page)
                required = range(1, 5) if page == 0x31 else range(4, 9)
                complete = complete and not reading.get('error') and all(
                    code in decoded and len(unhex(decoded[code]['raw'])) == 4
                    and decoded[code]['raw'] != 'ffffffff' for code in required)
            except (ValueError, TypeError):
                complete = False
        if complete:
            full = dict(line=line, **t, sample=sample)
            snapshots = summary['snapshots']
            if snapshots['first_full'] is None or t['start_ns'] < snapshots['first_full']['start_ns']:
                snapshots['first_full'] = full
            if snapshots['last_full'] is None or t['end_ns'] > snapshots['last_full']['end_ns']:
                snapshots['last_full'] = full
            # Snapshot/controller and SG clocks have no declared shared domain.
            # Keep the raw snapshots, without labeling one as certainly after EW.


def app_scan(path, obs, writers, summary, checkpoint):
    latest_end = -1
    written = 0
    for line, row in records(path, summary['inputs']['events']):
        t = times(row)
        if not valid_time(t) and integer(row.get('timestamp_ns')) is not None:
            t.update(start_ns=integer(row['timestamp_ns']), end_ns=integer(row['timestamp_ns']))
        kind = row.get('event', 'unknown')
        event(writers['events'], 'app', line, t, kind, row)
        if kind == 'write':
            returned = integer(row.get('returned_bytes'))
            requested = integer(row.get('requested_bytes'))
            if returned is not None and requested is not None and 0 <= returned <= requested:
                written += returned
                obs.add('app', line, 'write_returned_total', written, t, summary['partition_map']['data'])
            else:
                summary['application']['write_return_unknown'] += 1
        accepted = integer(row.get('total_accepted_bytes'))
        if accepted is not None:
            obs.add('app', line, 'reported_accepted_bytes', accepted, t, summary['partition_map']['data'],
                    'negative-counter' if accepted < 0 else '')
            if kind == 'write' and not row.get('error') and row.get('success', True):
                returned = integer(row.get('returned_bytes'))
                requested = integer(row.get('requested_bytes'))
                if returned is not None and requested is not None and 0 <= returned <= requested:
                    accepted += returned
            obs.add('app', line, 'accepted_bytes', accepted, t, summary['partition_map']['data'],
                    'negative-counter' if accepted < 0 else '')
        if valid_time(t) and t['end_ns'] >= latest_end:
            latest_end = t['end_ns']
            summary['application']['latest_event'] = row
            if accepted is not None:
                summary['application']['total_accepted_bytes'] = accepted
        if row.get('error') or row.get('errno'):
            summary['application']['error_events'] += 1
            if valid_time(t):
                error = dict(line=line, **t, event=kind, errno=row.get('errno'), error=row.get('error'),
                             file_index=row.get('file_index'), file_offset=row.get('file_offset'))
                for name, before in (('first_error', True), ('last_error', False)):
                    old = summary['application'][name]
                    if old is None or (t['start_ns'] < old['start_ns'] if before else t['end_ns'] > old['end_ns']):
                        summary['application'][name] = error
        if kind == 'final':
            summary['application']['final_seen'] = True
        if line % 100000 == 0:
            checkpoint()
    summary['application']['sum_write_returned_bytes'] = written


def nearest(path, ew, gap_seconds, clock_discontinuities):
    result = {}
    with path.open() as handle:
        for row in csv.DictReader(handle):
            key = f"{row['source']}.{row['metric']}.p{row['partition'] or '?'}"
            entry = result.setdefault(key, dict(before=None, after=None, overlapping=0, observed=0, unknown=0,
                                               uncertain=0))
            if row['status'] != 'observed':
                entry['unknown'] += 1
                continue
            entry['observed'] += 1
            if ew is None:
                continue
            start, end = int(row['start_ns']), int(row['end_ns'])
            point = {**row, 'start_ns': start, 'end_ns': end, 'value': int(row['value'])}
            point['same_ew_partition'] = ew['partition'] is not None and row['partition'] == str(ew['partition'])
            mono = {name: integer(row[name]) for name in ('mono_start_ns', 'mono_end_ns')}
            if row['source'] == 'sg':
                if (ew.get('pid') is None or integer(row['epoch']) != ew['epoch']
                        or not valid_monotonic(mono) or not valid_monotonic(ew)):
                    entry['uncertain'] += 1
                    entry['alignment'] = 'unestablished-clock-domain-or-discontinuity'
                    continue
                start, end = mono['mono_start_ns'], mono['mono_end_ns']
                ew_start, ew_end = ew['mono_start_ns'], ew['mono_end_ns']
                point['clock_basis'] = 'same-owner-monotonic'
            else:
                entry['uncertain'] += 1
                entry['alignment'] = 'unverified'
                if clock_discontinuities or not valid_time(point) or not valid_time(ew):
                    entry['alignment'] = 'clock-discontinuity-or-invalid-interval'
                    continue
                # Recorded wall times retain observational context, without
                # establishing synchronization with the traced SG process.
                ew_start, ew_end = ew['start_ns'], ew['end_ns']
                point.update(clock_basis='recorded-wall-time', alignment='unverified')
            if end < ew_start:
                if entry['before'] is None or ew_start - end < entry['before']['distance_ns']:
                    point['distance_ns'] = ew_start - end
                    point['within_gap_limit'] = point['distance_ns'] <= gap_seconds * NS
                    entry['before'] = point
            elif start > ew_end:
                if entry['after'] is None or start - ew_end < entry['after']['distance_ns']:
                    point['distance_ns'] = start - ew_end
                    point['within_gap_limit'] = point['distance_ns'] <= gap_seconds * NS
                    entry['after'] = point
            else:
                entry['overlapping'] += 1
    return result


def render(path, output, summary, boundary_window=False):
    if summary['capture_issues']['clock_discontinuities']:
        return 'unavailable: clock discontinuity prevents wall-clock alignment'
    try:
        import matplotlib
    except ImportError:
        return 'unavailable: matplotlib is not installed'
    matplotlib.use('Agg')
    from matplotlib import pyplot as plt
    origin, end = summary['observation_start_ns'], summary['observation_end_ns']
    if origin is None:
        return 'no timed observations'
    if boundary_window:
        if summary['first_ew'] is None:
            return 'no hardware EW observed'
        origin = summary['first_ew']['end_ns']
        start, end = origin - 120 * NS, origin + 30 * NS
    else:
        start = origin
    # Fixed 1024 bins per known series. Scatter envelopes never bridge gaps or
    # partition transitions, and keep extrema rather than smoothing EW away.
    width = max(1, (end - start + 1023) // 1024)
    groups = {'filesystem_available': 0, '31.remaining': 0, '0c.ew_remaining': 0,
              '0c.eod': 1, '0c.head': 1, 'logical_position': 2, 'last_logical_position': 2,
              '0c.received': 3, '0c.written': 3, 'accepted_bytes': 3, 'eop': 4}
    buckets = {}
    with path.open() as handle:
        for row in csv.DictReader(handle):
            if row['status'] != 'observed' or row['metric'] not in groups:
                continue
            left, stop = int(row['start_ns']), int(row['end_ns'])
            if stop < start or left > end:
                continue
            data_partition = summary['partition_map']['data']
            if data_partition is not None and row['partition'] not in ('', str(data_partition)):
                continue
            # Snapshots duplicate the raw SG pages; retain their statfs series.
            if row['source'] == 'samples' and row['metric'] != 'filesystem_available':
                continue
            key = (row['source'], row['metric'], row['partition'])
            index = max(0, min(1023, (left - start) // width))
            value = int(row['value'])
            point = ((left - origin + stop - origin) / (2 * NS), value, (stop - left) / (2 * NS))
            bucket = buckets.setdefault(key, {})
            low, high = bucket.get(index, (point, point))
            bucket[index] = (min(low, point, key=lambda p: p[1]), max(high, point, key=lambda p: p[1]))
    fig, axes = plt.subplots(5, 1, sharex=True, figsize=(13, 15))
    for (source, metric, partition), series in buckets.items():
        points = [p for pair in series.values() for p in pair]
        scale = 1 if metric in ('logical_position', 'last_logical_position', 'eop') else 10**9
        label = {'logical_position': 'First Block Location / long Block Number (objects)',
                 'last_logical_position': 'Last Block Location (short; HP documents same as First)'}.get(metric, metric)
        axes[groups[metric]].errorbar([p[0] for p in points], [p[1] / scale for p in points],
                                     xerr=[p[2] for p in points], fmt='.', markersize=2,
                                     label=f'{source} {label} P{partition or "?"}', alpha=.7)
    for axis, label in zip(axes, ('Remaining estimate (GB)', 'Native distance (GB)',
                                 'Logical objects', 'Byte counters (GB)', 'READ POSITION EOP')):
        axis.set_ylabel(label)
        axis.grid(alpha=.2)
        if axis.has_data():
            axis.legend(fontsize=7)
        for name, color in (('first_ew', 'orange'), ('first_hard_eom', 'red')):
            event_data = summary[name]
            if event_data:
                axis.axvspan((event_data['start_ns'] - origin) / NS,
                             (event_data['end_ns'] - origin) / NS, color=color, alpha=.25)
                axis.axvline((event_data['end_ns'] - origin) / NS, color=color, linewidth=.7)
    axes[0].set_title('Hardware EW ' + summary['ew_status'] + '; cross-source clock alignment unverified')
    reference = 'first WRITE EW return' if boundary_window else 'first observation'
    axes[-1].set_xlabel(f'Seconds from {reference}; horizontal bars are query bounds; no interpolation')
    axes[-1].set_xlim((start - origin) / NS, (end - origin) / NS)
    fig.tight_layout()
    fig.savefig(output, dpi=130)
    plt.close(fig)
    return 'written'


def analyze(sg, samples, events, metadata, output, plot=True, gap_seconds=5):
    if not math.isfinite(gap_seconds) or gap_seconds <= 0:
        raise ValueError('gap-seconds must be positive and finite')
    prefix = Path(output)
    paths = {name: Path(str(prefix) + '-' + name + '.csv') for name in ('observations', 'positions', 'events', 'commands')}
    summary_path, plot_path = Path(str(prefix) + '-summary.json'), Path(str(prefix) + '-curves.png')
    boundary_path = Path(str(prefix) + '-boundary.png')
    inputs = [Path(p).resolve() for p in (sg, samples, events, metadata) if p is not None]
    marker = Path(str(sg) + '.error') if sg is not None else None
    if marker is not None:
        inputs.append(marker.resolve())
    if any(p.resolve() in inputs for p in (*paths.values(), summary_path, plot_path, boundary_path)):
        raise ValueError('Outputs must not overwrite input evidence')
    meta = json.loads(Path(metadata).read_text()) if metadata else {}
    summary = dict(status='processing', ew_status='not reached in captured evidence', first_ew=None, first_hard_eom=None,
                   first_observer_error=None, first_observer_boundary=None,
                   partition_map=partition_map(meta), gap_seconds=gap_seconds,
                   inputs={name: {} for name in ('sg', 'samples', 'events')}, sg_events={},
                   application=dict(final_seen=False, error_events=0, total_accepted_bytes=None, write_return_unknown=0,
                                    first_error=None, last_error=None),
                   snapshots=dict(first_full=None, last_full=None, last_full_after_ew=None),
                   position_fields={
                       'logical_position': 'Short First Block Location: blocks and filemarks from BOM to current logical position; long Block Number: logical objects from BOP.',
                       'last_logical_position': 'Short Last Block Location, retained independently. HP documents it as equal to First Block Location; differing firmware values do not establish buffer occupancy.',
                       'buffer_bytes': 'Only the returned buffer-byte field when BYCU is clear; never derived from first/last position difference.',
                       'buffer_blocks': 'Only the returned buffer-block field when LOCU is clear; long format has neither buffer field.',
                   },
                   limits=['One trace must describe one drive/load with partitions 0/1; pid/tid alone cannot identify multiple devices.',
                           'EW signal means original current WRITE/WRITE FILEMARKS sense with EOM, NO SENSE, 00/02; residual validity is separate.',
                           'EW completion_known/complete describe logical command completion, not physical buffer drain or file durability.',
                           'Deferred sense and injected commands do not establish the first hardware EW.',
                           'Injected READ POSITION is a real observer query; its successful data is retained, and errors invalidate experiment acceptance.',
                           'Page31 EOP and page0C EW estimates have different boundaries; neither is ground truth.',
                           'READ POSITION counts logical objects; unknown buffer flags do not establish an empty buffer.',
                           'Host transferred bytes and application accepted bytes do not establish physical commitment.',
                           'accepted_bytes adds successful write returns to pre-write totals; reported_accepted_bytes preserves the original field.',
                           'write_returned_total sums observed returns and is not an inferred outstanding-buffer count.',
                           'Snapshot page0C partition is unknown; use SG observations with traced partition ownership.',
                           'SG EW neighbors require the same owner/epoch and valid monotonic brackets; non-SG neighbors use recorded wall time with unverified alignment and are suppressed on detected clock discontinuity.',
                           'Separate sample/application files do not establish a shared SG clock; last_full_after_ew is not inferred from wall timestamps.',
                           'Raw plots use wall-clock coordinates with unverified cross-source alignment; clock discontinuities disable plotting.',
                           'Counter differences are not buffer occupancy; compression and metadata affect comparisons.',
                           'No accuracy ranking or writable-capacity guarantee is inferred from one EW event.'],
                   artifacts={**{k: str(v) for k, v in paths.items()}, 'summary': str(summary_path)})
    prefix.parent.mkdir(parents=True, exist_ok=True)
    for name, path in (('sg', sg), ('samples', samples), ('events', events)):
        if path is not None:
            summary['inputs'][name]['size_bytes'] = Path(path).stat().st_size

    def checkpoint():
        summary['ew_status'] = 'observed' if summary['first_ew'] else 'not reached in captured evidence'
        if summary['first_ew'] is None and summary['sg_events'].get('hardware-ew'):
            summary['ew_status'] = 'observed-with-uncertain-time'
        elif summary['first_ew'] is None and any(summary.get('capture_issues', {}).values()):
            summary['ew_status'] = 'not established from incomplete evidence'
        summary['updated_ns'] = time.time_ns()
        summary_path.write_text(json.dumps(summary, indent=2, allow_nan=False) + '\n')

    checkpoint()
    with ExitStack() as stack:
        writers = {}
        for name, fields in (('observations', OBS_FIELDS), ('positions', POSITION_FIELDS),
                             ('events', EVENT_FIELDS), ('commands', COMMAND_FIELDS)):
            handle = stack.enter_context(paths[name].open('w', newline=''))
            writers[name] = csv.DictWriter(handle, fieldnames=fields)
            writers[name].writeheader()
        obs = Observations(writers['observations'], gap_seconds)
        sg_scan(sg, obs, writers, summary, checkpoint)
        checkpoint()
        sample_scan(samples, obs, summary)
        checkpoint()
        app_scan(events, obs, writers, summary, checkpoint)
        summary.update(observations=obs.count, observation_start_ns=obs.first_ns, observation_end_ns=obs.last_ns)
    summary['capture_issues'] = {
        'invalid_rows': sum(s.get('invalid_rows', 0) for s in summary['inputs'].values()),
        'partial_tails': sum(s.get('partial_tail', 0) for s in summary['inputs'].values()),
        'injected_errors': summary['sg_events'].get('injected-error', 0),
        'trace_error_marker': bool(marker is not None and marker.exists()),
        'trace_initialization': (summary['inputs']['sg'].get('trace_init', 0) != 1
                                 or bool(summary['inputs']['sg'].get('failed_init', 0))),
        'boundary_order_uncertain': summary['inputs']['sg'].get('boundary_order_uncertain', False),
        'invalid_command_times': summary['inputs']['sg'].get('invalid_command_times', 0),
        'clock_discontinuities': obs.clock_discontinuities + summary['inputs']['sg'].get('clock_discontinuities', 0),
    }
    summary['nearest'] = nearest(paths['observations'], summary['first_ew'], gap_seconds,
                                 summary['capture_issues']['clock_discontinuities'])
    checkpoint()
    summary['plot'] = render(paths['observations'], plot_path, summary) if plot else 'disabled'
    if summary['plot'] == 'written':
        summary['artifacts']['plot'] = str(plot_path)
    summary['boundary_plot'] = render(paths['observations'], boundary_path, summary, True) if plot else 'disabled'
    if summary['boundary_plot'] == 'written':
        summary['artifacts']['boundary_plot'] = str(boundary_path)
    summary['status'] = 'complete-with-issues' if any(summary['capture_issues'].values()) else 'complete'
    checkpoint()
    return summary


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sg', required=True, type=Path)
    parser.add_argument('--samples', type=Path)
    parser.add_argument('--events', type=Path)
    parser.add_argument('--metadata', type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--gap-seconds', type=float, default=5)
    parser.add_argument('--no-plot', action='store_true')
    args = parser.parse_args(argv)
    result = analyze(args.sg, args.samples, args.events, args.metadata, args.output, not args.no_plot, args.gap_seconds)
    print(json.dumps({'status': result['status'], 'ew_status': result['ew_status'], 'artifacts': result['artifacts']}))


if __name__ == '__main__':
    main()
