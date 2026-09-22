package mobile

import (
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"

	"github.com/samosvalishe/free-turn-proxy/internal/netctl"
)

// Protector реализуется хостом для исключения сокетов клиента из VPN-туннеля (VpnService.protect).
// Без этого TURN / VK API / DNS трафик заворачивается обратно в туннель.
type Protector interface {
	Protect(fd int) bool
}

var ErrProtect = errors.New("mobile: socket protect failed") //nolint:gochecknoglobals // sentinel для errors.Is

var protector atomic.Pointer[Protector]

// SetProtect устанавливает обработчик защиты сокетов хоста (nil - no-op).
func SetProtect(p Protector) {
	if p == nil {
		protector.Store(nil)
		netctl.SetControl(nil)
		return
	}
	protector.Store(&p)
	netctl.SetControl(func(_, _ string, c syscall.RawConn) error {
		ok := false
		if err := c.Control(func(fd uintptr) { ok = p.Protect(int(fd)) }); err != nil {
			return fmt.Errorf("socket control: %w", err)
		}
		if !ok {
			return ErrProtect
		}
		return nil
	})
}

func protectFD(fd int) bool {
	if p := protector.Load(); p != nil {
		return (*p).Protect(fd)
	}
	return false
}
