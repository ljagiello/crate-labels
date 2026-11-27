package ipp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// update rewrites the golden files under testdata/golden/ipp from the current code:
// go test ./internal/ipp -update
var update = flag.Bool("update", false, "rewrite golden files from the current code")

var (
	goldenDir  = filepath.Join("..", "..", "testdata", "golden")
	brotherGPA = filepath.Join("..", "..", "testdata", "ipp", "brother-hl-l3290cdw-get-printer-attributes.bin")
	brotherURI = "ipp://198.51.100.9:631/ipp/print"
	brotherEP  = Endpoint{Host: "198.51.100.9", Port: 631, Resource: "/ipp/print"}
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSON(t *testing.T, path string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(readFile(t, path), &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// golden compares got with the file at path, or writes it under -update.
func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the code's output (%d bytes, want %d); run go test -update after checking the change", path, len(got), len(want))
	}
}

// goldenJSON is golden for an indented JSON rendering of v.
func goldenJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, path, append(b, '\n'))
}

func brother(t *testing.T) *Printer {
	t.Helper()
	m, err := Decode(readFile(t, brotherGPA))
	if err != nil {
		t.Fatal(err)
	}
	return &Printer{Response: m}
}

// --- a JSON view of messages, for the decoded fixtures ---------------------------------

func valueJSON(v Value) map[string]any {
	out := map[string]any{"tag": fmt.Sprintf("0x%02x", byte(v.Tag))}
	switch d := v.Data.(type) {
	case Collection:
		members := []any{}
		for _, m := range d {
			members = append(members, attrJSON(m))
		}
		out["collection"] = members
	case Resolution:
		out["resolution"] = map[string]any{"x": d.X, "y": d.Y, "units": d.Units}
	case Range:
		out["range"] = []any{d.Lower, d.Upper}
	case TextWithLanguage:
		out["text_with_language"] = map[string]any{"language": d.Language, "text": d.Text}
	case []byte:
		out["hex"] = hex.EncodeToString(d)
	case nil:
		out["out_of_band"] = true
	default:
		out["value"] = d
	}
	return out
}

func attrJSON(a Attribute) map[string]any {
	values := []any{}
	for _, v := range a.Values {
		values = append(values, valueJSON(v))
	}
	return map[string]any{"name": a.Name, "values": values}
}

func messageJSON(m *Message) map[string]any {
	groups := []any{}
	for _, g := range m.Groups {
		attrs := []any{}
		for _, a := range g.Attributes {
			attrs = append(attrs, attrJSON(a))
		}
		groups = append(groups, map[string]any{"tag": fmt.Sprintf("0x%02x", byte(g.Tag)), "attributes": attrs})
	}
	return map[string]any{
		"version":     []any{int(m.Major), int(m.Minor)},
		"code":        fmt.Sprintf("0x%04x", m.Code),
		"request_id":  m.RequestID,
		"groups":      groups,
		"data_length": len(m.Data),
	}
}

// normalise round-trips through JSON so numbers compare as float64 like a fixture.
func normalise(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// --- codec -------------------------------------------------------------------------------

func TestBrotherFixtureRoundTripsByteForByte(t *testing.T) {
	data := readFile(t, brotherGPA)
	m, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Major != 1 || m.Minor != 1 || m.Code != 0 || m.RequestID != 1 {
		t.Fatalf("header = %d.%d code %#x id %d", m.Major, m.Minor, m.Code, m.RequestID)
	}
	if len(m.Groups) != 2 || m.Groups[0].Tag != TagOperationGroup || m.Groups[1].Tag != TagPrinterGroup {
		t.Fatalf("groups = %v", m.Groups)
	}
	out, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("re-encoded %d bytes, fixture is %d", len(out), len(data))
	}
}

// brother-gpa-decoded.json is the capture decoded value by value. It only changes if
// the capture does, so -update does not rewrite it.
func TestBrotherFixtureMatchesDecodedJSON(t *testing.T) {
	m, err := Decode(readFile(t, brotherGPA))
	if err != nil {
		t.Fatal(err)
	}
	want := readJSON(t, filepath.Join(goldenDir, "ipp", "brother-gpa-decoded.json"))
	if got := normalise(t, messageJSON(m)); !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("decoded message differs from brother-gpa-decoded.json; got:\n%.2000s", gb)
	}
}

