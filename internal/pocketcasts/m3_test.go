package pocketcasts

import (
	"bytes"
	"testing"
)

// Behavior 1: PodcastEpisodes decoder — top-level int32 fields 3/6 (NOT the
// wrapped Int32Value form used in up_next/sync).
func TestDecodeSyncEpisodes(t *testing.T) {
	ep := func(uuid string, played, dur int) []byte {
		b := encodeStringField(1, uuid)
		b = append(b, encodeVarintField(3, int64(played))...)
		b = append(b, encodeVarintField(6, int64(dur))...)
		return b
	}
	var body []byte
	body = append(body, encodeLengthDelimitedField(1, ep("u1", 120, 1800))...)
	body = append(body, encodeLengthDelimitedField(1, ep("u2", 0, 600))...)

	got := decodeSyncEpisodes(body)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
	if got[0] != (PlaybackInfo{UUID: "u1", PlayedUpTo: 120, Duration: 1800}) {
		t.Fatalf("ep0 = %+v", got[0])
	}
	if got[1].PlayedUpTo != 0 || got[1].Duration != 600 {
		t.Fatalf("ep1 = %+v", got[1])
	}
}

// Behavior 2: UpdateEpisode encodes position as an Int32Value submessage (field
// 3) and status/duration as varints — exact golden bytes.
func TestEncodeUpdateEpisode_Golden(t *testing.T) {
	got := encodeUpdateEpisode(EpisodeUpdate{
		UUID: "e", PodcastUUID: "p", Position: 120, Duration: 1800, Status: StatusInProgress,
	})
	want := []byte{
		0x0A, 0x01, 'e', // field 1: uuid "e"
		0x12, 0x01, 'p', // field 2: podcast "p"
		0x1A, 0x02, 0x08, 0x78, // field 3: Int32Value{ value(1)=120 }
		0x20, 0x02, // field 4: status=2 (inProgress)
		0x28, 0x88, 0x0E, // field 5: duration=1800
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("update bytes mismatch\n got: % x\nwant: % x", got, want)
	}
}

// Behavior 7: SkipSettings parse — forward(5)/back(6), each Int32Setting{Int32Value}.
func TestDecodeSkipSettings(t *testing.T) {
	int32Setting := func(field, value int) []byte {
		inner := encodeVarintField(1, int64(value))       // Int32Value{ value(1) }
		wrapped := encodeLengthDelimitedField(1, inner)   // Int32Setting{ value(1)=Int32Value }
		return encodeLengthDelimitedField(field, wrapped) // top-level field
	}
	var body []byte
	body = append(body, int32Setting(5, 30)...) // forward
	body = append(body, int32Setting(6, 15)...) // back

	s := decodeSkipSettings(body)
	if s.Forward != 30 || s.Back != 15 {
		t.Fatalf("got %+v, want forward=30 back=15", s)
	}
}

func TestDecodeSkipSettings_Defaults(t *testing.T) {
	s := decodeSkipSettings(nil)
	if s.Forward != 45 || s.Back != 10 {
		t.Fatalf("defaults wrong: %+v", s)
	}
}
