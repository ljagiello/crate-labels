package munbyn

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

var goldenDir = filepath.Join("..", "..", "..", "testdata", "golden", "munbyn")

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

// codecFixture is testdata/golden/munbyn/codec.json, written by hand. Frames, varints,
// zigzag values and builder outputs are protocol vectors. The rest describe how
// crate-labels reads replies.
type codecFixture struct {
	Frames []struct {
		Name    string `json:"name"`
		Payload string `json:"payload"`
		Frame   string `json:"frame"`
	} `json:"frames"`
	Varint []struct {
		Value uint64 `json:"value"`
		Hex   string `json:"hex"`
	} `json:"varint"`
	Zigzag32 []struct {
		Value  int32  `json:"value"`
		Zigzag uint32 `json:"zigzag"`
	} `json:"zigzag32"`
	Builder []struct {
		Ops  [][]any `json:"ops"`
		Hex  string  `json:"hex"`
		What string  `json:"what"`
	} `json:"builder"`
	DecodeFields []struct {
		Hex    string         `json:"hex"`
		Fields map[string]any `json:"fields"`
		What   string         `json:"what"`
	} `json:"decode_fields"`
	Reassembler []struct {
		Chunks   []string `json:"chunks"`
		Payloads []string `json:"payloads"`
		What     string   `json:"what"`
	} `json:"reassembler"`
	Replies []struct {
		Frame string         `json:"frame"`
		Reply map[string]any `json:"reply"`
		What  string         `json:"what"`
	} `json:"replies"`
	Problem []struct {
		Status    int32   `json:"status"`
		Condition *string `json:"condition"`
	} `json:"problem"`
	SectionRows []struct {
		WidthDots int `json:"width_dots"`
		Rows      int `json:"rows"`
		Bytes     int `json:"bytes"`
	} `json:"section_rows"`
}

