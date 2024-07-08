package connectpool

import (
	"net"
	"sync"
	"time"
)

type SimplePool struct {
	idleChan    chan conn
	waitChan    *conReq
	maxCnt      int
	cnt         int
	idleTimeout time.Duration
	factory     func() (net.Conn, error)
	lock        sync.Mutex
}

type conn struct {
	c          net.Conn
	lastActive time.Time
}

type conReq struct {
	connChan chan conn
}
