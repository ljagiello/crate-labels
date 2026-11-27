package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func must[T any](t *testing.T) func(v T, err error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var ctx = context.Background()

func TestDataDirOverride(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CRATE_LABELS_DATA", d)
	if got := DefaultPath(); got != filepath.Join(d, "crate-labels.sqlite") {
		t.Fatalf("DefaultPath = %q", got)
	}
	t.Setenv("CRATE_LABELS_DATA", "")
	if got := DataDir(); filepath.Base(filepath.Dir(got)) != "Application Support" || filepath.Base(got) != "crate-labels" {
		t.Fatalf("DataDir = %q", got)
	}
}

func TestSettingsRoundTripAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sqlite")
	first := must[*Store](t)(Open(path))
	check(t, first.SetSetting(ctx, "default_template", "dj-single"))
	check(t, first.SetSetting(ctx, "bpm_range.low", 88))
	check(t, first.SetSetting(ctx, "ui.options", map[string]any{"dj-single": map[string]any{"show_qr": false}}))
	check(t, first.Close())

	again := must[*Store](t)(Open(path))
	defer again.Close()
	all := must[map[string]json.RawMessage](t)(again.Settings(ctx))
	want := map[string]string{
		"default_template": `"dj-single"`,
		"bpm_range.low":    `88`,
		"ui.options":       `{"dj-single":{"show_qr":false}}`,
	}
	if len(all) != len(want) {
		t.Fatalf("settings = %s", all)
	}
	for k, v := range want {
		if string(all[k]) != v {
			t.Errorf("%s = %s, want %s", k, all[k], v)
		}
	}
	if got := again.SettingString(ctx, "default_template"); got != "dj-single" {
		t.Errorf("SettingString = %q", got)
	}
	check(t, again.SetSetting(ctx, "bpm_range.low", nil))
	if _, ok, _ := again.Setting(ctx, "bpm_range.low"); ok {
		t.Error("nil did not delete the setting")
	}
	check(t, again.SetSetting(ctx, "theme", json.RawMessage("null")))
	if _, ok, _ := again.Setting(ctx, "theme"); ok {
		t.Error("JSON null did not delete the setting")
	}
}

func TestDiscogsAccountHoldsNoSecret(t *testing.T) {
	s := open(t)
	check(t, s.SetDiscogsAccount(ctx, "testuser", "keychain:crate-labels.discogs/personal-access-token"))
	user, ref, ok, err := s.DiscogsAccount(ctx)
	check(t, err)
	if !ok || user != "testuser" || ref != "keychain:crate-labels.discogs/personal-access-token" {
		t.Fatalf("account = %q %q %v", user, ref, ok)
	}
	check(t, s.ClearDiscogsAccount(ctx))
	if _, _, ok, _ := s.DiscogsAccount(ctx); ok {
		t.Fatal("account not cleared")
	}
}

func TestPrintersCRUD(t *testing.T) {
	s := open(t)
	p := printer.Target{Name: " Desk M110 ", ModelID: "phomemo-m110", Address: "AA-BB",
		Settings: printer.Settings{"darkness": 10, "extra_rotate_cw": 180}}
	check(t, s.SavePrinter(ctx, &p))
	if len(p.ID) != 12 || p.Name != "Desk M110" {
		t.Fatalf("saved %+v", p)
	}
	got := must[printer.Target](t)(s.Printer(ctx, p.ID))
	if got.Settings.Int("darkness", 0) != 10 || got.Settings.Int("extra_rotate_cw", 0) != 180 {
		t.Fatalf("settings = %v", got.Settings)
	}
	if found := must[printer.Target](t)(s.FindPrinter(ctx, "desk m110")); found.ID != p.ID {
		t.Fatalf("FindPrinter by name = %+v", found)
	}
	q := printer.Target{Name: "Another", ModelID: "cups", Address: "Brother"}
	check(t, s.SavePrinter(ctx, &q))
	// Never used: sorted by name.
	list := must[[]printer.Target](t)(s.Printers(ctx))
	if list[0].Name != "Another" || list[1].Name != "Desk M110" {
		t.Fatalf("order = %v", list)
	}
	// Most recently used first.
	check(t, s.TouchPrinter(ctx, p.ID))
	list = must[[]printer.Target](t)(s.Printers(ctx))
	if list[0].ID != p.ID {
		t.Fatalf("touched printer not first: %v", list)
	}
	p.Name = "Crate M110"
	p.Settings["offset_dots"] = 4
	check(t, s.SavePrinter(ctx, &p))
	got = must[printer.Target](t)(s.Printer(ctx, p.ID))
	if got.Name != "Crate M110" || got.Settings.Int("offset_dots", 0) != 4 {
		t.Fatalf("update lost: %+v", got)
	}
	check(t, s.DeletePrinter(ctx, p.ID))
	if _, err := s.Printer(ctx, p.ID); !IsNotFound(err) {
		t.Fatalf("deleted printer: %v", err)
	}
	if err := s.DeletePrinter(ctx, p.ID); !IsNotFound(err) {
		t.Fatalf("delete twice: %v", err)
	}
	blank := printer.Target{Name: "  ", ModelID: "cups"}
	if err, ok := s.SavePrinter(ctx, &blank).(*InvalidError); !ok {
		t.Fatalf("blank name: %v", err)
	}
}

