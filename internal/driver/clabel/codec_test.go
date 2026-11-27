package clabel

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/driver/clabel/tspl"
	"github.com/ljagiello/crate-labels/internal/printer"
)

var goldenDir = filepath.Join("..", "..", "..", "testdata", "golden", "clabel")

func readGolden(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{goldenDir}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// codecFixture is testdata/golden/clabel/codec.json, written by hand. CRCs, frames,
// tenths and print times are protocol values. Replies and reassembler cases describe
// how crate-labels reads what the printer sends.
type codecFixture struct {
	CRC8 []struct {
		Hex  string `json:"hex"`
		CRC8 byte   `json:"crc8"`
	} `json:"crc8"`
	Frames []struct {
		Name     string `json:"name"`
		Function byte   `json:"function"`
		Sub      byte   `json:"sub"`
		Content  string `json:"content"`
		Frame    string `json:"frame"`
		CRC8     byte   `json:"crc8"`
	} `json:"frames"`
	Replies []struct {
		Frame    string         `json:"frame"`
		Function byte           `json:"function"`
		Sub      byte           `json:"sub"`
		Content  string         `json:"content"`
		Reply    map[string]any `json:"reply"`
		What     string         `json:"what"`
	} `json:"replies"`
	Reassembler []struct {
		Chunks []string `json:"chunks"`
		Frames []struct {
			Function byte   `json:"function"`
			Sub      byte   `json:"sub"`
			Content  string `json:"content"`
		} `json:"frames"`
		What string `json:"what"`
	} `json:"reassembler"`
	Tenths []struct {
		Dots, DPI, Tenths int
	} `json:"tenths"`
	PrintTimeMS []struct {
		Rows int     `json:"rows"`
		MS   float64 `json:"ms"`
	} `json:"print_time_ms"`
}

func loadCodec(t *testing.T) codecFixture {
	t.Helper()
	var c codecFixture
	if err := json.Unmarshal(readGolden(t, "codec.json"), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGoldenCRC8(t *testing.T) {
	for _, c := range loadCodec(t).CRC8 {
		if got := CRC8(unhex(t, c.Hex)); got != c.CRC8 {
			t.Errorf("CRC8(%.20s…) = %#02x, want %#02x", c.Hex, got, c.CRC8)
		}
	}
	if CRC8([]byte("123456789")) != 0xf4 {
		t.Error("CRC-8/SMBUS check value is not f4")
	}
}

func TestGoldenControlFrames(t *testing.T) {
	named := map[string][]byte{
		"identity_query":              IdentityQuery,
		"read_paper_type":             ReadSetting(SettingPaperType),
		"read_language":               ReadSetting(SettingLanguage),
		"write_paper_type_gap":        WriteSetting(SettingPaperType, byte(PaperGap)),
		"write_paper_type_continuous": WriteSetting(SettingPaperType, byte(PaperContinuous)),
		"write_language_tspl":         WriteSetting(SettingLanguage, LanguageTSPL),
	}
	frames := loadCodec(t).Frames
	if len(frames) != len(named) {
		t.Fatalf("codec.json has %d frames, test knows %d", len(frames), len(named))
	}
	for _, f := range frames {
		got, err := ControlFrame(f.Function, f.Sub, unhex(t, f.Content))
		if err != nil || hex.EncodeToString(got) != f.Frame || got[len(got)-2] != f.CRC8 {
			t.Errorf("%s: ControlFrame = %x, %v; want %s", f.Name, got, err, f.Frame)
		}
		if !bytes.Equal(named[f.Name], got) {
			t.Errorf("%s: named frame = %x", f.Name, named[f.Name])
		}
		if bin := readGolden(t, "frames", f.Name+".bin"); !bytes.Equal(bin, got) {
			t.Errorf("%s: frames/%s.bin = %x", f.Name, f.Name, bin)
		}
	}
	if _, err := ControlFrame(5, 4, make([]byte, MaxContent+1)); err == nil {
		t.Error("ControlFrame accepted content over 16 bits")
	}
}

func replyJSON(r Reply) map[string]any {
	m := map[string]any{"kind": r.Kind.String()}
	switch r.Kind {
	case ReplySetting:
		m["setting"], m["value"] = float64(r.Setting), nil
		if r.HasValue {
			m["value"] = float64(r.Value)
		}
	case ReplyStatus:
		conditions := []any{}
		for _, c := range r.Status.Conditions() {
			conditions = append(conditions, c)
		}
		m["status"], m["conditions"] = float64(r.Status), conditions
	case ReplyIdentity:
		fields := []any{}
		for _, f := range r.Fields {
			fields = append(fields, f)
		}
		m["fields"], m["firmware"] = fields, r.Firmware
	}
	return m
}

func TestGoldenReplies(t *testing.T) {
	for _, c := range loadCodec(t).Replies {
		raw := RawFrame{Function: c.Function, Sub: c.Sub, Content: unhex(t, c.Content)}
		if c.Frame != "" {
			var r Reassembler
			got := r.Push(unhex(t, c.Frame))
			if len(got) != 1 {
				t.Fatalf("%s: reassembled into %d frames", c.What, len(got))
			}
			raw = got[0]
		}
		if got := replyJSON(Decode(raw)); !reflect.DeepEqual(got, c.Reply) {
			t.Errorf("%s: got %v, want %v", c.What, got, c.Reply)
		}
	}
}

func TestStatusConditions(t *testing.T) {
	if got := StatusBits(0x85).Conditions(); !reflect.DeepEqual(got, []string{"lid-open", "no-paper", "head-hot"}) {
		t.Errorf("Conditions(0x85) = %v", got)
	}
	if got := StatusBits(0x02).Conditions(); got != nil {
		t.Errorf("Conditions(0x02) = %v, want none", got)
	}
}

func TestGoldenReassembler(t *testing.T) {
	for _, c := range loadCodec(t).Reassembler {
		var r Reassembler
		var got []RawFrame
		for _, chunk := range c.Chunks {
			got = append(got, r.Push(unhex(t, chunk))...)
		}
		if len(got) != len(c.Frames) {
			t.Fatalf("%s: %d frames, want %d", c.What, len(got), len(c.Frames))
		}
		for i, w := range c.Frames {
			if got[i].Function != w.Function || got[i].Sub != w.Sub || hex.EncodeToString(got[i].Content) != w.Content {
				t.Errorf("%s: frame %d = %+v, want %+v", c.What, i, got[i], w)
			}
		}
	}
}

// TestReassemblerSplitsAnywhere cuts a stream of frames and noise at every offset and
// in random pieces: the frames must come back the same, in order.
func TestReassemblerSplitsAnywhere(t *testing.T) {
	var stream []byte
	var want []RawFrame
	for i := range 10 {
		content := bytes.Repeat([]byte{byte(i), delimiter}, i*7)
		f, _ := ControlFrame(5, byte(0x80|i), content)
		stream = append(stream, 0x13, delimiter) // noise, including a lone delimiter
		stream = append(stream, f...)
		want = append(want, RawFrame{Function: 5, Sub: byte(0x80 | i), Content: content})
	}
	same := func(got []RawFrame) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i].Function != want[i].Function || got[i].Sub != want[i].Sub || !bytes.Equal(got[i].Content, want[i].Content) {
				return false
			}
		}
		return true
	}
	for cut := range len(stream) + 1 {
		var r Reassembler
		if got := append(r.Push(stream[:cut]), r.Push(stream[cut:])...); !same(got) {
			t.Fatalf("cut at %d: got %d frames, want %d", cut, len(got), len(want))
		}
	}
	rng := rand.New(rand.NewPCG(1, 8))
	for range 50 {
		var r Reassembler
		var got []RawFrame
		for at := 0; at < len(stream); {
			n := 1 + rng.IntN(30)
			got = append(got, r.Push(stream[at:min(at+n, len(stream))])...)
			at += n
		}
		if !same(got) {
			t.Fatalf("random pieces: got %d frames, want %d", len(got), len(want))
		}
	}
}

