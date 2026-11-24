package stopper

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const planIDBytes = 16

// planID identifies the allowed targets. Empty when there are none.
func planID(targets []domain.StopTarget) string {
	if len(targets) == 0 {
		return ""
	}
	var encoded [][]byte
	for _, target := range targets {
		encoded = append(encoded, encodeTarget(target))
	}
	slices.SortFunc(encoded, bytes.Compare)
	hash := sha256.New()
	hash.Write([]byte("portly-plan/1\x00"))
	hash.Write(encodeList(encoded))
	return hex.EncodeToString(hash.Sum(nil)[:planIDBytes])
}

// encodeTarget covers identity, named sockets, reach and state, never names.
func encodeTarget(target domain.StopTarget) []byte {
	var named, bindings, other [][]byte
	for _, socket := range target.Named {
		named = append(named, encodeTexts(socket.String(), string(socket.Protocol), strconv.Itoa(int(socket.Family)), socket.NetNS, socket.OSID))
	}
	for _, binding := range target.Bindings {
		bindings = append(bindings, encodeTexts(binding.HostAddr.String(), binding.Endpoint().String(), strconv.Itoa(int(binding.ContainerPort))))
	}
	for _, endpoint := range target.Other {
		other = append(other, encodeTexts(endpoint.String(), string(endpoint.Protocol)))
	}
	fields := [][]byte{
		encodeTexts(string(target.Kind), strconv.Itoa(target.Root.PID), target.Root.Start, target.Root.UserID, target.Root.Exe),
		encodeTexts(target.Container.Engine, target.Container.ID, target.Container.StartedAt),
		encodeSet(named), encodeSet(bindings), encodeSet(other),
		encodeTexts(strconv.FormatBool(target.Suspended), strconv.FormatBool(target.Paused)),
	}
	return encodeList(fields)
}

// encodeTexts length-prefixes every field, so none can run into the next.
func encodeTexts(texts ...string) []byte {
	var fields [][]byte
	for _, text := range texts {
		fields = append(fields, []byte(text))
	}
	return encodeList(fields)
}

// encodeSet ignores order and repeats.
func encodeSet(items [][]byte) []byte {
	slices.SortFunc(items, bytes.Compare)
	return encodeList(slices.CompactFunc(items, bytes.Equal))
}

// encodeList writes a count, then each length and value.
func encodeList(items [][]byte) []byte {
	encoded := binary.BigEndian.AppendUint32(nil, uint32(len(items)))
	for _, item := range items {
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(item)))
		encoded = append(encoded, item...)
	}
	return encoded
}
