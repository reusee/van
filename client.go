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
	session := makeSession()
	session.id = uint64(rand.Int63())
	client := &Client{
		Session:    session,
		remoteAddr: remoteAddr,
	}
	err := client.NewConn()
	if err != nil {
		return nil, err
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
	c.newConn <- conn
	return nil
}
