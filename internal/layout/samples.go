package layout

// Sample records for previews, reference renders and tests. Every artist, label, title
// and catalogue number is invented; the release ids only make the QR codes realistic.

// SampleNames lists the sample records in order: a four-track 12″ EP, a two-sided 7″
// single and a stress case with long text everywhere.
var SampleNames = []string{"ep12", "single7", "longform"}

// Sample returns a fresh copy of the named sample record, or nil for an unknown name.
func Sample(name string) *LabelData {
	switch name {
	case "ep12":
		return sampleEP()
	case "single7":
		return sampleSingle()
	case "longform":
		return sampleLongform()
	}
	return nil
}

// sampleEP is a four-track techno 12″ with tempos and keys in mixed notations.
func sampleEP() *LabelData {
	return &LabelData{
		Artist:  "Moth Signal (2)",
		Title:   "Low Orbit EP",
		Label:   "Tidewater Audio",
		Catno:   "TWA 017",
		Year:    "1996",
		Country: "Netherlands",
		Format:  `12"`,
		Genres:  []string{"Electronic"},
		Styles:  []string{"Techno", "Electro", "Acid"},
		Tracks: []Track{
			{Position: "A1", Title: "Low Orbit", Duration: "6:41", BPM: 132, Key: "5A", Included: true},
			{Position: "A2", Title: "Static Bloom", Duration: "5:58", BPM: 128, Key: "Gm", Included: true},
			{Position: "B1", Title: "Carrier Wave (Night Mix)", Duration: "7:12", BPM: 136, Key: "10B", Included: true},
			{Position: "B2", Title: "Ferrous", Duration: "5:20", BPM: 140, Key: "F#m", Included: true},
		},
		Price:     "18.50",
		Condition: "Very Good Plus (VG+) / Very Good (VG)",
		QRURL:     DiscogsURL(2360917, false),
		ReleaseID: 2360917,
	}
}

// sampleSingle is a soul 7″ with one track a side.
func sampleSingle() *LabelData {
	return &LabelData{
		Artist:  "Juniper Vale",
		Title:   "Paper Lanterns",
		Label:   "Small Hours",
		Catno:   "SH45-03",
		Year:    "2003",
		Country: "UK",
		Format:  `7"`,
		Genres:  []string{"Funk / Soul"},
		Styles:  []string{"Soul", "Northern Soul"},
		Tracks: []Track{
			{Position: "A", Title: "Paper Lanterns", BPM: 98, Key: "Dm", Included: true},
			{Position: "B", Title: "Late Bus Home", BPM: 104, Key: "3B", Included: true},
		},
		Price:     "7",
		Condition: "NM",
		QRURL:     DiscogsURL(3071552, false),
		ReleaseID: 3071552,
	}
}

// sampleLongform is the stress case: long names, a word longer than any label, tempos
// that need folding, missing data and a track left out.
func sampleLongform() *LabelData {
	return &LabelData{
		Artist:  "The Hollow Meridian Collective & Associates (3)",
		Title:   "Notes From A Very Long Drive Through The Northern Provinces (Deluxe Extended Edition)",
		Label:   "Unreasonably Long Imprint Name Phonographic Ltd.",
		Catno:   "ULIN-LP-00981-XR",
		Year:    "1979",
		Country: "Canada",
		Format:  "12",
		Genres:  []string{"Jazz", "Funk / Soul", "Folk, World, & Country"},
		Styles:  []string{"Fusion", "Jazz-Funk", "Spiritual Jazz", "Modal"},
		Tracks: []Track{
			{Position: "A1", Title: "Notes From A Very Long Drive Through The Northern Provinces (Part One)", BPM: 71, Key: "Db", Included: true},
			{Position: "A2", Title: "Interlude", Included: true},
			{Position: "A3", Title: "Overunderthroughbetweenaroundandbackagain", BPM: 186, Key: "12B", Included: true},
			{Position: "B1", Title: "Northern Provinces, Part Two (Live At The Harbour Hall)", BPM: 93, Key: "Ebm", Included: true},
			{Position: "B2", Title: "Coda", Included: true},
			{Position: "B3", Title: "Hidden Groove", BPM: 120, Key: "4A", Included: false},
		},
		Notes:     "Promo stamp on the back cover; inner sleeve missing",
		Price:     "CA$1,249.50",
		Condition: "Near Mint (NM or M-) / Very Good Plus (VG+)",
		QRURL:     DiscogsURL(9031177, false),
		ReleaseID: 9031177,
	}
}
