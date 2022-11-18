package nbio

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var addr = "127.0.0.1:8888"
var testfile = "test_tmp.file"
var gopher *Engine

func init() {
	if err := os.WriteFile(testfile, make([]byte, 1024*100), 0600); err != nil {
		log.Panicf("write file failed: %v", err)
	}

	addrs := []string{addr}
	g := NewEngine(Config{
		Network: "tcp",
		Addrs:   addrs,
	})

	g.OnOpen(func(c *Conn) {
		c.SetReadDeadline(time.Now().Add(time.Second * 10))
	})
	g.OnData(func(c *Conn, data []byte) {
		if len(data) == 8 && string(data) == "sendfile" {
			fd, err := os.Open(testfile)
			if err != nil {
				log.Panicf("open file failed: %v", err)
			}

			if _, err = c.Sendfile(fd, 0); err != nil {
				panic(err)
			}

			if err := fd.Close(); err != nil {
				log.Panicf("close file failed: %v", err)
			}
		} else {
			c.Write(append([]byte{}, data...))
		}
	})
	g.OnClose(func(c *Conn, err error) {})

	err := g.Start()
	if err != nil {
		log.Panicf("Start failed: %v\n", err)
	}

	gopher = g
}

func TestEcho(t *testing.T) {
	var done = make(chan int)
	var clientNum = 2
	var msgSize = 1024
	var total int64 = 0

	g := NewEngine(Config{})
	err := g.Start()
	if err != nil {
		log.Panicf("Start failed: %v\n", err)
	}
	defer g.Stop()

	g.OnOpen(func(c *Conn) {
		c.SetSession(1)
		if c.Session() != 1 {
			log.Panicf("invalid session: %v", c.Session())
		}
		c.SetLinger(1, 0)
		c.SetNoDelay(true)
		c.SetKeepAlive(true)
		c.SetKeepAlivePeriod(time.Second * 60)
		c.SetDeadline(time.Now().Add(time.Second))
		c.SetReadBuffer(1024 * 4)
		c.SetWriteBuffer(1024 * 4)
		log.Printf("connected, local addr: %v, remote addr: %v", c.LocalAddr(), c.RemoteAddr())
	})
	g.BeforeWrite(func(c *Conn) {
		c.SetWriteDeadline(time.Now().Add(time.Second * 5))
	})
	g.OnData(func(c *Conn, data []byte) {
		recved := atomic.AddInt64(&total, int64(len(data)))
		if len(data) > 0 && recved >= int64(clientNum*msgSize) {
			close(done)
		}
	})

	g.OnReadBufferAlloc(func(c *Conn) []byte {
		return make([]byte, 1024)
	})
	g.OnReadBufferFree(func(c *Conn, b []byte) {

	})

	one := func(n int) {
		c, err := Dial("tcp", addr)
		if err != nil {
			log.Panicf("Dial failed: %v", err)
		}
		g.AddConn(c)
		if n%2 == 0 {
			c.Writev([][]byte{make([]byte, msgSize)})
		} else {
			c.Write(make([]byte, msgSize))
		}
	}

	for i := 0; i < clientNum; i++ {
		if runtime.GOOS != "windows" {
			one(i)
		} else {
			go one(i)
		}
	}

	<-done
}

func TestSendfile(t *testing.T) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		panic(err)
	}

	buf := make([]byte, 1024*100)

	for i := 0; i < 3; i++ {
		if _, err := conn.Write([]byte("sendfile")); err != nil {
			log.Panicf("write 'sendfile' failed: %v", err)
		}

		if _, err := io.ReadFull(conn, buf); err != nil {
			log.Panicf("read file failed: %v", err)
		}
	}
}

func TestTimeout(t *testing.T) {
	g := NewEngine(Config{})
	err := g.Start()
	if err != nil {
		log.Panicf("Start failed: %v\n", err)
	}
	defer g.Stop()

	var done = make(chan int)
	var begin time.Time
	var timeout = time.Second
	g.OnOpen(func(c *Conn) {
		c.IsTCP()
		c.IsUDP()
		c.IsUnix()
		begin = time.Now()
		c.SetReadDeadline(begin.Add(timeout))
	})
	g.OnClose(func(c *Conn, err error) {
		to := time.Since(begin)
		if to > timeout*2 {
			log.Panicf("timeout: %v, want: %v", to, timeout)
		}
		close(done)
	})

	one := func() {
		c, err := DialTimeout("tcp", addr, time.Second)
		if err != nil {
			log.Panicf("Dial failed: %v", err)
		}
		g.AddConn(c)
	}
	one()

	<-done
}

