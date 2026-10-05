//go:build !linux

package central

import (
	"errors"
	"net"
)

func readTCPInfo(net.Conn) (tcpInfoSnapshot, error) {
	return tcpInfoSnapshot{}, errors.New("TCP_INFO diagnostics require Linux")
}
