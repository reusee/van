package van

import (
	"math/rand"
	"time"

	"net"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type Transport net.Conn
