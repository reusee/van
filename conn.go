package van

import (
	"container/heap"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

type Conn struct {
	closer.Closer
	session      *Session
	id           int64
	serial       uint32
	ackSerial    uint32
	incomingHeap *Heap
	recvIn       chan []byte
	Recv         chan []byte
}

func (s *Session) makeConn() *Conn {
	conn := &Conn{
		Closer:       closer.NewCloser(),
		session:      s,
		incomingHeap: new(Heap),
		recvIn:       make(chan []byte),
		Recv:         make(chan []byte),
	}
	heap.Init(conn.incomingHeap)
	recvLink := ic.Link(conn.recvIn, conn.Recv)
	conn.OnClose(func() {
		close(recvLink)
	})
	return conn
}

func (c *Conn) Send(data []byte) uint32 {
	packet := c.newPacket(data)
	c.session.outgoingPackets <- packet
	return packet.serial
}
