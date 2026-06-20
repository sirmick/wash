// Command wash-mount mounts a remote machine's filesystem locally over SFTP as
// a real kernel FUSE mount, so every process on the box sees it.
//
// It is an OPTIONAL wash component: it depends on the FUSE kernel module and
// fusermount3, which are not present everywhere (notably the in-browser VM has
// no FUSE). It is built and packaged opt-in, the same way wash-display is.
//
// Usage:
//
//	wash-mount [flags] [user@]host[:port]:/remote/path  /local/mountpoint
//
// Authentication is via the SSH agent ($SSH_AUTH_SOCK), matching wash-remote.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/sirmick/wash/internal/washmount"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("wash-mount: ")

	timeout := flag.Duration("timeout", 30*time.Second, "per-operation backend timeout")
	allowOther := flag.Bool("allow-other", false, "expose the mount to other users (needs user_allow_other or root)")
	insecure := flag.Bool("insecure-host-key", false, "skip host key verification (testing only)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: wash-mount [flags] [user@]host[:port]:/remote/path /local/mountpoint\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}

	user, host, remotePath, err := parseTarget(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	mountpoint := flag.Arg(1)

	sshClient, err := dial(user, host, *insecure)
	if err != nil {
		log.Fatalf("ssh connect %s: %v", host, err)
	}
	defer sshClient.Close()

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		log.Fatalf("sftp: %v", err)
	}
	defer client.Close()

	server, err := washmount.Mount(client, washmount.Options{
		MountPoint: mountpoint,
		RemoteRoot: remotePath,
		OpTimeout:  *timeout,
		AllowOther: *allowOther,
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("mounted %s:%s at %s", host, remotePath, mountpoint)

	// Unmount cleanly on signal so we never leave a wedged mountpoint behind.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("unmounting")
		// Escalating unmount: never leave a wedged mountpoint behind even if the
		// backend has gone unresponsive.
		if err := washmount.Unmount(server, mountpoint); err != nil {
			log.Printf("unmount: %v", err)
		}
	}()
	server.Wait()
}

// parseTarget splits "[user@]host[:port]:/remote/path" into its parts.
func parseTarget(s string) (user, host, path string, err error) {
	at := strings.IndexByte(s, '@')
	if at >= 0 {
		user, s = s[:at], s[at+1:]
	} else {
		user = os.Getenv("USER")
	}
	// The remote path begins at the last colon that is followed by a slash, so
	// host:port:/path and host:/path both parse.
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || s[slash-1] != ':' {
		return "", "", "", fmt.Errorf("target must be [user@]host[:port]:/remote/path, got %q", s)
	}
	host, path = s[:slash-1], s[slash:]
	if !strings.Contains(host, ":") {
		host += ":22"
	}
	return user, host, path, nil
}

func dial(user, host string, insecure bool) (*ssh.Client, error) {
	authSock := os.Getenv("SSH_AUTH_SOCK")
	if authSock == "" {
		return nil, fmt.Errorf("no SSH agent: set up ssh-add (SSH_AUTH_SOCK is empty)")
	}
	conn, err := net.Dial("unix", authSock)
	if err != nil {
		return nil, fmt.Errorf("dial ssh agent: %w", err)
	}
	ag := agent.NewClient(conn)

	hostKeyCb := ssh.InsecureIgnoreHostKey()
	if !insecure {
		kh, err := knownhosts.New(os.ExpandEnv("$HOME/.ssh/known_hosts"))
		if err != nil {
			return nil, fmt.Errorf("known_hosts (use -insecure-host-key to bypass): %w", err)
		}
		hostKeyCb = kh
	}

	return ssh.Dial("tcp", host, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(ag.Signers)},
		HostKeyCallback: hostKeyCb,
		Timeout:         10 * time.Second,
	})
}
