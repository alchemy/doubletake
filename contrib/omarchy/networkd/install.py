#!/usr/bin/env python3
"""Install the Omarchy/UFW integration, or stage it under --destdir."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess

START = '# BEGIN doubletake-networkd'
END = '# END doubletake-networkd'
CHAIN = 'doubletake-input'


def hook(text, remove=False):
    lines = text.splitlines()
    output = []
    inside = False
    own_lines = {f':{CHAIN} - [0:0]', f'-F {CHAIN}', f'-A ufw-before-input -j {CHAIN}'}
    if lines.count(START) != lines.count(END) or lines.count(START) > 1:
        raise ValueError('invalid managed markers')
    for line in lines:
        if line == START:
            if inside:
                raise ValueError('nested managed block')
            inside = True
        elif line == END:
            if not inside:
                raise ValueError('unmatched managed block')
            inside = False
        elif not inside or line not in own_lines:
            # Preserve foreign hooks inserted inside our markers by another installer.
            output.append(line)
    if inside:
        raise ValueError('unterminated managed block')
    if not remove:
        # Fail rather than guessing where custom non-managed references belong.
        if any(CHAIN in line for line in output):
            raise ValueError('unmanaged doubletake-input reference')
        if output.count('*filter') != 1 or not any(line.startswith(':ufw-before-input ') for line in output):
            raise ValueError('expected UFW filter table')
        start = output.index('*filter')
        commit = output.index('COMMIT', start)
        first_rule = next((i for i in range(start + 1, commit)
                           if output[i].startswith('-')), commit)
        # Do not insert inside another installer's managed block (e.g. Waycast).
        block_start = None
        for i in range(start + 1, first_rule):
            if output[i].startswith('# BEGIN '):
                if block_start is None:
                    block_start = i
            elif output[i].startswith('# END '):
                block_start = None
        if block_start is not None:
            first_rule = block_start
        output[first_rule:first_rule] = [START, f':{CHAIN} - [0:0]',
                                       f'-F {CHAIN}',
                                       f'-A ufw-before-input -j {CHAIN}', END]
    return '\n'.join(output) + '\n'


def main():
    os.umask(0o022)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--destdir', type=Path, default=Path('/'))
    parser.add_argument('--binary', type=Path, default=Path('bin/doubletake-networkd'))
    parser.add_argument('--uninstall', action='store_true')
    args = parser.parse_args()
    root = args.destdir.resolve()
    live = root == Path('/')
    if live and os.geteuid() != 0:
        parser.error('installation requires root; use --destdir to stage files')
    rules = root / 'etc/ufw/before.rules'
    original = rules.read_text()
    updated = hook(original, args.uninstall)
    source = Path(__file__).resolve().parent
    paths = {
        'doubletake-networkd.service': 'usr/lib/systemd/system/doubletake-networkd.service',
        'org.doubletake.Network1.service': 'usr/share/dbus-1/system-services/org.doubletake.Network1.service',
        'org.doubletake.Network1.conf': 'usr/share/dbus-1/system.d/org.doubletake.Network1.conf',
        'org.doubletake.network.policy': 'usr/share/polkit-1/actions/org.doubletake.network.policy',
        '60-doubletake-network.rules': 'etc/polkit-1/rules.d/60-doubletake-network.rules',
    }
    binary = root / 'usr/lib/doubletake/doubletake-networkd'
    if not args.uninstall and not args.binary.is_file():
        parser.error('build bin/doubletake-networkd first')
    if live:
        if not args.uninstall:
            for tool in ('systemctl', 'ufw', 'iptables', 'iptables-restore'):
                if shutil.which(tool) is None:
                    parser.error(f'missing required tool: {tool}')
            subprocess.run(['systemctl', 'is-active', '--quiet', 'ufw.service'], check=True)
        state = subprocess.run(['systemctl', 'show', '--property=LoadState', '--value',
                                'doubletake-networkd.service'], capture_output=True, text=True, check=True)
        subprocess.run(['systemctl', 'mask', '--runtime', 'doubletake-networkd.service'], check=True)
        if state.stdout.strip() != 'not-found':
            subprocess.run(['systemctl', 'stop', 'doubletake-networkd.service'], check=True)
    if args.uninstall:
        if live:
            subprocess.run(['systemctl', 'disable', 'doubletake-networkd.service'], check=False)
        for target in paths.values():
            (root / target).unlink(missing_ok=True)
        binary.unlink(missing_ok=True)
    else:
        for name, target in paths.items():
            path = root / target
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / name, path)
            path.chmod(0o644)
            if live:
                os.chown(path, 0, 0)
        binary.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(args.binary, binary)
        binary.chmod(0o755)
        if live:
            os.chown(binary.parent, 0, 0)
            binary.parent.chmod(0o755)
            os.chown(binary, 0, 0)
        backup = rules.with_name('before.rules.pre-doubletake')
        if not backup.exists():
            shutil.copy2(rules, backup)
    temp = rules.with_name('before.rules.doubletake.tmp')
    temp.write_text(updated)
    temp.chmod(rules.stat().st_mode & 0o777)
    temp.replace(rules)
    if live:
        subprocess.run(['ufw', 'reload'], check=True)
        subprocess.run(['systemctl', 'daemon-reload'], check=True)
        subprocess.run(['systemctl', 'unmask', '--runtime', 'doubletake-networkd.service'], check=True)
        if not args.uninstall:
            subprocess.run(['systemctl', 'enable', '--now', 'doubletake-networkd.service'], check=True)


if __name__ == '__main__':
    main()
