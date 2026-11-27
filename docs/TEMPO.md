# BPM detection

`internal/tempo` estimates the tempo printed on DJ labels. It is pure Go and runs in about
half a second for a whole track.

## What makes label BPMs hard

- **The number has to be exact.** The literature's ±4 % "Acc1" tolerance is ±7 BPM at 174.
  A label is right only if it shows the integer the DJ sees on the deck. All numbers below
  use |Δ| ≤ 0.25 BPM against the reference.
- **The metrical level is a convention.** Drum & bass is 174, not 87; hip hop is 90, not 180;
  dubstep is 140. The audio alone is often ambiguous: a breakbeat repeats every two beats,
  so the strongest periodicity is frequently half the tempo DJs count.
- **Short clips and beat trackers.** A common approach runs a neural beat tracker on a 30 s
  preview and turns the beat times into a tempo. Measured below, its octave choice is mostly
  sound (92 % within 4 % on the library mix), but about one answer in nine lands a BPM or so
  off, which is wrong on a label. A 30 s clip is not what limits precision: the method here
  is nearly as exact on the same clips.

## The method

1. **Onsets.** A log-frequency (12 bands per octave) magnitude spectrogram at 100 frames/s
   is turned into SuperFlux-style spectral flux (Böck & Widmer 2013), with a maximum filter
   across frequency. It is split into low (< 200 Hz: kick and bass), mid and high
   (> 2.5 kHz: hats, snare noise) bands.
2. **Candidates.** The autocorrelation of the whole track's onset envelope, read as a comb
   over the first four multiples of each beat period, gives a salience curve over
   40–300 BPM. Its peaks, plus the half and double of the strongest, are the candidates.
3. **Metrical level.** Each candidate is scored by a linear model over 20 features:
   - the periodicity of each band at the candidate's beat, half-beat and two-beat lags;
   - the comb salience;
   - its place on the tempo scale;
   - onset density;
   - how well it fits the tempo range of the release's Discogs styles (`styles.go`).

   The best score wins, and a softmax over the scores is the confidence. The weights are a
   conditional logit fitted to rekordbox beat grids (see below). This is the style-prior
   idea of Hörschläger et al. (SMC 2015), learned jointly with rhythm features rather than
   applied as a hard range.
4. **Refinement.** The chosen tempo is pinned by maximising the phase-coherent energy of the
   onset envelope at the beat frequency and its first four harmonics over the whole
   recording: a long DFT, i.e. "one fixed grid that lines up with every onset". On a steady
   track this resolves a few hundredths of a BPM. The same refinement in overlapping 20 s
   windows tells whether the tempo holds. If it doesn't, the result is the dominant tempo,
   with `steady: false`.

Styles narrow the choice. "Drum n Bass" means 160–185, "Hip Hop" 80–105, "Happy Hardcore"
160–190. Specific styles outrank broad ones like "Breakbeat", which outrank genres like
"Electronic". An explicit range (`--range 140-190`) is near-decisive.

## How it was measured

- **Reference.** The reference is a DJ's library: about 8,000 tracks (mostly hardcore, jungle, drum & bass, hip hop, breaks, techno and house) with
  constant-tempo rekordbox 7 beat grids, read from rekordbox's analysis files. rekordbox 7's
  "high precision" analysis runs a neural beat tracker with madmom-style decoding, with an
  analysis range of 70–180 BPM. That is what the CDJs show, so agreeing with it is the goal.
  Its range also means tracks above 180 are stored at half tempo, which caps what any
  estimator can score in the 90s.
- **Data splits.**
  - Training (2,511 tracks) and the held-out sets never overlap.
  - "Stratified" is 50 tracks per 10-BPM band from 70 to 189: hard, because it over-weights
    the ambiguous extremes. Its tune half was used during development; its test half was
    looked at once at the end.
  - "Library mix" is 600 random tracks in the library's real tempo distribution.
- **Styles.** In the evaluation they come from ID3 genre tags (~70 % of files have one, some
  of them label names). That is noisier than Discogs styles.

