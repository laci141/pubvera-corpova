# Corpova export-javítás — bizonyíték-jegyzőkönyv

**Dátum:** 2026-07-26
**Állapot:** a javítás MÉG NEM készült el. Az `index.html` érintetlen.
Ez a fájl a 3/3 (implementáció) jóváhagyásának alapja.

**Olvasási útmutató:** minden szakasz jelölve van, hogy **MÉRÉS** (tool-kimenet,
reprodukálható) vagy **ÍTÉLET** (tervezési döntés, indoklással). A kettőt sehol
nem keverem.

| # | Szakasz | Típus |
|---|---|---|
| 1 | A bukó teszt kimenete | **MÉRÉS** |
| 2 | Alapkulcs-ütközés a valós korpuszokon | **MÉRÉS** |
| 3 | A `@misc` alapkulcs terve | **ÍTÉLET** |
| 4 | Az ütközés-őr (a)/(b) döntése | **ÍTÉLET**, a 2. pont számaira építve |

---

## 1. A BUKÓ TESZT — **MÉRÉS**

A javítás előtt kell látni a tesztet pirosan. Ha csak a javítás utáni zöldet
látnánk, nem tudnánk, hogy a teszt egyáltalán képes kimutatni a hibát.

**Reprodukció:**

```bash
cd ~/pubvera-corpova
node export_wysiwyg_test.mjs; echo "EXIT=$?"
```

**Teljes kimenet a MOSTANI, javítatlan kódon (2026-07-26):**

```
PASS  S1 initial qcount  →  " · 10 / 10 rows"
PASS  S1 visible cards after filter  →  4
PASS  S1 qcount after filter  →  " · 4 / 10 rows"
PASS  S1 exportRows == visible  →  4
PASS  S1 CSV data rows  →  4
PASS  S1 XLSX data rows  →  4
PASS  S1 BibTeX entries  →  4
PASS  S1 JSON rows  →  4
PASS  S1 JSON envelope counts  →  [4,10]
PASS  S1 JSON filters  →  ["filter: \"magnesium\""]
PASS  S1 CSV provenance line  →  true
PASS  S1 XLSX caption row  →  true
PASS  S1 XLSX freeze below caption+header  →  2
PASS  S1 BibTeX provenance comment  →  true
PASS  S1 JSON app/query  →  ["Corpova","magnesium lowers blood pressure"]
PASS  S2 export follows sorted DOM order  →  ["case-report","cohort","rct","meta-analysis"]
PASS  S2 sort recorded in filters  →  ["sorted by \"Count\" asc"]
PASS  S2 JSON keeps full response  →  true
PASS  S2 JSON first row is screen-first row  →  "case-report"
PASS  S3 claim A filtered, claim B untouched  →  ["alpha trial 1","gamma trial 5","gamma trial 6","gamma trial 7"]
PASS  S3 qcount A  →  " · 1 / 4 rows"
PASS  S3 qcount B  →  " · 3 / 3 rows"
PASS  S4 empty view alert  →  "Run an analysis first — or clear the filters, nothing is visible."
PASS  S4 no file downloaded  →  5
FAIL  S5 consensus_verdict: two claims share no citation key  →  ["corpova_consensus_verdict_2026"] (expected [])
FAIL  S5 compare_summary: two claims share no citation key  →  ["corpova_compare_summary_2026"] (expected [])
FAIL  S5 controversies_summary: two claims share no citation key  →  ["corpova_controversies_summary_2026"] (expected [])
FAIL  S5 gaps_findings: two claims share no citation key  →  ["corpova_gaps_findings_2026"] (expected [])
FAIL  S5 evidence_pyramid: two claims share no citation key  →  ["corpova_evidence_pyramid_2026"] (expected [])
FAIL  S5 @article: different papers get different keys  →  ["effects2013_1"] (expected [])
PASS  S5 keys stay ASCII/BibTeX-safe  →  []

6 FAILURE(S)
EXIT=1
```

### Melyik bukik, mit vár, mit kap

