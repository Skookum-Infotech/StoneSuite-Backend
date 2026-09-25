#!/usr/bin/env bash
# Renders all 12 StoneSuite emails through the one shared template
# (services/templates/email.html) and prints them into a single PDF for review.
#
#   scripts/email-previews.sh [out-dir]        # default: .superpowers/email-previews
#
# Needs Chrome/Edge (headless print) and python with pypdf to merge the pages.
set -euo pipefail

out="${1:-.superpowers/email-previews}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"

EMAIL_PREVIEW_DIR="$out" go test ./controllers ./approvalchain -run TestWriteEmailPreviews -count=1 >/dev/null

browser=""
for b in chrome google-chrome chromium msedge \
  "/c/Program Files/Google/Chrome/Application/chrome.exe" \
  "/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe" \
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"; do
  if command -v "$b" >/dev/null 2>&1 || [ -x "$b" ]; then browser="$b"; break; fi
done
if [ -z "$browser" ]; then
  echo "HTML previews written to $out (no Chrome/Edge found for PDF)"; exit 0
fi

to_url() { if command -v cygpath >/dev/null 2>&1; then echo "file:///$(cygpath -m "$1")"; else echo "file://$1"; fi; }
to_path() { if command -v cygpath >/dev/null 2>&1; then cygpath -w "$1"; else echo "$1"; fi; }

for f in "$out"/*.html; do
  "$browser" --headless=new --disable-gpu --no-pdf-header-footer \
    --print-to-pdf="$(to_path "${f%.html}.pdf")" "$(to_url "$f")" >/dev/null 2>&1
done

python - "$(to_path "$out")" <<'PY'
import glob, os, sys
from pypdf import PdfWriter
out = sys.argv[1]
w = PdfWriter()
for f in sorted(glob.glob(os.path.join(out, "[0-9][0-9]-*.pdf"))):
    w.append(f)
w.write(os.path.join(out, "stonesuite-emails.pdf"))
PY
echo "Wrote $out/stonesuite-emails.pdf"
