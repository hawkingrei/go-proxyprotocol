package proxyprotocol

import (
	"bytes"
	"encoding/binary"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"
)

type mockBufferConn struct {
	*bytes.Buffer
	raddr net.Addr
}

func newMockBufferConn(buffer *bytes.Buffer, raddr net.Addr) net.Conn {
	return &mockBufferConn{
		Buffer: buffer,
		raddr:  raddr,
	}
}

func newMockBufferConnBytes(buffer []byte, raddr net.Addr) *mockBufferConn {
	return &mockBufferConn{
		Buffer: bytes.NewBuffer(buffer),
		raddr:  raddr,
	}
}

func (c *mockBufferConn) Close() error {
	return nil
}

func (c *mockBufferConn) RemoteAddr() net.Addr {
	if c.raddr != nil {
		return c.raddr
	}
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:12345")
	return addr
}

func (c *mockBufferConn) LocalAddr() net.Addr {
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:4000")
	return addr
}

func (c *mockBufferConn) SetDeadline(t time.Time) error {
	return nil
}

func (c *mockBufferConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *mockBufferConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func assertTrue(t *testing.T, val bool) {
	if !val {
		t.Errorf("Expect true but got: %v", val)
	}
}

func assertFalse(t *testing.T, val bool) {
	if val {
		t.Errorf("Expect false but got: %v", val)
	}
}

func assertNil(t *testing.T, val any) {
	isNil := val == nil || (reflect.ValueOf(val).Kind() == reflect.Ptr && reflect.ValueOf(val).IsNil())
	if !isNil {
		t.Errorf("Expect nil but got: %v", val)
	}
}

func assertEquals[T comparable](t *testing.T, val, expected T, comments ...any) {
	if val != expected {
		if len(comments) == 0 {
			t.Errorf("Expect %v but got: %v", expected, val)
		} else {
			t.Errorf(comments[0].(string), comments[0:]...)
		}
	}
}

func TestProxyProtocolConnCheckAllowed(t *testing.T) {
	l, _ := newListener(nil, "*", 5)
	raddr, _ := net.ResolveTCPAddr("tcp4", "192.168.1.100:8080")
	assertTrue(t, l.checkAllowed(raddr))
	l, _ = newListener(nil, "192.168.1.0/24,192.168.2.0/24", 5)
	for _, ipstr := range []string{"192.168.1.100:8080", "192.168.2.100:8080"} {
		raddr, _ := net.ResolveTCPAddr("tcp4", ipstr)
		assertTrue(t, l.checkAllowed(raddr))
	}
	for _, ipstr := range []string{"192.168.3.100:8080", "192.168.4.100:8080"} {
		raddr, _ := net.ResolveTCPAddr("tcp4", ipstr)
		assertFalse(t, l.checkAllowed(raddr))
	}
}

func TestProxyProtocolConnMustNotReadAnyDataAfterCLRF(t *testing.T) {
	buffer := []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306\r\nOther Data")
	conn := newMockBufferConn(bytes.NewBuffer(buffer), nil)

	l, _ := newListener(nil, "*", 5)
	wconn, err := l.createProxyProtocolConn(conn)
	assertNil(t, err)

	expectedString := "Other Data"
	buf := make([]byte, 10)
	n, _ := wconn.Read(buf)
	assertEquals(t, n, 10)
	assertEquals(t, string(buf[0:n]), expectedString)

	buffer = []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306\r\nOther Data")
	conn = newMockBufferConn(bytes.NewBuffer(buffer), nil)
	wconn, err = l.createProxyProtocolConn(conn)
	assertNil(t, err)
	buf = make([]byte, 5)
	n, err = wconn.Read(buf)
	assertNil(t, err)
	assertEquals(t, n, 5)
	assertEquals(t, string(buf[0:n]), "Other")
	n, err = wconn.Read(buf)
	assertNil(t, err)
	assertEquals(t, n, 5)
	assertEquals(t, string(buf[0:n]), " Data")

	buffer = []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306\r\nOther Data for a very long long long long long long long long long content")
	expectedString = "Other Data for a very long long long long long long long long long content"
	conn = newMockBufferConn(bytes.NewBuffer(buffer), nil)
	wconn, err = l.createProxyProtocolConn(conn)
	assertNil(t, err)
	buf = make([]byte, 1024)
	n, err = wconn.Read(buf)
	assertNil(t, err)
	assertEquals(t, string(buf[0:n]), expectedString)
}

func TestProxyProtocolV2ConnMustNotReadAnyDataAfterHeader(t *testing.T) {
	craddr, _ := net.ResolveTCPAddr("tcp4", "192.168.1.51:8080")
	buffer := encodeProxyProtocolV2Header("tcp4", "192.168.1.100:5678", "192.168.1.5:4000")
	expectedString := "Other Data"
	buffer = append(buffer, []byte(expectedString)...)
	l, _ := newListener(nil, "*", 5)
	conn := newMockBufferConn(bytes.NewBuffer(buffer), craddr)
	wconn, err := l.createProxyProtocolConn(conn)
	buf := make([]byte, len(expectedString))
	n, err := wconn.Read(buf)
	assertNil(t, err)
	assertEquals(t, string(buf[0:n]), expectedString)
}

func TestProxyProtocolV1HeaderRead(t *testing.T) {
	buffer := []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306\r\nOther Data")
	expectedString := "PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306\r\n"
	conn := newMockBufferConn(bytes.NewBuffer(buffer), nil)
	wconn := &proxyProtocolConn{
		Conn:              conn,
		headerReadTimeout: 5,
	}
	ver, buf, err := wconn.readHeader()
	assertNil(t, err)
	assertEquals(t, ver, proxyProtocolV1)
	assertEquals(t, string(buf), expectedString)
}

func TestProxyProtocolV1ExtractClientIP(t *testing.T) {
	craddr, _ := net.ResolveTCPAddr("tcp4", "192.168.1.51:8080")
	tests := []struct {
		buffer      []byte
		expectedIP  string
		expectedErr bool
	}{
		{
			buffer:      []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306\r\nOther Data"),
			expectedIP:  "192.168.1.100:5678",
			expectedErr: false,
		},
		{
			buffer:      []byte("PROXY UNKNOWN 192.168.1.100 192.168.1.50 5678 3306\r\n"),
			expectedIP:  "192.168.1.51:8080",
			expectedErr: false,
		},
		{
			buffer:      []byte("PROXY TCP 192.168.1.100 192.168.1.50 5678 3306 3307\r\n"),
			expectedIP:  "",
			expectedErr: true,
		},
		{
			buffer:      []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306 jkasdjfkljaksldfjklajsdkfjsklafjldsafa"),
			expectedIP:  "",
			expectedErr: true,
		},
		{
			buffer:      []byte("PROXY TCP4 192.168.1.100 192.168.1.50 5678 3306785478934785738275489275843728954782598345"),
			expectedIP:  "",
			expectedErr: true,
		},
		{
			buffer:      []byte("PROXY TCP6 2001:0db8:85a3:0000:0000:8a2e:0370:7334 2001:0db8:85a3:0000:0000:8a2e:0390:7334 5678 3306\r\n"),
			expectedIP:  "[2001:db8:85a3::8a2e:370:7334]:5678",
			expectedErr: false,
		},
		{
			buffer:      []byte("this is a invalid header"),
			expectedIP:  "",
			expectedErr: true,
		},
		{
			buffer:      []byte("PROXY"),
			expectedIP:  "",
			expectedErr: true,
		},
		{
			buffer:      []byte("PROXY MCP3 192.168.1.100 192.168.1.50 5678 3306\r\nOther Data"),
			expectedIP:  "",
			expectedErr: true,
		},
		{
			buffer:      []byte("PROXY UNKNOWN\r\n"),
			expectedIP:  "192.168.1.51:8080",
			expectedErr: false,
		},
	}

	l, _ := newListener(nil, "*", 5)
	for _, test := range tests {
		conn := newMockBufferConn(bytes.NewBuffer(test.buffer), craddr)
		wconn, err := l.createProxyProtocolConn(conn)
		if err == nil {
			clientIP := wconn.RemoteAddr()
			if test.expectedErr {
				t.Errorf("Buffer: %s\nExpect Error", string(test.buffer))
			}
			assertEquals(t, clientIP.String(), test.expectedIP, "Buffer:%s\nExpect: %s Got: %s", string(test.buffer), test.expectedIP, clientIP.String())
		} else {
			if !test.expectedErr {
				t.Errorf("Buffer:%s\nExpect %s But got Error: %v", string(test.buffer), test.expectedIP, err)
			}
		}
	}
}

func encodeProxyProtocolV2Header(network, srcAddr, dstAddr string) []byte {
	saddr, _ := net.ResolveTCPAddr(network, srcAddr)
	daddr, _ := net.ResolveTCPAddr(network, dstAddr)
	buffer := make([]byte, 1024)
	copy(buffer, proxyProtocolV2Sig)
	// Command
	buffer[v2CmdPos] = 0x21
	// Famly
	if network == "tcp4" {
		buffer[v2FamlyPos] = 0x11
		binary.BigEndian.PutUint16(buffer[14:14+2], 12)
		copy(buffer[16:16+4], []byte(saddr.IP.To4()))
		copy(buffer[20:20+4], []byte(daddr.IP.To4()))
		binary.BigEndian.PutUint16(buffer[24:24+2], uint16(saddr.Port))
		binary.BigEndian.PutUint16(buffer[26:26+2], uint16(saddr.Port))
		return buffer[0:28]
	} else if network == "tcp6" {
		buffer[v2FamlyPos] = 0x21
		binary.BigEndian.PutUint16(buffer[14:14+2], 36)
		copy(buffer[16:16+16], []byte(saddr.IP.To16()))
		copy(buffer[32:32+16], []byte(daddr.IP.To16()))
		binary.BigEndian.PutUint16(buffer[48:48+2], uint16(saddr.Port))
		binary.BigEndian.PutUint16(buffer[50:50+2], uint16(saddr.Port))
		return buffer[0:52]
	}
	return buffer
}

func TestProxyProtocolV2HeaderRead(t *testing.T) {
	craddr, _ := net.ResolveTCPAddr("tcp4", "192.168.1.51:8080")
	tests := []struct {
		buffer     []byte
		expectedIP string
	}{
		{
			buffer:     encodeProxyProtocolV2Header("tcp4", "192.168.1.100:5678", "192.168.1.5:4000"),
			expectedIP: "192.168.1.100:5678",
		},
		{
			buffer:     encodeProxyProtocolV2Header("tcp6", "[2001:db8:85a3::8a2e:370:7334]:5678", "[2001:db8:85a3::8a2e:370:8000]:4000"),
			expectedIP: "[2001:db8:85a3::8a2e:370:7334]:5678",
		},
	}

	l, _ := newListener(nil, "*", 5)
	for _, test := range tests {
		conn := newMockBufferConn(bytes.NewBuffer(test.buffer), craddr)
		wconn, err := l.createProxyProtocolConn(conn)
		clientIP := wconn.RemoteAddr()
		if err == nil {
			assertEquals(t, clientIP.String(), test.expectedIP, "Buffer:%v\nExpect: %s Got: %s", test.buffer, test.expectedIP, clientIP.String())
		} else {
			t.Errorf("Buffer:%v\nExpect: %s Got Error: %v", test.buffer, test.expectedIP, err)
		}
	}
}

func TestProxyProtocolV2HeaderReadLocalCommand(t *testing.T) {
	craddr, _ := net.ResolveTCPAddr("tcp4", "192.168.1.51:8080")
	buffer := encodeProxyProtocolV2Header("tcp4", "192.168.1.100:5678", "192.168.1.5:4000")
	buffer[v2CmdPos] = 0x20
	l, _ := newListener(nil, "*", 5)
	conn := newMockBufferConn(bytes.NewBuffer(buffer), craddr)
	wconn, err := l.createProxyProtocolConn(conn)
	clientIP := wconn.RemoteAddr()
	assertNil(t, err)
	assertEquals(t, clientIP.String(), craddr.String(), "Buffer:%v\nExpected: %s Got: %s", buffer, craddr.String(), clientIP.String())
}

func TestProxyProtocolListenerReadHeaderTimeout(t *testing.T) {
	addr := "127.0.0.1:18080"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		l, err := net.Listen("tcp", addr)
		assertNil(t, err)
		ppl, err := NewListener(l, "*", 1)
		assertNil(t, err)
		defer ppl.Close()
		wg.Done()
		conn, err := ppl.Accept()
		assertNil(t, conn)
		assertEquals(t, err.Error(), ErrHeaderReadTimeout.Error())
	}()

	wg.Wait()
	conn, err := net.Dial("tcp", addr)
	assertNil(t, err)
	time.Sleep(2 * time.Second)
	conn.Close()
}

