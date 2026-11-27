package phomemo

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// rowsWithBlanks is a 1-byte-per-row raster of n printed rows with the given rows blank.
func rowsWithBlanks(n int, blank ...int) []byte {
	r := bytes.Repeat([]byte{0xff}, n)
	for _, b := range blank {
		r[b] = 0
	}
	return r
}

func jobRows(jobs []Job) []int {
	var out []int
	for _, j := range jobs {
		out = append(out, j.Rows)
	}
	return out
}

func TestJobsSplitAtBlankRowsNearEvenCuts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  int
		blank []int
		want  []int
	}{
		{"fits in one job", 1200, nil, []int{1200}},
		{"one row over", 1201, nil, []int{600, 601}},
		{"no blank rows: even halves", 1300, nil, []int{650, 650}},
		{"blank row just before the middle", 1300, []int{600}, []int{600, 700}},
		{"blank row just after the middle", 1300, []int{700}, []int{700, 600}},
		{"blank row out of reach", 1300, []int{570}, []int{650, 650}},
		{"the nearer of two blank rows", 1300, []int{620, 670}, []int{670, 630}},
		{"a tie goes to the earlier row", 1300, []int{640, 660}, []int{640, 660}},
		{"no cap on the total", 3601, nil, []int{900, 900, 900, 901}},
		{"a blank row may not overfill a job", 2399, []int{1250}, []int{1199, 1200}},
	} {
		jobs, err := BuildJobs(rowsWithBlanks(tc.rows, tc.blank...), 1, JobSettings{Speed: 3, Darkness: 8, Media: GapMedia})
		if err != nil {
			t.Fatal(err)
		}
		if got := jobRows(jobs); !slices.Equal(got, tc.want) {
			t.Errorf("%s: job rows %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJobsKeepEveryRowWithinTheLimit(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		n := 1 + rng.IntN(6000)
		raster := make([]byte, 2*n)
		for r := range n {
			if rng.IntN(20) != 0 {
				raster[2*r] = byte(1 + rng.IntN(255))
			}
		}
		jobs, err := BuildJobs(raster, 2, JobSettings{Media: GapMedia})
		if err != nil {
			t.Fatal(err)
		}
		if want := (n + jobRowLimit - 1) / jobRowLimit; len(jobs) != want {
			t.Fatalf("%d rows: %d jobs, want %d", n, len(jobs), want)
		}
		var joined []byte
		for _, j := range jobs {
			if j.Rows < 1 || j.Rows > jobRowLimit {
				t.Fatalf("%d rows: a job of %d rows", n, j.Rows)
			}
			joined = append(joined, j.Data[11+8:len(j.Data)-8]...)
		}
		if !bytes.Equal(joined, raster) {
			t.Fatalf("%d rows: the jobs do not hold the raster in order", n)
		}
	}
}

func TestJobBytes(t *testing.T) {
	jobs, err := BuildJobs([]byte{0x80, 0x01, 0x00, 0xff}, 2, JobSettings{Speed: 3, Darkness: 8, Media: ContinuousMedia})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x1b, 0x4e, 0x0d, 0x03, 0x1b, 0x4e, 0x04, 0x08, 0x1f, 0x11, 0x0b, // speed, darkness, media
		0x1d, 0x76, 0x30, 0x00, 0x02, 0x00, 0x02, 0x00, 0x80, 0x01, 0x00, 0xff, // GS v 0: 2 bytes × 2 rows
		0x1f, 0xf0, 0x05, 0x00, 0x1f, 0xf0, 0x03, 0x00, // end, feed
	}
	if len(jobs) != 1 || !bytes.Equal(jobs[0].Data, want) || jobs[0].Rows != 2 {
		t.Fatalf("job = % x", jobs[0].Data)
	}
	jobs, _ = BuildJobs([]byte{0x80}, 1, JobSettings{Media: GapMedia, SkipFeed: true})
	if !bytes.HasSuffix(jobs[0].Data, []byte{0x00, 0x80, 0x1f, 0xf0, 0x05, 0x00}) {
		t.Errorf("short footer job = % x", jobs[0].Data)
	}
	if _, err := BuildJobs([]byte{1, 2, 3}, 2, JobSettings{}); err == nil {
		t.Error("a partial row must fail")
	}
	if jobs, err := BuildJobs(nil, 48, JobSettings{}); err != nil || len(jobs) != 0 {
		t.Errorf("an empty raster gives %d jobs, %v", len(jobs), err)
	}
}

