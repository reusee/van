package van

import (
	"bytes"
	"container/list"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/reusee/closer"
	ic "github.com/reusee/inf-chan"
	se "github.com/reusee/selector"
	"github.com/reusee/signaler"
)

const (
	DATA = byte(1)
	ACK  = byte(2)
	FIN  = byte(4)
)

type Session struct {
	closer.Closer
	*signaler.Signaler

	id            int64
	transports    []Transport
	conns         map[uint32]*Conn
	closedConnIds *list.List

	newTransport chan Transport
	errTransport chan Transport

	incomingPackets    chan *Packet
	incomingPacketsMap map[string]*Packet
	incomingAcks       chan *Packet
	recvIn             chan *Packet
	Recv               chan *Packet

	outgoingPackets   chan *Packet
	outCheckTicker    *time.Ticker
	sendingPacketsMap map[string]*Packet
	outPacketCase     *se.Case

	// getters
	getTransportCount chan int
	getStatResend     chan int

	// statistics and debug
	debugEntries  []func() []string
	resentPackets int
	resentBytes   int
	inBytes       int
	outBytes      int
}

type connIdRange struct {
	left  uint32
	right uint32
}

type Conn struct {
	Id           uint32
	serial       uint32
	ackSerial    uint32
	localClosed  bool
	remoteClosed bool
	start        time.Time
	inBytes      int
	outBytes     int
}

type Packet struct {
	Conn   *Conn
	connId uint32

	Type   byte
	serial uint32
	Data   []byte

	resendAt time.Time
}

func makeSession() *Session {
	session := &Session{
		Closer:             closer.NewCloser(),
		Signaler:           signaler.NewSignaler(),
		conns:              make(map[uint32]*Conn),
		closedConnIds:      list.New(),
		newTransport:       make(chan Transport, 128),
		errTransport:       make(chan Transport),
		incomingPackets:    make(chan *Packet),
		incomingPacketsMap: make(map[string]*Packet),
		incomingAcks:       make(chan *Packet),
		recvIn:             make(chan *Packet),
		Recv:               make(chan *Packet),
		outgoingPackets:    make(chan *Packet),
		sendingPacketsMap:  make(map[string]*Packet),
		outCheckTicker:     time.NewTicker(time.Millisecond * 100),
		getTransportCount:  make(chan int),
		getStatResend:      make(chan int),
	}
	recvLink := ic.Link(session.recvIn, session.Recv)
	session.OnClose(func() {
		close(recvLink)
		session.CloseSignaler()
	})
	session.setDebugEntries()
	go session.start()
	return session
}

func (s *Session) Log(format string, args ...interface{}) {
	s.Signal("Log", fmt.Sprintf(format, args...))
}

