# ÁTADÁS — Redis cache + Corpova, 2026-07-30

Ez a nap infrastruktúra- és cache-munka volt. A reggeli stance-javítás a
monorepóban zárult, a nap többi része a Pubvera első cache-rétegéről szólt,
a telepítéstől a produkciós mérésig.

Előzmény: `23-ATADAS-2026-07-30.md` (a reggeli, stance-gate-es rész).

---

## 1. GIT-ÁLLAPOT (mérve, nem emlékezetből)

### monorepo — `printing-press-library`

Ág: `fix/benefit-stance-gates`, HEAD **`64384ef7f`**, pusholva a `myfork`-ra.
A `26fe022c3` (a `scientific-consensus-pr` feje) fölött **két** commit:

| commit | mit csinál |
|---|---|
| `8bb22658f` | fix: benefit-claim stance kapuzása negációra és outcome-hatókörre |
| `64384ef7f` | chore: a 0 bájtos `stance_embedding_backup.go` törlése a repó gyökeréből |

A munkafa tiszta (csak `?? .claude/`). **PR-t NE nyiss** — az indoklás a 4.1-ben.

### Corpova — `pubvera-corpova`

Ág: `main`, HEAD **`5bf97c9`**, minden pusholva (`## main...origin/main`,
ahead nélkül). A mai hat commit, régiről újra:

| commit | mit csinál |
|---|---|
| `a83294a` | feat: Redis cache-réteg a CLI-payloadokra, szigorú soft dependencyként |
| `d4ea24d` | feat: a cache-kulcs a CLI-bináris saját hashére is épül (additív) |
| `1e4b22c` | fix: a startup log `corpova`-t ír, nem a régi repónevet |
| `31a184a` | docs: DEPLOY.md „Redis cache" szakasz (106 sor, tiszta hozzáadás) |
| `67949b0` | chore: `.gitattributes` — `.go` LF-re rögzítve, `bin/` explicit védve |
| `5bf97c9` | style: a `providers.go` hiányzó záró újsora |
| `c7fba01` | feat: single-flight a CLI-lábra — egy futás hideg kulcsonként |
| `eb26e16` | docs: skálázási mérés N=100/1000, és amit NEM old meg |

Zöld-állapot a nap végén: `go build` és `go vet` **nulla sort** írt ki,
`go test ./... -count=1` → **41 PASS / 0 FAIL / 0 SKIP** (30 → 41 a
single-flight tesztekkel), `gofmt -l` mind a hét gyökér-fájlra **üres**.

### szerver — Hetzner CX23, `178.105.220.79`

A Corpova a `31a184a` állapotot futtatja (a `67949b0` és `5bf97c9` nem
változtat kódot, deploy nem kellett hozzájuk). Ellenőrzött startup-log:

```
cache: CLI binary /app/bin/scientific-consensus-pp-cli hashed in 96ms
       — key prefix was sc:v1: and is now sc:v1:b659fff65cb9:
       (sample key 83 chars, ceiling 200)
corpova listening on 0.0.0.0:8090
cache: redis redis:6379 ready (engine=v1, cli=b659fff65cb9, ttl=168h0m0s)
```

---

## 2. AZ INFRASTRUKTÚRA, AHOGY MOST ÁLL

### `pubvera-shared` — új, külső Docker-hálózat

**Ez volt a nap legfontosabb lelete:** a nyolc app addig **nyolc külön,
izolált hálózaton** futott (`pubvera-corpova_default`, `pubvera-devicera_default`
és így tovább), saját compose-fájllal. Egy Docker-hálózat zárt: ha csak
feltettünk volna egy Redis konténert, **hét app egyáltalán nem látta volna** —
és ez nem a telepítéskor derült volna ki, hanem hetekkel később, a második app
cache-kódjánál, `name resolution failed`-ként.

Megoldás: külső (`external`) hálózat, amire több compose is rácsatlakozhat.
Egy konténer lehet több hálózaton egyszerre, tehát az appok megtartják a
sajátjukat.

```
docker network create pubvera-shared
```

**Csak a Corpova van rákötve.** A maradék hét akkor csatlakozik, amikor tényleg
kap cache- vagy session-kódot — az két sor a compose-ában plusz a `.env`, és
ez akkor is két sor, ha három hónap múlva csinálod. A drága, nehezen
változtatható döntés (a hálózati architektúra) viszont már megszületett.

### Redis

`/opt/pubvera/redis/docker-compose.yml`, image `redis:7.4-alpine`.

| tulajdonság | érték | miért |
|---|---|---|
| `maxmemory` | 512 MB | plafon, nem előfoglalás — a Redis annyit használ, amennyi benne van |
| policy | `allkeys-lru` | tele instance a leghidegebb kulcsokat szórja ki, nem hibázik |
| persistence | **ki** (`save ""`, `appendonly no`) | minden bejegyzés újraszámolható; a mentés `fork`-ot csinál, a copy-on-write megduplázhatná a memóriát |
| port | **nem publikált** | csak a `pubvera-shared`-ről érhető el; a jelszó a második vonal |
| `mem_limit` | 768 MB | a jemalloc fragmentáció miatt az RSS nagyobb, mint az adat |
| Watchtower | **kizárva** (`com.centurylinklabs.watchtower.enable=false`) | egy váratlan image-frissítés nem üríti ki a cache-t futó app alatt |