func TestResponseCacheTTLAndImages(t *testing.T) {
	s := open(t)
	now := time.UnixMilli(1_700_000_000_000)
	s.Now = func() time.Time { return now }
	check(t, s.CachePut(ctx, "https://api.discogs.com/releases/1", []byte(`{"id": 1}`)))
	body, ok, err := s.CacheGet(ctx, "https://api.discogs.com/releases/1", time.Minute)
	check(t, err)
	if !ok || string(body) != `{"id": 1}` {
		t.Fatalf("cache hit = %q %v", body, ok)
	}
	now = now.Add(2 * time.Minute)
	if _, ok, _ := s.CacheGet(ctx, "https://api.discogs.com/releases/1", time.Minute); ok {
		t.Fatal("stale entry returned")
	}
	check(t, s.ImagePut(ctx, "https://i.discogs.com/a.jpeg", "image/jpeg", []byte{0xff, 0xd8}))
	ct, img, ok, err := s.ImageGet(ctx, "https://i.discogs.com/a.jpeg")
	check(t, err)
	if !ok || ct != "image/jpeg" || !reflect.DeepEqual(img, []byte{0xff, 0xd8}) {
		t.Fatalf("image = %q %v %v", ct, img, ok)
	}
	// Images expire on read too, not only when Prune runs.
	now = now.Add(s.ImageMaxAge + time.Second)
	if _, _, ok, _ := s.ImageGet(ctx, "https://i.discogs.com/a.jpeg"); ok {
		t.Fatal("image older than ImageMaxAge returned")
	}
}

func TestDiscogsCachesKeepSixHoursAtMost(t *testing.T) {
	s := open(t)
	if s.CacheMaxAge > 6*time.Hour || s.ImageMaxAge > 6*time.Hour {
		t.Fatalf("CacheMaxAge %v, ImageMaxAge %v: Discogs data may be kept six hours at most", s.CacheMaxAge, s.ImageMaxAge)
	}
}

func TestCrateNames(t *testing.T) {
	s := open(t)
	first := must[Crate](t)(s.CreateCrate(ctx, ""))
	second := must[Crate](t)(s.CreateCrate(ctx, "   "))
	if first.Name != "Untitled crate" || second.Name != "Untitled crate 2" || len(first.ID) != 12 || first.Records != 0 {
		t.Fatalf("unnamed crates = %+v, %+v", first, second)
	}
	named := must[Crate](t)(s.CreateCrate(ctx, "  Sunday   warm-up "))
	if named.Name != "Sunday warm-up" {
		t.Fatalf("spaces not tidied: %q", named.Name)
	}
	must[Crate](t)(s.CreateCrate(ctx, "Écoute"))
	for _, bad := range []string{"sunday WARM-UP", "untitled crate", "ÉCOUTE", strings.Repeat("x", 41)} {
		if _, err := s.CreateCrate(ctx, bad); err == nil {
			t.Errorf("created %q", bad)
		}
	}
	if _, err := s.CreateCrate(ctx, strings.Repeat("é", 40)); err != nil {
		t.Errorf("40 characters: %v", err)
	}
	// Renaming keeps the rules, but a crate may keep (or re-case) its own name.
	if _, err := s.RenameCrate(ctx, first.ID, " "); err == nil {
		t.Error("blank rename accepted")
	}
	if _, err := s.RenameCrate(ctx, first.ID, "Sunday warm-up"); err == nil {
		t.Error("rename onto a taken name accepted")
	}
	if c := must[Crate](t)(s.RenameCrate(ctx, named.ID, "SUNDAY WARM-UP")); c.Name != "SUNDAY WARM-UP" {
		t.Errorf("re-cased = %q", c.Name)
	}
	if _, err := s.RenameCrate(ctx, "nope", "x"); !IsNotFound(err) {
		t.Errorf("rename missing: %v", err)
	}
	// Once "Untitled crate" is deleted, the name is free again.
	check(t, s.DeleteCrate(ctx, first.ID))
	if again := must[Crate](t)(s.CreateCrate(ctx, "")); again.Name != "Untitled crate" {
		t.Errorf("freed name not reused: %q", again.Name)
	}
}

