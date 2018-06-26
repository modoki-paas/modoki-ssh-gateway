package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"
)

type connWorker struct {
	adapters *adaptersType

	cid          string
	id           int
	uid          int
	defaultShell string
}

func (c *connWorker) run(tcpConn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, config)
	if err != nil {
		log.Printf("Failed to handshake (%s)", err)

		return
	}

	c.id, _ = strconv.Atoi(sshConn.Permissions.CriticalOptions[permIDKey])
	c.cid = sshConn.Permissions.CriticalOptions[permCIDKey]
	c.uid, _ = strconv.Atoi(sshConn.Permissions.CriticalOptions[permUIDKey])
	c.defaultShell = sshConn.Permissions.CriticalOptions[permShellKey]

	if c.defaultShell == "" {
		if p, err := c.adapters.consul.Client.Get(fmt.Sprint(defaultShellKVFormat, c.uid)); err == nil {
			c.defaultShell = string(p.Value)
		}
	}
	if c.defaultShell == "" {
		c.defaultShell = os.Getenv("MODOKI_DEFAULT_SHELL")
	}
	if c.defaultShell == "" {
		c.defaultShell = "sh"
	}

	log.Printf("New SSH connection from %s (%s)", sshConn.RemoteAddr(), sshConn.ClientVersion())

	go ssh.DiscardRequests(reqs)
	go c.handleChannels(chans)
}
