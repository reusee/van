package van

import (
	"bytes"
	"container/heap"
	"container/list"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"time"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
	se "github.com/reusee/selector"
	"github.com/reusee/signaler"
)

const (
	DATA = byte(1)
	ACK  = byte(2)
)

type Session struct {
	closer.Closer
	*signaler.Signaler

	id         uint64
	transports []Transport

	newTransport chan Transport
	errTransport chan Transport

	incomingPackets chan *Packet
	incomingAcks    chan uint32

	serial             uint32
	ackSerial          uint32
	sendingPacketsMap  map[uint32]*Packet
	sendingPacketsList *list.List
	outPackets         chan *Packet
	maxSendingPackets  int
	sendingPackets     int
	outCheckTicker     *time.Ticker

	incomingHeap *Heap
	recvIn       chan []byte
	Recv         chan []byte

	statResend int

	// getters
	getTransportCount chan int
	getStatResend     chan int
	// setters
	SetMaxSendingPackets chan int

	outPacketCase *se.Case
}

func makeSession() *Session {
	session := &Session{
		Closer:               closer.NewCloser(),
		Signaler:             signaler.NewSignaler(),
		newTransport:         make(chan Transport, 128),
		errTransport:         make(chan Transport),
		incomingPackets:      make(chan *Packet),
		incomingAcks:         make(chan uint32),
		sendingPacketsMap:    make(map[uint32]*Packet),
		sendingPacketsList:   list.New(),
		outPackets:           make(chan *Packet),
		maxSendingPackets:    1024,
		outCheckTicker:       time.NewTicker(time.Millisecond * 100),
		incomingHeap:         new(Heap),
		recvIn:               make(chan []byte),
		Recv:                 make(chan []byte),
		getTransportCount:    make(chan int),
		getStatResend:        make(chan int),
		SetMaxSendingPackets: make(chan int),
	}
	heap.Init(session.incomingHeap)
	recvLink := ic.Link(session.recvIn, session.Recv)
	session.OnClose(func() {
		close(recvLink)
		session.CloseSignaler()
	})
	go session.start()
	return session
}

func (s *Session) Log(format string, args ...interface{}) {
	s.Signal("Log", fmt.Sprintf(format, args...))
}

func (s *Session) start() {
	var closing bool
	selector := se.New()
	selector.Add(s.WaitClosing, func() {
		closing = true
	}, nil)
	selector.Add(s.newTransport, func(recv interface{}) {
		s.addTransport(recv.(Transport))
	}, nil)
	selector.Add(s.errTransport, func(recv interface{}) {
		s.removeTransport(recv.(Transport))
	}, nil)
	selector.Add(s.incomingPackets, func(recv interface{}) {
		s.handleIncomingPacket(recv.(*Packet))
		s.sendAck(recv.(*Packet).serial)
	}, nil)
	selector.Add(s.incomingAcks, func(recv interface{}) {
		s.handleIncomingAck(recv.(uint32))
	}, nil)
	s.outPacketCase = selector.Add(s.outPackets, func(recv interface{}) {
		s.handleNewOutPacket(recv.(*Packet))
	}, nil)
	selector.Add(s.outCheckTicker.C, func() {
		s.checkSendingPackets()
	}, nil)
	// getters
	selector.Add(s.getTransportCount, nil, func() interface{} {
		return len(s.transports)
	})
	selector.Add(s.getStatResend, nil, func() interface{} {
		return s.statResend
	})
	// setters
	selector.Add(s.SetMaxSendingPackets, func(recv interface{}) {
		s.maxSendingPackets = recv.(int)
	}, nil)

	for !closing {
		selector.Select()
	}

	//clear:
	for _, c := range s.transports {
		c.Close()
	}
}

