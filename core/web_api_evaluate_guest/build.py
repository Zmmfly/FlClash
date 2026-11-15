#!/usr/bin/env python3
"""Rebuild the import-free evaluator from explicitly supplied, pinned archives."""
import argparse
import hashlib
from pathlib import Path
import subprocess
import tarfile
import tempfile

SOURCE_SHA256 = 'a13ac243b86a74120814ef7e9e30ad5a2c1199b7b9963b1cf7c84e4cdc1cad99'
SDK_SHA256 = 'b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4'


def checked_archive(path, digest, destination):
    if hashlib.file_digest(path.open('rb'), 'sha256').hexdigest() != digest:
        raise ValueError(f'Archive digest mismatch: {path.name}')
    with tarfile.open(path) as archive:
        archive.extractall(destination, filter='data')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-archive', required=True, type=Path)
    parser.add_argument('--sdk-archive', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    with tempfile.TemporaryDirectory(prefix='flclash-evaluate-build-') as tmp:
        work = Path(tmp)
        checked_archive(args.source_archive, SOURCE_SHA256, work)
        checked_archive(args.sdk_archive, SDK_SHA256, work)
        source = work / 'rquickjs-sys-0.12.2' / 'quickjs'
        sdk = work / 'wasi-sdk-34.0-x86_64-linux'
        wraps = ['clock_time_get', 'fd_close', 'fd_seek', 'fd_write',
                 'fd_fdstat_get', 'environ_sizes_get', 'environ_get', 'proc_exit']
        command = [str(sdk / 'bin' / 'clang'), '-O2', '-DNDEBUG', '-D_GNU_SOURCE',
                   '-mexec-model=reactor', '-I' + str(source),
                   str(root / 'evaluator.c')]
        command += [str(source / f) for f in ['quickjs.c', 'dtoa.c', 'libregexp.c', 'libunicode.c']]
        command += ['-Wl,-z,stack-size=2097152', '-Wl,--initial-memory=4194304',
                    '-Wl,--max-memory=268435456', '-Wl,--strip-all']
        command += ['-Wl,--wrap=__wasi_' + name for name in wraps]
        command += ['-lm', '-o', str(args.output.resolve())]
        subprocess.run(command, check=True)
    print(hashlib.file_digest(args.output.open('rb'), 'sha256').hexdigest())


if __name__ == '__main__':
    main()
