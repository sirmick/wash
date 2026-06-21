// Package sftptest provides an in-process SSH+SFTP server for tests, so the
// FUSE/mount stack can be exercised against a real SFTP backend without an
// external sshd or VM. It serves the process's filesystem; callers confine
// access by choosing a remote root under a temp dir.
package sftptest

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// NewServer starts the in-process SSH+SFTP server on a loopback listener and
// returns its address plus a cleanup that stops the listener. Dial it (possibly
// repeatedly — e.g. to exercise reconnect) with Dial.
func NewServer() (addr string, cleanup func(), err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("gen host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return "", nil, fmt.Errorf("signer: %w", err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen: %w", err)
	}
	go func() {
		for {
			nConn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(nConn, cfg)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }, nil
}

// Dial opens one SSH+SFTP client to addr. The returned ssh.Client is the
// transport — close it to simulate a connection drop (the sftp client's ops
// then fail until the caller re-dials).
func Dial(addr string) (client *sftp.Client, sshConn *ssh.Client, err error) {
	sshClient, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial: %w", err)
	}
	client, err = sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("sftp client: %w", err)
	}
	return client, sshClient, nil
}

// New starts the server and returns a connected client plus a cleanup func that
// tears down the client, ssh connection, and listener. It never blocks.
func New() (client *sftp.Client, cleanup func(), err error) {
	addr, stopServer, err := NewServer()
	if err != nil {
		return nil, nil, err
	}
	client, sshClient, err := Dial(addr)
	if err != nil {
		stopServer()
		return nil, nil, err
	}
	return client, func() {
		client.Close()
		sshClient.Close()
		stopServer()
	}, nil
}

func serveConn(nConn net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range chReqs {
				ok := req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					if srv, err := sftp.NewServer(ch); err == nil {
						srv.Serve()
					}
					ch.Close()
				}
			}
		}()
	}
}
