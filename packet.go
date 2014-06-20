package van

import "time"

type Packet struct {
	serial uint32
	data   []byte

	index int // for heap

	sentTime          time.Time
	resendTimeout     time.Duration
	baseResendTimeout time.Duration

	acked bool
}

func (s *Session) newPacket(data []byte) *Packet {
	packet := &Packet{
		serial: s.serial,
		data:   data,
	}
	s.serial++
	return packet
}
