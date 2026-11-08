"""Publish a bounded offline snapshot under an owned directory descriptor."""

import hashlib
import os
from pathlib import Path
import stat
import uuid

from artifacts import check_deadline, require


DIRECTORY_FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW


def open_directory(path):
    require(path.is_absolute() and ".." not in path.parts, "Unsafe freeze directory")
    descriptor = os.open(path.anchor, DIRECTORY_FLAGS)
    try:
        for part in path.parts[1:]:
            child = os.open(part, DIRECTORY_FLAGS, dir_fd=descriptor)
            try:
                os.close(descriptor)
            except BaseException:
                os.close(child)
                raise
            descriptor = child
        return descriptor
    except BaseException:
        os.close(descriptor)
        raise


def directory_matches(path, descriptor):
    current = open_directory(path)
    try:
        wanted, actual = os.fstat(descriptor), os.fstat(current)
        require((wanted.st_dev, wanted.st_ino) == (actual.st_dev, actual.st_ino),
                "Freeze directory identity changed")
    finally:
        os.close(current)


def write_snapshot(directory, name, data, deadline):
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                         0o600, dir_fd=directory)
    try:
        identity = os.fstat(descriptor)
        require(stat.S_ISREG(identity.st_mode), "Freeze snapshot is not a regular file")
        position = 0
        while position < len(data):
            check_deadline(deadline)
            written = os.write(descriptor, data[position:position + 65536])
            require(written > 0, "Freeze snapshot write made no progress")
            position += written
        os.fsync(descriptor)
        check_deadline(deadline)
        return {"name": name, "device": identity.st_dev, "inode": identity.st_ino,
                "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}
    finally:
        os.close(descriptor)


def verify_snapshot(directory, expected, deadline):
    descriptor = os.open(expected["name"], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                         dir_fd=directory)
    try:
        before = os.fstat(descriptor)
        require(stat.S_ISREG(before.st_mode) and before.st_size == expected["size"] and
                (before.st_dev, before.st_ino) == (expected["device"], expected["inode"]),
                "Frozen snapshot identity or size changed")
        remaining, digest = expected["size"], hashlib.sha256()
        while remaining:
            check_deadline(deadline)
            chunk = os.read(descriptor, min(remaining, 65536))
            require(chunk, "Frozen snapshot became incomplete")
            digest.update(chunk)
            remaining -= len(chunk)
        require(not os.read(descriptor, 1), "Frozen snapshot grew")
        after = os.fstat(descriptor)
        named = os.stat(expected["name"], dir_fd=directory, follow_symlinks=False)
        require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) ==
                (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns) and
                (named.st_dev, named.st_ino) == (before.st_dev, before.st_ino) and
                digest.hexdigest() == expected["sha256"], "Frozen snapshot bytes changed")
        check_deadline(deadline)
    finally:
        os.close(descriptor)


def publish(output, snapshots, resolution, verify_authority, deadline):
    """Publish under exclusive caller ownership of the output parent and directory.

    Identity checks detect mutations; they do not isolate a concurrent writer.
    The final link is the commit point, not a crash-durability protocol.
    """
    require(output.is_absolute() and output.name not in ("", ".", ".."),
            "Freeze output must be a new absolute directory")
    parent = open_directory(output.parent)
    directory = None
    published = False
    try:
        os.mkdir(output.name, 0o700, dir_fd=parent)
        created = os.stat(output.name, dir_fd=parent, follow_symlinks=False)
        directory = os.open(output.name, DIRECTORY_FLAGS, dir_fd=parent)
        opened = os.fstat(directory)
        require(stat.S_ISDIR(created.st_mode) and
                (created.st_dev, created.st_ino) == (opened.st_dev, opened.st_ino),
                "Freeze output changed before opening")
        directory_matches(output, directory)
        with os.scandir(directory) as entries:
            require(next(entries, None) is None, "Freeze output must be empty")
        records = []
        for name, data in snapshots.items():
            check_deadline(deadline)
            directory_matches(output, directory)
            records.append(write_snapshot(directory, name, data, deadline))
        pending = ".resolution-" + uuid.uuid4().hex + ".pending"
        records.append(write_snapshot(directory, pending, resolution, deadline))
        os.fsync(directory)
        check_deadline(deadline)
        verify_authority()
        for record in records:
            verify_snapshot(directory, record, deadline)
        directory_matches(output, directory)
        check_deadline(deadline)
        # Never replace another marker. Keep the bounded prepared file instead
        # of unlinking a path another actor may have replaced. No fallible work
        # after this boundary can turn publication into a reported failure.
        os.link(pending, "resolution.json", src_dir_fd=directory, dst_dir_fd=directory,
                follow_symlinks=False)
        published = True
    finally:
        failure = None
        for descriptor in (directory, parent):
            if descriptor is not None:
                try:
                    os.close(descriptor)
                except OSError as error:
                    failure = error
        if failure is not None and not published:
            raise failure