Méretezés indoka, mérve: `free -m` → `available: 2969` MB, a 11 konténer
együtt 236 MB. Az 512-es plafon fragmentációval együtt ~600–750 MB RSS,
tehát ~2,2 GB tartalék marad.

**A 512 azért nem 300**, mert a `maxmemory` plafon: a magasabb szám addig nem
kerül semmibe, amíg meg nem töltöd — így viszont nem kell fél év múlva
visszanyúlni. Kapacitás: ~30 KB/válasz mellett 512 MB ≈ **17 000 cache-elt
keresés**, 7 napos TTL-nél napi ~2400 egyedi lekérdezés.

### Jelszó — KÉT helyen

`REDIS_PASSWORD` a szerveren:

- `/opt/pubvera/redis/.env` — amivel a Redis indul
- `/opt/pubvera/pubvera-corpova/.env` — amivel az app hitelesít

**Csere esetén mindkettőt**, plusz minden további app `.env`-jét. Ez a
legvalószínűbb jövőbeli `auth failed` ok, és **csendben hibázik**: a bukott
`AUTH` cache-missre degradál, tehát az eredmény továbbra is helyes, csak
lassú, és a log telik cache-failure sorokkal. A tünet a kódra mutat, az ok
egy fájlban van, amit a kód soha nem is említ. Benne van a `DEPLOY.md`-ben.

---

## 3. A CACHE-RÉTEG

### Mit cache-elünk, és mit nem

**Cache-eljük:** a gyerek CLI teljes JSON-kimenetét (OpenAlex-keresés +
heurisztikus pontozás), 7 napos TTL-lel. A gyerek CLI **mindig kulcs nélküli**
(`buildChildEnv` minden provider-kulcsot kiszed), tehát a payload minden
hívónak azonos, és megosztható.

**Nem cache-eljük:** a BYOK-kulcsokat és a belőlük származó LLM-szintézist.
Ez nem teljesítmény-kompromisszum, hanem **bérlők közti szivárgás** lett volna:
ha A felhasználó a saját kulcsával, a saját pénzén állított elő egy szintézist,
és eltárolnánk, B ugyanarra az állításra A fizetett kimenetét kapná. Ráadásul
az LLM nem determinisztikus, tehát ott nem függvényértéket tárolnánk, hanem
egy mintát — az invalidálás nem lenne véges feladat.

### A kulcs

```
sc:<engine>:<clihash>:<paramhash>
```

Két **független** invalidálási fogantyú, mert a verdikt kétféleképpen mozdulhat:

- **`clihash` — automatikus.** A CLI-bináris sha256-ának első 12 hex jegye.
  A bináris cseréje magától újrakulcsolja az egész cache-t, emberi lépés nélkül.
- **`engine` — kézi** (`cacheEngineVersion`). Akkor kell emelni, ha a **web**-layer
  pontozási logikája változik (divergencia-szabályok, tömörítés, szintézis-prompt) —
  az nem érinti a CLI-binárist, tehát semmilyen hash nem mozdul magától.

Miért kellett az automatikus: a `8bb22658f` CLI-commit 14 mű címkéjét és öt
korpusz pontszámát átírta. Kód-verzió nélküli kulcsnál azok a verdiktek **hét
napig** kimentek volna, és **semmilyen deploy nem javította volna**, mert az
image változik, a cache nem.

Fallback: ha a bináris nem olvasható, a szegmens a `nohash` literál lesz
(szándékosan **nem** valid hex, hogy ne lehessen összekeverni valódi hash-sel),
az indulás nem blokkolódik, és a kézi verzió továbbra is verziózza a kulcsot.
Ára szűk és a kódban ki van mondva: az így írt bejegyzéseket bináris-csere nem
érvényteleníti.

Kulcshossz mérve: régi alak 70, új alak **83** karakter, plafon 200. A hossz
konstrukcióból fix — a claim-ek hashelve vannak, sosem összefűzve —, és a teszt
azt állítja, hogy egy rövid claim, egy 2,5 KB-os claim és 50 ilyen claim
**ugyanazt a 83 karaktert** adja.

### Soft dependency — ez a réteg teherhordó tulajdonsága

- nil kliens → **minden** cache-hívás no-op; nincs `REDIS_ADDR` vagy
  `CACHE_DISABLED=1` → pontosan a cache előtti viselkedés
- **az indulás sosem tárcsáz**; az elérhetőség goroutine-ból mérődik, csak logol
- 2 s dial/read/write határidő, **plusz circuit breaker** (3 egymást követő
  hiba → 30 s kikapcsolás), tehát egy halott vagy vánszorgó Redis
  kérésenként ~0-ba kerül, nem 2 s-ba