func TestReassemblerTailIsBounded(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 2))
	var r Reassembler
	for range 300 {
		chunk := make([]byte, 64)
		for i := range chunk {
			switch rng.IntN(6) {
			case 0:
				chunk[i] = delimiter
			case 1:
				chunk[i] = 0
			default:
				chunk[i] = byte(rng.UintN(256))
			}
		}
		r.Push(chunk)
		if len(r.tail) > frameBytes+MaxContent {
			t.Fatalf("tail grew to %d bytes", len(r.tail))
		}
	}
	got := r.Push(ReadSetting(SettingLanguage))
	if len(got) == 0 || got[len(got)-1].Sub != byte(SettingLanguage) {
		t.Fatalf("frame after noise: got %+v", got)
	}
}

func TestGoldenTenthsAndPrintTime(t *testing.T) {
	c := loadCodec(t)
	for _, tc := range c.Tenths {
		if got := tspl.Tenths(tc.Dots, tc.DPI); got != tc.Tenths {
			t.Errorf("Tenths(%d, %d) = %d, want %d", tc.Dots, tc.DPI, got, tc.Tenths)
		}
	}
	for _, b := range c.PrintTimeMS {
		if got := CTPacing.PrintTime(b.Rows); got != msDuration(b.MS) {
			t.Errorf("PrintTime(%d) = %v, want %g ms", b.Rows, got, b.MS)
		}
	}
}

