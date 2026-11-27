# Bundled fonts

Every file here is an unmodified upstream release, copied from the google/fonts
repository at commit `e727fafe8e8a5d518972286d9763a32fdfbe2b91` and renamed to
short lowercase names. Each family's licence sits next
to its files as `<family>_ofl.txt`. All four families are under the SIL Open Font
License 1.1.

Base URL: `https://github.com/google/fonts/tree/e727fafe8e8a5d518972286d9763a32fdfbe2b91/ofl/`

| File | Upstream file | Font version | sha256 |
|---|---|---|---|
| `plexcond_400.ttf` | `ibmplexsanscondensed/IBMPlexSansCondensed-Regular.ttf` | 1.3 | `e7437c072eef2ef592ae6f2beb0000446287385907abb57ac1cf07bcbaa2aa33` |
| `plexcond_700.ttf` | `ibmplexsanscondensed/IBMPlexSansCondensed-Bold.ttf` | 1.3 | `b65492d86cdd5cd9d43d2671b55d5d36fec36859fc8b08bc6aba78e441d6c849` |
| `plexcond_ofl.txt` | `ibmplexsanscondensed/OFL.txt` | | `7e6b2818edbd8f6a01ae80641cc8f16a51080d08fb4e532be3a0b6f74adb07da` |
| `atkinson_400.ttf` | `atkinsonhyperlegible/AtkinsonHyperlegible-Regular.ttf` | 1.006 | `7fb917c89019896d0b52ee84b7cbb3304c18cb90b19a62f5e32712bd23e97669` |
| `atkinson_700.ttf` | `atkinsonhyperlegible/AtkinsonHyperlegible-Bold.ttf` | 1.006 | `5a3b0c8cc8ca545155150b4512a1fa248298df121c50d6557e651e61fbdab92f` |
| `atkinson_ofl.txt` | `atkinsonhyperlegible/OFL.txt` | | `f32d22b3908fcad2c86a74000614ec22e6a7f66ea7e867e616026a27aebdc143` |
| `plexmono_400.ttf` | `ibmplexmono/IBMPlexMono-Regular.ttf` | 2.3 | `6a3412f058c7d8dfd9170c41e85ade48e5156ecb89356110ca57a0a27734af46` |
| `plexmono_700.ttf` | `ibmplexmono/IBMPlexMono-Bold.ttf` | 2.3 | `ac27abd6450a64dd94467580a02fe6235156d5b92f2926ebbc8e7489df64e0be` |
| `plexmono_ofl.txt` | `ibmplexmono/OFL.txt` | | `7e6b2818edbd8f6a01ae80641cc8f16a51080d08fb4e532be3a0b6f74adb07da` |
| `enriqueta_400.ttf` | `enriqueta/Enriqueta-Regular.ttf` | 2.000 | `5e3aca4c6daf22188a92717b7ed53221519daa96b7f85117601ebf1890686716` |
| `enriqueta_700.ttf` | `enriqueta/Enriqueta-Bold.ttf` | 2.000 | `bb453797b5e353990c4569994754c2496640f427331c9f33b51414ed56777412` |
| `enriqueta_ofl.txt` | `enriqueta/OFL.txt` | | `0e74984817cdd51dbfbd8309022aa75f733e80dff0cb1d26ad2157e90446ba97` |

Copyright and Reserved Font Names, from the licence files:

- IBM Plex Sans Condensed and IBM Plex Mono: © 2017 IBM Corp., Reserved Font Name "Plex".
- Atkinson Hyperlegible: © 2020 Braille Institute of America, Inc.
- Enriqueta: © 2011 FontFuror, Reserved Font Names "Enriqueta" and "Enriqueta Pro".

None of the families has ♯ or ♭. The renderer draws those two signs itself (see
`accidental` in `fonts.go`). Glyphs that Atkinson Hyperlegible or Enriqueta lack, such as ⅓,
arrows and ″, are taken from IBM Plex Sans Condensed at the same weight.

To update a font, download the new upstream file, replace it here under the same name,
update this table, and regenerate the reference renders with
`go test ./internal/layout -run TestReferenceRenders -update`.
