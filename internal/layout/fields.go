package layout

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Field rules shared by the templates: names, tracks, keys, tempo, formats, grades and
// prices.

var discogsSuffix = regexp.MustCompile(`\s*\(\d+\)\s*$`)

// StripDiscogsSuffix removes the number Discogs appends to tell same-named artists and
// labels apart: "Moth Signal (2)" becomes "Moth Signal".
func StripDiscogsSuffix(name string) string {
	return strings.TrimSpace(discogsSuffix.ReplaceAllString(name, ""))
}

// artistName and labelName are the names as printed: trimmed, without the suffix.
func artistName(d *LabelData) string { return StripDiscogsSuffix(d.Artist) }
func labelName(d *LabelData) string  { return StripDiscogsSuffix(d.Label) }

func includedTracks(d *LabelData) []Track {
	var out []Track
	for _, t := range d.Tracks {
		if t.Included {
			out = append(out, t)
		}
	}
	return out
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// pickTrack chooses the track for single-track bodies: choice is a position ("B1",
// any case) or a 1-based index; blank means the first included track, else the first.
func pickTrack(d *LabelData, choice string) *Track {
	choice = strings.TrimSpace(choice)
	if choice != "" {
		for i := range d.Tracks {
			if strings.EqualFold(strings.TrimSpace(d.Tracks[i].Position), choice) {
				return &d.Tracks[i]
			}
		}
		if isASCIIDigits(choice) {
			if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(d.Tracks) {
				return &d.Tracks[n-1]
			}
		}
	}
	for i := range d.Tracks {
		if d.Tracks[i].Included {
			return &d.Tracks[i]
		}
	}
	if len(d.Tracks) > 0 {
		return &d.Tracks[0]
	}
	return nil
}

// joinFacts joins the non-empty parts with a centred dot.
func joinFacts(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, factSep)
}

// factSep separates facts on one line. A middle dot never occurs in Discogs genre or
// style names, so it cannot be confused with the commas and slashes some of them contain.
const factSep = " · "

// styleLine is up to n styles (else genres) in source order, joined with factSep.
func styleLine(d *LabelData, n int) string {
	for _, values := range [][]string{d.Styles, d.Genres} {
		var parts []string
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" && len(parts) < n {
				parts = append(parts, v)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, factSep)
		}
	}
	return ""
}

// --- Keys ------------------------------------------------------------------------------

// Key notations for the key_notation option.
const (
	KeyCamelot = "camelot"
	KeyMusical = "musical"
	KeyBoth    = "both"
	KeyOff     = "off"
)

var (
	pitchClass = map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}

	camelotRE = regexp.MustCompile(`^0?(1[0-2]|[1-9])\s*([ABab])$`)
	openKeyRE = regexp.MustCompile(`^0?(1[0-2]|[1-9])\s*([mdMD])$`)
	musicalRE = regexp.MustCompile(`^([A-Ga-g])\s*([#b]?)\s*(m|min|minor|maj|major|M|-)?$`)
)

// The Camelot wheel numbers keys by fifths: C major is 8B, each step clockwise adds a
// fifth (7 semitones), and a minor key shares the number of its relative major, three
// semitones above it. 7 is its own inverse modulo 12, which makes the mapping easy to
// run both ways.

// camelotCode is the Camelot code of the key with tonic pitch class pc.
func camelotCode(pc int, minor bool) string {
	major := pc
	letter := "B"
	if minor {
		major, letter = pc+3, "A"
	}
	n := ((7*major)%12+7)%12 + 1
	return strconv.Itoa(n) + letter
}

// tonicOf is the tonic pitch class of a Camelot number and whether the key is minor.
func tonicOf(n int, minor bool) int {
	major := (7 * ((n + 4) % 12)) % 12
	if minor {
		return (major + 9) % 12
	}
	return major
}

// tonicNames spells each pitch class as DJ software usually does: flats for the black
// keys, except F♯.
var tonicNames = [12]string{"C", "D♭", "D", "E♭", "E", "F", "F♯", "G", "A♭", "A", "B♭", "B"}

