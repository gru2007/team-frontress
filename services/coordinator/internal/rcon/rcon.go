// Package rcon speaks the Source RCON protocol.
//
// It is deliberately small: the coordinator authenticates, sends a handful of
// commands to set a match up, and reads "status" to see who is on the server.
package rcon

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Packet types from the Source RCON specification.
const (
	typeResponseValue = 0
	typeExecCommand   = 2
	typeAuthResponse  = 2
	typeAuth          = 3
)

// ErrAuthFailed means the RCON password was rejected.
var ErrAuthFailed = errors.New("rcon: authentication failed")

const maxPacketSize = 4096

// Conn is an authenticated RCON connection.
type Conn struct {
	conn    net.Conn
	r       *bufio.Reader
	nextID  int32
	timeout time.Duration
}

// Dial connects to addr ("host:port") and authenticates with password.
func Dial(addr, password string, timeout time.Duration) (*Conn, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	nc, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("rcon: %w", err)
	}
	c := &Conn{conn: nc, r: bufio.NewReader(nc), nextID: 1, timeout: timeout}
	if err := c.auth(password); err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// Close releases the connection.
func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) auth(password string) error {
	id := c.nextID
	c.nextID++
	if err := c.write(id, typeAuth, password); err != nil {
		return err
	}
	// The server answers with an empty RESPONSE_VALUE followed by an
	// AUTH_RESPONSE. A failed auth answers with id -1.
	for i := 0; i < 2; i++ {
		gotID, gotType, _, err := c.read()
		if err != nil {
			return err
		}
		if gotType == typeAuthResponse {
			if gotID == -1 {
				return ErrAuthFailed
			}
			if gotID != id {
				return fmt.Errorf("rcon: auth reply id %d, want %d", gotID, id)
			}
			return nil
		}
	}
	return errors.New("rcon: no auth response")
}

// Exec runs one command and returns the server's reply body.
func (c *Conn) Exec(cmd string) (string, error) {
	id := c.nextID
	c.nextID++
	if err := c.write(id, typeExecCommand, cmd); err != nil {
		return "", err
	}
	// A long reply arrives as several packets. Sending a second, empty command
	// and watching for its echo is the standard way to find the end; for the
	// short commands this package sends, one read plus a drain is enough.
	_, _, body, err := c.read()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(body)
	for {
		c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, _, more, err := c.read()
		if err != nil {
			break
		}
		sb.WriteString(more)
	}
	c.conn.SetReadDeadline(time.Time{})
	return sb.String(), nil
}

func (c *Conn) write(id, typ int32, body string) error {
	payload := make([]byte, 0, len(body)+10)
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(id))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(typ))
	payload = append(payload, hdr[:]...)
	payload = append(payload, body...)
	payload = append(payload, 0, 0)

	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(payload)))

	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	defer c.conn.SetWriteDeadline(time.Time{})
	if _, err := c.conn.Write(size[:]); err != nil {
		return fmt.Errorf("rcon: %w", err)
	}
	if _, err := c.conn.Write(payload); err != nil {
		return fmt.Errorf("rcon: %w", err)
	}
	return nil
}

func (c *Conn) read() (id, typ int32, body string, err error) {
	var size int32
	if err = binary.Read(c.r, binary.LittleEndian, &size); err != nil {
		return 0, 0, "", err
	}
	if size < 10 || size > maxPacketSize {
		return 0, 0, "", fmt.Errorf("rcon: bad packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err = io.ReadFull(c.r, buf); err != nil {
		return 0, 0, "", err
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:8]))
	body = string(bytes.TrimRight(buf[8:], "\x00"))
	return id, typ, body, nil
}

// StatusPlayers parses the player count out of a "status" reply.
//
// The line looks like: `players : 3 humans, 0 bots (24 max)`. A reply we cannot
// parse returns ok=false rather than a confident zero, because "zero players"
// is what ends a match.
func StatusPlayers(status string) (humans int, ok bool) {
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "players") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}