| # | Ellenőrzés | Kapott | Várt | Áll. |
|---|---|---|---|---|
| 1 | `consensus_verdict` — két claim közös kulcsa | `["corpova_consensus_verdict_2026"]` | `[]` | **FAIL** |
| 2 | `compare_summary` — két claim közös kulcsa | `["corpova_compare_summary_2026"]` | `[]` | **FAIL** |
| 3 | `controversies_summary` — két claim közös kulcsa | `["corpova_controversies_summary_2026"]` | `[]` | **FAIL** |
| 4 | `gaps_findings` — két claim közös kulcsa | `["corpova_gaps_findings_2026"]` | `[]` | **FAIL** |
| 5 | `evidence_pyramid` — két claim közös kulcsa | `["corpova_evidence_pyramid_2026"]` | `[]` | **FAIL** |
| 6 | `@article` — két különböző cikk kulcsa | `["effects2013_1"]` | `[]` | **FAIL** |
| 7 | kulcsok ASCII/BibTeX-biztosak | `[]` | `[]` | PASS |

**EXIT kód: `1`.** Ez a szándékos piros alapállapot, nem elromlott build.

### Összesen hány teszt — a „+7 vs 6" tisztázása

```bash
node export_wysiwyg_test.mjs 2>/dev/null | grep -cE '^(PASS|FAIL)'   # → 31
```

- **31 ellenőrzés összesen**
- **24 régi** (S1–S4, a 2026-07-23-i WYSIWYG-javításból) — mind **PASS**, nulla regresszió
- **+7 új** (S5) — ebből **6 piros**, **1 zöld**

**Miért indul a hetedik zölden?** Az `S5 keys stay ASCII/BibTeX-safe` nem a mai
hibát bizonyítja, hanem a *javítás* csapdája. Ma a kulcs
`corpova_consensus_verdict_2026`, ami **véletlenül** tiszta ASCII — azért, mert
a claim bele sem kerül. Abban a pillanatban, hogy a javítás beteszi a claimet a
kulcsba, egy magyar ékezetes claim (`a szívinfarktus kockázata csökken`) valódi
kockázattá válik. Ennek az ellenőrzésnek zöldnek kell lennie a javítás **előtt
és után is**.

Tehát: **7 új ellenőrzés, ebből 6 mutatja ki a hibát, 1 őrzi a javítást.**

### Mit bizonyít a 6 piros

1. **A ④-es lelet mind az 5 összegző analízis-típusra igaz**, nem csak a
   consensusra: `analysisBibTeX` (`index.html:1280`) a `corpova_<kulcs>_<év>`
   mintát adja, amiben **semmi nem különbözteti meg a claimeket**.
2. **A `@article` ág is ütközik fájlok között**: két teljesen különböző cikk
   (`Effects of magnesium…` és `Effects of vitamin D…`, mindkettő 2013) ugyanazt
   az `effects2013_1` kulcsot kapja, mert a `bibKey` (`index.html:1239–1243`)
   sorszámot használ, ami fájlon kívül értelmetlen.

---

## 2. ALAPKULCS-ÜTKÖZÉS A VALÓS KORPUSZOKON — **MÉRÉS**

**Kérdés:** a javasolt tartalom-alapú alapkulcs — `<w1><év><w2>`, az első két
érdemi címszóból — mennyire különböztet meg valóban?

**Adat:** `library/other/scientific-consensus/internal/scengine/testdata/corpora_full/`
a monorepóból (a 2026-07-24-i untruncated re-export, commit `580f572c1`).
12 korpusz, 246 mű. **A monorepó ehhez csak olvasva lett**; a mérőszkript a
scratchpadben (`bibkey_collision_measure.mjs`), nem a repóban.

**A mért kulcsfüggvény:**

```js
const STOP = new Set('a an the of on in for and to is are was were does do can vs versus with'.split(' '));
function words(s){
  return String(s||'').normalize('NFD').replace(/[̀-ͯ]/g,'')
    .toLowerCase().replace(/[^a-z0-9]+/g,' ').trim().split(/\s+/)
    .filter(w => w && !STOP.has(w));
}
function baseKey(r){
  const w = words(r.title);
  return (w[0]||'ref') + (Number(r.year)||'') + (w[1]||'');
}
```

