// Package rcon is a minimal Source RCON client.
//
// It exists because the agent has to drive a stock srcds — change level, set
// the battle password, kick who does not belong — and RCON is the interface
// every Source server already has. Nothing about the game binary needs to
// change for a machine to join the pool.
//
// Wire format (little-endian):
//
//	int32 size        // bytes after this field
//	int32 id          // request id, echoed in the response
//	int32 type        // 3 auth, 2 exec, 2 auth response, 0 response value
//	bytes body        // NUL-terminated
//	byte  0           // trailing NUL
package rcon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	typeResponse     = 0
	typeExecCommand  = 2
	typeAuthResponse = 2
	typeAuth         = 3

	maxPacket = 4096
)

var (
	// ErrAuthFailed means the RCON password was rejected.
	ErrAuthFailed = errors.New("rcon: authentication failed")
	// ErrClosed means the connection is gone and must be redialled.
	ErrClosed = errors.New("rcon: connection closed")
)

// Conn is an authenticated RCON connection. It serializes commands, so one
// connection can be shared.
type Conn struct {
	mu      sync.Mutex
	c       net.Conn
	nextID  int32
	timeout time.Duration
	closed  bool
}

// Dial connects and authenticates.
func Dial(addr, password string, timeout time.Duration) (*Conn, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("rcon: dial %s: %w", addr, err)
	}
	conn := &Conn{c: c, nextID: 1, timeout: timeout}
	if err := conn.auth(password); err != nil {
		_ = c.Close()
		return nil, err
	}
	return conn, nil
}

func (c *Conn) auth(password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	if err := c.write(id, typeAuth, password); err != nil {
		return err
	}
	// The server answers auth with an empty RESPONSE_VALUE followed by the auth
	// response proper; some builds skip the first, so both shapes are accepted.
	for i := 0; i < 2; i++ {
		gotID, gotType, _, err := c.read()
		if err != nil {
			return err
		}
		if gotType == typeAuthResponse {
			if gotID == -1 {
				return ErrAuthFailed
			}
			return nil
		}
	}
	return ErrAuthFailed
}

// Exec runs one console command and returns whatever the server printed.
func (c *Conn) Exec(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", ErrClosed
	}
	id := c.nextID
	c.nextID++
	if err := c.write(id, typeExecCommand, cmd); err != nil {
		c.closed = true
		return "", err
	}
	// Multi-packet responses are terminated by the echo of a second, empty
	// command; asking for it is the standard way to know where a reply ends.
	sentinel := c.nextID
	c.nextID++
	if err := c.write(sentinel, typeExecCommand, ""); err != nil {
		c.closed = true
		return "", err
	}

	var body strings.Builder
	for {
		gotID, _, payload, err := c.read()
		if err != nil {
			c.closed = true
			return body.String(), err
		}
		if gotID == sentinel {
			return body.String(), nil
		}
		body.WriteString(payload)
	}
}

// Execf is Exec with formatting.
func (c *Conn) Execf(format string, args ...any) (string, error) {
	return c.Exec(fmt.Sprintf(format, args...))
}

func (c *Conn) write(id, typ int32, body string) error {
	if err := c.c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	payload := make([]byte, 0, len(body)+14)
	var head [12]byte
	binary.LittleEndian.PutUint32(head[0:], uint32(len(body)+10))
	binary.LittleEndian.PutUint32(head[4:], uint32(id))
	binary.LittleEndian.PutUint32(head[8:], uint32(typ))
	payload = append(payload, head[:]...)
	payload = append(payload, body...)
	payload = append(payload, 0, 0)
	_, err := c.c.Write(payload)
	return err
}

func (c *Conn) read() (id, typ int32, body string, err error) {
	if err := c.c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, 0, "", err
	}
	var sizeBuf [4]byte
	if _, err := io.ReadFull(c.c, sizeBuf[:]); err != nil {
		return 0, 0, "", err
	}
	size := int32(binary.LittleEndian.Uint32(sizeBuf[:]))
	if size < 10 || size > maxPacket {
		return 0, 0, "", fmt.Errorf("rcon: implausible packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(c.c, buf); err != nil {
		return 0, 0, "", err
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:]))
	body = strings.TrimRight(string(buf[8:]), "\x00")
	return id, typ, body, nil
}

// Close shuts the connection down.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.c.Close()
}