func TestFuzz(t *testing.T) {
	wg := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				Dial("tcp4", addr)
			} else {
				Dial("tcp4", addr)
			}
		}(i)
	}

	wg.Wait()

	readed := 0
	wg2 := sync.WaitGroup{}
	wg2.Add(1)
	g := NewEngine(Config{NPoller: 1})
	g.OnData(func(c *Conn, data []byte) {
		readed += len(data)
		if readed == 4 {
			wg2.Done()
		}
	})
	err := g.Start()
	if err != nil {
		log.Panicf("Start failed: %v", err)
	}

	c, err := Dial("tcp", addr)
	if err == nil {
		log.Printf("Dial tcp4: %v, %v, %v", c.LocalAddr(), c.RemoteAddr(), err)
		g.AddConn(c)
		c.SetWriteDeadline(time.Now().Add(time.Second))
		c.Write([]byte{1})

		time.Sleep(time.Second / 10)

		bs := [][]byte{}
		bs = append(bs, []byte{2})
		bs = append(bs, []byte{3})
		bs = append(bs, []byte{4})
		c.Writev(bs)

		time.Sleep(time.Second / 10)

		c.Close()
		c.Write([]byte{1})
	} else {
		log.Panicf("Dial tcp4: %v", err)
	}

	gErr := NewEngine(Config{
		Network: "tcp4",
		Addrs:   []string{"127.0.0.1:8889", "127.0.0.1:8889"},
	})
	gErr.Start()
}

