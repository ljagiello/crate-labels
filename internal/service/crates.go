package service

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ljagiello/crate-labels/internal/store"
)

// The orders a crate can print in. Labels come off the printer in this order, so pick
// the one that matches how the records are filed.
const (
	OrderCrate  = "crate"  // the crate's own order, as arranged in the UI
	OrderArtist = "artist" // artist A to Z, ignoring a leading "The"; then catalogue number
	OrderCatno  = "catno"  // catalogue number, with the numbers in it compared as numbers; then artist
	OrderNewest = "newest" // newest year first, records with no year at the end; then artist
)

// CrateOrders lists every print order, the default first.
var CrateOrders = []string{OrderCrate, OrderArtist, OrderCatno, OrderNewest}

// CratePrintRequest prints a crate, or some of its records, as one job.
type CratePrintRequest struct {
	CrateID      string
	RecordIDs    []int64 // nil = every record
	Order        string  // one of CrateOrders; "" = OrderCrate
	Template     string
	Stock        string
	Printer      string
	Copies       int
	Options      map[string]any
	PrintOptions map[string]any
}

// SortCrate returns records in a print order. Records that tie on the order's keys keep
// their crate order, and records missing the main key (no artist, catalogue number or
// year) go after the rest.
func SortCrate(records []store.CrateRecord, order string) []store.CrateRecord {
	byKeys := func(a, b store.CrateRecord) int { return 0 }
	switch order {
	case OrderArtist:
		byKeys = func(a, b store.CrateRecord) int {
			return cmp.Or(blankLast(artistKey(a.Artist), artistKey(b.Artist), strings.Compare),
				blankLast(catnoKey(a.Catno), catnoKey(b.Catno), naturalCompare))
		}
	case OrderCatno:
		byKeys = func(a, b store.CrateRecord) int {
			return cmp.Or(blankLast(catnoKey(a.Catno), catnoKey(b.Catno), naturalCompare),
				blankLast(artistKey(a.Artist), artistKey(b.Artist), strings.Compare))
		}
	case OrderNewest:
		byKeys = func(a, b store.CrateRecord) int {
			return cmp.Or(newestFirst(a.Year, b.Year), blankLast(artistKey(a.Artist), artistKey(b.Artist), strings.Compare))
		}
	}
	out := slices.Clone(records)
	slices.SortFunc(out, func(a, b store.CrateRecord) int {
		return cmp.Or(byKeys(a, b), cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// blankLast compares a and b with by, except that an empty key sorts after any other.
func blankLast(a, b string, by func(a, b string) int) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	return by(a, b)
}

// artistKey is how an artist files: case-folded, and "The Prodigy" under P.
func artistKey(artist string) string {
	k := strings.ToLower(strings.Join(strings.Fields(artist), " "))
	if rest, ok := strings.CutPrefix(k, "the "); ok && rest != "" {
		k = rest
	}
	return k
}

// catnoKey case-folds a catalogue number and drops spaces, dots and dashes, so "KF 039",
// "kf-039" and "KF039" file together.
func catnoKey(catno string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' || r == '.' {
			return -1
		}
		return unicode.ToLower(r)
	}, catno)
}

// naturalCompare compares strings with their digit runs taken as numbers: "kf9" before
// "kf10", "kf039" level with "kf39".
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		ra, rb := run(a), run(b)
		var c int
		if isDigit(ra[0]) && isDigit(rb[0]) {
			na, nb := strings.TrimLeft(ra, "0"), strings.TrimLeft(rb, "0")
			c = cmp.Or(cmp.Compare(len(na), len(nb)), strings.Compare(na, nb))
		} else {
			c = strings.Compare(ra, rb)
		}
		if c != 0 {
			return c
		}
		a, b = a[len(ra):], b[len(rb):]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// run is the leading run of s: all digits, or all non-digits.
func run(s string) string {
	digits := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i]
}

// newestFirst orders years from newest to oldest; a missing or zero year goes last.
func newestFirst(a, b *int64) int {
	known := func(y *int64) bool { return y != nil && *y > 0 }
	switch {
	case !known(a) && !known(b):
		return 0
	case !known(a):
		return 1
	case !known(b):
		return -1
	}
	return cmp.Compare(*b, *a)
}

// PrintCrate queues a crate's records in the requested order as one job. A record with
// an edited label prints that; the rest print from their Discogs release.
func (s *Service) PrintCrate(ctx context.Context, r CratePrintRequest) (store.Job, error) {
	if r.Order == "" {
		r.Order = OrderCrate
	}
	if !slices.Contains(CrateOrders, r.Order) {
		return store.Job{}, Errorf("Print order %q is not one of %s.", r.Order, strings.Join(CrateOrders, ", "))
	}
	c, err := s.Store.Crate(ctx, r.CrateID)
	if err != nil {
		return store.Job{}, err
	}
	records, err := s.Store.CrateRecords(ctx, r.CrateID)
	if err != nil {
		return store.Job{}, err
	}
	if len(records) == 0 {
		return store.Job{}, Errorf("%s is empty: add records to it first.", c.Name)
	}
	if r.RecordIDs != nil {
		want := map[int64]bool{}
		for _, id := range r.RecordIDs {
			want[id] = true
		}
		records = slices.DeleteFunc(records, func(rec store.CrateRecord) bool { return !want[rec.ID] })
	}
	if len(records) == 0 {
		return store.Job{}, Errorf("Pick at least one record from %s to print.", c.Name)
	}
	var sources []Source
	for _, rec := range SortCrate(records, r.Order) {
		if len(rec.Data) > 0 {
			l, err := ParseLabel(rec.Data)
			if err != nil {
				return store.Job{}, Errorf("The edited label of %s can't be read; undo the edits and try again.",
					cmp.Or(rec.Title, "release "+strconv.FormatInt(rec.ReleaseID, 10)))
			}
			sources = append(sources, Source{Data: &l})
			continue
		}
		rid := rec.ReleaseID
		sources = append(sources, Source{ReleaseID: &rid})
	}
	return s.Print(ctx, PrintRequest{Sources: sources, Template: r.Template, Stock: r.Stock, Printer: r.Printer,
		Copies: r.Copies, Options: r.Options, PrintOptions: r.PrintOptions, Origin: "crate:" + c.Name})
}
