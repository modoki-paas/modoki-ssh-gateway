package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/docker/docker/api/types"

	"github.com/k0kubun/pp"

	"golang.org/x/crypto/ssh"
)

type ttySize struct {
	w, h uint
}

func (c *connWorker) createExec(ctx context.Context, execConfig types.ExecConfig) (string, error) {
	resp, err := c.adapters.docker.ContainerExecCreate(ctx, c.cid, execConfig)

	if err != nil {
		return "", err
	}

	return resp.ID, nil
}

func (c *connWorker) initExec(ctx context.Context, ch ssh.Channel, execConfig types.ExecConfig, size ttySize) (string, error) {
	execID, err := c.createExec(ctx, execConfig)

	if err != nil {
		return "", err
	}

	resp, err := c.adapters.docker.ContainerExecAttach(context.Background(), execID, types.ExecStartCheck{
		Tty:    execConfig.Tty,
		Detach: false,
	})

	if err != nil {
		return "", err
	}

	go func() {
		if execConfig.Tty {
			io.Copy(ch, resp.Conn)
		} else {
			stdcopy.StdCopy(ch, ch, resp.Conn)
		}
		ch.Close()
		resp.Conn.Close()
	}()
	go func() {
		io.Copy(resp.Conn, ch)

		ch.Close()
		resp.Conn.Close()
	}()

	if err := c.resizeTty(ctx, execID, size); err != nil {
		return "", err
	}

	return execID, nil
}

func (c *connWorker) resizeTty(ctx context.Context, execID string, size ttySize) error {
	if len(execID) == 0 {
		return nil
	}

	if size.w == 0 && size.h == 0 {
		return nil
	}

	return c.adapters.docker.ContainerExecResize(ctx, execID, types.ResizeOptions{Width: size.w, Height: size.h})
}

func (c *connWorker) handleSession(newChannel ssh.NewChannel) {
	ch, requests, err := newChannel.Accept()
	if err != nil {
		log.Printf("Could not accept channel (%s)", err)
		return
	}

	var execID string
	execConfig := types.ExecConfig{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Detach:       false,
	}
	var size ttySize

	go func() {
		for req := range requests {
			log.Println(pp.Sprint(req))

			errorReply := func(err error) {
				ch.Write([]byte(err.Error()))
				if req.WantReply {
					req.Reply(false, nil)
				}
			}

			switch req.Type {
			case "env":
				if len(req.Payload) < 4 {
					errorReply(errors.New("Invalid env format"))
					return
				}

				var key, val string

				if l := binary.BigEndian.Uint32(req.Payload[:4]); uint32(len(req.Payload)) <= 4+l {
					errorReply(errors.New("Invalid env format"))
					return
				} else {
					key = string(req.Payload[4 : 4+l])
					req.Payload = req.Payload[4+l:]
				}

				if l := binary.BigEndian.Uint32(req.Payload[:4]); uint32(len(req.Payload)) <= 4+l {
					errorReply(errors.New("Invalid env format"))
					return
				} else {
					val = string(req.Payload[4 : 4+l])
				}

				execConfig.Env = append(execConfig.Env, key+"="+val)

			case "shell":
				if execID != "" {
					req.Reply(false, nil)

					continue
				}

				execConfig.Cmd = []string{c.defaultShell}

				execID, err = c.initExec(context.Background(), ch, execConfig, size)

				if err != nil {
					errorReply(err)

					return
				}

				if req.WantReply {
					req.Reply(true, nil)
				}
			case "exec":
				if execID != "" {
					req.Reply(false, nil)
					continue
				}
				execConfig.Cmd = strings.Split(string(req.Payload), " ")

				execID, err = c.initExec(context.Background(), ch, execConfig, size)

				if err != nil {
					errorReply(err)
					return
				}

				if req.WantReply {
					req.Reply(true, nil)
				}
			case "pty-req":
				if execID != "" {
					req.Reply(false, nil)
					continue
				}
				termLen := req.Payload[3]
				w, h := parseDims(req.Payload[termLen+4:])

				size.w = uint(w)
				size.h = uint(h)

				if err := c.resizeTty(context.Background(), execID, size); err != nil {
					errorReply(err)

					return
				}
				if req.WantReply {
					req.Reply(true, nil)
				}
			case "window-change":
				w, h := parseDims(req.Payload)
				size.w = uint(w)
				size.h = uint(h)

				if err := c.resizeTty(context.Background(), execID, size); err != nil {
					errorReply(err)

					return
				}
				if req.WantReply {
					req.Reply(true, nil)
				}
			case "subsystem":
				// Currently not supported

				errorReply(errors.New("Not supported"))

				return
			case "signal":
				/*				signalName := string(req.Payload)

								signal := signalNameToSignal[signalName]*/

				if req.WantReply {
					req.Reply(true, nil)
				}
			}

		}
	}()
}

func (c *connWorker) handleChannels(chans <-chan ssh.NewChannel) {
	for newChannel := range chans {
		go c.handleChannel(newChannel)
	}
}

func (c *connWorker) handleChannel(newChannel ssh.NewChannel) {
	switch t := newChannel.ChannelType(); t {
	case "session":
		c.handleSession(newChannel)
	default: // x11, direct-tcpip forwarded-tcpip are not currently supported
		newChannel.Reject(ssh.UnknownChannelType, fmt.Sprintf("Unknown channel type: %s", t))
	}

}