func TestCrateRecordsOrderAndEdits(t *testing.T) {
	s := open(t)
	c := must[Crate](t)(s.CreateCrate(ctx, "Jungle 94"))
	year := int64(1995)
	a := must[CrateRecord](t)(s.AddToCrate(ctx, c.ID, NewCrateRecord{ReleaseID: 118638, Artist: "DJ Force & The Evolution",
		Title: "Simply Electric", Catno: "KF039", Year: &year, Thumb: "https://i.discogs.com/t.jpeg"}))
	b := must[CrateRecord](t)(s.AddToCrate(ctx, c.ID, NewCrateRecord{ReleaseID: 89569, Artist: "Various",
		Title: "Future Sound", Data: json.RawMessage(`{"artist":"Various","title":"Edited"}`)}))
	if a.EditedAt != nil || a.Data != nil || b.EditedAt == nil || string(b.Data) != `{"artist":"Various","title":"Edited"}` {
		t.Fatalf("added = %+v, %+v", a, b)
	}
	if c = must[Crate](t)(s.Crate(ctx, c.ID)); c.Records != 2 {
		t.Fatalf("crate = %+v", c)
	}
	recs := must[[]CrateRecord](t)(s.CrateRecords(ctx, c.ID))
	if recs[0].Position != 0 || recs[1].Position != 1 || *recs[0].Year != 1995 || recs[1].Year != nil ||
		recs[0].Thumb != "https://i.discogs.com/t.jpeg" || recs[1].Thumb != "" {
		t.Fatalf("records = %+v", recs)
	}
	if _, err := s.AddToCrate(ctx, c.ID, NewCrateRecord{ReleaseID: 118638}); err == nil {
		t.Fatal("the same release went in twice")
	}
	recs = must[[]CrateRecord](t)(s.ReorderCrate(ctx, c.ID, []int64{b.ID, a.ID}))
	if recs[0].ReleaseID != 89569 || recs[1].ReleaseID != 118638 {
		t.Fatalf("reordered = %+v", recs)
	}
	for _, bad := range [][]int64{{a.ID}, {a.ID, a.ID}, {a.ID, b.ID, 999}} {
		if _, err := s.ReorderCrate(ctx, c.ID, bad); err == nil {
			t.Errorf("reorder %v accepted", bad)
		}
	}
	r := must[CrateRecord](t)(s.SetCrateRecordData(ctx, a.ID, json.RawMessage(`{"artist":"x"}`)))
	if r.EditedAt == nil || string(r.Data) != `{"artist":"x"}` {
		t.Fatalf("edited = %+v", r)
	}
	r = must[CrateRecord](t)(s.SetCrateRecordData(ctx, a.ID, nil))
	if r.Data != nil || r.EditedAt != nil {
		t.Fatalf("cleared = %+v", r)
	}
	if _, err := s.SetCrateRecordData(ctx, 999, nil); !IsNotFound(err) {
		t.Fatalf("edit missing: %v", err)
	}
	check(t, s.RemoveFromCrate(ctx, b.ID))
	if c = must[Crate](t)(s.Crate(ctx, c.ID)); c.Records != 1 {
		t.Fatalf("count after remove = %d", c.Records)
	}
	check(t, s.DeleteCrate(ctx, c.ID))
	if _, err := s.CrateRecord(ctx, a.ID); !IsNotFound(err) {
		t.Fatalf("records did not cascade: %v", err)
	}
	if list := must[[]Crate](t)(s.Crates(ctx)); len(list) != 0 {
		t.Fatalf("crates = %+v", list)
	}
}

