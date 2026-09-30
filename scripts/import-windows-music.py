#!/usr/bin/env python3
"""Import audio from a Windows SFTP folder into the Home Node music library.

Requires paramiko and mutagen. The source is read-only; files are staged with a
.part extension so the media scanner never sees an incomplete track.
"""

import argparse
import hashlib
import os
import shutil
import stat
import sys
import time
import uuid
from pathlib import Path, PurePosixPath

import mutagen
import paramiko


AUDIO_EXTENSIONS = {".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wav"}
GENERIC_FOLDERS = {"music", "музыка", "мущыка", "любимые треки (2)", "мои любимые треки апреля"}
CHUNK_SIZE = 1024 * 1024


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(CHUNK_SIZE), b""):
            digest.update(block)
    return digest.hexdigest()


def remove_appledouble(path: Path) -> None:
    """SMB can create a macOS sidecar for a file we just staged or renamed."""
    path.with_name("._" + path.name).unlink(missing_ok=True)


def safe_component(value: str, fallback: str) -> str:
    value = "".join("_" if c in '/\\:*?\"<>|\x00' or ord(c) < 32 else c for c in value)
    value = value.strip(" .")
    return (value[:100].rstrip(" .") or fallback)


def safe_filename(relative: PurePosixPath) -> str:
    stem = safe_component(relative.stem, "Трек")
    return stem + relative.suffix.lower()


def first_tag(tags, *names: str) -> str:
    if not tags:
        return ""
    for name in names:
        values = tags.get(name)
        if values:
            return str(values[0]).strip()
    return ""


def destination_for(temp: Path, relative: PurePosixPath, root: Path) -> Path:
    try:
        tags = mutagen.File(str(temp), easy=True)
    except (mutagen.MutagenError, OSError):
        tags = None
    artist = first_tag(tags, "albumartist", "artist")
    album = first_tag(tags, "album")
    folders = relative.parts[:-1]
    if not artist and len(folders) >= 2 and folders[-2].casefold() not in GENERIC_FOLDERS:
        artist = folders[-2]
    if not artist and folders and folders[0].casefold() not in GENERIC_FOLDERS:
        artist = folders[0]
    if not artist and " - " in relative.stem:
        artist = relative.stem.rsplit(" - ", 1)[-1]
    if not album and folders and folders[-1].casefold() not in GENERIC_FOLDERS:
        album = folders[-1]
    artist = safe_component(artist, "Неизвестный исполнитель")
    album = safe_component(album, "Синглы")
    name = safe_filename(relative)
    return root / artist / album / name


def walk_remote(sftp: paramiko.SFTPClient, root: str):
    stack = [(root.rstrip("/"), PurePosixPath())]
    while stack:
        directory, relative = stack.pop()
        for item in sftp.listdir_attr(directory):
            remote = directory + "/" + item.filename
            child_relative = relative / item.filename
            if stat.S_ISDIR(item.st_mode):
                stack.append((remote, child_relative))
            elif stat.S_ISREG(item.st_mode) and child_relative.suffix.lower() in AUDIO_EXTENSIONS:
                yield remote, child_relative, item.st_size


def scan_existing(root: Path):
    known = {}
    count = 0
    for directory, _, names in os.walk(root):
        for name in names:
            path = Path(directory) / name
            if name.startswith(".") or path.suffix.lower() not in AUDIO_EXTENSIONS:
                continue
            known.setdefault(sha256_file(path), path)
            count += 1
    return known, count


