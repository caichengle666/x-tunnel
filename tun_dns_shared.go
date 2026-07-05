package main

import (
	"encoding/binary"
	"fmt"
)

const (
	dnsTypeA    uint16 = 1
	dnsTypeAAAA uint16 = 28
)

func unpackDNSHeader(q []byte) (id uint16, flags uint16, qdcount uint16) {
	if len(q) < 12 {
		return
	}
	id = binary.BigEndian.Uint16(q[0:2])
	flags = binary.BigEndian.Uint16(q[2:4])
	qdcount = binary.BigEndian.Uint16(q[4:6])
	return
}

func dnsQuestionType(q []byte) uint16 {
	if len(q) < 12 {
		return 0
	}
	pos := 12
	for pos < len(q) {
		if q[pos] == 0 {
			pos++
			break
		}
		if q[pos] >= 0xC0 {
			pos += 2
			break
		}
		pos += int(q[pos]) + 1
	}
	if pos+2 <= len(q) {
		return binary.BigEndian.Uint16(q[pos : pos+2])
	}
	return 0
}

func dnsQuestionTypeName(q []byte) string {
	switch dnsQuestionType(q) {
	case dnsTypeA:
		return "A"
	case dnsTypeAAAA:
		return "AAAA"
	default:
		return fmt.Sprintf("TYPE%d", dnsQuestionType(q))
	}
}