func (s *Session) AddDebugEntry(cb func() []string) {
	s.debugEntries = append(s.debugEntries, cb)
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
		s.sendAck(packet)
		s.handleIncomingPacket(packet)
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
	// getters
	selector.Add(s.getTransportCount, nil, func() interface{} {
		return len(s.transports)
	})
	selector.Add(s.getStatResend, nil, func() interface{} {
		return s.resentPackets
	})

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
	transport.(*net.TCPConn).SetWriteBuffer(8 * 1024 * 1024)
	s.transports = append(s.transports, transport)
	s.Signal("NewTransport", len(s.transports))
	// start reader
	var err error
	go func() {
		var serial uint32
		var length uint16
		var packetType byte
		var connId uint32
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
					Type:   DATA,
					connId: connId, // check conn in main thread, ensuring thread safety
					serial: serial,
					Data:   data,
				}
			case FIN: // finish
				err = binary.Read(transport, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				s.incomingPackets <- &Packet{
					Type:   FIN,
					connId: connId,
					serial: serial,
				}
			case ACK:
				err := binary.Read(transport, binary.LittleEndian, &serial)
				if err != nil {
					goto error_occur
				}
				s.incomingAcks <- &Packet{
					Type:   ACK,
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

func (s *Session) makeConn() *Conn {
	conn := &Conn{
		start: time.Now(),
	}
	return conn
}

func (s *Session) closeConn(conn *Conn) {
	delete(s.conns, conn.Id)
	// insert id
	merged := false
	for e := s.closedConnIds.Front(); e != nil; e = e.Next() {
		r := e.Value.(*connIdRange)
		if conn.Id == r.left-1 {
			r.left = conn.Id
			merged = true
			break
		} else if conn.Id == r.right+1 {
			r.right = conn.Id
			merged = true
			// try merge next range
			if next := e.Next(); next != nil {
				nr := next.Value.(*connIdRange)
				if nr.left == r.right+1 {
					r.right = nr.right
					s.closedConnIds.Remove(next)
				}
			}
			break
		} else if conn.Id < r.left-1 {
			r := &connIdRange{
				left:  conn.Id,
				right: conn.Id,
			}
			s.closedConnIds.InsertBefore(r, e)
			merged = true
			break
		}
	}
	if !merged {
		r := &connIdRange{
			left:  conn.Id,
			right: conn.Id,
		}
		s.closedConnIds.PushBack(r)
	}
}

func (s *Session) handleOutgoingPacket(packet *Packet) {
	s.sendingPacketsMap[fmt.Sprintf("%d:%d", packet.Conn.Id, packet.serial)] = packet
	s.sendPacket(packet)
}

func (s *Session) checkOutgoingPackets() {
	now := time.Now()
	for _, packet := range s.sendingPacketsMap {
		if now.After(packet.resendAt) {
			s.Log("resend %d", packet.serial)
			s.sendPacket(packet)
		}
	}
}

func (s *Session) Send(conn *Conn, data []byte) uint32 {
	packet := &Packet{
		Type:   DATA,
		Conn:   conn,
		serial: conn.serial,
		Data:   data,
	}
	conn.serial++
	conn.outBytes += len(packet.Data)
	s.outBytes += len(packet.Data)
	s.outgoingPackets <- packet
	return packet.serial
}

func (s *Session) Finish(conn *Conn) {
	packet := &Packet{
		Type:   FIN,
		Conn:   conn,
		serial: conn.serial,
	}
	conn.serial++
	s.outgoingPackets <- packet
	conn.localClosed = true
	if conn.remoteClosed {
		s.closeConn(conn)
	}
}

func (s *Session) sendPacket(packet *Packet) {
	// check transport
	if len(s.transports) == 0 {
		return
	}
	// pack packet
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, packet.Conn.Id)
	buf.WriteByte(packet.Type)
	binary.Write(buf, binary.LittleEndian, packet.serial)
	if packet.Type == DATA {
		binary.Write(buf, binary.LittleEndian, uint16(len(packet.Data)))
		buf.Write(packet.Data)
	}
	// select transport
	transport := s.transports[0]
	if len(s.transports) > 0 {
		transport = s.transports[rand.Intn(len(s.transports))]
	}
	// write to transport
	s.Log("Start send through %v", transport)
	t0 := time.Now()
	transport.SetWriteDeadline(time.Now().Add(time.Second * 5))
	n, err := transport.Write(buf.Bytes())
	s.Log("Sent in %v", time.Now().Sub(t0))
	if err != nil || n != len(buf.Bytes()) {
		if !s.IsClosing {
			s.removeTransport(transport)
			return
		}
	}
	// stat
	if packet.resendAt.Year() > 1970 { // is resend
		s.resentPackets++
		s.resentBytes += len(packet.Data)
	}
	// set packet
	packet.resendAt = time.Now().Add(time.Second * 5)
}

func (s *Session) sendAck(packet *Packet) {
	// check transport
	if len(s.transports) == 0 {
		return
	}
	// pack
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, packet.connId)
	buf.WriteByte(ACK)
	binary.Write(buf, binary.LittleEndian, packet.serial)
	// select transport
	transport := s.transports[0]
	if len(s.transports) > 0 {
		transport = s.transports[rand.Intn(len(s.transports))]
	}
	// write to transport
	transport.SetWriteDeadline(time.Now().Add(time.Second * 5))
	n, err := transport.Write(buf.Bytes())
	if err != nil || n != len(buf.Bytes()) {
		if !s.IsClosing {
			s.removeTransport(transport)
		}
	}
}

func (s *Session) handleIncomingPacket(packet *Packet) {
	conn, ok := s.conns[packet.connId]
	if !ok {
		// check whether closed
		isClosed := false
		for e := s.closedConnIds.Back(); e != nil; e = e.Prev() {
			r := e.Value.(*connIdRange)
			if packet.connId > r.right {
				break
			}
			if packet.connId >= r.left && packet.connId <= r.right {
				isClosed = true
				break
			}
		}
		if isClosed {
			return
		}
		// create new incoming conn
		conn = s.makeConn()
		conn.Id = packet.connId
		s.conns[conn.Id] = conn
		s.Signal("NewConn", conn)
		s.Log("NewConn from remote %d", conn.Id)
	}
	packet.Conn = conn
	s.Log("incoming data %d", packet.serial)
	if packet.serial == conn.ackSerial { // in order
		s.Log("in order %d", packet.serial)
		s.recvIn <- packet
		conn.ackSerial++
		conn.inBytes += len(packet.Data)
		s.inBytes += len(packet.Data)
		if packet.Type == FIN {
			conn.remoteClosed = true
			if conn.localClosed { // close conn
				s.closeConn(conn)
			}
		}
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
		s.recvIn <- packet
		conn.ackSerial++
		conn.inBytes += len(packet.Data)
		s.inBytes += len(packet.Data)
		if packet.Type == FIN {
			conn.remoteClosed = true
			if conn.localClosed { // close conn
				s.closeConn(conn)
			}
		}
		delete(s.incomingPacketsMap, nextKey)
		nextKey = fmt.Sprintf("%d:%d", conn.Id, conn.ackSerial)
		packet, ok = s.incomingPacketsMap[nextKey]
	}
}

func (s *Session) handleIncomingAck(packet *Packet) {
	key := fmt.Sprintf("%d:%d", packet.connId, packet.serial)
	if _, ok := s.sendingPacketsMap[key]; ok {
		delete(s.sendingPacketsMap, key)
	}
}
