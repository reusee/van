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
	"time"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
)

const (
	DATA = byte(1)
	ACK  = byte(2)
)

type Session struct {
	closer.Closer
	*Signaler

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
	outCheckTicker     *time.Ticker

	incomingHeap *Heap
	recvIn       chan []byte
	Recv         chan []byte

	statResend int

	// getters
	getConnsLen   chan int
	getStatResend chan int
	// setters
	SetMaxSendingBytes chan int
}

func makeSession() *Session {
	session := &Session{
		Closer:             closer.NewCloser(),
		Signaler:           NewSignaler(),
		newConn:            make(chan net.Conn, 128),
		errConn:            make(chan net.Conn),
		incomingPackets:    make(chan *Packet),
		incomingAcks:       make(chan uint32),
		sendingPacketsMap:  make(map[uint32]*Packet),
		sendingPacketsList: list.New(),
		outPackets:         make(chan *Packet),
		maxSendingBytes:    8 * 1024,
		outCheckTicker:     time.NewTicker(time.Millisecond * 100),
		incomingHeap:       new(Heap),
		recvIn:             make(chan []byte),
		Recv:               make(chan []byte),
		getConnsLen:        make(chan int),
		getStatResend:      make(chan int),
		SetMaxSendingBytes: make(chan int),
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
	for {
		s.Log("Select")
		if s.maxSendingBytes-s.sendingBytes < 1024 { // buffer full
			s.Log("buffer full waiting %d acks", len(s.sendingPacketsMap))
			select {
			case <-s.WaitClosing: // exit
				goto clear
			case conn := <-s.newConn: // new connection
				s.addConn(conn)
			case conn := <-s.errConn: // conn error
				s.delConn(conn)
			case packet := <-s.incomingPackets: // incoming packet
				s.handleIncomingPacket(packet)
				s.sendAck(packet.serial)
			case ackSerial := <-s.incomingAcks: // incoming acks
				s.handleIncomingAck(ackSerial)
			case <-s.outCheckTicker.C: // check outgoing packet peroidly
				s.checkSendingPackets()
			// getters
			case s.getConnsLen <- len(s.conns):
			case s.getStatResend <- s.statResend:
			// setters
			case n := <-s.SetMaxSendingBytes:
				s.maxSendingBytes = n
			}
		} else {
			select {
			case <-s.WaitClosing: // exit
				goto clear
			case conn := <-s.newConn: // new connection
				s.addConn(conn)
			case conn := <-s.errConn: // conn error
				s.delConn(conn)
			case packet := <-s.incomingPackets: // incoming packet
				s.handleIncomingPacket(packet)
				s.sendAck(packet.serial)
			case ackSerial := <-s.incomingAcks: // incoming acks
				s.handleIncomingAck(ackSerial)
			case packet := <-s.outPackets: // outgoing packet
				s.handleNewOutPacket(packet)
			case <-s.outCheckTicker.C: // check outgoing packet peroidly
				s.checkSendingPackets()
			// getters
			case s.getConnsLen <- len(s.conns):
			case s.getStatResend <- s.statResend:
			// setters
			case n := <-s.SetMaxSendingBytes:
				s.maxSendingBytes = n
			}
		}
	}
clear:
	for _, c := range s.conns {
		c.Close()
	}
}

func (s *Session) addConn(conn net.Conn) {
	s.conns = append(s.conns, conn)
	s.Signal("NewConn")
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
		s.Signal("DelConn")
		s.conns = append(s.conns[:index], s.conns[index+1:]...)
	}
	if len(s.conns) == 0 {
		panic("No conns. fixme") //TODO
	}
}

func (s *Session) Send(data []byte) {
	packet := s.newPacket(data)
	s.outPackets <- packet
}

func (s *Session) handleNewOutPacket(packet *Packet) {
	s.sendingPacketsMap[packet.serial] = packet
	s.sendingPacketsList.PushBack(packet)
	s.sendingBytes += len(packet.data)
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
		packet.resendTimeout = time.Millisecond * 1000 // first resend timeout
	} else {
		packet.resendTimeout *= 2
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
	}
	e := s.sendingPacketsList.Front()
	for e != nil {
		packet := e.Value.(*Packet)
		if packet.acked {
			s.sendingBytes -= len(packet.data)
			cur := e
			e = e.Next()
			s.sendingPacketsList.Remove(cur)
		} else {
			break
		}
	}
}
