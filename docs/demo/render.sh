#!/bin/sh
# Render tapes to frames with vhs and encode them here: vhs 0.12's own
# ffmpeg step writes nothing under ffmpeg 9. Several tapes are joined in
# order. A tape already recorded is reused: the tapes write to gitbay.org,
# so delete docs/demo/out/<name> to record one again. Writes
# docs/demo/out/demo.{mp4,webm,gif}.
#
#   DEMO_CODE=<mailed code> docs/demo/render.sh signup verify ship
set -eu
cd "$(dirname "$0")/../.."
out=docs/demo/out
bg=0b0b0b
list=$out/concat.txt
mkdir -p "$out"
: > "$list"
for name in "$@"; do
	[ -d "$out/$name" ] || vhs "docs/demo/$name.tape"
	ffmpeg -loglevel error -y \
		-framerate 50 -i "$out/$name/frame-text-%05d.png" \
		-framerate 50 -i "$out/$name/frame-cursor-%05d.png" \
		-filter_complex "[0][1]overlay,pad=1248:688:32:32:color=$bg" \
		-c:v libx264 -pix_fmt yuv420p -r 25 "$out/$name.mp4"
	echo "file '$name.mp4'" >> "$list"
done
ffmpeg -loglevel error -y -f concat -safe 0 -i "$list" -c copy "$out/demo.mp4"
ffmpeg -loglevel error -y -i "$out/demo.mp4" -c:v libvpx-vp9 -b:v 0 -crf 40 "$out/demo.webm"
ffmpeg -loglevel error -y -i "$out/demo.mp4" \
	-vf "fps=10,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=32[p];[b][p]paletteuse=dither=none" \
	"$out/demo.gif"
ls -l "$out"/demo.*
