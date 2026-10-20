package agentd

// The project's web home, from its git origin, so the Plan tab can link
// the commit hashes a finished node's body names. Derived at frame time
// rather than stored: it is a view of the checkout, not a fact about the
// workspace, and a workspace opened before this existed gets it too.

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	repoURLMu    sync.Mutex
	repoURLCache = map[string]repoURLEntry{}
	// A remote changes about as often as the project moves hosts; the
	// cache is here so a frame publish never waits on git twice a minute.
	repoURLTTL = 5 * time.Minute
)

type repoURLEntry struct {
	url string
	at  time.Time
}

// repoWebURL is the https home of the repo at root ("" when there is no
// origin, no git, or an origin that is not a web host).
func repoWebURL(root string) string {
	if root == "" {
		return ""
	}
	repoURLMu.Lock()
	if e, ok := repoURLCache[root]; ok && time.Since(e.at) < repoURLTTL {
		repoURLMu.Unlock()
		return e.url
	}
	repoURLMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	url := ""
	if out, err := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		url = webURLFromRemote(strings.TrimSpace(string(out)))
	}
	repoURLMu.Lock()
	repoURLCache[root] = repoURLEntry{url: url, at: time.Now()}
	repoURLMu.Unlock()
	return url
}

var scpRemote = regexp.MustCompile(`^[\w.-]+@([\w.-]+):(.+)$`)

// webURLFromRemote turns what git stores (https://, ssh://, git@host:path)
// into the https page the host serves for it, with no .git or trailing
// slash, so "/commit/<sha>" can be appended. Anything else yields "".
func webURLFromRemote(remote string) string {
	var host, path string
	switch {
	case strings.HasPrefix(remote, "https://"), strings.HasPrefix(remote, "http://"):
		rest := remote[strings.Index(remote, "://")+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:] // credentials never make it to the page
		}
		host, path, _ = strings.Cut(rest, "/")
	case strings.HasPrefix(remote, "ssh://"):
		rest := strings.TrimPrefix(remote, "ssh://")
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		host, path, _ = strings.Cut(rest, "/")
		if i := strings.Index(host, ":"); i >= 0 {
			host = host[:i]
		}
	default:
		m := scpRemote.FindStringSubmatch(remote)
		if m == nil {
			return ""
		}
		host, path = m[1], m[2]
	}
	path = strings.TrimSuffix(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return "https://" + host + "/" + path
}
