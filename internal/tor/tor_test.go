package tor

import (
	"strings"
	"testing"
)

func TestTorrcContent(t *testing.T) {
	conf, err := torrcContent("/data/state", "/data/hs", []Forward{
		{VirtualPort: 80, LocalAddr: ":80"},
		{VirtualPort: 1965, LocalAddr: "0.0.0.0:1965"},
		{VirtualPort: 22, LocalAddr: ":22"},
		{VirtualPort: 23, LocalAddr: ":23"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SocksPort 0",
		"DataDirectory /data/state",
		"HiddenServiceDir /data/hs",
		"HiddenServicePort 80 127.0.0.1:80",
		"HiddenServicePort 1965 127.0.0.1:1965",
		"HiddenServicePort 22 127.0.0.1:22",
		"HiddenServicePort 23 127.0.0.1:23",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("torrc missing %q:\n%s", want, conf)
		}
	}
}

func TestTorrcContentErrors(t *testing.T) {
	if _, err := torrcContent("/d", "/h", nil); err == nil {
		t.Error("no forwards accepted")
	}
	if _, err := torrcContent("/d", "/h", []Forward{{VirtualPort: 80, LocalAddr: "nonsense"}}); err == nil {
		t.Error("bad local addr accepted")
	}
}
