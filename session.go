package van

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"time"

	"github.com/reusee/closer"
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

	id         int64
	transports []Transport
	conns      map[int64]*Conn
	delConn    chan *Conn

	newTransport chan Transport
	errTransport chan Transport

	incomingPackets chan *Packet
	incomingAcks    chan *Packet

	outgoingPackets   chan *Packet
	outCheckTicker    *time.Ticker
	maxSendingPackets int
	sendingPackets    int

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
		Closer:            closer.NewCloser(),
		Signaler:          signaler.NewSignaler(),
		conns:             make(map[int64]*Conn),
		delConn:           make(chan *Conn),
		newTransport:      make(chan Transport, 128),
		errTransport:      make(chan Transport),
		incomingPackets:   make(chan *Packet),
		incomingAcks:      make(chan *Packet),
		outgoingPackets:   make(chan *Packet),
		maxSendingPackets: 1024,
		outCheckTicker:    time.NewTicker(time.Millisecond * 100),
		getTransportCount: make(chan int),
		getStatResend:     make(chan int),
	}
	session.OnClose(func() {
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
		packet := recv.(*Packet)
		s.handleIncomingPacket(packet)
		s.sendAck(packet)
	}, nil)
	selector.Add(s.incomingAcks, func(recv interface{}) {
		s.handleIncomingAck(recv.(*Packet))
	}, nil)
	s.outPacketCase = selector.Add(s.outgoingPackets, func(recv interface{}) {
		s.handleOutgoingPacket(recv.(*Packet))
	}, nil)
	selector.Add(s.outCheckTicker.C, func() {
		s.checkOutgoingPackets()
	}, nil)
	selector.Add(s.delConn, func(recv interface{}) {
		delete(s.conns, recv.(*Conn).id)
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

	// main loop
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
		var connId int64
		for {
			// read conn id
			err = binary.Read(transport, binary.LittleEndian, &connId)
			if err != nil {
				goto error_occur
			}
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
					connId: connId, // check conn in main thread, ensuring thread safety
					serial: serial,
					data:   data,
				}
			case ACK:
				err := binary.Read(transport, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				s.incomingAcks <- &Packet{
					connId: connId,
					serial: serial,
				}
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

func (s *Session) NewConn() *Conn {
	conn := s.makeConn()
	conn.id = rand.Int63()
	s.conns[conn.id] = conn
	conn.OnClose(func() {
		s.delConn <- conn
	})
	return conn
}

func (s *Session) handleOutgoingPacket(packet *Packet) {
	packet.conn.sendingPacketsMap[packet.serial] = packet
	packet.conn.sendingPacketsList.PushBack(packet)
	s.sendingPackets++
	if s.sendingPackets >= s.maxSendingPackets {
		s.outPacketCase.Disable()
	}
	s.sendPacket(packet)
}

func (s *Session) checkOutgoingPackets() {
	for _, conn := range s.conns {
		s.Log("checking conn %d, %d packets", conn.id, len(conn.sendingPacketsMap))
		now := time.Now()
		for e := conn.sendingPacketsList.Front(); e != nil; e = e.Next() {
			packet := e.Value.(*Packet)
			if packet.resendTimeout == 0 || packet.resendTimeout > 0 && packet.sentTime.Add(packet.resendTimeout).After(now) {
				s.Log("resend %d", packet.serial)
				s.sendPacket(packet)
			}
		}
	}
}

func (s *Session) sendPacket(packet *Packet) {
	// pack packet
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, packet.conn.id)
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

func (s *Session) sendAck(packet *Packet) {
	s.Log("send ack %d", packet.serial)
	// pack
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, packet.conn.id)
	buf.WriteByte(ACK)
	binary.Write(buf, binary.LittleEndian, packet.serial)
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
	conn, ok := s.conns[packet.connId]
	if !ok { // create new conn
		conn = s.makeConn()
		conn.id = packet.connId
		s.conns[conn.id] = conn
		conn.OnClose(func() {
			s.delConn <- conn
		})
		s.Signal("NewConn", conn)
		s.Log("NewConn from remote %d", conn.id)
	}
	packet.conn = conn
	s.Log("incoming data %d", packet.serial)
	if packet.serial == conn.ackSerial { // in order
		s.Log("in order %d", packet.serial)
		conn.recvIn <- packet.data
		conn.ackSerial++
	} else if packet.serial > conn.ackSerial { // out of order
		s.Log("out of order %d", packet.serial)
		heap.Push(conn.incomingHeap, packet)
	} else if packet.serial < conn.ackSerial { // duplicated
		s.Log("dup %d", packet.serial)
	}
	// try pop
	for conn.incomingHeap.Len() > 0 {
		packet := heap.Pop(conn.incomingHeap).(*Packet)
		if packet.serial == conn.ackSerial { // ready to provide
			s.Log("provide %d", packet.serial)
			conn.recvIn <- packet.data
			conn.ackSerial++
		} else if packet.serial < conn.ackSerial { // duplicated
			s.Log("dup %d", packet.serial)
		} else if packet.serial > conn.ackSerial { // not ready
			s.Log("not ready %d", packet.serial)
			heap.Push(conn.incomingHeap, packet)
			break
		}
	}
}

func (s *Session) handleIncomingAck(packet *Packet) {
	packet.conn = s.conns[packet.connId] // conn must be non-null here
	s.Log("incoming ack %d", packet.serial)
	conn := packet.conn
	if packet, ok := conn.sendingPacketsMap[packet.serial]; ok {
		packet.acked = true
		delete(conn.sendingPacketsMap, packet.serial)
		s.Signal("Ack " + strconv.Itoa(int(packet.serial)))
	}
	e := conn.sendingPacketsList.Front()
	for e != nil {
		packet := e.Value.(*Packet)
		if packet.acked {
			s.sendingPackets--
			if s.sendingPackets < s.maxSendingPackets {
				s.outPacketCase.Enable()
			}
			cur := e
			e = e.Next()
			conn.sendingPacketsList.Remove(cur)
		} else {
			break
		}
	}
}
