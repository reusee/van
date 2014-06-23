package van

import "github.com/reusee/closer"

type Conn struct {
	closer.Closer
	session   *Session
	Id        int64
	serial    uint32
	ackSerial uint32
}

func (s *Session) makeConn() *Conn {
	conn := &Conn{
		Closer:  closer.NewCloser(),
		session: s,
	}
	return conn
}

func (c *Conn) Send(data []byte) uint32 {
	packet := c.newPacket(data)
	c.session.outgoingPackets <- packet
	return packet.serial
}
