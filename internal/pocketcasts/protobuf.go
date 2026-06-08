package pocketcasts

// Hand-rolled protobuf encode/decode — no dependency on a protobuf runtime.
// Ported byte-for-byte from the menubar's APIService.swift so the wire format
// stays identical across surfaces.

// encodeVarint encodes an unsigned LEB128 varint.
func encodeVarint(value uint64) []byte {
	var out []byte
	v := value
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if v == 0 {
			break
		}
	}
	return out
}

// encodeStringField encodes a length-delimited field (wire type 2).
func encodeStringField(field int, value string) []byte {
	tag := byte((field << 3) | 2)
	b := []byte(value)
	out := []byte{tag}
	out = append(out, encodeVarint(uint64(len(b)))...)
	out = append(out, b...)
	return out
}

// encodeVarintField encodes a varint field (wire type 0).
func encodeVarintField(field int, value int64) []byte {
	tag := byte((field << 3) | 0)
	out := []byte{tag}
	out = append(out, encodeVarint(uint64(value))...)
	return out
}

// encodeLengthDelimitedField wraps raw bytes as a length-delimited field.
func encodeLengthDelimitedField(field int, value []byte) []byte {
	tag := byte((field << 3) | 2)
	out := []byte{tag}
	out = append(out, encodeVarint(uint64(len(value)))...)
	out = append(out, value...)
	return out
}

// decodeVarint reads a varint at offset. Returns (value, bytesConsumed).
func decodeVarint(data []byte, offset int) (uint64, int) {
	var value uint64
	var shift uint
	pos := offset
	for pos < len(data) {
		b := data[pos]
		value |= uint64(b&0x7F) << shift
		pos++
		if b&0x80 == 0 {
			break
		}
		shift += 7
	}
	return value, pos - offset
}

// field is one decoded protobuf field: its number, wire type, and payload.
type field struct {
	number   int
	wireType int
	varint   uint64 // valid when wireType == 0
	bytes    []byte // valid when wireType == 2
}

// walkFields iterates the top-level fields of a protobuf message, calling fn
// for each. Unknown/unsupported wire types stop iteration (matches the Swift
// "can't skip, bail" behavior).
func walkFields(data []byte, fn func(f field)) {
	offset := 0
	for offset < len(data) {
		tag := data[offset]
		number := int(tag >> 3)
		wireType := int(tag & 0x07)
		offset++

		switch wireType {
		case 0: // varint
			v, n := decodeVarint(data, offset)
			offset += n
			fn(field{number: number, wireType: 0, varint: v})
		case 2: // length-delimited
			length, n := decodeVarint(data, offset)
			offset += n
			if offset+int(length) > len(data) {
				return
			}
			b := data[offset : offset+int(length)]
			offset += int(length)
			fn(field{number: number, wireType: 2, bytes: b})
		default:
			return // can't skip group/fixed types — bail
		}
	}
}

// decodeInt32Value unwraps a google.protobuf.Int32Value { value(1) = varint }.
func decodeInt32Value(data []byte) int {
	var out int
	walkFields(data, func(f field) {
		if f.number == 1 && f.wireType == 0 {
			out = int(f.varint)
		}
	})
	return out
}

// firstSubmessage returns the bytes of the first length-delimited field matching
// fieldNumber, or nil. Used to drill into nested wrapper submessages.
func firstSubmessage(data []byte, fieldNumber int) []byte {
	var out []byte
	found := false
	walkFields(data, func(f field) {
		if !found && f.number == fieldNumber && f.wireType == 2 {
			out = f.bytes
			found = true
		}
	})
	return out
}
