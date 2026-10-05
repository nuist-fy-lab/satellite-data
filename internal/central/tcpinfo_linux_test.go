//go:build linux

package central

import (
	"net"
	"testing"
)

func TestReadTCPInfoFromTCPConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case server := <-accepted:
		defer server.Close()
	case err := <-acceptErr:
		t.Fatal(err)
	}
	info, err := readTCPInfo(client)
	if err != nil {
		t.Fatal(err)
	}
	if info.SndCWND == 0 {
		t.Fatal("TCP_INFO reported zero congestion window")
	}
}