**Eredmény:**

```
corpus              all_studies  in-file dup  visible(6)  in-file dup
------------------  -----------  -----------  ----------  -----------
cellphones                  31            0           6            0
coffee                      11            0           3            0
meditation                  10            0           3            0
melatonin                   19            0           4            0
omega3                      62            6           3            0
probiotics                   9            0           3            0
redmeat_run1                11            0           6            0
redmeat_run2                11            0           6            0
saffron                     12            0           3            0
sweeteners                   7            0           3            0
vaccines                    37            0           4            0
vitamind                    26            6           3            0

─── cross-file ───
distinct works (by DOI/title):      235
total all_studies rows:             246
rows carrying a DOI:                241  (98.0%)
distinct base keys:                 222
base keys claimed by >1 work:       10
works affected by a clash:          23  (9.3% of rows)

the clashing keys:
  omega20183   ←  2 works       vitamin2020d      ←  3 works
  omega20093   ←  2 works       vitamin2021d      ←  2 works
  omega20173   ←  2 works       vitamin2013d      ←  3 works
  omega20193   ←  2 works       vitamin2011d      ←  2 works
  20192019esc  ←  3 works       evidence2020that  ←  2 works
```

### A mért tények

- **Fájlon belül, a jelenlegi látható exportban (3+3 sor): 0 ütközés**, mind a
  12 korpuszon. A mai `consensus_studies` export tehát nem érintett.
- **Fájlon belül, az `all_studies`-on** (a tervezett „All analyzed studies"
  tábla): `omega3` **6**, `vitamind` **6**. A többi 0.
- **Fájlok között: 10 alapkulcsot 2-3 különböző mű is igényelne**, ez **23 mű =
  a sorok 9,3%-a**.
- **DOI-lefedettség: 98,0%** (241/246).

Fontos definíció: ütközésnek azt számoltam, amikor **különböző** művek (eltérő
DOI) kapnák ugyanazt a kulcsot. Amikor *ugyanaz* a mű kapja ugyanazt a kulcsot
két fájlban, az nem hiba, hanem a kívánt Zotero-összevonás.

### Mit mutat a kulcsok LISTÁJA a puszta arányon túl

`omega20183`, `omega20093`, `omega20173`, `omega20193` — mind „Omega-3 …"
kezdetű címekből. `vitamin2013d`, `vitamin2020d`, `vitamin2021d` — mind
„Vitamin D …" kezdetűekből. **Egy téma-fókuszú korpuszban a cím első két érdemi
szava maga a téma.** A `<w1><év><w2>` tehát pont ott gyenge megkülönböztető,
ahol a legnagyobb szükség lenne rá — és **egy Corpova-export mindig téma-fókuszú,
mert egyetlen claimre fut**. A 9,3% ezért alsó becslés, nem felső.