func TestUDP(t *testing.T) {
	g := NewEngine(Config{})
	timeout := time.Second / 5
	chTimeout := make(chan *Conn, 1)
	g.OnOpen(func(c *Conn) {
		log.Printf("onOpen: %v, %v", c.LocalAddr().String(), c.RemoteAddr().String())
		c.SetReadDeadline(time.Now().Add(timeout))
	})
	g.OnData(func(c *Conn, data []byte) {
		log.Println("onData:", c.LocalAddr().String(), c.RemoteAddr().String(), string(data))
		_, err := c.Write(data)
		if err != nil {
			t.Fatal(err)
		}
	})
	g.OnClose(func(c *Conn, err error) {
		log.Println("onClose:", c.RemoteAddr().String(), err)
		select {
		case chTimeout <- c:
		default:
		}
	})

	err := g.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer g.Stop()

	addrstr := fmt.Sprintf("127.0.0.1:%d", 9999)
	addr, err := net.ResolveUDPAddr("udp", addrstr)
	if err != nil {
		t.Fatalf("ResolveUDPAddr error: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}

	lisConn, _ := g.AddConn(conn)

	newClientConn := func() *net.UDPConn {
		connUDP, errDial := net.DialUDP("udp4", nil, &net.UDPAddr{
			IP:   net.IPv4(127, 0, 0, 1),
			Port: 9999,
		})
		if errDial != nil {
			t.Fatalf("net.DialUDP failed: %v", err)
		}
		return connUDP
	}

	connTimeout := newClientConn()
	n, err := connTimeout.Write([]byte("test timeout"))
	if err != nil {
		log.Fatalf("write udp failed: %v, %v", n, err)
	}
	defer connTimeout.Close()
	begin := time.Now()
	select {
	case c := <-chTimeout:
		if c.RemoteAddr().String() != connTimeout.LocalAddr().String() {
			log.Fatalf("invalid udp conn")
		}
		used := time.Since(begin)
		if used < timeout {
			log.Fatalf("test timeout failed: %v < %v", used.Seconds(), timeout.Seconds())
		}
		log.Printf("test udp conn timeout success")
	case <-time.After(timeout + time.Second):
		log.Fatalf("timeout")
	}

	clientNum := 2
	msgPerClient := 2
	wg := sync.WaitGroup{}
	for i := 0; i < clientNum; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			conn := newClientConn()
			defer conn.Close()
			for j := 0; j < msgPerClient; j++ {
				str := fmt.Sprintf("message-%d", clientNum*idx+j)
				wbuf := []byte(str)
				rbuf := make([]byte, 1024)
				if _, werr := conn.Write(wbuf); werr == nil {
					log.Printf("send msg success: %v, %s", conn.LocalAddr().String(), str)
					if packLen, _, rerr := conn.ReadFromUDP(rbuf); rerr == nil {
						if str != string(wbuf[:packLen]) {
							log.Fatalf("recv msg not equal: %v, [%v != %v]", conn.LocalAddr().String(), str, string(rbuf[:packLen]))
						}
						log.Printf("recv msg success: %v, %s", conn.LocalAddr().String(), str)
					} else {
						log.Printf("recv msg failed: %v, %v", conn.LocalAddr().String(), rerr)
					}
				} else {
					log.Println("send msg failed:", werr)
				}
			}
		}(i)
	}
	wg.Wait()

	var done = make(chan int)
	var cntFromServer int32
	var fromClientStr = "from client"
	var fromServerStr = "from server"
	g.OnOpen(func(c *Conn) {
		c.IsTCP()
		c.IsUDP()
		c.IsUnix()
		log.Println("onOpen:", c.LocalAddr().String(), c.RemoteAddr().String())
		c.SetReadDeadline(time.Now().Add(timeout))
	})
	g.OnData(func(c *Conn, data []byte) {
		log.Println("OnData:", c.LocalAddr().String(), c.RemoteAddr().String(), string(data))
		if string(data) == fromClientStr {
			c.Write([]byte(fromServerStr))
		} else {
			if atomic.AddInt32(&cntFromServer, 1) == 3 {
				c.Close()
			} else {
				c.Write([]byte(fromClientStr))
			}
		}
	})
	clientConn := newClientConn()
	nbc, err := g.AddConn(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	g.OnClose(func(c *Conn, err error) {
		log.Println("onClose:", c.LocalAddr().String(), c.RemoteAddr().String(), err)
		if nbc == c {
			close(done)
		}
	})
	nbc.Write([]byte(fromClientStr))
	<-done
	lisConn.Close()
	time.Sleep(timeout * 2)
}

func TestUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}

	unixAddr := "./test.unix"
	defer os.Remove(unixAddr)
	g := NewEngine(Config{
		Network: "unix",
		Addrs:   []string{unixAddr},
	})
	var connSvr *Conn
	var connCli *Conn
	g.OnOpen(func(c *Conn) {
		if connSvr == nil {
			connSvr = c
		}
		c.Type()
		c.IsTCP()
		c.IsUDP()
		c.IsUnix()
		log.Printf("unix onOpen: %v, %v", c.LocalAddr().String(), c.RemoteAddr().String())
	})
	g.OnData(func(c *Conn, data []byte) {
		log.Println("unix onData:", c.LocalAddr().String(), c.RemoteAddr().String(), string(data))
		if c == connSvr {
			_, err := c.Write([]byte("world"))
			if err != nil {
				t.Fatal(err)
			}
		}
		if c == connCli && string(data) == "world" {
			c.Close()
		}
	})
	chClose := make(chan *Conn, 2)
	g.OnClose(func(c *Conn, err error) {
		log.Println("unix onClose:", c.LocalAddr().String(), c.RemoteAddr().String(), err)
		chClose <- c
	})

	err := g.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer g.Stop()

	c, err := net.Dial("unix", unixAddr)
	if err != nil {
		t.Fatalf("unix Dial: %v, %v, %v", c.LocalAddr(), c.RemoteAddr(), err)
	}
	defer c.Close()
	time.Sleep(time.Second / 10)
	buf := []byte("hello")
	connCli, err = g.AddConn(c)
	if err != nil {
		t.Fatalf("unix AddConn: %v, %v, %v", c.LocalAddr(), c.RemoteAddr(), err)
	}
	_, err = connCli.Write(buf)
	if err != nil {
		t.Fatalf("unix Write: %v, %v, %v", c.LocalAddr(), c.RemoteAddr(), err)
	}
	<-chClose
	<-chClose
}

func TestStop(t *testing.T) {
	gopher.Stop()
	os.Remove(testfile)
}
