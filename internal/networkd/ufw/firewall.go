// Package ufw manages only the dedicated runtime chain installed in UFW.
package ufw

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const Chain = "doubletake-input"

type Backend struct{ expected string }

func run(name string, args []string, input string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/bin:/usr/sbin", "LC_ALL=C"}
	cmd.Stdin = strings.NewReader(input)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("%s: %w: %s", name, e, b)
	}
	return string(b), nil
}
func hook() error {
	_, err := run("/usr/bin/iptables", []string{"-w", "2", "-C", "ufw-before-input", "-j", Chain}, "")
	return err
}
func (b *Backend) Apply(rules []string) error {
	if err := hook(); err != nil {
		return fmt.Errorf("missing UFW helper hook: %w", err)
	}
	sort.Strings(rules)
	text := "*filter\n-F " + Chain + "\n" + strings.Join(rules, "\n") + "\nCOMMIT\n"
	if _, err := run("/usr/bin/iptables-restore", []string{"--wait", "2", "--noflush"}, text); err != nil {
		return err
	}
	actual, err := run("/usr/bin/iptables", []string{"-w", "2", "-S", Chain}, "")
	if err != nil {
		return err
	}
	b.expected = actual
	return nil
}
func (b *Backend) Intact() bool {
	if hook() != nil {
		return false
	}
	actual, err := run("/usr/bin/iptables", []string{"-w", "2", "-S", Chain}, "")
	return err == nil && actual == b.expected
}

// Cleanup never recreates a removed hook and never touches another chain.
func Cleanup() error {
	if _, err := run("/usr/bin/iptables", []string{"-w", "2", "-S", Chain}, ""); err != nil {
		return err
	}
	_, err := run("/usr/bin/iptables", []string{"-w", "2", "-F", Chain}, "")
	return err
}