func TestValueTypesRoundTrip(t *testing.T) {
	nested := Collection{
		NewCollection("media-size", Integer("x-dimension", 5000), Integer("y-dimension", 3000)),
		Keyword("media-type", "labels"),
	}
	attrs := []Attribute{
		Integer("copies", 1, 2, -3),
		{Name: "flag", Values: []Value{{TagBoolean, true}, {TagBoolean, false}}},
		Enum("print-quality", 5),
		Attr("printer-resolution", TagResolution, Resolution{300, 600, UnitsDPCM}),
		Attr("range", TagRangeOfInteger, Range{1, 999}),
		Attr("info", TagTextWithLanguage, TextWithLanguage{"pl", "Drukarka łódź"}),
		Attr("raw", TagOctetString, []byte{0, 1, 0xfe}),
		Attr("date", TagDateTime, make([]byte, 11)),
		{Name: "missing", Values: []Value{{TagNoValue, nil}}},
		{Name: "registered-later", Values: []Value{{Tag(0x17), nil}}},
		Attr("media-col", TagBeginCollection, nested, nested),
		Mime("document-format", "image/urf"),
	}
	m := NewMessage(0x0002, 7, Group{Tag: TagJobGroup, Attributes: attrs})
	m.Data = []byte("DOC")
	enc, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dec, m) {
		t.Fatalf("decoded %+v\nwant %+v", dec, m)
	}
	again, _ := Encode(dec)
	if !bytes.Equal(again, enc) {
		t.Fatal("second encoding differs")
	}
}

// RFC 8010 section 3.1.6 lays a one-member collection out like this.
func TestCollectionWireFormat(t *testing.T) {
	out, err := Encode(NewMessage(1, 1, Group{Tag: TagJobGroup, Attributes: []Attribute{NewCollection("c", Integer("n", 1))}}))
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex("02 34 00 01 63 00 00 4a 00 00 00 01 6e 21 00 00 00 04 00 00 00 01 37 00 00 00 00 03")
	if !bytes.Equal(out[8:], want) {
		t.Fatalf("got % x", out[8:])
	}
}

func TestOutOfBandRange(t *testing.T) {
	for tag, want := range map[Tag]bool{0x0f: false, 0x10: true, 0x13: true, 0x1f: true, 0x20: false, TagInteger: false} {
		if tag.IsOutOfBand() != want {
			t.Errorf("0x%02x: %v", byte(tag), !want)
		}
	}
	// RFC 8010 section 3.8: an out-of-band value has no meaning, so bytes sent with one
	// are not kept.
	m, err := Decode(mustHex("0101 0000 00000001 04 13 0001 61 0002 abcd 03"))
	if err != nil || m.Groups[0].Attributes[0].Values[0].Data != nil {
		t.Fatalf("%v %v", m, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, tc := range []struct{ hex, want string }{
		{"0101 0000 00000001 21 0001 61 0004 00000001 03", "before the first delimiter tag"},
		{"0101 0000 00000001 01 21 0001 61 0002 0001 03", "must be 4 octets"},
		{"0101 0000 00000001 01 44 0001 61 0005 6162", "only 2 remain"},
		{"0101 0000 00000001 01", "no end-of-attributes-tag"},
		{"0101 0000 00000001 01 44 0000 0001 61 03", "empty name) opens a group"},
		{"0101 0000 00000001 01 34 0001 63 0000 21 0000 0004 00000001 37 0000 0000 03", "before its memberAttrName"},
		{"0101 0000 00000001 01 35 0001 74 0008 0002 656e 0001 41 ff 03", "after its text"},
	} {
		_, err := Decode(mustHex(tc.hex))
		if err == nil || !errors.Is(err, ErrFormat) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.hex, err, tc.want)
		}
	}
}

func TestEncodeErrors(t *testing.T) {
	for _, m := range []*Message{
		NewMessage(1, 1, Group{Tag: TagOperationGroup, Attributes: []Attribute{{Name: "x"}}}),
		NewMessage(1, 1, Group{Tag: TagJobGroup, Attributes: []Attribute{NewCollection("c", Attribute{Name: "empty"})}}),
		NewMessage(1, 1, Group{Tag: TagJobGroup, Attributes: []Attribute{Attr("odd", TagInteger, 1.5)}}),
		NewMessage(1, 1, Group{Tag: TagJobGroup, Attributes: []Attribute{Attr("long", TagText, strings.Repeat("x", 70000))}}),
	} {
		if _, err := Encode(m); !errors.Is(err, ErrFormat) {
			t.Errorf("%v: err = %v", m.Groups[0].Attributes[0].Name, err)
		}
	}
}

func TestLookupStaysInItsGroup(t *testing.T) {
	m := NewMessage(0, 1,
		Group{Tag: TagOperationGroup, Attributes: []Attribute{Integer("job-id", 1)}},
		Group{Tag: TagJobGroup, Attributes: []Attribute{Integer("job-id", 2)}})
	if m.Lookup(TagJobGroup, "job-id").First() != 2 || m.Lookup(TagPrinterGroup, "job-id") != nil {
		t.Fatal("lookup crossed groups")
	}
}

