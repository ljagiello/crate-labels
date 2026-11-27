package tempo

import (
	"math"
	"strings"
)

// Tempo ranges by Discogs genre and style, as DJs count them: drum & bass is 174, not 87;
// hip hop is 90, not 180; dubstep is 140, not 70. They follow the conventions of DJ
// software and record shops, the same idea as Hörschläger et al., "Addressing tempo
// estimation octave errors in electronic music by incorporating style information
// extracted from Wikipedia" (SMC 2015).
//
// Ranges are soft: rangePrior lets a tempo slightly outside still win if the audio is
// clear about it.

type bpmRange struct{ lo, hi float64 }

// specificStyles name a tempo range on their own.
var specificStyles = map[string]bpmRange{
	// drum & bass, jungle, hardcore and friends
	"drum n bass": {160, 185}, "jungle": {150, 180}, "hardcore": {125, 200},
	"happy hardcore": {160, 190}, "gabber": {150, 220}, "speedcore": {200, 300},
	"breakcore": {150, 250}, "hardstyle": {140, 160}, "hard house": {135, 155},
	"donk": {135, 160}, "makina": {150, 180}, "footwork": {155, 165}, "juke": {155, 165},
	// trance
	"trance": {125, 145}, "hard trance": {135, 155}, "psy-trance": {135, 150},
	"goa trance": {130, 150}, "progressive trance": {125, 138}, "tech trance": {130, 145},
	// techno
	"techno": {118, 150}, "acid": {118, 150}, "minimal": {118, 132}, "dub techno": {112, 130},
	"schranz": {140, 165}, "ebm": {110, 135}, "hard techno": {135, 160},
	// house and disco
	"house": {115, 132}, "deep house": {112, 126}, "tech house": {120, 130},
	"progressive house": {122, 132}, "acid house": {115, 130}, "garage house": {115, 128},
	"hip-house": {110, 128}, "italo house": {115, 130}, "euro house": {120, 135},
	"ghetto house": {125, 135}, "electro house": {124, 132}, "chicago house": {115, 128},
	"disco": {105, 130}, "nu-disco": {110, 125}, "italo-disco": {110, 130},
	"euro-disco": {110, 135}, "hi nrg": {120, 140}, "eurodance": {125, 145},
	// UK bass
	"uk garage": {128, 140}, "speed garage": {128, 140}, "bassline": {135, 142},
	"dubstep": {136, 145}, "grime": {136, 145}, "uk funky": {125, 135},
	// breaks and electro
	"breaks": {120, 140}, "big beat": {95, 140}, "electro": {110, 135},
	"freestyle": {110, 130}, "miami bass": {120, 140}, "baltimore club": {125, 135},
	// slower
	"downtempo": {70, 110}, "trip hop": {70, 105}, "dub": {65, 90}, "dancehall": {85, 110},
	"roots reggae": {65, 85}, "lovers rock": {65, 90},
	// hip hop
	"boom bap": {80, 100}, "gangsta": {80, 100}, "g-funk": {85, 100}, "conscious": {80, 100},
	"jazzy hip-hop": {80, 98}, "hardcore hip-hop": {80, 100}, "pop rap": {85, 110},
	"bounce": {95, 105}, "trap": {130, 160},
}

// broadStyles say something about tempo only when nothing more specific is given.
var broadStyles = map[string]bpmRange{
	"breakbeat": {95, 150}, "industrial": {90, 160}, "synth-pop": {100, 140},
	"new wave": {110, 160}, "ambient": {60, 130}, "experimental": {60, 200},
	"idm": {80, 170}, "leftfield": {80, 150}, "bass music": {120, 175},
	"instrumental": {70, 140}, "abstract": {70, 140}, "turntablism": {80, 110},
}

// genres are Discogs's top-level genres, used last.
var genres = map[string]bpmRange{
	"electronic": {85, 180}, "hip hop": {80, 105}, "funk / soul": {90, 125},
	"rock": {95, 165}, "pop": {90, 130}, "latin": {90, 130}, "reggae": {60, 115},
}

func normStyle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "&", "n")
	s = strings.Join(strings.Fields(s), " ")
	switch s {
	case "drum and bass", "drum n' bass", "drum 'n' bass", "dnb", "d n b":
		return "drum n bass"
	case "hip-hop", "hiphop", "rap":
		return "hip hop"
	case "psytrance", "psy trance":
		return "psy-trance"
	case "trip-hop":
		return "trip hop"
	case "hardcore techno":
		return "gabber"
	}
	return s
}

// How much a range says: an explicit range or specific styles, broad styles or genres, or
// nothing.
const (
	levelNone = iota
	levelSpecific
	levelBroad
)

// styleRanges is the tempo range opt implies and how specific it is.
func styleRanges(opt Options) (lo, hi float64, level int) {
	if opt.MinBPM > 0 && opt.MaxBPM > opt.MinBPM {
		return opt.MinBPM, opt.MaxBPM, levelSpecific
	}
	for i, table := range []map[string]bpmRange{specificStyles, broadStyles, genres} {
		lo, hi = math.Inf(1), math.Inf(-1)
		for _, s := range opt.Styles {
			if r, found := table[normStyle(s)]; found {
				lo, hi = math.Min(lo, r.lo), math.Max(hi, r.hi)
			}
		}
		if hi > lo {
			if i == 0 {
				return lo, hi, levelSpecific
			}
			return lo, hi, levelBroad
		}
	}
	return 0, 0, levelNone
}

// styleRange is the tempo range opt implies, and whether it implies one.
func styleRange(opt Options) (lo, hi float64, ok bool) {
	lo, hi, level := styleRanges(opt)
	return lo, hi, level != levelNone
}

// StyleRange is the tempo range Analyze prefers for opt: MinBPM/MaxBPM when set, else the
// union of the ranges of the most specific Discogs styles given, else a generic one.
func StyleRange(opt Options) (lo, hi float64) {
	if lo, hi, ok := styleRange(opt); ok {
		return lo, hi
	}
	return 70, 180
}