- `Get` és `Set` panicot is elkap; a `Set` goroutine-nak saját `recover`-je van
- **a miss-út (`runCLIRaw`) egyáltalán nem hív cache-t** — ez szerkezeti
  garancia: ha a cache-kód nincs jelen azon az úton, Redis-hiba fogalmilag sem
  válhat HTTP-hibává
- nem-valid JSON a cache-ben → újraszámol, sosem szolgálja ki
- **bukott CLI-futás sosem kerül a cache-be** (egy pillanatnyi upstream-hiba
  különben egy hétre beégne)

### Kliens

Saját, kézzel írt RESP2 (`GET`, `SET EX`, `PING`, `AUTH`), nem `go-redis`.
Ok: a web-modul stdlib-only, nincs `go.sum`, és a Dockerfile build-stage-e
sosem futtat `go mod download`-ot. Négy parancsért nem éri meg új függőséget
és build-változtatást bevinni.

---

## 4. MÉRÉSEK

### Lokális (stub-Redis, heurisztikus ág, LLM nélkül)

| eset | idő | bizonyíték |
|---|---|---|
| hideg, semmi nincs cache-elve | 2,05 s | egyszeri futás |
| A: Redis-hit, CLI lemez-cache **törölve** | 3,3 ms | 4 futás mediánja, CLI-számláló 1-en maradt |
| B: kontroll, csak CLI lemez-cache, Redis ki | 520 ms | 4 futás mediánja, stub-Redis log üres |

A Redis saját járuléka **A és B különbsége**: ~517 ms ismételt lekérdezésenként,
azon felül, amit a CLI saját cache-e már ad.

**A szétválasztás azért kellett**, mert az első futás *két* cache-t töltött fel:
a Redist és a CLI saját 5 perces lemez-cache-ét. Enélkül a „2. futás gyors"
bármelyiknek betudható lett volna. A `clicount` wrapper (a CLI-hívásokat számláló
és továbbadó program) tette a „nem futott a CLI"-t méréssé, nem feltevéssé.

### Produkciós (konténer-hálózaton át, éles Redis)

| mérés | érték |
|---|---|
| hideg (első kérés kulcsforma-váltás után) | **2,25 s** |
| hit | **0,018–0,019 s** (~125×) |
| Redis-oldali számlálók | `keyspace_hits`, `keyspace_misses` egyeztek a futásokkal |

**A legfontosabb produkciós bizonyíték:** deploy után, friss konténerben az
**első** kérés is azonnal hit volt (0,019 s), nem 2,2 s — mert a bináris nem
változott, a kulcs-hash ugyanaz, és a Redis-bejegyzés túlélte a teljes
konténer-cserét. Ezt a CLI saját cache-e nem tudta volna: az 5 perces és a
konténer fájlrendszerén él, tehát minden Watchtower-pull után nulláról indul.

### A mérés korlátja — ez a jelentésben is így áll

A számok a **heurisztikus** ágra vonatkoznak, LLM-kulcs nélkül. A BYOK-út
kérésenként fizeti a szintézist (~120 s request-büdzsé), tehát a Redis az
OpenAlex-lekérés + heurisztikus pontozás lábát spórolja, nem a teljes kérést.

---

## 4B. SINGLE-FLIGHT — a stampede lezárva

### A hiány, amit megszüntet

A `a83294a` commit-üzenete „Known gap"-ként rögzítette: ha N párhuzamos kérés
érkezik ugyanarra a **hideg** kulcsra, mind az N elindítja a saját CLI-futását.
Produkcióban egy hideg kérés 2,25 s heurisztikus ágon és ~120 s LLM-mel, tehát
N × ennyi CPU és OpenAlex-hívás **egyetlen** két magos szerveren.

### A baseline — a stampede mért tény volt, nem elmélet

| metrika | baseline (javítás előtt) | utána |
|---|---:|---:|
| CLI-futás | **10** | **1** |
| Redis SET | 10 | 1 |
| Redis GET | 10 | 10 |
| különböző válasz-törzs | **10** | **1** |
| wall | 2,412 s | 2,048 s |

A **különböző válasz-törzs** sora a döntő, és ez a mérés legokosabb eleme: a
stub CLI minden futásba **egyedi `run_id`-t** tesz. Előtte mind a 10 törzs
különbözött (mindenki a saját futását kapta), utána mind a 10 bájtazonos.
Így a „egy futás" nem következtetés, hanem tény — és cáfolható lett volna.

### A megvalósítás

`singleflight.go` (~190 sor), a **cache-kulcsra** kulcsolva — az már kódolja az
endpointot, a limitet és a normalizált claimeket, tehát „azonos kulcs"
konstrukció szerint „azonos argv". Külső függőség nélkül
(`map[string]*flightCall` + `sync.Mutex` + `done` csatorna), mert az
`x/sync/singleflight` ennek a stdlib-only modulnak nem elérhető.

Három tervezési döntés, ami számít:

**A cache-írás beköltözött a futásba.** Nem a hívónál van, hanem a megosztott
futáson belül — ezért lett N hívóra **1 SET**, nem N. Ha kívül maradt volna,
tíz hívó tízszer írta volna ugyanazt a bájtsort a Redisbe.

