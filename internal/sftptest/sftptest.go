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

// New starts the server and returns a connected client plus a cleanup func that
// tears down the client, ssh connection, and listener. It never blocks.
func New() (client *sftp.Client, cleanup func(), err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("gen host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("signer: %w", err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
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

	sshClient, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		ln.Close()
		return nil, nil, fmt.Errorf("ssh dial: %w", err)
	}
	client, err = sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		ln.Close()
		return nil, nil, fmt.Errorf("sftp client: %w", err)
	}
	return client, func() {
		client.Close()
		sshClient.Close()
		ln.Close()
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
