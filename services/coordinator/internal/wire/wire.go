// Package wire implements the framed-protobuf link between the game and the
// coordinator.
//
// Frame layout, little-endian, matching the C++ reader in the game module:
//
//	uint32 payload_len   // serialized CMsgEnvelope length, <= MaxFrameSize
//	bytes  payload
//
// The link is a plain TCP stream. It carries no transport security of its own;
// deployments that cross the public internet should terminate TLS in front of
// it (see docs/DEPLOYMENT.md). Identity does not depend on the transport: it
// comes from the Steam auth ticket in the hello and, for anything that touches
// war state, from corroboration between the host and the clients.
package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

// MaxFrameSize bounds a single envelope. Match snapshots are the largest
// legitimate message and stay far below this.
const MaxFrameSize = 1 << 20 // 1 MiB

var (
	ErrFrameTooLarge = errors.New("wire: frame exceeds maximum size")
	ErrClosed        = errors.New("wire: connection closed")
)

// Conn is a framed protobuf connection. Writes are serialized; reads are
// expected to happen from a single goroutine.
type Conn struct {
	c  net.Conn
	br *bufio.Reader

	wmu sync.Mutex
	bw  *bufio.Writer

	closeOnce sync.Once
	closed    chan struct{}

	writeTimeout time.Duration
}

// NewConn wraps an accepted or dialed TCP connection.
func NewConn(c net.Conn, writeTimeout time.Duration) *Conn {
	return &Conn{
		c:            c,
		br:           bufio.NewReaderSize(c, 16*1024),
		bw:           bufio.NewWriterSize(c, 16*1024),
		closed:       make(chan struct{}),
		writeTimeout: writeTimeout,
	}
}

// RemoteAddr reports the peer address, for logging and rate limiting.
func (c *Conn) RemoteAddr() net.Addr { return c.c.RemoteAddr() }

// Read blocks until the next envelope arrives or the deadline expires.
// A zero deadline means no timeout.
func (c *Conn) Read(deadline time.Time) (*pb.CMsgEnvelope, error) {
	if err := c.c.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	var hdr [4]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		// Desynchronised or hostile peer: the stream cannot be recovered.
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return nil, err
	}
	env := &pb.CMsgEnvelope{}
	if err := proto.Unmarshal(buf, env); err != nil {
		return nil, fmt.Errorf("wire: malformed envelope: %w", err)
	}
	return env, nil
}

// Write serializes and flushes one envelope.
func (c *Conn) Write(env *pb.CMsgEnvelope) error {
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	if env.SentAtMs == nil {
		env.SentAtMs = proto.Uint64(uint64(time.Now().UnixMilli()))
	}
	buf, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	if len(buf) > MaxFrameSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(buf))
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.writeTimeout > 0 {
		if err := c.c.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			return err
		}
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(buf)))
	if _, err := c.bw.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := c.bw.Write(buf); err != nil {
		return err
	}
	return c.bw.Flush()
}

// Close shuts the socket down once; further Writes return ErrClosed.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.c.Close()
	})
	return err
}

// Closed reports a channel that is closed when the connection is torn down.
func (c *Conn) Closed() <-chan struct{} { return c.closed }
