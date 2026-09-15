#!/usr/bin/env python3
"""Run real socket/firewall tests in a disposable user/network namespace."""
import os
import subprocess
import sys

if len(sys.argv) == 1:
    parent = os.readlink('/proc/self/ns/net')
    raise SystemExit(subprocess.call(['unshare', '--user', '--map-root-user', '--net',
                                    sys.executable, __file__, parent]))
if os.readlink('/proc/self/ns/net') == sys.argv[1]:
    raise SystemExit('refusing to operate in the host network namespace')
subprocess.run(['ip', 'link', 'set', 'lo', 'up'], check=True)
subprocess.run(['ip', 'addr', 'add', '192.0.2.1/32', 'dev', 'lo'], check=True)
subprocess.run(['ip', 'addr', 'add', '192.0.2.2/32', 'dev', 'lo'], check=True)
subprocess.run(['iptables-restore'], input='''*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
:ufw-before-input - [0:0]
:doubletake-input - [0:0]
-A INPUT -p tcp -j ACCEPT
-A INPUT -j ufw-before-input
-A ufw-before-input -j doubletake-input
-A ufw-before-input -p udp --dport 60202 -j ACCEPT
COMMIT
''', text=True, check=True)
env = dict(os.environ, DOUBLETAKE_NETNS_TEST='1')
raise SystemExit(subprocess.call(['go', 'test', '-race', '-count=1', './internal/networkd/...'], env=env))
