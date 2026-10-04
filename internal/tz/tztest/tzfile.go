// Package tztest provides a minimal tzfile (zoneinfo binary) encoder for
// tests that need to construct timezone rules in memory, e.g. to simulate
// runtime timezone rule updates.
package tztest

import (
	"bytes"
	"encoding/binary"
	"time"
)

// Transition is one DST rule change at a UTC instant.
type Transition struct {
	Time   time.Time // UTC instant of the change
	Offset int       // new UTC offset in seconds
	IsDST  bool
	Abbr   string
}

// Spec is a minimal zone definition.
type Spec struct {
	Name          string
	InitialOffset int
	InitialAbbr   string
	Transitions   []Transition // sorted by Time ascending
}

// Encode produces a version-2 tzfile for spec.
func Encode(spec Spec) []byte {
	var ttinfos []ttinfo
	var abbrs bytes.Buffer
	abbrIndex := map[string]int{}

	addAbbr := func(a string) int {
		if idx, ok := abbrIndex[a]; ok {
			return idx
		}
		idx := abbrs.Len()
		abbrs.WriteString(a)
		abbrs.WriteByte(0)
		abbrIndex[a] = idx
		return idx
	}

	// ttinfo[0] = initial zone
	ttinfos = append(ttinfos, ttinfo{
		offset: spec.InitialOffset,
		isdst:  0,
		abbr:   addAbbr(spec.InitialAbbr),
	})
	for _, tr := range spec.Transitions {
		isdst := byte(0)
		if tr.IsDST {
			isdst = 1
		}
		ttinfos = append(ttinfos, ttinfo{
			offset: tr.Offset,
			isdst:  isdst,
			abbr:   addAbbr(tr.Abbr),
		})
	}

	// Transition times and type indices.
	n := len(spec.Transitions)
	transTimes := make([]int64, n)
	transTypes := make([]byte, n)
	for i, tr := range spec.Transitions {
		transTimes[i] = tr.Time.Unix()
		transTypes[i] = byte(i + 1)
	}

	charcnt := abbrs.Len()
	typecnt := len(ttinfos)

	var buf bytes.Buffer
	buf.Write([]byte("TZif"))
	buf.WriteByte('2')
	buf.Write(make([]byte, 15))

	// Header (32-bit block counts).
	writeInt32(&buf, 0) // tzh_ttisgmtcnt
	writeInt32(&buf, 0) // tzh_ttisstdcnt
	writeInt32(&buf, 0) // tzh_leapcnt
	writeInt32(&buf, int32(n))
	writeInt32(&buf, int32(typecnt))
	writeInt32(&buf, int32(charcnt))

	// 32-bit block.
	for _, t := range transTimes {
		writeInt32(&buf, int32(t))
	}
	buf.Write(transTypes)
	for _, ti := range ttinfos {
		writeInt32(&buf, int32(ti.offset))
		buf.WriteByte(ti.isdst)
		buf.WriteByte(byte(ti.abbr))
	}
	buf.Write(abbrs.Bytes())

	// 64-bit block: its own magic header + counts.
	buf.Write([]byte("TZif"))
	buf.WriteByte('2')
	buf.Write(make([]byte, 15))
	writeInt32(&buf, 0) // tzh_ttisgmtcnt
	writeInt32(&buf, 0) // tzh_ttisstdcnt
	writeInt32(&buf, 0) // tzh_leapcnt
	writeInt32(&buf, int32(n))
	writeInt32(&buf, int32(typecnt))
	writeInt32(&buf, int32(charcnt))
	for _, t := range transTimes {
		writeInt64(&buf, t)
	}
	buf.Write(transTypes)
	for _, ti := range ttinfos {
		writeInt32(&buf, int32(ti.offset))
		buf.WriteByte(ti.isdst)
		buf.WriteByte(byte(ti.abbr))
	}
	buf.Write(abbrs.Bytes())

	// Empty POSIX footer (Go falls back to the last transition's zone).
	buf.WriteByte('\n')
	buf.WriteByte('\n')

	return buf.Bytes()
}

type ttinfo struct {
	offset int
	isdst  byte
	abbr   int
}

func writeInt32(buf *bytes.Buffer, v int32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	buf.Write(b[:])
}

func writeInt64(buf *bytes.Buffer, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	buf.Write(b[:])
}
