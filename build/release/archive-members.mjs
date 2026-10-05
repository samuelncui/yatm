// Read ordinary package members, including paths carried by tar metadata records.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { gunzipSync } from 'node:zlib';

export function checkMemberPath(name) {
  assert(name && !name.startsWith('/') && !/[\\\x00-\x1f]/.test(name)
    && name.split('/').every(part => part && part !== '.' && part !== '..'), `Unsafe archive path: ${name}`);
}

export function readArchive(archive) {
  const tar = gunzipSync(fs.readFileSync(archive));
  const files = new Map();
  const directories = new Set();
  let attributes = {};
  let longName;
  for (let offset = 0; offset + 512 <= tar.length;) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every(byte => byte === 0)) break;
    const field = (start, end) => header.subarray(start, end).toString().replace(/\0.*$/s, '');
    const size = Number.parseInt(field(124, 136).trim(), 8);
    assert(Number.isSafeInteger(size) && size >= 0 && offset + 512 + size <= tar.length, 'Invalid package tar size');
    const type = String.fromCharCode(header[156]);
    const data = tar.subarray(offset + 512, offset + 512 + size);
    offset += 512 + Math.ceil(size / 512) * 512;
    if (type === 'x' || type === 'g') {
      const metadata = {};
      for (let position = 0; position < data.length;) {
        const space = data.indexOf(32, position);
        const length = Number(data.subarray(position, space).toString());
        assert(space > position && Number.isSafeInteger(length) && length > space - position + 1
          && position + length <= data.length, 'Invalid PAX record');
        const record = data.subarray(space + 1, position + length - 1).toString();
        const equals = record.indexOf('=');
        assert(equals > 0, 'Invalid PAX attribute');
        const key = record.slice(0, equals);
        assert(!/(?:LIBARCHIVE|SCHILY)\.(?:xattr|acl)|sparse/i.test(key), 'Private filesystem metadata in archive');
        metadata[key] = record.slice(equals + 1);
        position += length;
      }
      assert(!('size' in metadata) && !('linkpath' in metadata), 'Unsupported PAX member');
      if (type === 'g') assert(!('path' in metadata), 'Unsupported global PAX path');
      else attributes = metadata;
      continue;
    }
    if (type === 'L') {
      longName = data.toString().replace(/\0.*$/s, '').replace(/\n$/, '');
      continue;
    }
    assert(['0', '\0', '5'].includes(type), 'Package archive must use ordinary files and directories');
    const prefix = field(257, 263).startsWith('ustar') ? field(345, 500) : '';
    let name = attributes.path || longName || (prefix ? `${prefix}/${field(0, 100)}` : field(0, 100));
    attributes = {};
    longName = undefined;
    name = name.replace(/^\.\//, '').replace(/\/$/, '');
    if (!name || name === '.') {
      assert.equal(type, '5', 'Invalid archive root');
      continue;
    }
    checkMemberPath(name);
    assert(!files.has(name) && !directories.has(name), `Duplicate archive member: ${name}`);
    if (type === '5') directories.add(name);
    else files.set(name, data);
  }
  return { files, directories };
}

export function checkDirectories({ files, directories }) {
  for (const directory of directories) {
    assert([...files.keys()].some(name => name.startsWith(`${directory}/`)), `Unexpected archive directory: ${directory}`);
    assert(!files.has(directory), `File/directory collision: ${directory}`);
  }
}
