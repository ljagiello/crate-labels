package heatshrink

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the encoded corpus in testdata/golden/heatshrink from the current encoder")

var goldenDir = filepath.Join("..", "..", "testdata", "golden", "heatshrink")

// corpusManifest lists the corpus. The inputs and descriptions are written by hand; the
// encoded files and their lengths are snapshots of Encode, rewritten by -update.
type corpusManifest struct {
	WindowBits    int `json:"window_bits"`
	LookaheadBits int `json:"lookahead_bits"`
	MinLength     int `json:"min_length"`
	Cases         []struct {
		Name       string `json:"name"`
		Input      string `json:"input"`
		Encoded    string `json:"encoded"`
		InputLen   int    `json:"input_len"`
		EncodedLen int    `json:"encoded_len"`
		What       string `json:"what"`
	} `json:"cases"`
	DecodeCases []struct {
		Encoded string `json:"encoded"`
		Decoded string `json:"decoded"`
		What    string `json:"what"`
	} `json:"decode_cases"`
}

func loadManifest(t *testing.T) corpusManifest {
	t.Helper()
	var m corpusManifest
	if err := json.Unmarshal(readGolden(t, "manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Cases) == 0 {
		t.Fatal("manifest lists no cases")
	}
	return m
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestMain rewrites the encoded corpus before the tests read it when -update is given.
func TestMain(m *testing.M) {
	flag.Parse()
	if *update {
		if err := rewriteCorpus(); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

func rewriteCorpus() error {
	path := filepath.Join(goldenDir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m corpusManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	m.WindowBits, m.LookaheadBits, m.MinLength = WindowBits, LookaheadBits, MinLength
	for i, c := range m.Cases {
		in, err := os.ReadFile(filepath.Join(goldenDir, c.Input))
		if err != nil {
			return err
		}
		enc := Encode(in)
		if err := os.WriteFile(filepath.Join(goldenDir, c.Encoded), enc, 0o644); err != nil {
			return err
		}
		m.Cases[i].InputLen, m.Cases[i].EncodedLen = len(in), len(enc)
	}
	out, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func TestManifestParameters(t *testing.T) {
	m := loadManifest(t)
	if m.WindowBits != WindowBits || m.LookaheadBits != LookaheadBits || m.MinLength != MinLength {
		t.Fatalf("corpus is %d/%d/%d, package is %d/%d/%d",
			m.WindowBits, m.LookaheadBits, m.MinLength, WindowBits, LookaheadBits, MinLength)
	}
	if MinLength != 3 {
		t.Fatalf("MinLength = %d; the break-even rule gives 3 for 11/4", MinLength)
	}
}

// TestCorpusSnapshot pins the encoder's output. The exact bytes are not a format
// requirement; any change reaches the printer, so it must be deliberate (rerun with
// -update).
func TestCorpusSnapshot(t *testing.T) {
	for _, c := range loadManifest(t).Cases {
		in, want := readGolden(t, c.Input), readGolden(t, c.Encoded)
		if len(in) != c.InputLen || len(want) != c.EncodedLen {
			t.Errorf("%s: files are %d/%d bytes, manifest says %d/%d", c.Name, len(in), len(want), c.InputLen, c.EncodedLen)
		}
		if got := Encode(in); !bytes.Equal(got, want) {
			t.Errorf("%s: Encode changed (%d bytes, snapshot %d); rerun with -update if intended", c.Name, len(got), len(want))
		}
	}
}

// TestCorpusRoundTrip feeds every encoded corpus file to the ported library decoder at
// several sink and output buffer sizes, and checks the token rules on each.
func TestCorpusRoundTrip(t *testing.T) {
	sizes := []struct{ inBuf, sink, out int }{
		{256, 64, 1024}, {1, 1, 1}, {16, 7, 13}, {256, 200, 4096}, {1 << 20, 1 << 20, 1 << 20},
	}
	for _, c := range loadManifest(t).Cases {
		in, enc := readGolden(t, c.Input), readGolden(t, c.Encoded)
		checkTokens(t, c.Name, enc, in)
		for _, s := range sizes {
			got, err := decode(enc, WindowBits, LookaheadBits, s.inBuf, s.sink, s.out)
			if err != nil {
				t.Fatalf("%s %+v: %v", c.Name, s, err)
			}
			if !bytes.Equal(got, in) {
				t.Fatalf("%s %+v: decoder gave %d bytes, want %d", c.Name, s, len(got), len(in))
			}
		}
	}
}

type token struct {
	literal          bool
	value            byte
	distance, length int
}

// parseTokens splits a stream into whole tokens and returns the pad bits after them. It
// is written from the format description, independently of both coders.
func parseTokens(t *testing.T, stream []byte) (tokens []token, pad int) {
	t.Helper()
	total, pos := len(stream)*8, 0
	read := func(n int) int {
		v := 0
		for range n {
			v = v<<1 | int(stream[pos/8]>>(7-pos%8)&1)
			pos++
		}
		return v
	}
	for {
		left := total - pos
		if left == 0 {
			return tokens, 0
		}
		need := 1 + WindowBits + LookaheadBits
		if stream[pos/8]>>(7-pos%8)&1 == 1 {
			need = 9
		}
		if left < need {
			if left >= 8 {
				t.Fatalf("%d bits left over after the last token", left)
			}
			if v := read(left); v != 0 {
				t.Fatalf("pad bits are %b, want zeros", v)
			}
			return tokens, left
		}
		if read(1) == 1 {
			tokens = append(tokens, token{literal: true, value: byte(read(8))})
		} else {
			d := read(WindowBits) + 1
			tokens = append(tokens, token{distance: d, length: read(LookaheadBits) + 1})
		}
	}
}

// checkTokens enforces the encoder's rules on a stream and replays it: every
// back-reference is MinLength..16 bytes long and reaches back no further than the bytes
// already produced, so it never touches a decoder's initial window.
func checkTokens(t *testing.T, name string, stream, want []byte) {
	t.Helper()
	tokens, _ := parseTokens(t, stream)
	var out []byte
	for i, tk := range tokens {
		if tk.literal {
			out = append(out, tk.value)
			continue
		}
		if tk.length < MinLength || tk.length > 1<<LookaheadBits {
			t.Fatalf("%s: token %d has length %d", name, i, tk.length)
		}
		if tk.distance > len(out) || tk.distance > 1<<WindowBits {
			t.Fatalf("%s: token %d reaches back %d bytes with %d produced", name, i, tk.distance, len(out))
		}
		for range tk.length {
			out = append(out, out[len(out)-tk.distance])
		}
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("%s: tokens replay to %d bytes, want %d", name, len(out), len(want))
	}
}

func TestBlankAndBlackSectionsCompressFully(t *testing.T) {
	// One literal, then 239 references of 16 and one of 15: 9 + 240*16 bits = 482 bytes.
	for _, b := range []byte{0x00, 0xff} {
		if got := len(Encode(bytes.Repeat([]byte{b}, 3840))); got != 482 {
			t.Errorf("3840 bytes of %02x encode to %d bytes, want 482", b, got)
		}
	}
}

func TestWindowEdge(t *testing.T) {
	farthest := func(name string) int {
		tokens, _ := parseTokens(t, Encode(readGolden(t, name)))
		d := 0
		for _, tk := range tokens {
			d = max(d, tk.distance)
		}
		return d
	}
	if d := farthest("window-2048.in.bin"); d != 2048 {
		t.Errorf("window-2048: farthest reference is %d, want 2048", d)
	}
	if d := farthest("window-2049.in.bin"); d >= 2048 {
		t.Errorf("window-2049: farthest reference is %d, want under 2048", d)
	}
}

func TestShortInputs(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{nil, ""},
		{[]byte{0x7f}, "bf80"},         // 1 0111_1111, then 7 pad bits
		{[]byte{0xf0, 0x0f}, "f843c0"}, // two literals
		{[]byte("ABAB"), "a0d0a83420"}, // four literals: a 2-byte match is below MinLength
		{bytes.Repeat([]byte("A"), 8), "a0800300"},
	} {
		if got := hex.EncodeToString(Encode(tc.in)); got != tc.want {
			t.Errorf("Encode(%x) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func randomInput(rng *rand.Rand, n int, alphabet []byte) []byte {
	data := make([]byte, n)
	for i := range data {
		if alphabet == nil {
			data[i] = byte(rng.UintN(256))
		} else {
			data[i] = alphabet[rng.IntN(len(alphabet))]
		}
	}
	return data
}

var alphabets = [][]byte{{0, 0xff}, {0, 1, 2, 0xff}, {0xaa, 0x55}, {0}, nil}

func TestRandomRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 80 {
		data := randomInput(rng, rng.IntN(6000), alphabets[trial%len(alphabets)])
		enc := Encode(data)
		checkTokens(t, "random", enc, data)
		got, err := Decode(enc)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("trial %d (%d bytes): round trip failed (%v)", trial, len(data), err)
		}
	}
}

// optimalBits is the smallest stream size in bits for data under the encoder's rules,
// found by trying every distance at every position.
func optimalBits(data []byte) int {
	n := len(data)
	longest := make([]int, n)
	for i := range n {
		for d := 1; d <= min(i, 1<<WindowBits); d++ {
			k := 0
			for k < min(1<<LookaheadBits, n-i) && data[i-d+k] == data[i+k] {
				k++
			}
			longest[i] = max(longest[i], k)
		}
	}
	cost := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		cost[i] = cost[i+1] + 9
		for l := MinLength; l <= longest[i]; l++ {
			cost[i] = min(cost[i], cost[i+l]+1+WindowBits+LookaheadBits)
		}
	}
	return cost[0]
}

func streamBits(t *testing.T, stream []byte) int {
	_, pad := parseTokens(t, stream)
	return len(stream)*8 - pad
}

// TestEncodeIsOptimal compares Encode with an exhaustive search on small inputs: the hash
// chains must find every longest match, and the parse must reach the minimum size.
func TestEncodeIsOptimal(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := range 60 {
		data := randomInput(rng, 1+rng.IntN(700), alphabets[trial%len(alphabets)])
		if got, want := streamBits(t, Encode(data)), optimalBits(data); got != want {
			t.Fatalf("trial %d (%d bytes): %d bits, optimum %d", trial, len(data), got, want)
		}
	}
	tie := readGolden(t, "tie-nearest.in.bin")
	if got, want := streamBits(t, Encode(tie)), optimalBits(tie); got != want {
		t.Fatalf("tie-nearest: %d bits, optimum %d", got, want)
	}
}

func TestPaddingNeverYieldsBytes(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 5))
	seen := map[int]bool{}
	for range 400 {
		data := randomInput(rng, 1+rng.IntN(59), []byte{0, 1, 0xff})
		enc := Encode(data)
		_, pad := parseTokens(t, enc)
		seen[pad] = true
		got, err := decode(enc, WindowBits, LookaheadBits, 1, 1, 1)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("decode(%x) = %x, %v; want %x", enc, got, err, data)
		}
	}
	for pad := range 8 {
		if !seen[pad] {
			t.Errorf("no stream ended with %d pad bits; the test does not cover it", pad)
		}
	}
}

func TestCorruptedStreamIsDetected(t *testing.T) {
	data := readGolden(t, "label-two-track-section-03.in.bin")
	enc := Encode(data)
	for _, at := range []int{0, len(enc) / 3, len(enc) / 2} {
		broken := bytes.Clone(enc)
		broken[at] ^= 0x10
		if got, _ := Decode(broken); bytes.Equal(got, data) {
			t.Errorf("flipping a bit at byte %d went unnoticed", at)
		}
	}
}

func TestDecodeCases(t *testing.T) {
	for _, c := range loadManifest(t).DecodeCases {
		enc, _ := hex.DecodeString(c.Encoded)
		want, _ := hex.DecodeString(c.Decoded)
		got, err := Decode(enc)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: Decode(%s) = %x, %v; want %x", c.What, c.Encoded, got, err, want)
		}
	}
}

func TestDecoderRejectsBadParameters(t *testing.T) {
	for _, p := range [][3]int{{256, 11, 11}, {0, 11, 4}, {256, 3, 2}, {256, 16, 4}, {256, 11, 2}} {
		if _, err := NewDecoder(p[0], uint8(p[1]), uint8(p[2])); err == nil {
			t.Errorf("NewDecoder%v accepted bad parameters", p)
		}
	}
}

func BenchmarkEncodeLabelSection(b *testing.B) {
	data, err := os.ReadFile(filepath.Join(goldenDir, "label-two-track-section-01.in.bin"))
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		Encode(data)
	}
}