def import_music(args):
    destination = Path(args.destination).resolve(strict=True)
    if not destination.is_dir():
        raise ValueError("Destination is not a directory")
    if shutil.disk_usage(destination).free < args.min_free_gb * 1024**3:
        raise ValueError("Destination has too little free space")

    started = time.monotonic()
    print("Indexing music already on the server...", flush=True)
    known, existing_count = scan_existing(destination)
    print(f"Indexed {existing_count} existing tracks ({len(known)} unique hashes).", flush=True)

    ssh = paramiko.SSHClient()
    ssh.load_system_host_keys()
    ssh.set_missing_host_key_policy(paramiko.RejectPolicy())
    ssh.connect(args.host, username=args.user, key_filename=args.key, look_for_keys=False,
                allow_agent=False, timeout=10)
    added = duplicates = failed = transferred = 0
    staging = destination / ".homenode-import-temp"
    staging.mkdir(exist_ok=True)
    try:
        sftp = ssh.open_sftp()
        files = list(walk_remote(sftp, args.source))
        if args.limit:
            files = files[:args.limit]
        total_bytes = sum(size for _, _, size in files)
        free_bytes = shutil.disk_usage(destination).free
        print(f"Found {len(files)} audio files ({total_bytes / 1024**3:.2f} GiB); "
              f"free {free_bytes / 1024**3:.2f} GiB.", flush=True)
        if total_bytes + args.min_free_gb * 1024**3 > free_bytes:
            raise ValueError("Not enough free space for a safe import")
        for index, (remote, relative, size) in enumerate(files, 1):
            temp = staging / (uuid.uuid4().hex + ".part")
            try:
                digest = hashlib.sha256()
                copied = 0
                with sftp.open(remote, "rb") as source, temp.open("xb") as target:
                    source.prefetch(file_size=size, max_concurrent_requests=64)
                    while True:
                        block = source.read(CHUNK_SIZE)
                        if not block:
                            break
                        target.write(block)
                        digest.update(block)
                        copied += len(block)
                    target.flush()
                    os.fsync(target.fileno())
                if copied != size or sha256_file(temp) != digest.hexdigest():
                    raise IOError("Size or SHA-256 verification failed")
                transferred += copied
                if digest.hexdigest() in known:
                    duplicates += 1
                    temp.unlink()
                    remove_appledouble(temp)
                else:
                    target_path = destination_for(temp, relative, destination)
                    created_directories = []
                    candidate = target_path.parent
                    while candidate != destination and not candidate.exists():
                        created_directories.append(candidate)
                        candidate = candidate.parent
                    target_path.parent.mkdir(parents=True, exist_ok=True)
                    for directory in created_directories:
                        remove_appledouble(directory)
                    if target_path.exists():
                        target_path = target_path.with_name(
                            f"{target_path.stem} [{digest.hexdigest()[:12]}]{target_path.suffix}")
                    if target_path.exists():
                        raise FileExistsError(f"Target collision: {target_path}")
                    temp.rename(target_path)
                    remove_appledouble(temp)
                    remove_appledouble(target_path)
                    known[digest.hexdigest()] = target_path
                    added += 1
            except Exception as exc:
                failed += 1
                print(f"ERROR {relative}: {exc}", file=sys.stderr, flush=True)
                temp.unlink(missing_ok=True)
                remove_appledouble(temp)
            if index % 10 == 0 or index == len(files):
                print(f"Progress {index}/{len(files)}: added={added}, duplicates={duplicates}, "
                      f"failed={failed}, transferred={transferred / 1024**3:.2f} GiB, "
                      f"elapsed={int(time.monotonic() - started)}s", flush=True)
        sftp.close()
    finally:
        ssh.close()
        try:
            staging.rmdir()
        except OSError:
            pass
    print(f"Done: added={added}, duplicates={duplicates}, failed={failed}.", flush=True)
    return 1 if failed else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True, help="Windows SSH hostname or LAN IP")
    parser.add_argument("--user", required=True, help="Windows account name")
    parser.add_argument("--key", required=True, help="SSH private key path")
    parser.add_argument("--source", required=True, help="Windows SFTP path, e.g. /C:/Users/user/Music")
    parser.add_argument("--destination", required=True, help="Mounted Home Node music directory")
    parser.add_argument("--min-free-gb", type=int, default=10)
    parser.add_argument("--limit", type=int, default=0, help="Import only this many tracks for a smoke test")
    args = parser.parse_args()
    return import_music(args)


if __name__ == "__main__":
    sys.exit(main())