func TestCrateCapIsCheckedInTheInsert(t *testing.T) {
	s := open(t)
	s.CrateRecordsMax = 3
	c := must[Crate](t)(s.CreateCrate(ctx, "Race"))
	errs := make(chan error, 6)
	for i := range 6 {
		go func() {
			_, err := s.AddToCrate(ctx, c.ID, NewCrateRecord{ReleaseID: int64(i + 1)})
			errs <- err
		}()
	}
	failed := 0
	for range 6 {
		if err := <-errs; err != nil {
			if _, ok := err.(*InvalidError); !ok {
				t.Errorf("refusal is %T: %v", err, err)
			}
			failed++
		}
	}
	if c = must[Crate](t)(s.Crate(ctx, c.ID)); c.Records != 3 || failed != 3 {
		t.Fatalf("records = %d, refused = %d", c.Records, failed)
	}
	if _, err := s.AddToCrate(ctx, "nope", NewCrateRecord{ReleaseID: 1}); !IsNotFound(err) {
		t.Fatalf("missing crate: %v", err)
	}
}

func TestJobLifecycleAndInterrupt(t *testing.T) {
	s := open(t)
	rec := json.RawMessage(`{"artist":"A","title":"T"}`)
	j := must[Job](t)(s.CreateJob(ctx, NewJob{PrinterID: "p1", PrinterName: "Desk", Template: "dj-single", Stock: "40x30",
		Records: []json.RawMessage{rec, rec}, Copies: 3, Options: map[string]any{"show_qr": true},
		PrintOptions: map[string]any{"darkness": 12}}))
	if j.Status != JobQueued || j.Total != 6 || j.Copies != 3 || len(j.Records) != 2 || j.StartedAt != nil {
		t.Fatalf("job = %+v", j)
	}
	if j.Options["show_qr"] != true || j.PrintOptions["darkness"] != float64(12) {
		t.Fatalf("options = %v %v", j.Options, j.PrintOptions)
	}
	printing, half, msg := JobPrinting, 0.5, "Sending"
	j = must[Job](t)(s.UpdateJob(ctx, j.ID, JobUpdate{Status: &printing, Progress: &half, Message: &msg}))
	if j.StartedAt == nil || j.FinishedAt != nil || j.Progress != 0.5 || j.Message != "Sending" {
		t.Fatalf("printing = %+v", j)
	}
	done, six := JobDone, 6
	j = must[Job](t)(s.UpdateJob(ctx, j.ID, JobUpdate{Status: &done, Printed: &six}))
	if j.FinishedAt == nil || j.Printed != 6 {
		t.Fatalf("done = %+v", j)
	}
	stale := must[Job](t)(s.CreateJob(ctx, NewJob{PrinterID: "p1", Template: "t", Stock: "s", Records: []json.RawMessage{rec}, Owner: "dead:1"}))
	live := must[Job](t)(s.CreateJob(ctx, NewJob{PrinterID: "p1", Template: "t", Stock: "s", Records: []json.RawMessage{rec}, Owner: "live:1"}))
	if live.Owner != "live:1" {
		t.Fatalf("owner = %q", live.Owner)
	}
	active := must[[]Job](t)(s.Jobs(ctx, 50, true))
	if len(active) != 2 || active[1].ID != stale.ID {
		t.Fatalf("active = %+v", active)
	}
	if all := must[[]Job](t)(s.Jobs(ctx, 50, false)); all[0].ID != live.ID || all[2].ID != j.ID {
		t.Fatalf("not newest first: %+v", all)
	}
	ids := must[[]int64](t)(s.InterruptStaleJobs(ctx, func(owner string) bool { return owner == "dead:1" }))
	if len(ids) != 1 || ids[0] != stale.ID {
		t.Fatalf("interrupted %v", ids)
	}
	if got := must[Job](t)(s.Job(ctx, live.ID)); got.Status != JobQueued {
		t.Fatalf("a live owner's job was interrupted: %+v", got)
	}
	got := must[Job](t)(s.Job(ctx, stale.ID))
	if got.Status != JobInterrupted || got.Error != InterruptedMessage || got.FinishedAt == nil {
		t.Fatalf("stale = %+v", got)
	}
	bogus := "exploded"
	if _, err := s.UpdateJob(ctx, j.ID, JobUpdate{Status: &bogus}); err == nil {
		t.Fatal("bad status accepted")
	}
	if _, err := s.Job(ctx, 999); !IsNotFound(err) {
		t.Fatalf("missing job: %v", err)
	}
}