Mellékes, de árulkodó eset: `20192019esc` — egy évszámmal kezdődő cím
(„2019 ESC Guidelines…"), ahol a `w1` maga is évszám lett.

> ⚠️ **A corpora_full-ban NINCS `authors` mező** (a `workBrief` nem hordozza —
> `consensus.go:14–26`). Ez a mérés tehát a **cím-alapú** kulcsot méri, vagyis
> pontosan azt a változatot, amit most implementálunk. **Ha a B sáv megjön és a
> `w1` helyére szerzőnév lép, ez az ütközési arány ÚJRAMÉRENDŐ** — a szerzőnév
> erősebb megkülönböztető, tehát a DOI-hash akár feleslegessé is válhat. A
> mérőszkript változtatás nélkül újrafuttatható az új korpuszokon.

---

## 3. A `@misc` ALAPKULCS TERVE — **ÍTÉLET**

Ez váltja le a `corpova_<kulcs>_<év>` mintát (`index.html:1280`), ami az 1.
szakasz szerint mind az 5 analízis-típuson ütközik.

### Formátum

```
corpova_<analízis>_<claimSlug>_<YYYYMMDD>
```

Az `<analízis>` szegmens az export-kulcsból: `consensus_verdict` → `consensus`,
`compare_summary` → `compare`, `controversies_summary` → `controversies`,
`gaps_findings` → `gaps`, `evidence_pyramid` → `evidence`.

### Példa két különböző claimre (ma exportálva)

```
claim: "magnesium lowers blood pressure"
  →  corpova_consensus_magnesium_lowers_blood_20260726

claim: "vitamin D prevents fractures"
  →  corpova_consensus_vitamin_d_prevents_20260726
```

Magyar, ékezetes claimre:

```
claim: "a szívinfarktus kockázata csökken"
  →  corpova_consensus_szivinfarktus_kockazata_20260726
```

### ASCII-normalizálás — az `asciiSlug()` lépései

1. `.normalize('NFD').replace(/[̀-ͯ]/g,'')` — szétbontja a betűt és az
   ékezetet, majd az ékezetet eldobja: `í→i`, `ő→o`, `ü→u`, `á→a`
2. `.toLowerCase()`
3. `.replace(/[^a-z0-9]+/g,' ')` — **minden** más szóközzé: írásjel, kötőjel,
   emoji, és a nem bomló betűk is (`ß`, `ł`, `đ`)
4. szavakra bont, stopszavak ki:
   `a an the of on in for and to is are was were does do can vs versus with`
5. **egész szavakat** vesz, amíg a hossz ≤ 28 karakter — soha nem vág szó közepén
6. üres eredmény → `untitled`

A kimenet garantáltan `[a-z0-9_]`. Ezt őrzi az `S5 keys stay ASCII/BibTeX-safe`
ellenőrzés, ami a javítás előtt és után is zöld kell legyen.

### Ugyanaz a claim kétszer, ugyanazon a napon?

**Igen, ütközik — és ez elfogadható. Döntés: 2026-07-26, Laci.**

Ugyanaz az elemzés ugyanarról a claimről ugyanazon a napon: a Zotero/Mendeley
vonja össze a két bejegyzést, ne vegye fel kétszer. Ez ugyanaz az elv, ami a
`@article` ágon is helyes.

Az egyetlen eset, amit ez elveszít: ugyanaznapi újrafuttatás **más
providerrel/limittel**, ami **más verdiktet** ad — az új bejegyzés csendben
felülírja a régit. Ez felmerült és tudatosan vállalt csere; a `runTag`
(tartalom-ujjlenyomat) ötlete emiatt **elvetve**, a rövidebb, olvashatóbb kulcs
javára.

Ez a döntés kommentként bekerül az `index.html`-be:

```js
// Same claim, same analysis, same day → same key, on purpose: it is the same
// analysis, so Zotero/Mendeley should merge the two entries rather than file
// two copies. The one case this loses is a re-run on the same day with a
// different provider that reaches a different verdict — that entry silently
// replaces the earlier one. Accepted trade-off (2026-07-26).
```

---

## 4. AZ ÜTKÖZÉS-ŐR: (a) vagy (b) — **ÍTÉLET**

### A megvizsgált tervezési hiba

Az eredeti javaslatom a DOI-hasht **csak ütközéskor** tette volna a kulcsra.
Ez kontextus-függő kulcsot ad:

```
ugyanaz a cikk, A fájlban (nincs ütközés)  →  effects2013vitamin
ugyanaz a cikk, B fájlban (van ütközés)    →  effects2013vitamin_h4q
```

Két különböző kulcs ugyanarra a cikkre — **pont a Zotero-összevonás bukik**,
ami az egész tartalom-alapú kulcs értelme volt. A kulcs nem függhet attól, mi
*más* van a fájlban.

### Döntés: **(a) — ha van DOI, a hash MINDIG rákerül**

**Indoklás, a 2. szakasz mért számaival:**

1. **9,3% a mért fájlok-közti ütközés** (23 mű / 246). Ez a „még védhető" 2%-os
   küszöb fölött van.
2. **Az ütközés strukturális, nem véletlen.** A `omega2018*` / `vitamin20**d`
   mintázat azt mutatja, hogy a téma-szavak ismétlődnek. Egy Corpova-export
   mindig egyetlen claimre fut, tehát mindig ilyen. Több adat ezt nem javítja.
3. **98,0% DOI-lefedettség** — a hash gyakorlatilag mindig rendelkezésre áll.
4. **És a döntő érv:** (a) **kontextus-független**. A kulcs csak a cikktől függ,
   soha nem attól, mi más van a fájlban. (b) ezt szerkezetileg nem tudja
   megadni.

Az ár: csúnyább kulcs. Vállalt.

### A végleges `@article` formátum

```
<w1><év><w2>_<hash3>          hash3 = base36( normalizált DOI  ||  normalizált cím )
```

```
"Omega-3 fatty acids and cardiovascular disease"  2018  10.1001/x  →  omega20183_h4q
"Omega-3 supplementation in depression"           2018  10.1002/y  →  omega20183_p7x
```

- **Ugyanaz a cikk → ugyanaz a DOI → ugyanaz a kulcs minden fájlban** → a Zotero
  összevon.
- **Különböző cikk → különböző kulcs** → nincs néma felülírás.

**A DOI nélküli 2% (5 mű a 246-ból):** ne `a`/`b`/`c` pozíció-utótagot kapjanak,
hanem a **normalizált címből** számolt hasht. Így a kulcs **100%-ban
kontextus-független** marad. Az `a`/`b`/`c` csak végső esetre marad: ha két
sornak azonos a normalizált címe ÉS egyiknek sincs DOI-ja — ekkor viszont
amúgy is megkülönböztethetetlen duplikátumok.

Ugyanaz az `uniqueKey(base, used)` helper szolgálja ki a `@misc` és a `@article`
ágat is, fájlonként egy `Set`-tel, hogy a szabály egyetlen helyen legyen.

---

## Amit a 3/3 majd MÉRNI fog

1. A 6 piros ellenőrzés zöldre vált; `EXIT=0`.
2. A 24 régi ellenőrzés PASS marad — nulla regresszió.
3. Új: fájlon belüli egyediség — N bejegyzés → N *különböző* kulcs.
4. Új: cím és/vagy év nélküli sor is kap érvényes, egyedi kulcsot.
5. Az ASCII-őr (7. ellenőrzés) zöld marad a magyar ékezetes claimmel.
6. **Valódi végponti bizonyíték:** két különböző claimre futtatott elemzés
   `.bib` fájljai egymás mellett —
   `cat a.bib b.bib | grep -o '@[a-z]*{[^,]*' | sort | uniq -d` → **üres**.

## Nyitott, a 3/3-on kívül

- **B sáv (monorepó, külön session):** `workBrief` +`first_author` +`venue`
  (`consensus.go:14–26`), a `topByStance` (`:326–330`) és az `allStudyBriefs`
  (`:341–345`) átmásolja. **Első lépés MÉRÉS, nem feltevés:** a mező létezése
  (`scwork.go:28,32`) nem jelenti, hogy az OpenAlex→`scWork` leképezés ki is
  tölti a consensus útvonalon.
- **„All analyzed studies (N)" tábla** a Corpovában: saját `.qfilter` (különben
  nem kap view providert — `index.html:1109`), saját `data-key` a
  `consensus_studies`-tól különböző, `scopeItems`-kompatibilis sorok
  (`.study-card` / `tr` / `li`), alapból csukott `<details>`, angol felirat.

