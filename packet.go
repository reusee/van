package van

import "time"

type Packet struct {
	conn *Conn

	serial uint32
	data   []byte

	index int // for heap

	sentTime          time.Time
	resendTimeout     time.Duration
	baseResendTimeout time.Duration

	acked bool
}

func (c *Conn) newPacket(data []byte) *Packet {
	packet := &Packet{
		conn:   c,
		serial: c.serial,
		data:   data,
	}
	c.serial++
	return packet
}
