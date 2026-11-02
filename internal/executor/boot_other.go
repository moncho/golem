//go:build !darwin && !linux

package executor

import (
	"errors"
	"time"
)

func systemBootTime() (time.Time, error) {
	return time.Time{}, errors.New("boot time is not available on this platform")
}
