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
	b, err := encodeReply("mac2", 8425, "darwin")
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
	if _, err := encodeReply("", 8425, "linux"); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := encodeReply("ok", 0, "linux"); err == nil {
		t.Error("port 0 accepted")
	}
	if _, err := encodeReply("ok", 70000, "linux"); err == nil {
		t.Error("port 70000 accepted")
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
		"oversized":          make([]byte, maxPacketSize+1),
	}
	for name, b := range invalid {
		if _, err := decode(b); !errors.Is(err, errInvalidPacket) {
			t.Errorf("%s: err = %v, want errInvalidPacket", name, err)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"mac2", "Junya's MacBook", "日本語", strings.Repeat("a", 64)} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 65), "tab\there", "nul\x00"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", bad)
		}
	}
}
