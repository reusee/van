package van

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"time"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
	se "github.com/reusee/selector"
	"github.com/reusee/signaler"
)

const (
	_DataPacket = byte(1)
	_AckPacket  = byte(2)

	DATA = iota
)

type Message struct {
	Type   int
	ConnId int64
	Data   []byte
}

type Session struct {
	closer.Closer
	*signaler.Signaler

	id         int64
	transports []Transport
	conns      map[int64]*Conn
	delConn    chan *Conn

	newTransport chan Transport
	errTransport chan Transport

	incomingPackets    chan *Packet
	incomingPacketsMap map[string]*Packet
	incomingAcks       chan *Packet
	recvIn             chan Message
	Recv               chan Message

	outgoingPackets   chan *Packet
	outCheckTicker    *time.Ticker
	maxSendingPackets int
	sendingPacketsMap map[string]*Packet

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
		Closer:             closer.NewCloser(),
		Signaler:           signaler.NewSignaler(),
		conns:              make(map[int64]*Conn),
		delConn:            make(chan *Conn),
		newTransport:       make(chan Transport, 128),
		errTransport:       make(chan Transport),
		incomingPackets:    make(chan *Packet),
		incomingPacketsMap: make(map[string]*Packet),
		incomingAcks:       make(chan *Packet),
		recvIn:             make(chan Message),
		Recv:               make(chan Message),
		outgoingPackets:    make(chan *Packet),
		sendingPacketsMap:  make(map[string]*Packet),
		maxSendingPackets:  1024,
		outCheckTicker:     time.NewTicker(time.Millisecond * 100),
		getTransportCount:  make(chan int),
		getStatResend:      make(chan int),
	}
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
		delete(s.conns, recv.(*Conn).Id)
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
			case _DataPacket: // data
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
			case _AckPacket:
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
	conn.Id = rand.Int63()
	s.conns[conn.Id] = conn
	conn.OnClose(func() {
		s.delConn <- conn
	})
	return conn
}

func (s *Session) handleOutgoingPacket(packet *Packet) {
	s.sendingPacketsMap[fmt.Sprintf("%d:%d", packet.conn.Id, packet.serial)] = packet
	if len(s.sendingPacketsMap) >= s.maxSendingPackets {
		s.outPacketCase.Disable()
	}
	s.sendPacket(packet)
}

func (s *Session) checkOutgoingPackets() {
	now := time.Now()
	for _, packet := range s.sendingPacketsMap {
		if packet.resendTimeout == 0 || packet.resendTimeout > 0 && packet.sentTime.Add(packet.resendTimeout).After(now) {
			s.Log("resend %d", packet.serial)
			s.sendPacket(packet)
		}
	}
}

func (s *Session) sendPacket(packet *Packet) {
	// pack packet
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, packet.conn.Id)
	buf.WriteByte(_DataPacket)
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
	binary.Write(buf, binary.LittleEndian, packet.conn.Id)
	buf.WriteByte(_AckPacket)
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
		conn.Id = packet.connId
		s.conns[conn.Id] = conn
		conn.OnClose(func() {
			s.delConn <- conn
		})
		s.Signal("NewConn", conn)
		s.Log("NewConn from remote %d", conn.Id)
	}
	packet.conn = conn
	s.Log("incoming data %d", packet.serial)
	if packet.serial == conn.ackSerial { // in order
		s.Log("in order %d", packet.serial)
		s.recvIn <- Message{
			Type:   DATA,
			ConnId: conn.Id,
			Data:   packet.data,
		}
		conn.ackSerial++
	} else if packet.serial > conn.ackSerial { // out of order
		s.Log("out of order %d", packet.serial)
		s.incomingPacketsMap[fmt.Sprintf("%d:%d", conn.Id, packet.serial)] = packet
	} else if packet.serial < conn.ackSerial { // duplicated
		s.Log("dup %d", packet.serial)
	}
	// try pop
	nextKey := fmt.Sprintf("%d:%d", conn.Id, conn.ackSerial)
	packet, ok = s.incomingPacketsMap[nextKey]
	for ok {
		s.Log("provide %d", packet.serial)
		s.recvIn <- Message{
			Type:   DATA,
			ConnId: conn.Id,
			Data:   packet.data,
		}
		conn.ackSerial++
		delete(s.incomingPacketsMap, nextKey)
		nextKey = fmt.Sprintf("%d:%d", conn.Id, conn.ackSerial)
		packet, ok = s.incomingPacketsMap[nextKey]
	}
}

func (s *Session) handleIncomingAck(packet *Packet) {
	packet.conn = s.conns[packet.connId] // conn must be non-null here
	s.Log("incoming ack %d", packet.serial)
	conn := packet.conn
	key := fmt.Sprintf("%d:%d", conn.Id, packet.serial)
	if _, ok := s.sendingPacketsMap[key]; ok {
		delete(s.sendingPacketsMap, key)
		if len(s.sendingPacketsMap) < s.maxSendingPackets {
			s.outPacketCase.Enable()
		}
	}
}