func loadCodec(t *testing.T) codecFixture {
	t.Helper()
	var c codecFixture
	if err := json.Unmarshal(readGolden(t, "codec.json"), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGoldenFrames(t *testing.T) {
	named := map[string][]byte{
		"device_info":               DeviceInfoCommand,
		"print_end":                 PrintEndCommand,
		"paper_type_set_gap":        PaperTypeSetCommand(PaperGap),
		"paper_type_set_continuous": PaperTypeSetCommand(PaperContinuous),
		"paper_type_set_black_mark": PaperTypeSetCommand(PaperBlackMark),
	}
	frames := loadCodec(t).Frames
	if len(frames) != len(named) {
		t.Fatalf("codec.json has %d frames, test knows %d", len(frames), len(named))
	}
	for _, f := range frames {
		got, err := Wrap(unhex(t, f.Payload))
		if err != nil || hex.EncodeToString(got) != f.Frame {
			t.Errorf("%s: Wrap = %x, %v; want %s", f.Name, got, err, f.Frame)
		}
		if !bytes.Equal(named[f.Name], got) {
			t.Errorf("%s: named command = %x, want %s", f.Name, named[f.Name], f.Frame)
		}
		if bin := readGolden(t, "frames", f.Name+".bin"); !bytes.Equal(bin, got) {
			t.Errorf("%s: frames/%s.bin = %x, want %x", f.Name, f.Name, bin, got)
		}
		var r Reassembler
		if p := r.Push(got); len(p) != 1 || hex.EncodeToString(p[0]) != f.Payload {
			t.Errorf("%s: reassembled into %x", f.Name, p)
		}
	}
}

func TestWrapLimits(t *testing.T) {
	if _, err := Wrap(make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("Wrap accepted a payload over 14 bits")
	}
	f, err := Wrap(make([]byte, MaxPayload))
	if err != nil {
		t.Fatal(err)
	}
	var r Reassembler
	if p := r.Push(f); len(p) != 1 || len(p[0]) != MaxPayload {
		t.Fatalf("a %d-byte payload did not come back", MaxPayload)
	}
}

func TestGoldenVarintAndZigzag(t *testing.T) {
	c := loadCodec(t)
	for _, v := range c.Varint {
		if got := hex.EncodeToString(AppendVarint(nil, v.Value)); got != v.Hex {
			t.Errorf("varint(%d) = %s, want %s", v.Value, got, v.Hex)
		}
		if back, next, ok := varintAt(unhex(t, v.Hex), 0); !ok || back != v.Value || next != len(v.Hex)/2 {
			t.Errorf("varintAt(%s) = %d, %d, %v", v.Hex, back, next, ok)
		}
	}
	for _, z := range c.Zigzag32 {
		if got := Zigzag32(z.Value); got != z.Zigzag {
			t.Errorf("zigzag32(%d) = %d, want %d", z.Value, got, z.Zigzag)
		}
	}
}

func TestGoldenBuilder(t *testing.T) {
	for _, c := range loadCodec(t).Builder {
		var b Builder
		for _, op := range c.Ops {
			field := int(op[1].(float64))
			switch op[0] {
			case "uint":
				b.Uint(field, uint32(op[2].(float64)))
			case "signed":
				b.Signed(field, int32(op[2].(float64)))
			case "blob":
				b.Blob(field, unhex(t, op[2].(string)))
			default:
				t.Fatalf("unknown op %v", op)
			}
		}
		if got := hex.EncodeToString(b.Payload()); got != c.Hex {
			t.Errorf("%s: got %s, want %s", c.What, got, c.Hex)
		}
	}
}

func TestGoldenDecodeFields(t *testing.T) {
	for _, c := range loadCodec(t).DecodeFields {
		got := DecodeFields(unhex(t, c.Hex))
		want := map[int]Field{}
		for k, v := range c.Fields {
			n, _ := strconv.Atoi(k)
			switch v := v.(type) {
			case float64:
				want[n] = Field{Num: uint64(v)}
			case string:
				want[n] = Field{Bytes: unhex(t, v), IsBytes: true}
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", c.What, got, want)
			continue
		}
		for k, w := range want {
			g := got[k]
			if g.IsBytes != w.IsBytes || g.Num != w.Num || !bytes.Equal(g.Bytes, w.Bytes) {
				t.Errorf("%s: field %d = %+v, want %+v", c.What, k, g, w)
			}
		}
	}
}

func pushAll(r *Reassembler, chunks ...[]byte) []string {
	got := []string{}
	for _, c := range chunks {
		for _, p := range r.Push(c) {
			got = append(got, hex.EncodeToString(p))
		}
	}
	return got
}

func TestGoldenReassembler(t *testing.T) {
	for _, c := range loadCodec(t).Reassembler {
		var chunks [][]byte
		for _, s := range c.Chunks {
			chunks = append(chunks, unhex(t, s))
		}
		var r Reassembler
		got := pushAll(&r, chunks...)
		want := append([]string{}, c.Payloads...)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", c.What, got, want)
		}
	}
}

// TestReassemblerSplitsAnywhere cuts a stream of frames and noise at every offset and
// in random pieces: the payloads must come back the same, in order.
func TestReassemblerSplitsAnywhere(t *testing.T) {
	var stream []byte
	var want []string
	for i := range 12 {
		p := bytes.Repeat([]byte{byte(i), 0x55}, 1+i*19)
		f, _ := Wrap(p)
		stream = append(stream, 0x13, 0x55) // noise, including a lone start byte
		stream = append(stream, f...)
		want = append(want, hex.EncodeToString(p))
	}
	for cut := range len(stream) + 1 {
		var r Reassembler
		if got := pushAll(&r, stream[:cut], stream[cut:]); !reflect.DeepEqual(got, want) {
			t.Fatalf("cut at %d: got %d payloads, want %d", cut, len(got), len(want))
		}
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for range 50 {
		var r Reassembler
		var got []string
		for at := 0; at < len(stream); {
			n := 1 + rng.IntN(40)
			got = append(got, pushAll(&r, stream[at:min(at+n, len(stream))])...)
			at += n
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("random pieces: got %d payloads, want %d", len(got), len(want))
		}
	}
}

// TestReassemblerTailIsBounded feeds noise that keeps opening false starts: what the
// reassembler keeps between notifications never exceeds one maximal frame.
func TestReassemblerTailIsBounded(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 9))
	var r Reassembler
	for range 300 {
		chunk := make([]byte, 64)
		for i := range chunk {
			chunk[i] = byte(rng.UintN(256))
			if rng.IntN(8) == 0 {
				chunk[i] = frameStart
			}
		}
		r.Push(chunk)
		if len(r.tail) > headerBytes+MaxPayload {
			t.Fatalf("tail grew to %d bytes", len(r.tail))
		}
	}
	f, _ := Wrap([]byte{8, 1})
	// Whatever the noise left open, a whole frame after it still comes through.
	if got := pushAll(&r, f); len(got) == 0 || got[len(got)-1] != "0801" {
		t.Fatalf("frame after noise: got %v", got)
	}
}

func replyJSON(r Reply) map[string]any {
	m := map[string]any{"kind": r.Kind.String(), "event": float64(r.Event)}
	if r.Code != 0 {
		m["code"] = float64(r.Code)
	}
	if r.Event == EventDeviceReport {
		m["report"], m["state"] = float64(r.Report), float64(r.State)
	}
	if r.Kind == KindInfo {
		m["paper"], m["firmware"], m["battery"] = float64(r.Paper), r.Firmware, float64(r.Battery)
	}
	return m
}

func TestGoldenReplies(t *testing.T) {
	for _, c := range loadCodec(t).Replies {
		var r Reassembler
		payloads := r.Push(unhex(t, c.Frame))
		if len(payloads) != 1 {
			t.Fatalf("%s: reassembled into %d payloads", c.What, len(payloads))
		}
		if got := replyJSON(Decode(payloads[0])); !reflect.DeepEqual(got, c.Reply) {
			t.Errorf("%s: got %v, want %v", c.What, got, c.Reply)
		}
	}
}

func TestGoldenProblem(t *testing.T) {
	byName := map[string]string{}
	for _, c := range conditions {
		byName[c.Name] = c.Message
	}
	for _, c := range loadCodec(t).Problem {
		got := Problem(c.Status)
		var want string
		switch {
		case c.Condition == nil:
			want = ""
		case *c.Condition == "unknown":
			want = "The printer sent status code " + strconv.Itoa(int(c.Status)) + ", which crate-labels does not recognise."
		default:
			want = byName[*c.Condition]
			if want == "" {
				t.Fatalf("fixture names condition %q, which the driver lacks", *c.Condition)
			}
		}
		if got != want {
			t.Errorf("Problem(%d) = %q, want %q", c.Status, got, want)
		}
	}
	bits, messages := map[uint]bool{}, map[string]bool{}
	for _, c := range conditions {
		if bits[c.Bit] || messages[c.Message] || c.Message == "" {
			t.Errorf("condition %+v repeats a bit or a message", c)
		}
		bits[c.Bit], messages[c.Message] = true, true
	}
	if len(bits) != 9 {
		t.Errorf("%d conditions, want bits 0-8", len(bits))
	}
}

func TestGoldenSectionRows(t *testing.T) {
	for _, c := range loadCodec(t).SectionRows {
		if r, b := SectionRows(c.WidthDots), SectionBytes(c.WidthDots); r != c.Rows || b != c.Bytes {
			t.Errorf("width %d: %d rows / %d bytes, want %d / %d", c.WidthDots, r, b, c.Rows, c.Bytes)
		}
	}
	for w := 8; w <= 576; w += 8 {
		if b := SectionBytes(w); b > sectionCap || b+w <= sectionCap || SectionRows(w)%stripRows != 0 {
			t.Errorf("width %d: %d bytes per section is not the most whole strips under %d", w, b, sectionCap)
		}
	}
}

func TestWorkedExampleTwoRows(t *testing.T) {
	// One 8-dot row f0 and one 0f: two heatshrink literals, f843c0, in one package.
	sections, err := Sections([]byte{0xf0, 0x0f}, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := "551d802408042a19080112" + "03f843c0" + "1804200128013001380140015006580160" + "01"
	if len(sections) != 1 || len(sections[0]) != 1 || hex.EncodeToString(sections[0][0]) != want {
		t.Fatalf("got %x, want %s", sections, want)
	}
}

func TestSectionsRejectsPartialRows(t *testing.T) {
	for _, tc := range []struct {
		n, width int
	}{{0, 384}, {47, 384}, {48, 0}, {48, 380}} {
		if _, err := Sections(make([]byte, tc.n), tc.width); err == nil {
			t.Errorf("Sections(%d bytes, %d dots) accepted bad input", tc.n, tc.width)
		}
	}
}
