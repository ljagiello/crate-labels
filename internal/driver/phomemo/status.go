package phomemo

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The printer answers [FirmwareQuery] with 1a 07 followed by one byte per version
// component: 1a 07 02 04 04 is version 2.4.4. Other notifications are only logged.
const (
	replyPrefix   byte = 0x1a
	replyFirmware byte = 0x07
)

// Version is a firmware version, one number per dotted component.
type Version []int

// firmwareIn returns the version a firmware reply carries, or ok = false for any
// other frame.
func firmwareIn(frame []byte) (v Version, ok bool) {
	if len(frame) < 3 || frame[0] != replyPrefix || frame[1] != replyFirmware {
		return nil, false
	}
	v = make(Version, 0, len(frame)-2)
	for _, b := range frame[2:] {
		v = append(v, int(b))
	}
	return v, true
}

// ParseVersion reads a dotted version such as "2" or "2.4.4".
func ParseVersion(s string) (Version, error) {
	var v Version
	for part := range strings.SplitSeq(strings.TrimSpace(s), ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%q is not a dotted version number", s)
		}
		v = append(v, n)
	}
	return v, nil
}

func (v Version) String() string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// Before reports whether v is an earlier version than w. Missing trailing components
// count as zero, so 2.4 and 2.4.0 are the same version.
func (v Version) Before(w Version) bool {
	return slices.Compare(trimZeros(v), trimZeros(w)) < 0
}

func trimZeros(v Version) Version {
	for len(v) > 0 && v[len(v)-1] == 0 {
		v = v[:len(v)-1]
	}
	return v
}
