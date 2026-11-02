package executor

import (
	"encoding/binary"
	"errors"
	"syscall"
	"time"
)

// systemBootTime reads kern.boottime, a struct timeval whose first 8 bytes
// are the seconds on 64-bit systems.
func systemBootTime() (time.Time, error) {
	raw, err := syscall.Sysctl("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	if len(raw) < 8 {
		return time.Time{}, errors.New("short kern.boottime")
	}
	sec := int64(binary.LittleEndian.Uint64([]byte(raw[:8])))
	if sec <= 0 {
		return time.Time{}, errors.New("invalid kern.boottime")
	}
	return time.Unix(sec, 0), nil
}
