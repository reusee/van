package van

import (
	"bytes"
	"container/heap"
	"container/list"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
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

	id    uint64
	conns []net.Conn

	newConn chan net.Conn
	errConn chan net.Conn

	incomingPackets chan *Packet
	incomingAcks    chan uint32

	serial             uint32
	ackSerial          uint32
	sendingPacketsMap  map[uint32]*Packet
	sendingPacketsList *list.List
	outPackets         chan *Packet
	maxSendingBytes    int
	sendingBytes       int
	maxSendingPackets  int
	sendingPackets     int
	outCheckTicker     *time.Ticker

	incomingHeap *Heap
	recvIn       chan []byte
	Recv         chan []byte

	statResend int

	// getters
	getConnsLen   chan int
	getStatResend chan int
	// setters
	SetMaxSendingBytes   chan int
	SetMaxSendingPackets chan int

	outPacketCase *se.Case
}

func makeSession() *Session {
	session := &Session{
		Closer:               closer.NewCloser(),
		Signaler:             signaler.NewSignaler(),
		newConn:              make(chan net.Conn, 128),
		errConn:              make(chan net.Conn),
		incomingPackets:      make(chan *Packet),
		incomingAcks:         make(chan uint32),
		sendingPacketsMap:    make(map[uint32]*Packet),
		sendingPacketsList:   list.New(),
		outPackets:           make(chan *Packet),
		maxSendingBytes:      8 * 1024,
		maxSendingPackets:    128,
		outCheckTicker:       time.NewTicker(time.Millisecond * 100),
		incomingHeap:         new(Heap),
		recvIn:               make(chan []byte),
		Recv:                 make(chan []byte),
		getConnsLen:          make(chan int),
		getStatResend:        make(chan int),
		SetMaxSendingBytes:   make(chan int),
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
	selector.Add(s.newConn, func(recv interface{}) {
		s.addConn(recv.(net.Conn))
	}, nil)
	selector.Add(s.errConn, func(recv interface{}) {
		s.delConn(recv.(net.Conn))
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
	selector.Add(s.getConnsLen, nil, func() interface{} {
		return len(s.conns)
	})
	selector.Add(s.getStatResend, nil, func() interface{} {
		return s.statResend
	})
	// setters
	selector.Add(s.SetMaxSendingBytes, func(recv interface{}) {
		s.maxSendingBytes = recv.(int)
	}, nil)
	selector.Add(s.SetMaxSendingPackets, func(recv interface{}) {
		s.maxSendingPackets = recv.(int)
	}, nil)

	for !closing {
		selector.Select()
	}

	//clear:
	for _, c := range s.conns {
		c.Close()
	}
}

func (s *Session) addConn(conn net.Conn) {
	s.conns = append(s.conns, conn)
	s.Signal("NewConn", len(s.conns))
	// start reader
	var err error
	go func() {
		var serial uint32
		var length uint16
		var packetType byte
		for {
			// read packet type
			err = binary.Read(conn, binary.LittleEndian, &packetType)
			if err != nil {
				goto error_occur
			}
			// data or ack
			switch packetType {
			case DATA: // data
				err = binary.Read(conn, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				err = binary.Read(conn, binary.LittleEndian, &length)
				if err != nil {
					goto error_occur
				}
				data := make([]byte, length)
				n, err := io.ReadFull(conn, data)
				if err != nil || n != int(length) {
					goto error_occur
				}
				s.incomingPackets <- &Packet{
					serial: serial,
					data:   data,
				}
			case ACK:
				err := binary.Read(conn, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				s.incomingAcks <- serial
			}
		}
		return
	error_occur:
		if !s.IsClosing { // conn error
			s.errConn <- conn
		}
		return
	}()
}

func (s *Session) delConn(conn net.Conn) {
	s.Log("conn error")
	index := -1
	for i, c := range s.conns {
		if c == conn {
			index = i
			break
		}
	}
	if index > 0 { // delete
		s.Log("delete conn")
		s.conns[index].Close()
		s.Signal("DelConn", len(s.conns))
		s.conns = append(s.conns[:index], s.conns[index+1:]...)
	}
	if len(s.conns) == 0 {
		panic("No conns. fixme") //TODO
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
	s.sendingBytes += len(packet.data)
	s.sendingPackets++
	if s.sendingPackets >= s.maxSendingPackets || s.sendingBytes >= s.maxSendingBytes {
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
	// select conn
	conn := s.conns[rand.Intn(len(s.conns))]
	// write to conn
	n, err := conn.Write(buf.Bytes())
	if err != nil || n != len(buf.Bytes()) {
		if !s.IsClosing {
			s.delConn(conn)
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
	// select conn
	conn := s.conns[rand.Intn(len(s.conns))]
	// write to conn
	n, err := conn.Write(buf.Bytes())
	if err != nil || n != len(buf.Bytes()) {
		if !s.IsClosing {
			s.delConn(conn)
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
			s.sendingBytes -= len(packet.data)
			s.sendingPackets--
			if s.sendingBytes < s.maxSendingBytes && s.sendingPackets < s.maxSendingPackets {
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
