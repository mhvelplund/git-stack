package forge

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// ParseRemoteURL extracts host and project path from a git remote URL
// (https, ssh:// or scp-like git@host:path).
func ParseRemoteURL(raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Repo{}, err
		}
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(raw, ":"); at > 1 && !strings.HasPrefix(raw, "/") {
		// at > 1: "C:\repo" is a Windows path, not host "C".
		host, path = raw[:at], raw[at+1:]
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
	} else {
		return Repo{}, fmt.Errorf("cannot parse remote URL %q", raw)
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || !strings.Contains(path, "/") {
		return Repo{}, fmt.Errorf("cannot parse remote URL %q", raw)
	}
	return Repo{RemoteURL: raw, Host: host, Path: path}, nil
}

// SSHHostname resolves an ssh config alias (e.g. "work-gitlab") to its real
// hostname with `ssh -G`. It returns host unchanged if it looks like a real
// hostname or cannot be resolved.
func SSHHostname(host string) string {
	if strings.Contains(host, ".") || host == "localhost" {
		return host
	}
	out, err := exec.Command("ssh", "-G", host).Output()
	if err != nil {
		return host
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(line, "hostname "); ok {
			return strings.TrimSpace(name)
		}
	}
	return host
}
