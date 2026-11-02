package executor

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// systemBootTime reads the btime line of /proc/stat.
func systemBootTime() (time.Time, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || sec <= 0 {
				return time.Time{}, errors.New("invalid btime in /proc/stat")
			}
			return time.Unix(sec, 0), nil
		}
	}
	if err := s.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, errors.New("no btime in /proc/stat")
}