// --- request bytes ---------------------------------------------------------------------------

func encode(t *testing.T, m *Message) []byte {
	t.Helper()
	b, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// labelJob is a 50×80 mm label on the Brother, which has only Letter loaded.
func labelJob(t *testing.T) JobRequest {
	t.Helper()
	p := brother(t)
	c := ChooseMedia(p, [2]float64{50, 80}, false)
	if c == nil || c.Exact {
		t.Fatalf("choice = %+v", c)
	}
	return JobRequest{Name: "Vinyl Label", DocumentFormat: MimeURF, JobAttributes: Ticket{Media: c, Grey: true}.Attributes(p)}
}

// requestCases are the protocol requests kept as golden bytes in requests/.
func requestCases(t *testing.T) map[string]*Message {
	job := labelJob(t)
	return map[string]*Message{
		"gpa":                    GetPrinterAttributesRequest(brotherEP, 1, DefaultUser),
		"gpa-printer-status":     GetPrinterAttributesRequest(brotherEP, 5, DefaultUser, statusAttributes...),
		"print-job-header-50x80": JobRequestMessage(OpPrintJob, brotherEP, 2, DefaultUser, job, nil),
		"validate-job-50x80":     JobRequestMessage(OpValidateJob, brotherEP, 2, DefaultUser, job, nil),
		"get-job-attributes-42":  GetJobAttributesRequest(brotherEP, 3, DefaultUser, 42),
		"cancel-job-42":          CancelJobRequest(brotherEP, 4, DefaultUser, 42),
	}
}

func TestRequestFixtures(t *testing.T) {
	for name, m := range requestCases(t) {
		t.Run(name, func(t *testing.T) {
			bin := encode(t, m)
			golden(t, filepath.Join(goldenDir, "ipp", "requests", name+".bin"), bin)
			dec, err := Decode(bin)
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, filepath.Join(goldenDir, "ipp", "requests", name+".json"), messageJSON(dec))
		})
	}
}

func TestValidateJobIsPrintJobWithoutDocument(t *testing.T) {
	doc := []byte("UNIRAST\x00...")
	p := encode(t, JobRequestMessage(OpPrintJob, brotherEP, 2, DefaultUser, labelJob(t), doc))
	if !bytes.HasSuffix(p, doc) {
		t.Fatal("Print-Job does not end with the document")
	}
	v := encode(t, JobRequestMessage(OpValidateJob, brotherEP, 2, DefaultUser, labelJob(t), nil))
	if !bytes.Equal(v[2:4], []byte{0, 4}) || !bytes.Equal(v[4:], p[4:len(p)-len(doc)]) {
		t.Fatal("Validate-Job is not Print-Job with op 4 and no document")
	}
}

// The operation group follows RFC 8011 section 4.1.4: charset, natural language, then
// the target.
func TestOperationGroupOrder(t *testing.T) {
	for name, m := range requestCases(t) {
		g := m.Groups[0]
		var names []string
		for _, a := range g.Attributes[:4] {
			names = append(names, a.Name)
		}
		if g.Tag != TagOperationGroup || strings.Join(names, ",") != "attributes-charset,attributes-natural-language,printer-uri,requesting-user-name" {
			t.Errorf("%s: %v", name, names)
		}
		if m.Lookup(TagOperationGroup, "printer-uri").First() != brotherURI {
			t.Errorf("%s: printer-uri", name)
		}
	}
}

func TestEndpointParsing(t *testing.T) {
	for _, tc := range []struct{ in, uri, url string }{
		{brotherURI, brotherURI, "http://198.51.100.9:631/ipp/print"},
		{"ipp://198.51.100.9", brotherURI, "http://198.51.100.9:631/ipp/print"},
		{"198.51.100.9", brotherURI, "http://198.51.100.9:631/ipp/print"},
		{"http://Printer.local.:8631/ipp/x", "ipp://printer.local:8631/ipp/x", "http://printer.local:8631/ipp/x"},
		{"ipp://[fe80::1]/ipp/print", "ipp://[fe80::1]:631/ipp/print", "http://[fe80::1]:631/ipp/print"},
		{"ipp://host//", "ipp://host:631/ipp/print", "http://host:631/ipp/print"},
	} {
		ep, err := ParseEndpoint(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if ep.PrinterURI() != tc.uri || ep.HTTPURL() != tc.url {
			t.Errorf("%s: uri %s url %s", tc.in, ep.PrinterURI(), ep.HTTPURL())
		}
	}
	for _, bad := range []string{"ipps://printer.local/ipp/print", "ipp://", "ipp://host:99999/x"} {
		if _, err := ParseEndpoint(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}
