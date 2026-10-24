// Package cli holds helpers shared by the fogplay command-line tools.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// ConfigDir is where the tools keep their files, e.g. ~/.config/fogplay.
func ConfigDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "fogplay"), nil
}

func LoadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// SaveJSON writes v readable only by the current user, since it may hold tokens.
func SaveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// Prompt reads a line from the terminal; secret input is not echoed.
func Prompt(label string, secret bool) (string, error) {
	fmt.Fprint(os.Stderr, label)
	if secret && term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}

// LocalIP returns the address of the interface this machine uses for
// outbound traffic, which is the one other PCs on the LAN can reach.
func LocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80") // no packets are sent
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
