package van

import (
	"encoding/binary"
	"math/rand"
	"net"
	"sync"
)

type Client struct {
	*Session
	remoteAddr string
	lock       sync.Mutex
	nextConnId uint32
}

func NewClient(remoteAddr string) (*Client, error) {
	session := makeSession()
	session.id = rand.Int63()
	client := &Client{
		Session:    session,
		remoteAddr: remoteAddr,
		nextConnId: 1,
	}
	err := client.NewTransport()
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) NewTransport() error {
	// dial
	transport, err := net.Dial("tcp", c.remoteAddr)
	if err != nil {
		return err
	}
	// send session id
	err = binary.Write(transport, binary.LittleEndian, c.id)
	if err != nil {
		return err
	}
	c.newTransport <- transport
	return nil
}

func (c *Client) CloseRandomTransport() {
	c.lock.Lock()
	if len(c.transports) > 1 {
		c.transports[rand.Intn(len(c.transports))].Close()
	}
	c.lock.Unlock()
}

func (c *Client) NewConn() *Conn {
	conn := c.makeConn()
	conn.Id = c.nextConnId
	c.nextConnId++
	c.conns[conn.Id] = conn
	conn.OnClose(func() {
		c.delConn <- conn
	})
	return conn
}
