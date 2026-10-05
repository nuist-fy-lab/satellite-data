//go:build linux

package central

import (
	"errors"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func readTCPInfo(conn net.Conn) (tcpInfoSnapshot, error) {
	for {
		unwrapper, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			break
		}
		next := unwrapper.NetConn()
		if next == nil || next == conn {
			break
		}
		conn = next
	}
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return tcpInfoSnapshot{}, errors.New("connection does not expose a raw socket")
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		return tcpInfoSnapshot{}, err
	}
	var info *unix.TCPInfo
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		info, socketErr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil {
		return tcpInfoSnapshot{}, err
	}
	if socketErr != nil {
		return tcpInfoSnapshot{}, socketErr
	}
	if info == nil {
		return tcpInfoSnapshot{}, errors.New("TCP_INFO returned no data")
	}
	return tcpInfoSnapshot{
		RTT:          time.Duration(info.Rtt) * time.Microsecond,
		RTTVar:       time.Duration(info.Rttvar) * time.Microsecond,
		RTO:          time.Duration(info.Rto) * time.Microsecond,
		SndCWND:      info.Snd_cwnd,
		SndSsthresh:  info.Snd_ssthresh,
		SndWindow:    info.Snd_wnd,
		RcvWindow:    info.Rcv_wnd,
		Unacked:      info.Unacked,
		Lost:         info.Lost,
		Retrans:      info.Retrans,
		TotalRetrans: info.Total_retrans,
		DeliveryRate: info.Delivery_rate,
		PacingRate:   info.Pacing_rate,
		BytesRetrans: info.Bytes_retrans,
	}, nil
}
