package van

import (
	"encoding/binary"
	"math/rand"
	"net"
)

type Client struct {
	*Session
	remoteAddr string
}

func NewClient(remoteAddr string) (*Client, error) {
	// dial
	conn, err := net.Dial("tcp", remoteAddr)
	if err != nil {
		return nil, err
	}
	// send session id
	sessionId := uint64(rand.Int63())
	err = binary.Write(conn, binary.LittleEndian, sessionId)
	if err != nil {
		return nil, err
	}
	// session
	session := makeSession()
	session.id = sessionId
	session.newConnIn <- conn
	client := &Client{
		Session:    session,
		remoteAddr: remoteAddr,
	}
	return client, nil
}

func (c *Client) NewConn() error {
	// dial
	conn, err := net.Dial("tcp", c.remoteAddr)
	if err != nil {
		return err
	}
	// send session id
	err = binary.Write(conn, binary.LittleEndian, c.id)
	if err != nil {
		return err
	}
	c.newConnIn <- conn
	return nil
}