**A BYOK-szintézis NEM közös.** Csak a kulcs nélküli CLI-láb osztott; az
LLM-hívás kérésenként fut, különben A fizetett kimenetét kapná B. Ezt teszt
bizonyítja: két párhuzamos hívó külön kulccsal → 1 CLI-futás, **2** LLM-hívás.

**Nincs végtelen várakozás.** Minden várakozó a **saját** ctx-ére selectel; a
futásnak saját határideje van (`WithoutCancel` + 120 s), így a vezető távozása
nem ragasztja be a többit; a hiba ugyanolyan gyorsan terjed, mint a siker; a
panic hibává alakul; és az utolsó várakozó távozásakor a futás megszakad.

A hibák megosztása is végiggondolt: a `cliError` típus viszi a HTTP-státuszt is,
és biztonságos megosztani, mert a szövege olyan gyerekprocesszből jön, ami
**soha senki kulcsát nem kapta meg** — hívó-független. Emellett minden hívó
**külön redaktálja a saját kulcsát** belőle, kérésenként.

### Skálázási mérés — `docs/SCALING-2026-07-30-single-flight.md`

**A) Azonos kulcs**

| N | CLI-futás | SET | különböző törzs | latency min/med/p95/max | goroutine-csúcs |
|---:|---:|---:|---:|---|---:|
| 100 | **1** | 1 | 1 | 2,052 / 2,056 / 2,057 / 2,057 s | 206 |
| 1000 (hullámokban) | **1** | 1 | 1 | 9,778 / 10,155 / 10,529 / 10,531 s | 2006 |

N=100-nál a leglassabb **5 ms-mal** van a leggyorsabb mögött — nincs sorosítás
az ébresztésben (egyetlen `close(done)`, mindenki már parkol rajta). N=1000-nél
a szórást teljesen megmagyarázza a szándékos hullámoztatás: `latency + dispatch`
≈ 10,53 s mind az 1000-re, tehát abszolút időben ~23 ms-on belül végeztek.
**999 join + 1 vezető = 1000, runs = 1.**

A `joins + 1 = beérkezett kérés` képlet **minden** burst-nél kijött:

| burst | HTTP 200 | `joined` sor | joins+1 | CLI-futás |
|---|---:|---:|---:|---:|
| 1000 (a) | 445 | 444 | **445** | **1** |
| 1000 (b) | 499 | 498 | **499** | **1** |
| 700 | 377 | 376 | **377** | **1** |
| 500 | 352 | 351 | **352** | **1** |
| 300 | 274 | 273 | **274** | **1** |
| 200 | 200 | 199 | **200** | **1** |
| 100 | 100 | 99 | **100** | **1** |

Egyetlen hívó sem veszett el a várakozásban, és a futásszám **mindig 1**.

**B) Különböző kulcsok, N=100 — EZ INDOKOLJA A KÖVETKEZŐ LÉPÉST**

| metrika | érték |
|---|---|
| CLI-futás | **100** (a single-flight itt szándékosan nem segít) |
| Redis SET | 100 |
| hiba | 0 |
| wall | **3,195 s** |
| latency min / med / p95 / max | **2,633 / 3,142 / 3,187 / 3,195 s** |
| goroutine-csúcs | 602 |

A stub 2,0 s-ot alszik, tehát minden 2,0 s fölötti idő **tiszta versengés**:
a medián kérés **+1,14 s**-ot, a leglassabb **+1,20 s**-ot fizetett. És ez
**padló, nem becslés**: a mérés a CX23-nál **több magos** gépen futott, egy
olyan stubbal, ami **semmi valódi munkát nem végez**. Produkcióban ugyanez a
100 kérés 100 valódi CLI-futás lenne, fejenként 2,25 s CPU-val és
OpenAlex-forgalommal — LLM nélkül.

**C) Vegyes, 50 azonos + 50 különböző**

**51 futás, 51 SET.** Az 50 közös kulcsú kérés egyetlen bájtazonos törzset
kapott, az 50 különböző pedig sem egymásra, sem a közösre nem várt. Ez
bizonyítja, hogy az izoláció **kulcsonkénti, és csak kulcsonkénti**.

### Produkciós bizonyíték

Három párhuzamos kérés ugyanarra az új állításra, éles szerveren:

```
17:26:32 cache: miss ...546b4ced (misses=1)
17:26:32 cache: miss ...546b4ced (misses=2)
17:26:32 single-flight: ...546b4ced joined an in-flight CLI run (waiters=2)
17:26:32 cache: miss ...546b4ced (misses=3)
17:26:32 single-flight: ...546b4ced joined an in-flight CLI run (waiters=3)
```

Három miss, **egy** CLI-futás, két csatlakozó. A lokálisan mért viselkedés a
produkcióban is ugyanaz.

### ⚠️ Amit a mérés MEGDÖNTÖTT — a listen-backlog

N=1000-nél egyszerre elengedve **555 kérés hibázott**. Az első hipotézisem
(`TIME_WAIT` / efemer-port kimerülés) **téves volt**, és a mérés cáfolta:

