package van

import (
	"bytes"
	"container/heap"
	"container/list"
	"encoding/binary"
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
	*Logger

	id    uint64
	conns []net.Conn

	newConnIn chan net.Conn
	newConn   chan net.Conn
	errConn   chan net.Conn

	incomingPacketsIn chan *Packet
	incomingPackets   chan *Packet
	incomingAcksIn    chan uint32
	incomingAcks      chan uint32

	getConnsLen chan int

	serial             uint32
	ackSerial          uint32
	sendingPacketsMap  map[uint32]*Packet
	sendingPacketsList *list.List
	outPacketsIn       chan *Packet
	outPackets         chan *Packet
	maxInflight        int
	inflight           int
	outCheckTicker     *time.Ticker

	incomingHeap *Heap
	recvIn       chan []byte
	Recv         chan []byte

	StatResend int
}

func makeSession() *Session {
	session := &Session{
		Closer:             closer.NewCloser(),
		Signaler:           NewSignaler(),
		Logger:             newLogger(),
		newConnIn:          make(chan net.Conn),
		newConn:            make(chan net.Conn),
		incomingPacketsIn:  make(chan *Packet),
		incomingPackets:    make(chan *Packet),
		incomingAcksIn:     make(chan uint32),
		incomingAcks:       make(chan uint32),
		getConnsLen:        make(chan int),
		sendingPacketsMap:  make(map[uint32]*Packet),
		sendingPacketsList: list.New(),
		outPacketsIn:       make(chan *Packet),
		outPackets:         make(chan *Packet),
		maxInflight:        2 * 1024 * 1024,
		outCheckTicker:     time.NewTicker(time.Millisecond * 100),
		incomingHeap:       new(Heap),
		recvIn:             make(chan []byte),
		Recv:               make(chan []byte),
	}
	heap.Init(session.incomingHeap)
	l1 := ic.Link(session.newConnIn, session.newConn)
	l2 := ic.Link(session.incomingPacketsIn, session.incomingPackets)
	l3 := ic.Link(session.outPacketsIn, session.outPackets)
	l4 := ic.Link(session.recvIn, session.Recv)
	l5 := ic.Link(session.incomingAcksIn, session.incomingAcks)
	session.OnClose(func() {
		close(l1)
		close(l2)
		close(l3)
		close(l4)
		close(l5)
		session.Logger.Close()
	})
	go session.start()
	return session
}

func (s *Session) start() {
	for {
		if s.maxInflight-s.inflight < 1024 { // inflight overflow
			s.Log("inflight overflow waiting %d acks", len(s.sendingPacketsMap))
			select {
			case <-s.WaitClosing: // exit
				return
			case conn := <-s.newConn: // new connection
				s.addConn(conn)
			case packet := <-s.incomingPackets: // incoming packet
				s.handleIncomingPacket(packet)
				s.sendAck(packet.serial)
			case ackSerial := <-s.incomingAcks: // incoming acks
				s.Log("ack %d", ackSerial)
				s.handleIncomingAck(ackSerial)
			case <-s.outCheckTicker.C: // check outgoing packet peroidly
				s.Log("check and send")
				s.checkSendingPackets()
			// getters
			case s.getConnsLen <- len(s.conns):
			}
		} else {
			select {
			case <-s.WaitClosing: // exit
				return
			case conn := <-s.newConn: // new connection
				s.addConn(conn)
			case packet := <-s.incomingPackets: // incoming packet
				s.handleIncomingPacket(packet)
				s.sendAck(packet.serial)
			case ackSerial := <-s.incomingAcks: // incoming acks
				s.Log("ack %d", ackSerial)
				s.handleIncomingAck(ackSerial)
			case packet := <-s.outPackets: // outgoing packet
				s.handleNewOutPacket(packet)
			case <-s.outCheckTicker.C: // check outgoing packet peroidly
				s.Log("check and send")
				s.checkSendingPackets()
			// getters
			case s.getConnsLen <- len(s.conns):
			}
		}
	}
}

func (s *Session) addConn(conn net.Conn) {
	s.conns = append(s.conns, conn)
	s.Signal("NewConn")
	// close conn when session close
	s.OnClose(func() {
		conn.Close()
	})
	// start reader
	go func() {
		var serial uint32
		var length uint16
		var packetType byte
		for {
			// read packet type
			err := binary.Read(conn, binary.LittleEndian, &packetType)
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
				s.incomingPacketsIn <- &Packet{
					serial: serial,
					data:   data,
				}
			case ACK:
				err := binary.Read(conn, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				s.incomingAcksIn <- serial
			}
		}
		return
	error_occur:
		if s.IsClosing { // session is closing
			return
		} else { // conn error
			s.errConn <- conn
			return
		}
	}()
}

func (s *Session) Send(data []byte) {
	packet := s.newPacket(data)
	s.outPacketsIn <- packet
}

func (s *Session) handleNewOutPacket(packet *Packet) {
	s.sendingPacketsMap[packet.serial] = packet
	s.sendingPacketsList.PushBack(packet)
	s.sendPacket(packet)
}

func (s *Session) checkSendingPackets() {
	now := time.Now()
	s.Log("checking %d packets", len(s.sendingPacketsMap))
	for _, packet := range s.sendingPacketsMap {
		// check timeout
		if packet.resendTimeout > 0 && now.Sub(packet.sentTime) < packet.resendTimeout {
			continue
		}
		s.sendPacket(packet)
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
		if s.IsClosing {
			return
		} else {
			s.errConn <- conn
			return
		}
	}
	// set inflight
	s.inflight += len(packet.data)
	// set packet
	packet.sentTime = time.Now()
	if packet.resendTimeout == 0 { // newly created packet
		packet.resendTimeout = time.Millisecond * 2000 // first resend timeout
	} else {
		packet.resendTimeout *= 2
		// stat
		s.StatResend++
	}
}

func (s *Session) sendAck(serial uint32) {
	// pack
	buf := new(bytes.Buffer)
	buf.WriteByte(ACK)
	binary.Write(buf, binary.LittleEndian, serial)
	// select conn
	conn := s.conns[rand.Intn(len(s.conns))]
	// write to conn
	n, err := conn.Write(buf.Bytes())
	if err != nil || n != len(buf.Bytes()) {
		if s.IsClosing {
			return
		} else {
			s.errConn <- conn
			return
		}
	}
}

func (s *Session) handleIncomingPacket(packet *Packet) {
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
	if packet, ok := s.sendingPacketsMap[ackSerial]; ok {
		packet.acked = true
		delete(s.sendingPacketsMap, ackSerial)
	}
	e := s.sendingPacketsList.Front()
	for e != nil {
		packet := e.Value.(*Packet)
		if packet.acked {
			s.Log("inflight reduce %d", len(packet.data))
			s.inflight -= len(packet.data)
			cur := e
			e = e.Next()
			s.sendingPacketsList.Remove(cur)
		} else {
			break
		}
	}
}