---

## 5. MÉRÉS — a lokális szerver a friss `index.html`-t szolgálja-e?

**Miért kérdés.** Ha a HTML `go:embed`-del be lenne égetve a binárisba, a
lokális `scientific-consensus-web.exe` a **fordításkori** `index.html`-t adná
vissza, és minden végponttól végpontig mért „zöld" hamis lenne.

### A mérés

```bash
# a szerver MÁR FUTOTT (ADDR=127.0.0.1:8899), amikor a jelölő a fájlba került
# index.html 2. sora: <!-- EMBED-PROBE-260726 -->
curl -s -m 10 http://127.0.0.1:8899/ | grep -c 'EMBED-PROBE-260726'
```

### PROBE COUNT, amikor a jelölő BENT volt:

```
1
```

### Kellett-e újraindítás és ismétlés?

**Nem, és nem is volt.** A kérdés akkor merült volna fel, ha `0` jön — az
kétértelmű lett volna (`go:embed` VAGY indításkori egyszeri beolvasás).
`1` jött, és ez **egyértelmű**: a jelölő a **szerver indítása UTÁN** került a
fájlba, mégis megjelent a válaszban. Ez egyszerre zárja ki a `go:embed`-et és az
indításkori beolvasást — a szerver **kérésenként olvas a lemezről**.