// ParseKey returns the Camelot code ("8A") for a key written as Camelot ("8A", "08a"),
// Open Key ("1m") or a note name ("Am", "F#m", "F♯m", "Bbm", "Db"; a lone lowercase
// letter is minor). ok is false when the text is none of these.
func ParseKey(text string) (string, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "", false
	}
	raw = strings.NewReplacer("♯", "#", "♭", "b").Replace(raw)
	if m := camelotRE.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("%d%s", n, strings.ToUpper(m[2])), true
	}
	if m := openKeyRE.FindStringSubmatch(raw); m != nil {
		// Open Key starts at C major / A minor = 1, where Camelot has 8.
		n, _ := strconv.Atoi(m[1])
		letter := "B"
		if strings.ToLower(m[2]) == "m" {
			letter = "A"
		}
		return fmt.Sprintf("%d%s", (n+6)%12+1, letter), true
	}
	if m := musicalRE.FindStringSubmatch(raw); m != nil {
		letter, accidental, quality := m[1], m[2], m[3]
		pc := pitchClass[strings.ToUpper(letter)[0]]
		switch accidental {
		case "#":
			pc++
		case "b":
			pc--
		}
		minor := quality == "m" || quality == "min" || quality == "minor" || quality == "-" ||
			(quality == "" && unicode.IsLower(rune(letter[0])))
		return camelotCode(((pc%12)+12)%12, minor), true
	}
	return "", false
}

// musicalName is the note name of a Camelot code, e.g. "8A" → "Am", "3B" → "D♭".
func musicalName(code string) string {
	n, err := strconv.Atoi(code[:len(code)-1])
	if err != nil {
		return code
	}
	minor := strings.HasSuffix(code, "A")
	name := tonicNames[tonicOf(n, minor)]
	if minor {
		name += "m"
	}
	return name
}

// KeyDisplay formats a track key for a label in the given notation. Keys that do not
// parse print as typed, except with "off".
func KeyDisplay(text, notation string) string {
	if notation == KeyOff || strings.TrimSpace(text) == "" {
		return ""
	}
	code, ok := ParseKey(text)
	if !ok {
		return strings.TrimSpace(text)
	}
	switch notation {
	case KeyMusical:
		return musicalName(code)
	case KeyBoth:
		return code + " " + musicalName(code)
	}
	return code
}

// --- Tempo -----------------------------------------------------------------------------

// BPMRange is a one-octave window [Low, 2·Low) that tempos are folded into, so a
// half-time or double-time reading prints in the range the DJ thinks in.
type BPMRange struct {
	ID    string
	Low   int
	Label string
}

// BPMRanges are the choices of the bpm_range option, after "off". Each window keeps one
// family of tempos as detected and folds the rest into it:
//   - 60–119: downtempo and hip-hop stay; house and techno print at half time.
//   - 75–149: house, techno and trance stay; drum and bass prints at half time.
//   - 85–169: hip-hop through techno stay; slow jams double, drum and bass halves.
//   - 95–189: house through drum and bass and hardcore stay; hip-hop doubles.
var BPMRanges = []BPMRange{
	{"60-119", 60, "60–119"},
	{"75-149", 75, "75–149"},
	{"85-169", 85, "85–169"},
	{"95-189", 95, "95–189"},
}

// bpmRangeLow is the window's low end for a bpm_range choice; 0 means no folding.
func bpmRangeLow(id string) int {
	for _, r := range BPMRanges {
		if r.ID == id {
			return r.Low
		}
	}
	return 0
}

