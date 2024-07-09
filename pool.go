package connectpool

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Option func(p *SimplePool)

// WithMaxIdleCnt 自定义最大空闲连接数量
func WithMaxIdleCnt(maxIdleCnt int32) Option {
	return func(p *SimplePool) {
		p.idleChan = make(chan conn, maxIdleCnt)
	}
}

// WithMaxCnt 自定义最大连接数量
func WithMaxCnt(maxCnt int32) Option {
	return func(p *SimplePool) {
		p.maxCnt = maxCnt
	}
}

func WithPingFunc(ping func(net.Conn) error) Option {
	return func(p *SimplePool) {
		p.ping = ping
	}
}

type conn struct {
	c          net.Conn
	lastActive time.Time
}

type conReq struct {
	connChan chan conn
}

type SimplePool struct {
	idleChan    chan conn
	waitChan    chan *conReq
	maxCnt      int32
	cnt         int32
	idleTimeout time.Duration
	factory     func() (net.Conn, error)
	// 检查连接是否有效的方法
	ping func(net.Conn) error
	lock sync.Mutex
}

func NewSimplePool(factory func() (net.Conn, error), opts ...Option) *SimplePool {
	res := &SimplePool{
		idleChan: make(chan conn, 16),
		waitChan: make(chan *conReq, 128),
		factory:  factory,
		//ping:     ping,
		maxCnt: 128,
	}
	for _, opt := range opts {
		opt(res)
	}
	return res
}

func (p *SimplePool) Get(ctx context.Context) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	for {
		select {
		case c := <-p.idleChan:
			if c.lastActive.Add(p.idleTimeout).Before(time.Now()) {
				atomic.AddInt32(&p.cnt, -1)
				_ = c.c.Close()
				continue
			}
			// ping 检查单条连接是否有效
			if err := p.ping(c.c); err != nil {
				atomic.AddInt32(&p.cnt, -1)
				_ = c.c.Close()
				continue
			}
			return c.c, nil
		default:
			cnt := atomic.AddInt32(&p.cnt, 1)
			if cnt <= p.maxCnt {
				return p.factory()
			}
			atomic.AddInt32(&p.cnt, -1)
			req := &conReq{
				connChan: make(chan conn, 1),
			}
			// 可能阻塞在这两句，对应不同的情况。
			// 所以实际上 waitChan 根本不需要设计很大的容量
			// 另外，这里需不需要加锁？
			p.waitChan <- req
			select {
			case <-ctx.Done():
				// 选项1：从队列里面删掉 req 自己
				// 选项2：在这里转发
				go func() {
					c := <-req.connChan
					_ = p.Put(ctx, c.c)
				}()
				return nil, ctx.Err()
			case c := <-req.connChan:
				return c.c, nil
			}
		}
	}
}

func (p *SimplePool) Put(ctx context.Context, c net.Conn) error {
	p.lock.Lock()
	if len(p.waitChan) > 0 {
		req := <-p.waitChan
		p.lock.Unlock()
		req.connChan <- conn{c: c, lastActive: time.Now()}
		return nil
	}
	p.lock.Unlock()
	select {
	case p.idleChan <- conn{c: c, lastActive: time.Now()}:
	default:
		defer func() {
			atomic.AddInt32(&p.cnt, -1)
		}()
		_ = c.Close()
	}
	return nil
}
