# DubbelWeg

Windows program that finds files with exactly the same content (any drive, also USB sticks)
and removes the extra copies safely:
- fixed drives: to the Recycle Bin;
- USB sticks / cards: moved to `_Dubbelen (verwijderd)` at the root of that stick.
At least one copy of every file always stays. Files are compared by size, then a quick
fingerprint, then a full SHA-256 of the whole file.

The screen is a local page in an Edge/Chrome app window (127.0.0.1:47124); nothing goes online.
Removals are logged in `%LOCALAPPDATA%\DubbelWeg\verwijderd.txt`.

Build: push to GitHub; the workflow tests, builds `DubbelWeg.exe` and publishes it as release `latest`.

Extra tabs:
- Mappen samenvoegen: merge folder "Van" into "Naar" (identical files are not copied twice; same name
  with other content: keep both with "(2)" or keep the newest; the other goes to the Recycle Bin).
- Beschadigde bestanden: finds broken pictures (won't decode, half-grey), cut-off PDFs/documents,
  files that are only zeros; moves them to "_Beschadigd (nakijken)" (with herkomst.txt) or to the bin.
- Prullenbakken: per drive the size of the Recycle Bin, old bins of other/former Windows users
  (emptied with administrator permission), and DubbelWeg/SchijfSync collection folders.