Egyezik a kóddal (`main.go:215`, `handleRoot`):

```go
if data, err := os.ReadFile("index.html"); err == nil {
```

*(A jelölő eltávolítása után mért `0` csak a takarítást igazolja — az `1` a
diagnosztikus mérés.)*

### Következtetés — egy mondatban

**A lokális szerver a friss `index.html`-t szolgálja, tehát a végponttól
végpontig bizonyítás (valódi lekérés → valódi BibTeX-letöltés) ÉRVÉNYES, és a
3/3 bizonyítási tervéből semmit nem kell elhagyni** — a
`node export_wysiwyg_test.mjs` emellett marad, nem helyette.

---
---

# 3/3 — A JAVÍTÁS ÉS A BIZONYÍTÁSA

Innentől a javítás **utáni** állapot. A fájl így a teljes ívet mutatja:
**piros → javítás → zöld**.

## 6. MÉRÉS — a 32 ellenőrzés a javítás után

```bash
cd ~/pubvera-corpova
node export_wysiwyg_test.mjs; echo "EXIT=$?"
```

```
PASS  S1 initial qcount  →  " · 10 / 10 rows"
PASS  S1 visible cards after filter  →  4
PASS  S1 qcount after filter  →  " · 4 / 10 rows"
PASS  S1 exportRows == visible  →  4
PASS  S1 CSV data rows  →  4
PASS  S1 XLSX data rows  →  4
PASS  S1 BibTeX entries  →  4
PASS  S1 JSON rows  →  4
PASS  S1 JSON envelope counts  →  [4,10]
PASS  S1 JSON filters  →  ["filter: \"magnesium\""]
PASS  S1 CSV provenance line  →  true
PASS  S1 XLSX caption row  →  true
PASS  S1 XLSX freeze below caption+header  →  2
PASS  S1 BibTeX provenance comment  →  true
PASS  S1 JSON app/query  →  ["Corpova","magnesium lowers blood pressure"]
PASS  S2 export follows sorted DOM order  →  ["case-report","cohort","rct","meta-analysis"]
PASS  S2 sort recorded in filters  →  ["sorted by \"Count\" asc"]
PASS  S2 JSON keeps full response  →  true
PASS  S2 JSON first row is screen-first row  →  "case-report"
PASS  S3 claim A filtered, claim B untouched  →  ["alpha trial 1","gamma trial 5","gamma trial 6","gamma trial 7"]
PASS  S3 qcount A  →  " · 1 / 4 rows"
PASS  S3 qcount B  →  " · 3 / 3 rows"
PASS  S4 empty view alert  →  "Run an analysis first — or clear the filters, nothing is visible."
PASS  S4 no file downloaded  →  5
PASS  S5 consensus_verdict: two claims share no citation key  →  []
PASS  S5 compare_summary: two claims share no citation key  →  []
PASS  S5 controversies_summary: two claims share no citation key  →  []
PASS  S5 gaps_findings: two claims share no citation key  →  []
PASS  S5 evidence_pyramid: two claims share no citation key  →  []
PASS  S5 @article: different papers get different keys  →  []
PASS  S5 claims sharing a truncated slug get different keys  →  []
PASS  S5 keys stay ASCII/BibTeX-safe  →  []

ALL CHECKS PASSED
EXIT=0
```

```
TOTAL CHECKS: 32
FAILING:      0
EXIT:         0
```

**Az 1. szakasz 7 pirosa mind zöld. A 24 régi ellenőrzés változatlanul zöld.**

---

## 7. MÉRÉS — hol változott az `index.html`, és hol NEM

