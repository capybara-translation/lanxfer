package discovery

import (
	"errors"
	"strings"
	"testing"
)

func TestQueryRoundtrip(t *testing.T) {
	p, err := decode(encodeQuery())
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != typeQuery {
		t.Errorf("Type = %q, want %q", p.Type, typeQuery)
	}
}

func TestReplyRoundtrip(t *testing.T) {
	b, err := encodeReply("mac2", 8425, "darwin", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != typeReply || p.Name != "mac2" || p.Port != 8425 || p.OS != "darwin" {
		t.Errorf("decoded %+v", p)
	}
}

func TestEncodeReplyRejectsBadInput(t *testing.T) {
	if _, err := encodeReply("", 8425, "linux", ""); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := encodeReply("ok", 0, "linux", ""); err == nil {
		t.Error("port 0 accepted")
	}
	if _, err := encodeReply("ok", 70000, "linux", ""); err == nil {
		t.Error("port 70000 accepted")
	}
	if _, err := encodeReply("ok", 8425, "linux", "NOT-AN-ID"); err == nil {
		t.Error("malformed ID accepted")
	}
	if _, err := encodeAnnounce("", 8425, "linux", ""); err == nil {
		t.Error("announce with empty name accepted")
	}
	if _, err := encodeSelfQuery("ok", 0, "linux", ""); err == nil {
		t.Error("self query with port 0 accepted")
	}
}

func TestDecodeRejectsInvalid(t *testing.T) {
	invalid := map[string][]byte{
		"not json":           []byte("hello"),
		"empty object":       []byte(`{}`),
		"wrong version":      []byte(`{"lanxfer":2,"type":"query"}`),
		"unknown type":       []byte(`{"lanxfer":1,"type":"bogus"}`),
		"reply without name": []byte(`{"lanxfer":1,"type":"reply","port":8425}`),
		"reply control char": []byte(`{"lanxfer":1,"type":"reply","name":"a\nb","port":8425}`),
		"reply long name":    []byte(`{"lanxfer":1,"type":"reply","name":"` + strings.Repeat("a", 65) + `","port":8425}`),
		"reply port 0":       []byte(`{"lanxfer":1,"type":"reply","name":"x","port":0}`),
		"reply port too big": []byte(`{"lanxfer":1,"type":"reply","name":"x","port":70000}`),
		"reply long os":      []byte(`{"lanxfer":1,"type":"reply","name":"x","port":1,"os":"` + strings.Repeat("o", 33) + `"}`),
		"announce no name":   []byte(`{"lanxfer":1,"type":"announce","port":8425}`),
		"announce port 0":    []byte(`{"lanxfer":1,"type":"announce","name":"x","port":0}`),
		"announce long os":   []byte(`{"lanxfer":1,"type":"announce","name":"x","port":1,"os":"` + strings.Repeat("o", 33) + `"}`),
		"oversized":          make([]byte, maxPacketSize+1),
	}
	for name, b := range invalid {
		if _, err := decode(b); !errors.Is(err, errInvalidPacket) {
			t.Errorf("%s: err = %v, want errInvalidPacket", name, err)
		}
	}
}

const (
	idA = "0123456789abcdef0123456789abcdef"
	idB = "fedcba9876543210fedcba9876543210"
)

func TestEncodeDecodeAnnounce(t *testing.T) {
	b, err := encodeAnnounce("mac2", 8425, "darwin", idA)
	if err != nil {
		t.Fatal(err)
	}
	p, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}
	want := packet{Lanxfer: protocolVersion, Type: typeAnnounce, Name: "mac2", Port: 8425, OS: "darwin", ID: idA}
	if p != want {
		t.Errorf("decoded %+v, want %+v", p, want)
	}
}

func TestDecodeReplyWithID(t *testing.T) {
	b, err := encodeReply("mac2", 8425, "darwin", idA)
	if err != nil {
		t.Fatal(err)
	}
	p, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != typeReply || p.ID != idA {
		t.Errorf("decoded %+v, want a reply with ID %s", p, idA)
	}
}