- a hibakód `WSAECONNREFUSED` (`connectex: ... actively refused`), **nem**
  `WSAEADDRINUSE` — az utóbbi lenne a port-kimerülés jele
- a `netstat` a burst-ök után **1 db** `TIME_WAIT` socketet mutatott a porton
- az **N=200 több 1000-es burst UTÁN is 0 hibával** futott, ami port-kimerülésnél
  lehetetlen lenne
- az elutasítottak **8-38 ms** alatt buknak (RST már a `connect`-nél), a beérők
  a teljes ~2,06 s-ot viszik

A valódi ok a loopback listener **accept-sorának** túlcsordulása: az OS utasítja
el a kapcsolatot, **mielőtt** a Go szerver elfogadná. Mért küszöb ezen a gépen:
200-ig tiszta, 300-nál 26, 500-nál 148, 700-nál 323, 1000-nél 501 elutasítás.

**Ez ennek a Windows-gépnek a socket-sora, NEM a szerver logikája, és nem visz
át a Linux CX23-ra.** A megoldás nem keep-alive volt (HTTP/1.1-nél N egyidejű
kérés N socketet kíván), hanem **hullámokban** elengedni a kapcsolatokat
(150-es hullám, 120 ms szünet) + a futásablak megnyújtása, hogy mind az 1000
ugyanabba a futásba essen.

### A mérés bevallott korlátai

**A loadgen és a szerver ugyanazon a gépen fut.** Ezért a mérőeszköz két külön
számot ad: `dispatch` (kliensoldali szórás a barriertől a kérés kiadásáig) és
`latency`. A staggerelt futáson kívül a dispatch végig **0-2 ms** volt, tehát a
latenciák a szerverről szólnak — enélkül nem lehetne szétválasztani, ki lassult.

**A harness két processzt indít futásonként** (számláló wrapper + stub CLI), a
produkció egyet. A B-teszt processz-létrehozási költsége tehát nem hasonlítható
közvetlenül — de a valódi CLI CPU- és OpenAlex-terhelése, amit a stub egyáltalán
nem végez, ennél sokkal többel alulbecsüli a produkciós hatást.

**A `go test -race` nem futtatható** ezen a gépen: nincs C-fordító, a cgo
`gcc not found`-ot jelent. A konkurrencia-állítások így determinisztikus
teszteken, a csoport-suite 20-szoros ismétlésén és a zárolás átnézésén állnak,
**nem** a race detectoron. Ez nyitott tétel — lásd 5.0.

### Az ideiglenes műszerezés, és a kivétele

A goroutine-csúcsokhoz egy `/debug/goroutines` végpont + 2 ms-os, **folyamaton
belüli** mintavevő került a `main.go`-ba (kívülről pollozás helyett, hogy a
mérés ne adja hozzá azt a terhelést, amit mér). **Kivéve**: a `main.go` a
`c7fba01` tartalmán áll, a `git diff` üres, és a fájlban 0 előfordulása van a
`debug/goroutines` / `sampleGoroutines` / `goroutinePeak` / `sync/atomic`
mintáknak. A harness (számláló wrapper, stub CLI, stub-Redis, loadgen) a repón
kívül maradt, sosem lett commitolva.

---

## 5. NYITOTT TÉTELEK

### 5.0 `go test -race` a CI-be — a legsürgősebb

A `singleflight.go` a legkonkurrensebb kód az egész appban (`sync.Mutex`, `map`,
goroutine-ok, megosztott eredmény), és **race detector nélkül** ment ki. A
Windows-gépen nem futtatható (nincs C-fordító), a GitHub Actions **Linux**
futtatóján viszont alapból megy. Egy sor a workflow-ba, és minden push-nál
lefut — amúgy is hasznos, mert jelenleg a tesztek csak akkor futnak, amikor
kézzel elindítod őket.

### 5.1 Egyidejűségi korlát (semaphore) — a mérés indokolja

A stampede **lezárva** (`c7fba01`), de az csak az **azonos** kulcsra jövő
terhelést oldja meg. A **különböző** kulcsokra semmi védelem nincs:
100 különböző állítás = 100 egyidejű CLI-futás, korlátozás nélkül.

A szám, amire hivatkozni kell: **B-mérés, medián +1,14 s tiszta versengésből**,
több magos gépen, nem dolgozó stubbal — tehát padló. A CX23 két magján, valódi
CLI-vel jóval rosszabb; a mai kapacitásmérés szerint **~16 egyidejű kérés
telíti a gépet**.

Javasolt korlát: **4-6, NEM 16.** A 16 a *telítés* pontja, nem a *biztonságos*
pont, és a másik hét app is osztozik ugyanazon a két magon. Túllépésnél a helyes
válasz **503 + `Retry-After`**, nem korlátlan sorbaállás — egy gyors „most nem,
gyere 30 s múlva" jobb, mint egy lassú összeomlás.

A `clicount` wrapper a méréshez a Claude Code scratchpadjében volt, ami a
session végével eltűnik — **újra kell építeni**.

### 5.2 Motor-vonal (monorepo, scientific-consensus)

