package tspl

import (
	"bytes"
	"reflect"
	"testing"
)

// TestTinyPage builds the smallest page, two 8-dot rows at 203 dpi, and checks the text
// byte for byte. The DEFLATE bytes are Go's own, so they are checked by inflating.
func TestTinyPage(t *testing.T) {
	page := Page{WidthTenths: Tenths(8, 203), HeightTenths: Tenths(2, 203), GapTenths: 20,
		Density: 8, Speed: 1, Mode: ModeDeflate}
	data, err := page.Encode([]byte{0xf0, 0x0f}, 1)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "SIZE 1.0 mm,0.3 mm\r\nGAP 2 mm,0 mm\r\nDIRECTION 0,0\r\nREFERENCE 0,0\r\nDENSITY 8\r\nSPEED 1\r\nCLS\r\nBITMAP 0,0,1,2,3,"
	if !bytes.HasPrefix(data, []byte(prefix)) || !bytes.HasSuffix(data, []byte("PRINT 1,1\r\n")) {
		t.Fatalf("page text:\n%q", data)
	}
	p, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	header := []string{"SIZE 1.0 mm,0.3 mm", "GAP 2 mm,0 mm", "DIRECTION 0,0", "REFERENCE 0,0", "DENSITY 8", "SPEED 1", "CLS"}
	if !reflect.DeepEqual(p.Header, header) || p.Footer != "PRINT 1,1\r\n" || len(p.Bands) != 1 {
		t.Fatalf("header %q, footer %q, %d bands", p.Header, p.Footer, len(p.Bands))
	}
	b := p.Bands[0]
	if b.X != 0 || b.Y != 0 || b.BytesPerRow != 1 || b.Rows != 2 || b.Mode != ModeDeflate {
		t.Fatalf("band %+v", b)
	}
	if rows, err := p.Raster(); err != nil || !bytes.Equal(rows, []byte{0xf0, 0x0f}) {
		t.Fatalf("rows %x, %v", rows, err)
	}
}

func TestBandsAndModes(t *testing.T) {
	raster := make([]byte, 3*95) // 95 rows of 3 bytes: bands of 40, 40, 15
	for i := range raster {
		raster[i] = byte(i * 7)
	}
	for _, tc := range []struct {
		page   Page
		bands  []int
		invert bool
	}{
		{Page{Mode: ModeDeflate}, []int{40, 40, 15}, false},
		{Page{Mode: ModeOverwrite, BandRows: 50}, []int{50, 45}, false},
		{Page{Mode: ModeOverwrite, Invert: true}, []int{40, 40, 15}, true},
	} {
		data, err := tc.page.Encode(raster, 3)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		y := 0
		for _, b := range p.Bands {
			if b.Y != y || b.Mode != tc.page.Mode {
				t.Errorf("band at y=%d (want %d) mode %d", b.Y, y, b.Mode)
			}
			got = append(got, b.Rows)
			y += b.Rows
		}
		if !reflect.DeepEqual(got, tc.bands) {
			t.Errorf("%+v: bands %v, want %v", tc.page, got, tc.bands)
		}
		rows, err := p.Raster()
		if err != nil {
			t.Fatal(err)
		}
		want := raster
		if tc.invert {
			want = inverted(raster)
		}
		if !bytes.Equal(rows, want) {
			t.Errorf("%+v: rows differ", tc.page)
		}
	}
}

func TestNumberFormats(t *testing.T) {
	for _, tc := range []struct {
		dots, dpi, tenths int
		text              string
	}{
		{8, 203, 10, "1.0"}, {2, 203, 3, "0.3"}, {576, 203, 721, "72.1"},
		{568, 300, 481, "48.1"}, {354, 300, 300, "30.0"}, {1066, 203, 1334, "133.4"},
	} {
		if got := Tenths(tc.dots, tc.dpi); got != tc.tenths || FormatTenths(got) != tc.text {
			t.Errorf("Tenths(%d, %d) = %d %q, want %d %q", tc.dots, tc.dpi, got, FormatTenths(got), tc.tenths, tc.text)
		}
	}
	for t10, want := range map[int]string{0: "0", 20: "2", 25: "2.5", 150: "15", 1001: "100.1"} {
		if got := trimmedTenths(t10); got != want {
			t.Errorf("trimmedTenths(%d) = %q, want %q", t10, got, want)
		}
	}
}

func TestEncodeRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		n, bpr int
		mode   Mode
	}{{0, 1, ModeDeflate}, {5, 2, ModeDeflate}, {4, 0, ModeDeflate}, {4, 2, Mode(1)}} {
		if _, err := (Page{Mode: tc.mode}).Encode(make([]byte, tc.n), tc.bpr); err == nil {
			t.Errorf("Encode(%d bytes, %d per row, mode %d) accepted bad input", tc.n, tc.bpr, tc.mode)
		}
	}
}

func TestRawDeflateHasNoZlibHeader(t *testing.T) {
	data, err := RawDeflate(make([]byte, 72*40))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= 2 && data[0] == 0x78 && (uint16(data[0])<<8|uint16(data[1]))%31 == 0 {
		t.Fatalf("output starts with a zlib header: %x", data[:2])
	}
	b := Band{BytesPerRow: 72, Rows: 40, Mode: ModeDeflate, Data: data}
	if rows, err := b.Packed(); err != nil || len(rows) != 72*40 {
		t.Fatalf("Packed: %d bytes, %v", len(rows), err)
	}
}