func TestProxyProtocolListenerProxyNotAllowed(t *testing.T) {
	addr := "127.0.0.1:18081"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		l, err := net.Listen("tcp", addr)
		assertNil(t, err)
		ppl, err := NewListener(l, "192.168.1.1", 1)
		assertNil(t, err)
		defer ppl.Close()
		wg.Done()
		conn, err := ppl.Accept()
		assertNil(t, err)
		time.Sleep(2 * time.Second)
		conn.Close()
	}()

	wg.Wait()
	conn, err := net.Dial("tcp", addr)
	assertNil(t, err)
	time.Sleep(2 * time.Second)
	conn.Close()
}

func TestProxyProtocolListenerCloseInOtherGoroutine(t *testing.T) {
	addr := "127.0.0.1:18082"
	l, err := net.Listen("tcp", addr)
	assertNil(t, err)
	ppl, err := NewListener(l, "*", 1)
	assertNil(t, err)
	go func() {
		conn, err := ppl.Accept()
		assertNil(t, conn)
		opErr, ok := err.(*net.OpError)
		assertTrue(t, ok)
		assertEquals(t, opErr.Err.Error(), "use of closed network connection")
	}()

	time.Sleep(1 * time.Second)
	ppl.Close()
	time.Sleep(2 * time.Second)
}