Mindhárom Claude Code / **Opus**, mérés-először, bukó teszttel indítva,
és **friss ágon a `26fe022c3`-ról** — lásd 5.3.

- **M3 — a legnagyobb hozadék.** A hibaosztály **58 %-a érintetlen**: a 239
  szándék-ige előfordulásból az `increas*` csak 100; a `prevent*` (51),
  `reduc*` (25), `improv*` (20), `protect*` (20), `enhanc*` (13), `lower*` (8),
  `alleviat*` (2) nincs kapuzva. Kérdés: kiterjeszthető-e az outcome-hatókör az
  összes szándék-igére a `meditation` kontroll eltörése nélkül.
  **Az N9x (tompa kizárás) NEM megoldás.**
- **M2 — a legszűkebb.** A `harmCues` nem ismeri az `is a risk factor for`
  alakot; `increas\w+ (the )?(risk|…)`-ot vár szomszédosan. Ez az a tétel, ami a
  tegnapi (d) kritériumot szerkezetileg teljesíthetetlenné tette — nem csak egy
  hibát javít, hanem feloldja a H2-es zsákutcát is.
- **M4 — boilerplate-törékenység.** A relevancia-kapu a **teljes** absztrakton
  fut, nem a klipelten. A `lexical_gate_variants_test.go` készen áll, csak egy új
  variáns kell bele; mérés a 13 korpuszon.