A 24 régi ellenőrzés zöldsége akkor jelent valamit, ha a júliusi WYSIWYG-lánc
tényleg érintetlen. Ezt a **Git saját hunk-fejléceivel** mértem, nem
szövegmintával — egy `grep 'function registerView'` csak a *deklarációt* nézné,
és 0-t adna akkor is, ha a függvény *törzsét* írtam volna át.

```bash
git diff -U0 index.html | grep -E '^@@'
```

A 18 hunk **régi oldali** sortartománya:

```
419  1023  1037  1239-1240  1242  1246  1248  1251  1257
1270  1273  1278  1280  1296  1298  1343  1619  1758
```

Összevetve a védett tartományokkal:

| Védett tartomány (régi sorszám) | Mi van ott | Érintve? |
|---|---|---|
| 1050–1058 | `rowsFromLastData` | **nem** |
| 1065–1077 | `viewProviders` / `registerView` / `exportRows` | **nem** |
| 1082–1100 | `scopeItems` / `updateQcounts` | **nem** |
| 1106–1133 | `bindExportViews` (benne az 1109-es `.qfilter` sor) | **nem** |
| 1137–1148 | `exportHeader` | **nem** |
| 1151–1171 | `toCSV` | **nem** |
| 1183–1236 | `downloadXLSX` | **nem** |

**Mind a 18 hunk kívül esik.** A legközelebbi a régi 1239. sor (`bibKey`), három
sorral a `downloadXLSX` tartománya alatt.

> ⚠️ **Ennek a mérésnek a korlátja.** Ez azt méri, **hol** változott a kód, nem
> azt, hogy **mi** törött el. Egy védett tartományon kívüli változás is ronthat
> közvetve — például egy átnevezett helper, amit a `bindExportViews` hív. A
> „clear" tehát **szükséges, de nem elégséges**. Az elégséges bizonyíték a 24
> régi ellenőrzés zöldsége (6. szakasz). **A kettő EGYÜTT a bizonyíték:** „nem
> nyúltunk hozzá" ÉS „működik".

### Mi változott

```
 index.html | 169 insertions(+), 14 deletions(-)
```

| Függvény | Változás |
|---|---|
| `keyWords`, `asciiSlug`, `hash3`, `normDoi`, `uniqueKey` | **új** helperek |
| `bibKey(r, i)` → `bibKey(r, used)` | sorszám helyett tartalom-alapú kulcs + ujjlenyomat |
| `toBibTeX(rows)` → `toBibTeX(rows, used)` | átveszi a fájl kulcs-névterét |
| `analysisBibTeX(key, rows)` → `(key, rows, used)` | új `@misc` kulcsformátum |
| `downloadBibTeX` | létrehozza a fájlonkénti `used` Set-et |
| `renderAllStudiesSection` | **új** — az „All analyzed studies" szekció |
| `ANALYSIS_SLUG` | **új** konstans |
| `lastData`, `ANALYSIS_LABEL`, `metaRow` bibTitle-regex | +`consensus_all` kulcs |
| CSS `.all-studies-section` | **új**, a `.excluded-section` mintájára |

---

## 8. MÉRÉS — valódi lekérés, valódi BibTeX (`@article` ág)

Élő szerver a 8899-es porton, két különböző claim, a **valódi**
`renderResult()` és `downloadBibTeX()` a friss `index.html`-ből.

```
claim: "omega-3 supplementation reduces cardiovascular risk"
  all_studies from CLI:     37
  rows in lastData:         37
  @article entries in .bib: 37
  citation keys:            37  (distinct: 37)
  sample keys:              20192019esc_5uq, 20192019esc_g90, vitamin2018d_9r6, omega20183_gdp

claim: "vitamin D supplementation prevents bone fractures"
  all_studies from CLI:     40
  rows in lastData:         40
  @article entries in .bib: 40
  citation keys:            40  (distinct: 40)
  sample keys:              use2007calcium_lye, calcium2006plus_9ug, fracture2005prevention_w3h, vitamin1992d_yq8
```

**Az eredeti panasz — „nulla DOI jut ki" — megszűnt: 37, illetve 40 mű megy ki
DOI-val.**