func TestBlockRowsSplitAJobIntoShortBlocks(t *testing.T) {
	jobs, _ := BuildJobs(rowsWithBlanks(640), 1, JobSettings{Media: GapMedia, BlockRows: 240})
	_, blocks, _ := parseJob(t, jobs[0].Data)
	var rows []int
	for _, b := range blocks {
		rows = append(rows, b.rows)
	}
	if !slices.Equal(rows, []int{240, 240, 160}) || jobs[0].Rows != 640 {
		t.Errorf("blocks %v, job rows %d", rows, jobs[0].Rows)
	}
}

func TestPauseFollowsTheJobLength(t *testing.T) {
	for rows, want := range map[int]time.Duration{
		0:    3 * time.Second,
		240:  3 * time.Second,
		960:  3 * time.Second,
		1100: 3218750 * time.Microsecond,
		1200: 3375 * time.Millisecond,
		1920: 4500 * time.Millisecond,
	} {
		if got := (Job{Rows: rows}).Pause(); got != want {
			t.Errorf("pause after %d rows = %v, want %v", rows, got, want)
		}
	}
}

func TestFirmwareReply(t *testing.T) {
	for hexFrame, want := range map[string]string{
		"1a07020404": "2.4.4",
		"1a070109":   "1.9",
	} {
		v, ok := firmwareIn(unhex(t, hexFrame))
		if !ok || v.String() != want {
			t.Errorf("%s = %v, %v; want %s", hexFrame, v, ok, want)
		}
	}
	for _, other := range []string{"", "1a", "1a07", "0107", "1a0841424320", "1a11030002", "020506"} {
		if v, ok := firmwareIn(unhex(t, other)); ok {
			t.Errorf("%q read as firmware %v", other, v)
		}
	}
}

func TestVersions(t *testing.T) {
	v := func(s string) Version {
		t.Helper()
		out, err := ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, tc := range []struct {
		a, b   string
		before bool
	}{
		{"2.4", "2.4.4", true}, {"2.4.4", "2.4.4", false}, {"2.4.0", "2.4", false}, {"2.4", "2.4.0", false},
		{"1.9", "2", true}, {"2.0.1", "2", false}, {"10", "9", false},
	} {
		if got := v(tc.a).Before(v(tc.b)); got != tc.before {
			t.Errorf("%s before %s = %v", tc.a, tc.b, got)
		}
	}
	for _, bad := range []string{"", "2.x", "-1", "2..4"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) should fail", bad)
		}
	}
}

func TestLegacyBlockRows(t *testing.T) {
	cat := catalog(t)
	m110, _ := cat.Model("phomemo-m110")
	m220, _ := cat.Model("phomemo-m220")
	for _, tc := range []struct {
		m        *printer.Model
		firmware Version
		want     int
	}{
		{m110, Version{1, 9}, 240}, {m110, Version{2}, 0}, {m110, Version{2, 4, 4}, 0}, {m110, nil, 0},
		{m220, Version{1, 9}, 0},
	} {
		if got := legacyBlockRows(tc.m.Options, tc.firmware); got != tc.want {
			t.Errorf("%s firmware %v: %d block rows, want %d", tc.m.ID, tc.firmware, got, tc.want)
		}
	}
}

func TestIdentifyModel(t *testing.T) {
	cat := catalog(t)
	var models []*printer.Model
	for _, m := range cat.ModelList() {
		if m.Family == Family {
			models = append(models, m)
		}
	}
	m110, m220, d30 := cat.Models["phomemo-m110"], cat.Models["phomemo-m220"], cat.Models["phomemo-d30"]
	for _, tc := range []struct {
		name         string
		hello, label string
		known, want  *printer.Model
	}{
		{"the saved model always wins", "0107", "M110-1A2B", d30, d30},
		{"a saved model of another family is ignored", "", "M220-77", cat.Models["clabel-ct320b"], m220},
		{"the name beats the hello", "0101", "Phomemo M220", nil, m220},
		{"a hello two models send", "0101", "", nil, nil}, // the M110 and the D30
		{"a hello several models send", "0107", "", nil, nil},
		{"a name alone", "", "D30-1234", nil, d30},
		{"nothing to go on", "", "Speaker", nil, nil},
	} {
		if got := IdentifyModel(unhex(t, tc.hello), tc.label, tc.known, models); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// With only the M220 and the D30 in the family, 01 07 names the M220 by itself.
	if got := IdentifyModel(unhex(t, "0107"), "", nil, []*printer.Model{m220, d30}); got != m220 {
		t.Errorf("a unique hello: %v", got)
	}
	if got := otherSuspects(unhex(t, "0107"), "M110", m110, models); len(got) != 0 {
		t.Errorf("an M110 saying 01 07 named M110: suspects %v", got)
	}
	if got := otherSuspects(nil, "", d30, models); len(got) != 0 {
		t.Errorf("no evidence: suspects %v", got)
	}
}