### Results

No row is on a track used for training. The stratified test half was not looked at until the
end. The library-mix set is held out from training too, but it chose the regularisation
strength and the one dropped feature, so its crate-labels numbers are slightly optimistic.
"Exact" means within ±0.25 BPM of rekordbox; "octave-tolerant" also accepts ×2 and ÷2.

| Estimator | Audio | Stratified test (277): exact / octave-tolerant | Library mix (600): exact / octave-tolerant |
|---|---|---|---|
| **crate-labels**, with styles | whole track | **79.4 %** / 89.2 % | **94.3 %** / 97.2 % |
| crate-labels, no styles | whole track | 79.1 % / 90.3 % | 93.8 % / 97.5 % |
| crate-labels, with styles | 30 s clip from 40 % in | 79.1 % / 88.4 % | 93.3 % / 96.2 % |
| Beat This small (Foscarin et al. 2024) beats, median-interval tempo fit | 30 s clip from 40 % in | 75.1 % / 81.9 % | 80.8 % / 84.8 % |
| Davies & Plumbley / QM + Mixxx's grid fit, with styles | whole track | 72.9 % / 85.6 % | 81.2 % / 88.5 % |
| Percival & Tzanetakis 2014 (+ beat-fit refinement), with styles | whole track | 70.0 % / 91.0 % | 71.7 % / 97.7 % |
| Ellis 2007 / librosa (+ beat-fit refinement), with styles | whole track | 63.9 % / 75.5 % | 68.5 % / 73.8 % |
| Best ensemble of the above ("gated"), with styles | whole track | 80.1 % / 89.9 % | 94.2 % / 97.5 % |

- **The style weight is set by hand.** Fitted on ID3 genre tags, a release's specific style
  (Drum n Bass, Happy Hardcore, Gabber…) got a weight of 1.3, too weak to overrule the audio
  even on a bare 174 BPM click tagged Drum n Bass. It is set to 5, because Discogs styles are
  curated per release. On the noisy-tag sets above that costs 2 and 1 tracks. Two of the three
  changed answers are "Hardcore" tracks rekordbox stores at 98 and 100, where it now says 196
  and 200, which rekordbox's 70–180 range can't express. Before the change, the with-styles
  rows read 80.1 % and 94.5 %.
- **Precision.** Among exact answers the median error is 0.00 BPM and the 90th percentile
  0.01 BPM.
- **Confidence.** Answers with confidence ≥ 0.7 were exact 92–97 % of the time; answers
  below 0.4 rarely were.
- **Errors.** Nearly all remaining errors are the metrical level at the extremes: tracks
  rekordbox puts at 70–90 that read as double, and tracks at 180.
- **The published estimators.** Their tempo priors centre on 120 BPM, so they halve or
  take two-thirds of 150–180 BPM music. No ensemble of them beat the single estimator. The
  best one changed 6 of 600 answers, breaking as many as it fixed.


## Reproducing

```sh
# ref.tsv: bpm<TAB>anything<TAB>path[<TAB>comma-separated styles] from any trusted source
go run ./internal/tempo/tempoeval -list ref.tsv -split test -n 0
# refit the level weights (prints weights to paste into level.go)
TEMPO_TRAIN=train.tsv TEMPO_HELDOUT=heldout.tsv \
  go test -tags tempotrain -run TestTrainLevel -timeout 2h -v ./internal/tempo
```

The reference lists name files in your library; keep them out of the repository.

## Limits

- **Ambiguous at the edges.** Below 90 and above 175 the "right" octave is often a matter
  of convention: rekordbox itself folds 180+ tracks to half. Check low-confidence results
  (the UI flags them), and use the ½× / 2× buttons.
- **Needle drops carry the turntable's speed error.** 0.3 % off at 174 is 0.5 BPM. Set the
  pitch to 0 and play at the speed Discogs gives (33 or 45).
- **Variable-tempo music** (live drummers, DJ mixes) gets its dominant tempo with
  `steady: false`.