func (s *Session) addTransport(transport Transport) {
	s.transports = append(s.transports, transport)
	s.Signal("NewTransport", len(s.transports))
	// start reader
	var err error
	go func() {
		var serial uint32
		var length uint16
		var packetType byte
		for {
			// read packet type
			err = binary.Read(transport, binary.LittleEndian, &packetType)
			if err != nil {
				goto error_occur
			}
			// data or ack
			switch packetType {
			case DATA: // data
				err = binary.Read(transport, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				err = binary.Read(transport, binary.LittleEndian, &length)
				if err != nil {
					goto error_occur
				}
				data := make([]byte, length)
				n, err := io.ReadFull(transport, data)
				if err != nil || n != int(length) {
					goto error_occur
				}
				s.incomingPackets <- &Packet{
					serial: serial,
					data:   data,
				}
			case ACK:
				err := binary.Read(transport, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				s.incomingAcks <- serial
			}
		}
		return
	error_occur:
		if !s.IsClosing { // transport error
			s.errTransport <- transport
		}
		return
	}()
}

func (s *Session) removeTransport(transport Transport) {
	s.Log("transport error")
	index := -1
	for i, c := range s.transports {
		if c == transport {
			index = i
			break
		}
	}
	if index > 0 { // delete
		s.Log("remove transport")
		s.transports[index].Close()
		s.Signal("RemoveTransport", len(s.transports))
		s.transports = append(s.transports[:index], s.transports[index+1:]...)
	}
	if len(s.transports) == 0 {
		panic("No transport. fixme") //TODO
	}
}

func (s *Session) Send(data []byte) uint32 {
	packet := s.newPacket(data)
	s.outPackets <- packet
	return packet.serial
}

func (s *Session) handleNewOutPacket(packet *Packet) {
	s.sendingPacketsMap[packet.serial] = packet
	s.sendingPacketsList.PushBack(packet)
	s.sendingPackets++
	if s.sendingPackets >= s.maxSendingPackets {
		s.outPacketCase.Disable()
	}
	s.sendPacket(packet)
}

func (s *Session) checkSendingPackets() {
	s.Log("checking %d packets", len(s.sendingPacketsMap))
	now := time.Now()
	for e := s.sendingPacketsList.Front(); e != nil; e = e.Next() {
		packet := e.Value.(*Packet)
		if packet.resendTimeout == 0 || packet.resendTimeout > 0 && packet.sentTime.Add(packet.resendTimeout).After(now) {
			s.Log("resend %d", packet.serial)
			s.sendPacket(packet)
		}
	}
}

func (s *Session) sendPacket(packet *Packet) {
	// pack packet
	buf := new(bytes.Buffer)
	buf.WriteByte(DATA)
	binary.Write(buf, binary.LittleEndian, packet.serial)
	binary.Write(buf, binary.LittleEndian, uint16(len(packet.data)))
	buf.Write(packet.data)
	// select transport
	transport := s.transports[rand.Intn(len(s.transports))]
	// write to transport
	n, err := transport.Write(buf.Bytes())
	if err != nil || n != len(buf.Bytes()) {
		if !s.IsClosing {
			s.removeTransport(transport)
			return
		}
	}
	// set packet
	packet.sentTime = time.Now()
	if packet.resendTimeout == 0 { // newly created packet
		packet.resendTimeout = time.Millisecond * 500 // first resend timeout
		packet.baseResendTimeout = time.Millisecond * 500
	} else {
		packet.baseResendTimeout *= 2
		packet.resendTimeout = packet.baseResendTimeout + time.Millisecond*time.Duration(rand.Intn(500))
		// stat
		s.statResend++
	}
}

func (s *Session) sendAck(serial uint32) {
	s.Log("send ack %d", serial)
	// pack
	buf := new(bytes.Buffer)
	buf.WriteByte(ACK)
	binary.Write(buf, binary.LittleEndian, serial)
	// select transport
	transport := s.transports[rand.Intn(len(s.transports))]
	// write to transport
	n, err := transport.Write(buf.Bytes())
	if err != nil || n != len(buf.Bytes()) {
		if !s.IsClosing {
			s.removeTransport(transport)
		}
	}
}

func (s *Session) handleIncomingPacket(packet *Packet) {
	s.Log("incoming data %d", packet.serial)
	if packet.serial == s.ackSerial { // in order
		s.Log("in order %d", packet.serial)
		s.recvIn <- packet.data
		s.ackSerial++
	} else if packet.serial > s.ackSerial { // out of order
		s.Log("out of order %d", packet.serial)
		heap.Push(s.incomingHeap, packet)
	} else if packet.serial < s.ackSerial { // duplicated
		s.Log("dup %d", packet.serial)
	}
	// try pop
	for s.incomingHeap.Len() > 0 {
		packet := heap.Pop(s.incomingHeap).(*Packet)
		if packet.serial == s.ackSerial { // ready to provide
			s.Log("provide %d", packet.serial)
			s.recvIn <- packet.data
			s.ackSerial++
		} else if packet.serial < s.ackSerial { // duplicated
			s.Log("dup %d", packet.serial)
		} else if packet.serial > s.ackSerial { // not ready
			s.Log("not ready %d", packet.serial)
			heap.Push(s.incomingHeap, packet)
			break
		}
	}
}

func (s *Session) handleIncomingAck(ackSerial uint32) {
	s.Log("incoming ack %d", ackSerial)
	if packet, ok := s.sendingPacketsMap[ackSerial]; ok {
		packet.acked = true
		delete(s.sendingPacketsMap, ackSerial)
		s.Signal("Ack " + strconv.Itoa(int(packet.serial)))
	}
	e := s.sendingPacketsList.Front()
	for e != nil {
		packet := e.Value.(*Packet)
		if packet.acked {
			s.sendingPackets--
			if s.sendingPackets < s.maxSendingPackets {
				s.outPacketCase.Enable()
			}
			cur := e
			e = e.Next()
			s.sendingPacketsList.Remove(cur)
		} else {
			break
		}
	}
}