// FoldBPM brings bpm into [low, 2·low) by whole octaves and rounds it to an integer;
// low ≤ 0 only rounds. A value that rounds up onto 2·low prints as low.
func FoldBPM(bpm float64, low int) int {
	if math.IsNaN(bpm) || math.IsInf(bpm, 0) {
		return 0
	}
	if low <= 0 || bpm <= 0 {
		return int(math.Round(bpm))
	}
	octaves := math.Floor(math.Log2(bpm / float64(low)))
	v := math.Round(math.Ldexp(bpm, -int(octaves)))
	if v >= float64(2*low) {
		v /= 2
	}
	return int(v)
}

// tempoText is a track's folded BPM as printed, or "" when it is unknown.
func tempoText(t *Track, low int) string {
	if t == nil || t.BPM <= 0 {
		return ""
	}
	return strconv.Itoa(FoldBPM(float64(t.BPM), low))
}

// --- Formats, grades and prices ----------------------------------------------------------

// inchMarks are the ways a record size is followed by "inch" in Discogs and in typing.
var inchMarks = []string{`"`, "″", "”", "''", "-inch", " inch", "inch", "in"}

// FormatDisplay prints a record size with a proper inch sign: `12"` and "12" become
// "12″", `6½"` becomes "6½″", "7 EP" becomes "7″ EP". A bare number gets the sign only
// when it is a common record size (7, 10 or 12); anything else, such as "25cm" or "LP",
// is kept as written.
func FormatDisplay(raw string) string {
	text := strings.TrimSpace(raw)
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 {
		return text
	}
	if rest := text[end:]; strings.HasPrefix(rest, "½") {
		end += len("½")
	}
	size, rest := text[:end], text[end:]
	marked := false
	for _, mark := range inchMarks {
		if after, ok := strings.CutPrefix(rest, mark); ok {
			if mark == "in" && after != "" && unicode.IsLetter([]rune(after)[0]) {
				continue // "7inc…" or "10ink" is not an inch mark
			}
			rest, marked = after, true
			break
		}
	}
	common := size == "7" || size == "10" || size == "12"
	if !marked && (!common || (rest != "" && !unicode.IsSpace([]rune(rest)[0]) && !unicode.IsLetter([]rune(rest)[0]))) {
		return text
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		return size + "″ " + rest
	}
	return size + "″"
}

var grades = []string{"M", "NM", "VG+", "VG", "G+", "G", "F", "P"}

var gradeWords = map[string]string{
	"MINT": "M", "NEAR MINT": "NM", "VERY GOOD PLUS": "VG+", "VERY GOOD": "VG",
	"GOOD PLUS": "G+", "GOOD": "G", "FAIR": "F", "POOR": "P", "M-": "NM", "EX": "VG+",
}

var parenRE = regexp.MustCompile(`\(([^)]*)\)`)

func isGrade(s string) bool {
	for _, g := range grades {
		if s == g {
			return true
		}
	}
	return false
}

// parseGrade gives the short grade for "VG+", "Near Mint (NM or M-)" and similar, or "".
func parseGrade(text string) string {
	t := strings.ToUpper(strings.TrimSpace(text))
	if t == "" {
		return ""
	}
	if loc := parenRE.FindStringSubmatchIndex(t); loc != nil {
		inner := strings.TrimSpace(strings.SplitN(t[loc[2]:loc[3]], " OR ", 2)[0])
		if isGrade(inner) {
			return inner
		}
		t = strings.TrimSpace(t[:loc[0]])
	}
	if isGrade(t) {
		return t
	}
	return gradeWords[t]
}

var conditionSplit = regexp.MustCompile(`\s*[/,;|]\s*`)

// condition is a disc grade and a cover grade; "" means none.
type condition struct{ disc, cover string }

// parseCondition reads "NM / VG+" as disc NM, cover VG+. Text that is not a grade
// (such as "Generic") is ignored.
func parseCondition(text string) condition {
	t := strings.TrimSpace(text)
	if t == "" {
		return condition{}
	}
	parts := conditionSplit.Split(t, 2)
	c := condition{disc: parseGrade(parts[0])}
	if len(parts) > 1 {
		c.cover = parseGrade(parts[1])
	}
	return c
}