func TestDecodeQueryWithSelf(t *testing.T) {
	b, err := encodeSelfQuery("mac2", 8425, "darwin", idA)
	if err != nil {
		t.Fatal(err)
	}
	p, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}
	want := packet{Lanxfer: protocolVersion, Type: typeQuery, Name: "mac2", Port: 8425, OS: "darwin", ID: idA}
	if p != want {
		t.Errorf("decoded %+v, want %+v", p, want)
	}
}

func TestDecodeQueryWithBadSelfKeepsQuery(t *testing.T) {
	for name, in := range map[string]string{
		"no name":   `{"lanxfer":1,"type":"query","port":8425}`,
		"bad name":  `{"lanxfer":1,"type":"query","name":"a\nb","port":8425}`,
		"port 0":    `{"lanxfer":1,"type":"query","name":"x","port":0}`,
		"port high": `{"lanxfer":1,"type":"query","name":"x","port":70000}`,
		"long os":   `{"lanxfer":1,"type":"query","name":"x","port":1,"os":"` + strings.Repeat("o", 33) + `"}`,
	} {
		p, err := decode([]byte(in))
		if err != nil {
			t.Errorf("%s: %v, want the query kept", name, err)
			continue
		}
		if p.Type != typeQuery || p.Name != "" || p.Port != 0 || p.OS != "" {
			t.Errorf("%s: decoded %+v, want a plain query without self info", name, p)
		}
	}
}

func TestDecodeQueryWithoutSelfHasNoName(t *testing.T) {
	p, err := decode(encodeQuery())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "" || p.Port != 0 || p.ID != "" {
		t.Errorf("plain query decoded as %+v", p)
	}
}

func TestDecodeIgnoresMalformedID(t *testing.T) {
	for _, id := range []string{"short", strings.ToUpper(idA), idA + "0", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		for _, typ := range []string{"reply", "announce", "query"} {
			in := `{"lanxfer":1,"type":"` + typ + `","name":"x","port":8425,"id":"` + id + `"}`
			p, err := decode([]byte(in))
			if err != nil {
				t.Errorf("%s id %q: %v, want the packet kept", typ, id, err)
				continue
			}
			if p.ID != "" {
				t.Errorf("%s id %q: ID = %q, want empty", typ, id, p.ID)
			}
			if p.Name != "x" || p.Port != 8425 {
				t.Errorf("%s id %q: other fields lost: %+v", typ, id, p)
			}
		}
	}
}

func TestDecodeOldReplyHasNoID(t *testing.T) {
	// A v0.6 reply, byte for byte.
	p, err := decode([]byte(`{"lanxfer":1,"type":"reply","name":"mac2","port":8425,"os":"darwin"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != typeReply || p.Name != "mac2" || p.Port != 8425 || p.OS != "darwin" || p.ID != "" {
		t.Errorf("decoded %+v", p)
	}
}

// The worst case for size is a name made of characters that JSON escapes
// to six bytes each (HTML-sensitive ASCII), plus the longest OS and an ID.
func TestMaxLengthPacketsFitTheLimit(t *testing.T) {
	name := strings.Repeat("<", 64)
	goos := strings.Repeat(">", maxOSLen)
	encoders := map[string]func(string, int, string, string) ([]byte, error){
		"reply":      encodeReply,
		"announce":   encodeAnnounce,
		"self query": encodeSelfQuery,
	}
	for what, enc := range encoders {
		b, err := enc(name, 65535, goos, idA)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if len(b) > maxPacketSize {
			t.Errorf("%s is %d bytes, over the %d limit", what, len(b), maxPacketSize)
		}
		if _, err := decode(b); err != nil {
			t.Errorf("%s does not decode: %v", what, err)
		}
	}
}
