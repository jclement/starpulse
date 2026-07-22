package words
import "testing"
func TestDict(t *testing.T){ if !Valid("CRANE"){t.Error("crane")}; if Valid("zzzzz"){t.Error("zzzzz")}; if Count()<8000{t.Errorf("count %d",Count())} }
