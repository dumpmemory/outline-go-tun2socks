package tunnel

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/Jigsaw-Code/outline-go-tun2socks/core"
)

type bridgeConn struct {
	endpoint    tcpip.Endpoint
	q           *waiter.Queue
	pending     []byte
	closedMu    sync.Mutex
	readClosed  bool
	writeClosed bool
}

func (c *bridgeConn) Read(b []byte) (int, error) {
	panic("Unimplemented") // io.Copy always prefers WriteTo
}

func (c *bridgeConn) WriteTo(w io.Writer) (n int64, err error) {
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	c.q.EventRegister(&waitEntry, waiter.EventIn)
	defer c.q.EventUnregister(&waitEntry)
	for {
		result, tcpErr := c.endpoint.Read(w, tcpip.ReadOptions{})
		if _, ok := tcpErr.(*tcpip.ErrWouldBlock); ok {
			<-notifyCh
			continue
		}
		n += int64(result.Total)
		if _, ok := tcpErr.(*tcpip.ErrClosedForReceive); ok {
			// EOF case.  Success.
			return
		} else if tcpErr != nil {
			err = errors.New(tcpErr.String())
			return
		}
	}
}

func (c *bridgeConn) Write(b []byte) (int, error) {
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	c.q.EventRegister(&waitEntry, waiter.EventOut)
	defer c.q.EventUnregister(&waitEntry)
	reader := bytes.NewReader(b)
	for {
		_, tcpErr := c.endpoint.Write(reader, tcpip.WriteOptions{Atomic: true})
		_, isErrWouldBlock := tcpErr.(*tcpip.ErrWouldBlock)
		if tcpErr != nil && !isErrWouldBlock {
			return len(b) - reader.Len(), errors.New(tcpErr.String()) // TODO EOF?
		} else if reader.Len() == 0 {
			return len(b), nil
		}
		<-notifyCh
	}
}

func (c *bridgeConn) Close() error {
	c.endpoint.Close()
	return nil
}

func (c *bridgeConn) LocalAddr() net.Addr {
	panic("not implemented") // TODO: Implement
}

func (c *bridgeConn) RemoteAddr() net.Addr {
	panic("not implemented") // TODO: Implement
}

func (c *bridgeConn) SetDeadline(t time.Time) error {
	panic("not implemented") // TODO: Implement
}

func (c *bridgeConn) SetReadDeadline(t time.Time) error {
	panic("not implemented") // TODO: Implement
}

func (c *bridgeConn) SetWriteDeadline(t time.Time) error {
	panic("not implemented") // TODO: Implement
}

func (c *bridgeConn) CloseRead() error {
	err := c.endpoint.Shutdown(tcpip.ShutdownRead)
	c.closedMu.Lock()
	c.readClosed = true
	if c.writeClosed {
		c.endpoint.Close()
	}
	c.closedMu.Unlock()
	if err != nil {
		return errors.New(err.String())
	}
	return nil
}

func (c *bridgeConn) CloseWrite() error {
	err := c.endpoint.Shutdown(tcpip.ShutdownWrite)
	c.closedMu.Lock()
	c.writeClosed = true
	if c.readClosed {
		c.endpoint.Close()
	}
	c.closedMu.Unlock()
	if err != nil {
		return errors.New(err.String())
	}
	return nil
}

func tcpbridge(handler core.TCPConnHandler, r *tcp.ForwarderRequest) {
	id := r.ID()
	dstAddr := net.TCPAddr{
		IP:   []byte(id.LocalAddress),
		Port: int(id.LocalPort),
	}
	var q waiter.Queue
	endpoint, stackerr := r.CreateEndpoint(&q) // SYNACK
	if stackerr != nil {
		log.Printf("Endpoint creation failed for request %v (%s): %v", r, dstAddr.String(), stackerr)
		return
	}

	conn := bridgeConn{endpoint: endpoint, q: &q}
	err := handler.Handle(&conn, &dstAddr)
	if err != nil {
		log.Printf("Proxying failed: %v", err)
	}
	sendRST := false
	r.Complete(sendRST)
}

func tcphandler(handler core.TCPConnHandler) func(*tcp.ForwarderRequest) {
	// The returned function will be run in a fresh goroutine for each incoming socket.
	return func(r *tcp.ForwarderRequest) {
		tcpbridge(handler, r)
	}
}

func MakeTCPBridge(s *stack.Stack, handler core.TCPConnHandler) *tcp.Forwarder {
	return tcp.NewForwarder(s, 0, 10, tcphandler(handler))
}