// Currencies are the choices of the currency option.
var Currencies = []string{"USD", "EUR", "GBP", "CAD", "AUD", "JPY", "PLN"}

var (
	currencyPrefix = map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "CAD": "CA$", "AUD": "A$", "JPY": "¥"}
	currencySuffix = map[string]string{"PLN": " zł"}
	wholeUnits     = map[string]bool{"JPY": true} // no minor unit in everyday prices
	symbolCodes    = [][2]string{{"CA$", "CAD"}, {"A$", "AUD"}, {"$", "USD"}, {"€", "EUR"}, {"£", "GBP"}, {"¥", "JPY"}, {"zł", "PLN"}}
	numberRE       = regexp.MustCompile(`\d[\d.,]*`)
)

// parsePrice reads "12.99", "$12", "£15", "20 EUR" or "12,50 €"; ok is false without a
// number.
func parsePrice(text, defaultCurrency string) (amount float64, currency string, ok bool) {
	t := strings.TrimSpace(text)
	currency = defaultCurrency
	if t == "" {
		return 0, currency, false
	}
	upper := strings.ToUpper(t)
	found := false
	for _, code := range append(append([]string{}, Currencies...), "CHF", "SEK", "NOK", "DKK") {
		if strings.Contains(upper, code) {
			currency, found = code, true
			break
		}
	}
	if !found {
		for _, sc := range symbolCodes {
			if strings.Contains(t, sc[0]) {
				currency = sc[1]
				break
			}
		}
	}
	token := numberRE.FindString(t)
	if token == "" {
		return 0, currency, false
	}
	v, err := parseNumber(strings.TrimRight(token, ".,"))
	if err != nil {
		return 0, currency, false
	}
	return v, currency, true
}

// parseNumber reads "1,500" and "1.500,00" as thousands, "12,50" and "12.99" as decimals.
func parseNumber(token string) (float64, error) {
	if strings.Contains(token, ",") && strings.Contains(token, ".") {
		decimal, thousands := ".", ","
		if strings.LastIndex(token, ",") > strings.LastIndex(token, ".") {
			decimal, thousands = ",", "."
		}
		return strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(token, thousands, ""), decimal, "."), 64)
	}
	for _, sep := range []string{",", "."} {
		if !strings.Contains(token, sep) {
			continue
		}
		parts := strings.Split(token, sep)
		head, groups := parts[0], parts[1:]
		if len(groups) > 1 || (len(groups[0]) == 3 && sep == ",") {
			return strconv.ParseFloat(head+strings.Join(groups, ""), 64)
		}
		return strconv.ParseFloat(head+"."+groups[0], 64)
	}
	return strconv.ParseFloat(token, 64)
}

// FormatPrice prints a price for a tag: cents only when there are some ("$12.99",
// "$12"), thousands grouped, the currency as a symbol where one is common and as its
// code otherwise. It returns "" when the text holds no number.
func FormatPrice(text, defaultCurrency string) string {
	amount, currency, ok := parsePrice(text, defaultCurrency)
	if !ok || amount < 0 {
		return ""
	}
	cents := int64(math.Round(amount * 100))
	if wholeUnits[currency] {
		cents = int64(math.Round(amount)) * 100
	}
	digits := groupThousands(cents / 100)
	if cents%100 != 0 {
		digits += fmt.Sprintf(".%02d", cents%100)
	}
	if p, ok := currencyPrefix[currency]; ok {
		return p + digits
	}
	if s, ok := currencySuffix[currency]; ok {
		return digits + s
	}
	return currency + " " + digits
}

// groupThousands writes v with a comma every three digits.
func groupThousands(v int64) string {
	s := strconv.FormatInt(v, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// currencyMark is the symbol (or code) a write-in price line starts with.
func currencyMark(currency string) string {
	if p, ok := currencyPrefix[currency]; ok {
		return p
	}
	if s, ok := currencySuffix[currency]; ok {
		return strings.TrimSpace(s)
	}
	return currency
}
