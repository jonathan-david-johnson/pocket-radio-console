package pocketcasts

import (
	"bytes"
	"testing"
	"time"
)

// Behavior 1a: encodeLoginRequest produces the exact bytes the menubar sends.
// Golden fixture hand-computed from the wire format:
//   field1(email) tag 0x0A, field2(password) tag 0x12, field3(scope) tag 0x1A.
func TestEncodeLoginRequest_Golden(t *testing.T) {
	got := encodeLoginRequest("a@b.com", "pw", "mobile")
	want := []byte{
		0x0A, 0x07, 'a', '@', 'b', '.', 'c', 'o', 'm', // field 1: "a@b.com"
		0x12, 0x02, 'p', 'w', // field 2: "pw"
		0x1A, 0x06, 'm', 'o', 'b', 'i', 'l', 'e', // field 3: "mobile"
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("login bytes mismatch\n got: %v\nwant: %v", got, want)
	}
}

// Behavior 1b: decodeLoginResponse parses {token, uuid, email}.
func TestDecodeLoginResponse(t *testing.T) {
	body := append(encodeStringField(1, "tok123"), encodeStringField(2, "uuid-abc")...)
	body = append(body, encodeStringField(3, "me@x.com")...)

	s, ok := decodeLoginResponse(body)
	if !ok {
		t.Fatal("decode failed")
	}
	if s.Token != "tok123" || s.UUID != "uuid-abc" || s.Email != "me@x.com" {
		t.Fatalf("got %+v", s)
	}
}

func TestDecodeLoginResponse_MissingToken(t *testing.T) {
	body := encodeStringField(2, "uuid-only")
	if _, ok := decodeLoginResponse(body); ok {
		t.Fatal("expected failure without token")
	}
}

// Behavior 2: decodeUpNextResponse parses ordered episodes with playedUpTo /
// duration merged from the sync records.
func TestDecodeUpNextResponse_MergesSync(t *testing.T) {
	ep1 := episodeMsg("Title One", "https://1.mp3", "pod-1", "uuid-1", time.Unix(1700000000, 0))
	ep2 := episodeMsg("Title Two", "https://2.mp3", "pod-2", "uuid-2", time.Time{})
	sync1 := syncMsg("uuid-1", 120, 1800)

	var body []byte
	body = append(body, encodeLengthDelimitedField(4, ep1)...)
	body = append(body, encodeLengthDelimitedField(4, ep2)...)
	body = append(body, encodeLengthDelimitedField(5, sync1)...)

	eps := decodeUpNextResponse(body)
	if len(eps) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(eps))
	}
	if eps[0].Title != "Title One" || eps[0].URL != "https://1.mp3" || eps[0].UUID != "uuid-1" {
		t.Fatalf("episode 0 wrong: %+v", eps[0])
	}
	if eps[0].PlayedUpTo != 120 || eps[0].Duration != 1800 {
		t.Fatalf("sync not merged: playedUpTo=%d duration=%d", eps[0].PlayedUpTo, eps[0].Duration)
	}
	if eps[0].Published.Unix() != 1700000000 {
		t.Fatalf("published not parsed: %v", eps[0].Published)
	}
	if eps[1].PlayedUpTo != 0 || eps[1].Duration != 0 {
		t.Fatalf("episode 1 should have no sync data: %+v", eps[1])
	}
	// Order preserved (top of queue first).
	if eps[0].UUID != "uuid-1" || eps[1].UUID != "uuid-2" {
		t.Fatalf("order not preserved")
	}
}

func TestVarintRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 16384, 1 << 35} {
		enc := encodeVarint(v)
		got, n := decodeVarint(enc, 0)
		if got != v || n != len(enc) {
			t.Fatalf("varint %d round-trip: got %d (n=%d, enc=%v)", v, got, n, enc)
		}
	}
}

// episodeMsg builds an EpisodeResponse sub-message body.
func episodeMsg(title, url, podcast, uuid string, published time.Time) []byte {
	var b []byte
	b = append(b, encodeStringField(1, title)...)
	b = append(b, encodeStringField(2, url)...)
	b = append(b, encodeStringField(3, podcast)...)
	b = append(b, encodeStringField(4, uuid)...)
	if !published.IsZero() {
		ts := encodeVarintField(1, published.Unix()) // Timestamp.seconds
		b = append(b, encodeLengthDelimitedField(5, ts)...)
	}
	return b
}

// syncMsg builds an EpisodeSyncResponse: uuid(1), playedUpTo(6), duration(7).
func syncMsg(uuid string, playedUpTo, duration int) []byte {
	var b []byte
	b = append(b, encodeStringField(1, uuid)...)
	b = append(b, encodeLengthDelimitedField(6, encodeVarintField(1, int64(playedUpTo)))...)
	b = append(b, encodeLengthDelimitedField(7, encodeVarintField(1, int64(duration)))...)
	return b
}