// TestPacingIsTheKnownGoodTable pins the timing the printer sees. A change needs a test
// on the printer first.
func TestPacingIsTheKnownGoodTable(t *testing.T) {
	want := Pacing{Settle: 700 * time.Millisecond, PageBase: 1500 * time.Millisecond,
		PagePerRow: 3 * time.Millisecond, ChunkGapMS: 5, ChunkBytes: 200}
	if CTPacing != want {
		t.Fatalf("CTPacing = %+v", CTPacing)
	}
	if d := New(); d.pacing != CTPacing || d.waits != DefaultWaits {
		t.Fatalf("New() uses %+v and %+v", d.pacing, d.waits)
	}
}

// TestHeadLayout checks the placement rule from its definition: the content box is
// centred on the label, inside the head, and as wide as the printable width allows.
func TestHeadLayout(t *testing.T) {
	c := catalog(t)
	for _, id := range []string{"clabel-ct230b", "clabel-ct320b"} {
		m, _ := c.Model(id)
		for _, s := range c.StocksFor(m) {
			for _, edge := range []float64{0, 1.5, 3, 5} {
				l, err := HeadLayout(m, s, printer.Merge(m.Options, printer.Settings{"fixed_edge_mm": edge}))
				if err != nil {
					t.Fatalf("%s %s edge %g: %v", id, s.ID, edge, err)
				}
				perMM := m.DotsPerMM()
				centre := (s.FeedMM[0]/2 - edge) * perMM
				mid := float64(l.OffsetDots) + float64(l.ContentDots)/2
				if l.WidthDots != m.HeadDots || l.OffsetDots < 0 || l.OffsetDots+l.ContentDots > l.WidthDots {
					t.Errorf("%s %s edge %g: %+v leaves the head", id, s.ID, edge, l)
				}
				if d := mid - centre; d < -1 || d > 1 {
					t.Errorf("%s %s edge %g: box centre %.2f, label centre %.2f", id, s.ID, edge, mid, centre)
				}
				printable := 0.0
				if s.RotateCW%180 == 0 {
					printable = s.DesignMM[0]
				} else {
					printable = s.DesignMM[1]
				}
				if float64(l.ContentDots) > printable*perMM+1 {
					t.Errorf("%s %s edge %g: box %d dots, printable %.1f", id, s.ID, edge, l.ContentDots, printable*perMM)
				}
				if canvas := New().Canvas(m, s); s.Media != printer.MediaContinuous && l.Rows != rowsOf(canvas, s) {
					t.Errorf("%s %s: %d rows, canvas along feed %d", id, s.ID, l.Rows, rowsOf(canvas, s))
				}
			}
		}
	}
}

// rowsOf is the canvas size along feed: its height, or its width for a quarter turn.
func rowsOf(c printer.Canvas, s *printer.Stock) int {
	if s.RotateCW%180 != 0 {
		return c.Width
	}
	return c.Height
}

func TestHeadLayoutRefusesALabelOffTheHead(t *testing.T) {
	c := catalog(t)
	m, _ := c.Model("clabel-ct320b")
	s, _ := c.Stock("50x30")
	if _, err := HeadLayout(m, s, printer.Settings{"fixed_edge_mm": 30.0}); err == nil {
		t.Fatal("a label 30 mm off the head got a layout")
	}
}

func msDuration(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }
