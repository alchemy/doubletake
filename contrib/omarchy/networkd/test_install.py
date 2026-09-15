import unittest
from install import hook

BASE = '*filter\n:ufw-before-input - [0:0]\n-A ufw-before-input -i lo -j ACCEPT\nCOMMIT\n'

class InstallerTests(unittest.TestCase):
    def test_idempotent_and_reversible(self):
        installed = hook(BASE)
        self.assertEqual(installed, hook(installed))
        self.assertEqual(BASE, hook(installed, remove=True))
        self.assertLess(installed.index('-F doubletake-input'), installed.index('-A ufw-before-input -i lo'))

    def test_preserves_waycast(self):
        base = BASE.replace('-A ufw-before-input -i lo', ':waycast-input - [0:0]\n-A ufw-before-input -j waycast-input\n-A ufw-before-input -i lo')
        self.assertEqual(base, hook(hook(base), remove=True))

    def test_waycast_markers_not_nested(self):
        block = '# BEGIN WAYCAST HOOK\n-F waycast-input\n-A ufw-before-input -j waycast-input\n# END WAYCAST HOOK\n'
        base = BASE.replace('-A ufw-before-input -i lo', block + '-A ufw-before-input -i lo')
        result = hook(base)
        self.assertIn(block, result)
        self.assertEqual(base, hook(result, remove=True))
        # Waycast's installer can insert its hook just before our first -F.
        interleaved = hook(BASE).replace('-F doubletake-input', block + '-F doubletake-input')
        self.assertIn(block, hook(interleaved, remove=True))

    def test_rejects_unowned_chain(self):
        with self.assertRaises(ValueError):
            hook(BASE.replace('COMMIT', ':doubletake-input - [0:0]\nCOMMIT'))

    def test_rejects_broken_block(self):
        with self.assertRaises(ValueError):
            hook(BASE + '# BEGIN doubletake-networkd\n')


class StagedInstallationTests(unittest.TestCase):
    def test_install_update_uninstall(self):
        import subprocess
        import sys
        import tempfile
        from pathlib import Path
        import xml.etree.ElementTree as ET
        script = Path(__file__).with_name('install.py').resolve()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            rules = root / 'etc/ufw/before.rules'
            rules.parent.mkdir(parents=True)
            rules.write_text(BASE)
            binary = root / 'built-helper'
            binary.write_text('#!/bin/sh\nexit 0\n')
            command = [sys.executable, str(script), '--destdir', tmp, '--binary', str(binary)]
            subprocess.run(command, check=True)
            first = rules.read_text()
            subprocess.run(command, check=True)
            self.assertEqual(first, rules.read_text())
            installed = root / 'usr/lib/doubletake/doubletake-networkd'
            self.assertEqual(installed.stat().st_mode & 0o777, 0o755)
            for path in root.rglob('org.doubletake.*'):
                if path.suffix in ('.conf', '.policy'):
                    ET.parse(path)
            self.assertEqual(BASE, rules.with_name('before.rules.pre-doubletake').read_text())
            subprocess.run(command + ['--uninstall'], check=True)
            self.assertEqual(BASE, rules.read_text())
            self.assertFalse(installed.exists())

if __name__ == '__main__':
    unittest.main()