func TestHistoryReplacesRepeatsAndCaps(t *testing.T) {
	s := open(t)
	data := json.RawMessage(`{"artist":"DJ Ham","title":"It Would Be","catno":"REC 015","year":"1996","release_id":95303}`)
	add := func(data json.RawMessage, stock string, copies int) {
		t.Helper()
		must[HistoryEntry](t)(s.AddHistory(ctx, NewHistoryEntry{Data: data, Stock: stock, Template: "dj-single",
			Printer: "Desk", Copies: copies, Options: map[string]any{"show_qr": true}}))
	}
	add(data, "40x30", 1)
	add(data, "40x30", 2)
	add(data, "50x30", 1)
	rows := must[[]HistoryEntry](t)(s.History(ctx, 100))
	if len(rows) != 2 || rows[0].Stock != "50x30" || rows[1].Stock != "40x30" || rows[1].Copies != 2 {
		t.Fatalf("history = %+v", rows)
	}
	if rows[1].Catno != "REC 015" || rows[1].Year != "1996" || rows[1].ReleaseID == nil || *rows[1].ReleaseID != 95303 {
		t.Fatalf("entry = %+v", rows[1])
	}
	if string(rows[1].Data) != string(data) || rows[1].Options["show_qr"] != true {
		t.Fatalf("data/options = %s %v", rows[1].Data, rows[1].Options)
	}
	s.HistoryLimit = 3
	for i := range 5 {
		add(json.RawMessage(`{"title":"t`+string(rune('0'+i))+`"}`), "40x30", 1)
	}
	rows = must[[]HistoryEntry](t)(s.History(ctx, 100))
	if len(rows) != 3 || rows[0].Title != "t4" || rows[2].Title != "t2" {
		t.Fatalf("capped = %+v", rows)
	}
	check(t, s.DeleteHistory(ctx, rows[0].ID))
	if rows = must[[]HistoryEntry](t)(s.History(ctx, 100)); len(rows) != 2 {
		t.Fatalf("after delete = %d", len(rows))
	}
	if err := s.DeleteHistory(ctx, 12345); !IsNotFound(err) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestLabelPrintsAndOverrides(t *testing.T) {
	s := open(t)
	now := time.UnixMilli(1_800_000_000_000)
	s.Now = func() time.Time { return now }
	states := must[map[int64]PrintedState](t)(s.PrintedStates(ctx, []int64{118638, 5, 5, 0, -1}))
	if len(states) != 2 || states[118638] != (PrintedState{Override: OverrideAuto}) {
		t.Fatalf("fresh states = %+v", states)
	}
	job := must[Job](t)(s.CreateJob(ctx, NewJob{PrinterID: "p", Template: "t", Stock: "s"}))
	check(t, s.LogPrints(ctx, []LabelPrint{
		{ReleaseID: 118638, JobID: job.ID, Template: "dj-single", Copies: 2},
		{ReleaseID: 0, JobID: job.ID, Template: "dj-single", Copies: 3}, // typed by hand
		{ReleaseID: 7, Template: "dj-single", Copies: 0},                // nothing printed
	}))
	now = now.Add(time.Hour)
	check(t, s.LogPrints(ctx, []LabelPrint{{ReleaseID: 118638, Template: "seller-small", Copies: 1}}))
	st := must[PrintedState](t)(s.PrintedState(ctx, 118638))
	if !st.Printed || st.Labels != 3 || st.LastPrintedAt == nil || *st.LastPrintedAt != now.UnixMilli() || st.Override != OverrideAuto {
		t.Fatalf("printed twice = %+v", st)
	}
	if n := must[int](t)(s.LabelsPrinted(ctx)); n != 6 {
		t.Fatalf("labels printed = %d", n)
	}
	// The user overrides the log both ways; auto hands it back.
	if st = must[PrintedState](t)(s.SetPrintedOverride(ctx, 118638, OverrideNotPrinted)); st.Printed || st.Labels != 3 {
		t.Fatalf("not printed = %+v", st)
	}
	if st = must[PrintedState](t)(s.SetPrintedOverride(ctx, 5, OverridePrinted)); !st.Printed || st.Labels != 0 {
		t.Fatalf("hand-marked printed = %+v", st)
	}
	if st = must[PrintedState](t)(s.SetPrintedOverride(ctx, 5, OverrideAuto)); st.Printed || st.Override != OverrideAuto {
		t.Fatalf("back to auto = %+v", st)
	}
	if _, err := s.SetPrintedOverride(ctx, 5, "maybe"); err == nil {
		t.Fatal("bad override accepted")
	}
	// A new print lifts "not printed", but keeps "printed".
	must[PrintedState](t)(s.SetPrintedOverride(ctx, 9, OverridePrinted))
	check(t, s.LogPrints(ctx, []LabelPrint{{ReleaseID: 118638, Template: "dj-single", Copies: 1}, {ReleaseID: 9, Template: "dj-single", Copies: 1}}))
	states = must[map[int64]PrintedState](t)(s.PrintedStates(ctx, []int64{118638, 9}))
	if !states[118638].Printed || states[118638].Override != OverrideAuto || states[9].Override != OverridePrinted {
		t.Fatalf("after reprint = %+v", states)
	}
	// Deleting history does not un-print anything.
	h := must[HistoryEntry](t)(s.AddHistory(ctx, NewHistoryEntry{Data: json.RawMessage(`{"release_id":118638}`), Template: "dj-single", Stock: "s"}))
	check(t, s.DeleteHistory(ctx, h.ID))
	if st = must[PrintedState](t)(s.PrintedState(ctx, 118638)); !st.Printed {
		t.Fatal("deleting history un-printed a release")
	}
}

func TestPrintedStatesInLargeBatches(t *testing.T) {
	s := open(t)
	var ids []int64
	var prints []LabelPrint
	for i := range 1200 {
		ids = append(ids, int64(i+1))
		if i%3 == 0 {
			prints = append(prints, LabelPrint{ReleaseID: int64(i + 1), Template: "dj-single", Copies: 1})
		}
	}
	check(t, s.LogPrints(ctx, prints))
	states := must[map[int64]PrintedState](t)(s.PrintedStates(ctx, ids))
	printed := 0
	for _, st := range states {
		if st.Printed {
			printed++
		}
	}
	if len(states) != 1200 || printed != 400 {
		t.Fatalf("states = %d, printed = %d", len(states), printed)
	}
}

func TestInMemoryStoreKeepsOneDatabase(t *testing.T) {
	s := must[*Store](t)(Open(":memory:"))
	defer s.Close()
	c := must[Crate](t)(s.CreateCrate(ctx, "A"))
	must[CrateRecord](t)(s.AddToCrate(ctx, c.ID, NewCrateRecord{ReleaseID: 1}))
	check(t, s.DeleteCrate(ctx, c.ID))
	if list := must[[]Crate](t)(s.Crates(ctx)); len(list) != 0 {
		t.Fatal("crate not deleted")
	}
}

func readVersion(t *testing.T, path string) (version, appID int) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.QueryRow("PRAGMA user_version").Scan(&version)
	_ = raw.QueryRow("PRAGMA application_id").Scan(&appID)
	return version, appID
}

func TestNewDatabaseIsMarkedAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.sqlite")
	s := must[*Store](t)(Open(path))
	if v := must[int](t)(s.SchemaVersionOf(ctx)); v != SchemaVersion {
		t.Fatalf("version = %d", v)
	}
	must[Crate](t)(s.CreateCrate(ctx, "Keep me"))
	s.Close()
	if v, id := readVersion(t, path); v != SchemaVersion || id != applicationID {
		t.Fatalf("header = version %d, application id %#x", v, id)
	}
	s = must[*Store](t)(Open(path))
	defer s.Close()
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode = %q %v", mode, err)
	}
	if list := must[[]Crate](t)(s.Crates(ctx)); len(list) != 1 {
		t.Fatalf("reopened crates = %+v", list)
	}
}

func TestForeignAndNewerDatabasesAreRefusedUntouched(t *testing.T) {
	dir := t.TempDir()
	write := func(name, script string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		raw, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(script); err != nil {
			t.Fatal(err)
		}
		raw.Close()
		return path
	}
	cases := map[string]struct {
		path, want string
	}{
		// A pre-release build: tables and a version, but no application id.
		"pre-release": {write("old.sqlite", "CREATE TABLE crate (id TEXT); PRAGMA user_version = 3;"), "pre-release"},
		// Some other program's database.
		"foreign": {write("other.sqlite", "CREATE TABLE notes (x TEXT); PRAGMA application_id = 42;"), "not a crate-labels database"},
		// Ours, from a newer build.
		"newer": {write("newer.sqlite", "PRAGMA application_id = 1668443234; CREATE TABLE settings (key TEXT); PRAGMA user_version = 99;"),
			"newer crate-labels"},
	}
	for name, c := range cases {
		before, err := os.ReadFile(c.path)
		check(t, err)
		if _, err := Open(c.path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		after, err := os.ReadFile(c.path)
		check(t, err)
		if !bytes.Equal(before, after) {
			t.Errorf("%s: a refused file was changed", name)
		}
	}
}

func TestPreReleaseFileInTheDataFolderIsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRATE_LABELS_DATA", dir)
	old := filepath.Join(dir, "crate-labels.db")
	raw, err := sql.Open("sqlite", "file:"+old)
	check(t, err)
	_, err = raw.Exec("CREATE TABLE crate (id TEXT PRIMARY KEY, name TEXT); INSERT INTO crate VALUES ('a', 'Old'); PRAGMA user_version = 3;")
	check(t, err)
	raw.Close()
	before, err := os.ReadFile(old)
	check(t, err)

	s := must[*Store](t)(Open(""))
	defer s.Close()
	if s.Path() != filepath.Join(dir, FileName) {
		t.Fatalf("opened %s", s.Path())
	}
	if list := must[[]Crate](t)(s.Crates(ctx)); len(list) != 0 {
		t.Fatalf("crates from the old file leaked in: %+v", list)
	}
	after, err := os.ReadFile(old)
	check(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("the pre-release file was changed")
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	s := open(t)
	before := must[int](t)(s.SchemaVersionOf(ctx))
	steps := append(append([]string{}, migrations...), "CREATE TABLE extra (x INTEGER);", "THIS IS NOT SQL;")
	if err := s.migrate(ctx, steps); err == nil {
		t.Fatal("a broken step applied")
	}
	if v := must[int](t)(s.SchemaVersionOf(ctx)); v != before {
		t.Fatalf("version moved to %d", v)
	}
	if _, err := s.db.ExecContext(ctx, "SELECT * FROM extra"); err == nil {
		t.Fatal("the step before the broken one was kept")
	}
}

func TestPruneBoundsTheCaches(t *testing.T) {
	s := open(t)
	now := time.UnixMilli(1_800_000_000_000)
	s.Now = func() time.Time { return now }
	s.CacheMaxAge, s.CacheMaxBytes = time.Hour, 25
	s.ImageMaxAge, s.ImageMaxBytes = time.Hour, 10
	check(t, s.CachePut(ctx, "old", []byte("0123456789")))
	now = now.Add(2 * time.Hour)
	for _, u := range []string{"a", "b", "c"} {
		check(t, s.CachePut(ctx, u, []byte("0123456789")))
		now = now.Add(time.Second)
	}
	check(t, s.ImagePut(ctx, "i1", "image/jpeg", []byte("123456")))
	now = now.Add(time.Second)
	check(t, s.ImagePut(ctx, "i2", "image/jpeg", []byte("123456")))
	check(t, s.Prune(ctx))
	for url, want := range map[string]bool{"old": false, "a": false, "b": true, "c": true} {
		if _, ok, _ := s.CacheGet(ctx, url, 100*time.Hour); ok != want {
			t.Errorf("cache %s kept = %v", url, ok)
		}
	}
	if _, _, ok, _ := s.ImageGet(ctx, "i1"); ok {
		t.Error("the older image was kept over the byte cap")
	}
	if _, _, ok, _ := s.ImageGet(ctx, "i2"); !ok {
		t.Error("the newest image was dropped")
	}
}