- **M1 marad nyitva, és tudjuk, miért.** A három Hemilä `cd000980` review
  támogató cue-ja `prevent\w+` a **címben** („for **preventing** … the common
  cold"), és a claim outcome-oldala ott van a hatókörében → a hatókör-kapu nem
  tud tüzelni. Ehhez **nyelvtani mód** kell (*„for preventing X"* = szándék vs
  *„prevented X"* = lelet). Önállóan mérendő.

### 5.3 PR-stratégia — fontos

A `fix/benefit-stance-gates` ág a `26fe022c3`-ról indul, ami a
`scientific-consensus-pr` feje. Egy `main` felé menő PR tehát **az összes eddigi
commitot vinné, köztük magát a CLI-t** (a `main`-en nincs
`library/other/scientific-consensus/`) → *„PR over auto-review size cap"*,
pont az, ami a **#1309-et megfogta** (a Greptile háromszor nem futott le).

**Ezért:** PR-t csak azután, hogy a #1309 bemegy. Akkor ez a diff öt fájl lesz —
pont akkora, amit a Greptile átnéz. És minden **további** motor-javítás **külön
ágra** menjen a `26fe022c3`-ról, ne ennek a tetejére, különben újra a size cap
felé csúszunk.

A #1309-re a komment **már írható**: a `16-pr-description.md` kész, és most már
a stance-polaritás javítása is beleírható, méréssel.

### 5.4 Kisebbek

- **`cacheEngineVersion` a `nohash` állapotban**: ha a bináris olvashatatlan,
  az akkor írt bejegyzéseket bináris-csere nem érvényteleníti. Szűk, dokumentált.
- **`20-HANDOFF-2026-07-29.md`** 9. szakasza ma frissítve (416 sor); a `.bak`
  törölhető.
- **A hét másik app** cache-elése: **ne másoljuk vakon.** A Recallis / Trialvera
  / Devicera payloadja tiszta upstream adat, nincs benne saját pontozás, tehát
  ott motor-verzió sem kell — de előbb meg kell mérni, van-e ismétlődő
  lekérdezés. Ahhoz forgalom kell, tehát a monetizáció után.
- **Következő termék-lépések:** egységes SVG-logó a 8 app fejlécében →
  Google OAuth + Lemon Squeezy (a Redis-session most már megvan alattuk).

---

## 6. MÓDSZERTANI TANULSÁGOK (a 8-as sorozat folytatása)

### 8.10 A `gofmt` a LEMEZT olvassa, az indexet sosem

A `gofmt -l` hónapokig listázta a `main.go`-t, miközben a **tárolt** tartalom
hibátlan volt. Bizonyíték: `gofmt -d main.go` → `@@ -1,578 +1,578 @@`, és
`cat -A` alatt **minden** törölt sor `^M$`-re végződik, a hozzáadottak nem.
Döntő teszt: CR-mentesített másolat (22197 bájt, 0 CR) → `gofmt -l` néma.

A `git ls-files --eol` `w/` oszlopa a **worktree**, az `i/` az **index** —
ezek eltérhetnek, és a `gofmt` csak a worktree-t látja. Ma emiatt tévedtem:
`w/lf`-et láttam a `providers.go`-n, és valódi formázási hibára következtettem
a `main.go`-nál is.

Megoldás: `.gitattributes` → `*.go text eol=lf`. A `git add --renormalize .`
**üresen** tért vissza, és ez nem hiba, hanem a diagnózis: mind az 5 blob már
LF volt az indexben, az eltérés kizárólag a kicsekkolási konverzióban
(`core.autocrlf=true`) keletkezett.

### 8.11 A `bin/**` védelme nem elhagyható

A CLI-binárisnak nincs kiterjesztése, tehát ma **csak tartalom-szimatolás**
sorolja binárisnak. Egy jövőbeli `* text=auto` sor elvinné: az ELF sérülne, és
— csendben — átírná a hashét, amin a **produkciós cache-kulcs** áll. Ezért van
explicit `bin/** binary` sor (ami `-text -diff -merge` egyszerre).

Bizonyítás minden lépésnél: `sha256sum bin/… | cut -c1-12` → `b659fff65cb9`
előtte és utána, és a blob OID (`1b33e0c6…`) a HEAD-ben és az indexben egyaránt.
A `TestRealCLIBinariesHashDistinctly` teszt független megerősítés.

### 8.12 A `cd` megmarad egy összefűzött parancsban

`cd "$S/stubredis" && go build … ; go build -o "$S/server-cached.exe" .` —
a **második** build is a stub könyvtárában futott, tehát a mérés rossz
binárison ment volna. Javítás: `go build -C <dir>` (Go 1.20+), ami nem mozdítja
el a shell munkakönyvtárát.

Ugyanez a hibaosztály vitt a nem létező `scientific-consensus-web` könyvtárba a
session elején — a helyes repó `C:\Users\LACI\pubvera-corpova`.

### 8.13 A `head` kilépési kódja elfedi a parancsét

`go build ./... | head -20 && echo "BUILD_EXIT=$?"` — a `$?` a **`head`** kódja,
ami szinte mindig 0. Záró ellenőrzésnél **csővezeték nélkül**, és a bizonyíték
a **nulla kimeneti sor**, nem az exit kód. Kivétel a `go test`: az sikernél is
kiír `ok` sort, ott a `FAIL` hiánya + `exit=0` a helyes kritérium.

### 8.14 Heredoc-beillesztés szétesik a terminálban

Ma **négyszer** tört szét egy hosszú `<<'EOF'` blokk (a `20-HANDOFF`
szerkesztésénél, a Redis-compose-nál kétszer, és a `git commit -F -`-nél, ami
emiatt csendben nem hozott létre commitot). Megoldások, sorrendben:
`base64 -w0` egysoros feltöltés `md5sum`-ellenőrzéssel; commit-üzenet **fájlból**
(`git commit -F <fájl>`), soha `-F -`-fel.

### 8.15 Darabszámot mérni kell, nem összeadni

„25 teszt zöld (18 + 5)" — a 18+5 az 23, a 25 pedig a *változtatás előtti*
összes volt (18 cache + 7 providers). A helyes forma:
`grep -c '^--- PASS'` és `grep -c '^--- FAIL'` egy elmentett `-v` futásból.
Végleges: 30 top-level, 30 PASS, 0 FAIL, 0 SKIP, 0 alteszt.

### 8.17 Lokális terheléses mérés Windowson: a listen-backlog a korlát

~200 egyidejű kapcsolat fölött a loopback listener **accept-sora** csordul túl,
és az OS `WSAECONNREFUSED`-del utasítja el a kapcsolatot, **mielőtt** a Go
szerver elfogadná. Mért küszöb: 100→0 hiba, 200→0, 300→26, 500→148, 700→323,
1000→501. **Nem** `TIME_WAIT`/port-kimerülés (az `WSAEADDRINUSE` lenne, a
`netstat` 1 db `TIME_WAIT`-et mutatott, és az N=200 több 1000-es burst **után**
is hibátlan volt). Megoldás: **hullámokban** elengedni a kapcsolatokat, nem
keep-alive-val — HTTP/1.1-nél N egyidejű kérés N socketet kíván. Ez a gép
socket-sora, nem a szerveré, és nem visz át Linuxra.

### 8.18 Terheléses mérésnél a klienst és a szervert szét kell választani

Ha a loadgen és a szerver ugyanazon a gépen fut, a mérőeszköznek **külön** kell
mérnie a `dispatch`-et (kliensoldali szórás a barriertől a kérés kiadásáig) és a
`latency`-t. Ha a dispatch 0-2 ms, a latencia bizonyítottan a szerverről szól.
Enélkül nem lehet megmondani, ki lassult — és rossz következtetést vonnánk le.

### 8.19 Az „egy futás" bizonyítása: egyedi azonosító, ne csak számláló

A stub CLI minden futásba **egyedi `run_id`-t** tett. Így a válaszok
bájtazonossága bizonyítja, hogy egyetlen futás eredményét kapták — a puszta
számláló („runs=1") ezt nem zárná ki teljesen. Cáfolható mérés > hihető szám.

### 8.16 A Beszel a megfigyelésre jó, a méretezéshez a `free -m` kell

A Beszel „Memóriahasználat" panelja ~2,3 GB-ot mutatott, a `free -m` 840 MB
`used`-ot. Nem mondanak ellent: a Beszel a `buff/cache`-t (1969 MB) is
beleszámolja, ami visszavehető lemez-gyorsítótár. Méretezéshez az
**`available`** a helyes szám.

---

## 7. ELVETETT IRÁNYOK — NE INDULJANAK ÚJRA

- **`go-redis` behúzása.** A web-modul stdlib-only; új függőség + `go.sum` +
  Docker build-változtatás négy parancsért. A saját RESP2 elég, és a soft
  dependency miatt egy protokoll-hiba legrosszabb esetben elveszti a cache-t.
- **Az LLM-szintézis cache-elése.** Bérlők közti szivárgás, és nem
  determinisztikus → az invalidálás nem véges feladat.
- **A `for attempt := 0; attempt < 2; attempt++` → `for range 2` átírás.**
  Kozmetikai, a retry-ciklus a soft dependency szíve, és Go 1.22+ kell hozzá.
  (Utólag mérve: `go.mod` → 1.26.4, Dockerfile → `golang:1.26-alpine`, tehát
  működött volna — de a döntés így is helyes volt.)
- **`gofmt -w` a `main.go`-ra.** 578 sor sorvég-átírása elfedett volna egy
  egyszavas változtatást, és használhatatlanná tette volna a `git blame`-et.
- **PowerShell `WriteAllBytes` a sorvég-konverzióra.** 1299 bájtos, nem
  elemezhető script, ami közvetlenül írta volna a `main.go`-t. A konverziót a
  **Git** végezze: `git add .gitattributes` → `rm main.go` →
  `git checkout -- main.go`.
- **A studies-limit alapértékének emelése.** Marad **10**. A cache nem oldja meg
  a ~120 s-os request-büdzsé problémáját: az első, hideg kérés ugyanannyi.
- **10–20 ismétléses benchmark `hyperfine`-nal, szórással.** Nagy nyílt
  forráskódú PR-ra méretezett módszertan; itt 5 futás, első eldobva (warm-up),
  a maradék mediánja elég — a döntő mérés amúgy is a szerveren van.

---

## 8. HASZNOS PARANCSOK

**➤ GIT BASH — állapot**

```bash
cd /c/Users/LACI/pubvera-corpova
git --no-pager log --oneline -6
git status -sb
```

**➤ GIT BASH — szerver, cache-állapot**

```bash
ssh root@178.105.220.79 'docker logs corpova --tail 20 | grep -iE "cache|listening"'
```

```bash
ssh root@178.105.220.79 'docker exec redis sh -c "redis-cli --no-auth-warning -a \$REDIS_PASSWORD KEYS \"sc:v1:*\"; echo ---; redis-cli --no-auth-warning -a \$REDIS_PASSWORD DBSIZE; echo ---; redis-cli --no-auth-warning -a \$REDIS_PASSWORD INFO stats | grep -E \"keyspace_hits|keyspace_misses\""'
```

**➤ GIT BASH — deploy kikényszerítése (a Watchtower magától is lehúzza)**

```bash
ssh root@178.105.220.79 'cd /opt/pubvera/pubvera-corpova && docker compose pull && docker compose up -d && sleep 15 && docker logs corpova --tail 10 | grep -iE "cache|listening"'
```

**➤ GIT BASH — hideg/meleg mérés**

```bash
ssh root@178.105.220.79 'time curl -s -o /dev/null -X POST -H "Content-Type: application/json" -d "{\"claim\":\"vitamin D supplementation reduces respiratory infections\",\"limit\":10}" http://127.0.0.1:8090/api/consensus'
```

**➤ GIT BASH — a bináris hashe (ennek egyeznie kell a log `cli=` értékével)**

```bash
cd /c/Users/LACI/pubvera-corpova
sha256sum bin/scientific-consensus-pp-cli-linux | cut -c1-12
```

Emlékeztető: a hub (`pubvera.com`) változásaihoz **`git push` nem elég** —
`cd ~/pubvera && npx wrangler pages deploy . --project-name pubvera`.
És ha „a deploy zöld, de a régi verziót látom": előbb Cloudflare →
Caching → **Purge Everything**, csak utána `Ctrl+Shift+R`.

---

## 9. A KÖVETKEZŐ SESSION ELSŐ HÁROM LÉPÉSE

1. **`go test -race` a CI-be** (5.0) — a legsürgősebb, mert a `singleflight.go`
   race detector nélkül ment ki, és lokálisan nem futtatható. Egy sor a GitHub
   Actions workflow-ba. Kicsi, gyors, és utána minden push-nál lefut.
2. **Döntsd el a vonalat**: motor (M2 a legszűkebb, M3 a legnagyobb hozadék)
   vagy termék (semaphore → a hét app `mem_limit`-je → logó → OAuth). Ne
   félbe-félbe.
3. **Ha motor:** friss ág a `26fe022c3`-ról, Claude Code / **Opus**,
   mérés-először, bukó teszttel indítva. A `fix/benefit-stance-gates` tetejére
   **ne** — és **PR-t se nyiss** belőle, amíg a #1309 be nem megy (5.3).
   A #1309-re a komment már írható: a `16-pr-description.md` kész, és a
   stance-javítás méréssel beleírható.

Ha a semaphore-t választod: a `clicount` wrappert újra kell építeni (a
scratchpad eltűnt), és a B-mérés számai (medián +1,14 s) az indoklás.