A minta-kulcsok között ott a bizonyíték, hogy az **(a) döntés dolgozik**:
`20192019esc_5uq` és `20192019esc_g90` — két különböző mű, azonos alapkulcs,
az ujjlenyomat választja szét őket.

### Független ellenőrzés, a lemezre írt fájlokból

```bash
cat proof-a.bib proof-b.bib > merged.bib
grep -c '^@article{' merged.bib                          # entries
grep -o '^@article{[^,]*' merged.bib | sort -u | wc -l   # distinct keys
grep -o '^@article{[^,]*' merged.bib | sort | uniq -d | wc -l  # duplicates
```

```
proof-a.bib: 37 entries, 37 distinct keys
proof-b.bib: 40 entries, 40 distinct keys
--- merged (what Zotero sees) ---
entries:        77
distinct keys:  77
duplicate keys: 0
non-ASCII keys: 0
```

**77 bejegyzés, 77 különböző kulcs, nulla ütközés.** A két elemzés `.bib`-je
egymás mellé importálva nem írja felül egymást.

---

## 9. MÉRÉS — valódi lekérés, `@misc` ág (az eredeti hiba helye)

Ez az a kulcs, amiből a júl. 24-i 9 exportban **9-szer ugyanaz** lett.
Négy claim, köztük két nagyon hasonló és egy ékezetes:

```
SCRIPT EXIT=0     (szűrő nélkül futtatva, teljes kimenet)
```

| Claim | Generált `@misc` kulcs |
|---|---|
| `omega-3 supplementation reduces cardiovascular risk` | `corpova_consensus_omega_3_supplementation_g04_20260726` |
| `vitamin D supplementation prevents bone fractures` | `corpova_consensus_vitamin_d_supplementation_ex9_20260726` |
| `vitamin D supplementation prevents bone fractures in elderly women` | `corpova_consensus_vitamin_d_supplementation_nss_20260726` |
| `a szívinfarktus kockázata csökken` | `corpova_consensus_szivinfarktus_kockazata_7sl_20260726` |

**A 2. és 3. sor a lényeg:** a két claim slugja **azonos**
(`vitamin_d_supplementation`, a 28 karakteres vágás miatt), a kulcsuk mégis
**különböző** — `ex9` vs `nss` —, mert az ujjlenyomat a **teljes** normalizált
claimből készül. Ez a rés, amit a 8. ellenőrzés őriz.

**A 4. sor** az ékezet-kezelés valódi adaton: `szívinfarktus kockázata` →
`szivinfarktus_kockazata`, tiszta ASCII.

```bash
grep -o '^@misc{[^,]*' merged-misc.bib | sort | uniq -d | wc -l
grep -o '^@misc{[^,]*' merged-misc.bib | LC_ALL=C grep -c '[^ -~]'
```

```
entries: 4   distinct: 4   duplicates: 0
non-ASCII @misc keys: 0
```

---

## 10. Takarítás

- **A 8899-es szerver leállítva:** `taskkill //F //IM scientific-consensus-web.exe`
- **Az `EMBED-PROBE-260726` jelölő eltávolítva** az `index.html`-ből (5. szakasz).
- A mérőszkriptek (`bibkey_collision_measure.mjs`, `e2e_bibtex_proof.mjs`,
  `e2e_misc_proof.mjs`) és a `.bib` bizonyítékok a scratchpadben maradtak,
  szándékosan nincsenek a repóban.

## 11. Mi NEM készült el (nyitott)

- **B sáv** (monorepó, külön session): `workBrief` +`first_author` +`venue`.
  Első lépése **mérés**: kitölti-e egyáltalán az OpenAlex→`scWork` leképezés
  ezeket a mezőket a consensus útvonalon. Amikor megvan, a `bibKey`-be a szerző
  vezetékneve kerül a cím első szava helyett, és **a 2. szakasz 9,3%-os
  ütközési aránya újramérendő**.
- Az „All analyzed studies" szekció **csak a `consensus` végponton** van meg. A
  `compare` (`claim_a.all_studies` / `claim_b.all_studies`) ugyanígy megkapná;
  a `controversies` CLI-kimenetében nincs `all_studies`, oda nem való.
