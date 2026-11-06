#!/bin/bash
# Draws the Windows tray icons into trayicons/: a phone with a pointer, in
# six states for a dark and a light taskbar. Needs rsvg-convert and magick.
# Run it from the repo root after changing the drawing, and commit the result.
set -euo pipefail

out=trayicons
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"

warn='#F5A524'
arrow='M8 6.5V14.6l2.1-2 1.4 3 1.5-.7-1.4-2.9H14.6Z'

# svg THEME STATE SMALL: one icon as SVG. SMALL draws for 16 and 20 pixels:
# a bigger phone, and the party badge as a plain dot.
svg() {
	local theme=$1 state=$2 small=$3 fg acc ink
	if [ "$theme" = dark ]; then fg='#FFFFFF' acc='#C8F24A' ink='#0E0F0C'; else fg='#1F1F1F' acc='#4F7A00' ink='#FFFFFF'; fi
	local phone='<rect x="2.5" y="1.5" width="7" height="12" rx="1.5"/>' screen='<rect x="4" y="3" width="4" height="8"/>' home='M5 12h2'
	if [ "$small" = 1 ]; then
		phone='<rect x="1.5" y="0.5" width="8" height="14" rx="1.5"/>' screen='<rect x="3" y="2" width="5" height="10"/>' home='M4.5 13h2'
	fi
	local on=0 badge='' cut="<path d=\"$arrow\" stroke-width=\"1.4\" stroke-linejoin=\"round\"/>"
	case $state in
	connected) on=1 ;;
	party*)
		on=1
		if [ "$small" = 1 ]; then
			badge="<circle cx=\"12.5\" cy=\"12.5\" r=\"3\" fill=\"$acc\"/>"
			cut+='<circle cx="12.5" cy="12.5" r="4"/>'
		else
			badge="<circle cx=\"12\" cy=\"12\" r=\"4\" fill=\"$acc\"/><text x=\"12\" y=\"14.6\" font-family=\"DejaVu Sans, sans-serif\" font-weight=\"700\" font-size=\"7\" text-anchor=\"middle\" fill=\"$ink\">${state#party}</text>"
			cut+='<circle cx="12" cy="12" r="5"/>'
		fi
		;;
	warning)
		badge="<circle cx=\"12.5\" cy=\"3.5\" r=\"3.2\" fill=\"$warn\"/><path d=\"M12.5 1.9v2.1M12.5 5.1v.2\" stroke=\"#1F1F1F\" stroke-width=\"1.1\" stroke-linecap=\"round\"/>"
		cut+='<circle cx="12.5" cy="3.5" r="4.2"/>'
		;;
	esac
	local fill_screen='' arrow_fill=$fg
	if [ $on = 1 ]; then fill_screen="<g fill=\"$acc\">$screen</g>" arrow_fill=$acc; fi
	cat <<SVG
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">
<defs><mask id="cut" maskUnits="userSpaceOnUse" x="0" y="0" width="16" height="16">
<rect width="16" height="16" fill="#fff"/><g fill="#000" stroke="#000">$cut</g></mask></defs>
<g mask="url(#cut)"><g fill="none" stroke="$fg">$phone<path d="$home"/></g>$fill_screen</g>
<path d="$arrow" fill="$arrow_fill"/>$badge
</svg>
SVG
}

for theme in dark light; do
	for state in idle connected party1 party2 party3 warning; do
		pngs=()
		for size in 16 20 24 32 40 48; do
			small=0
			[ "$size" -le 20 ] && small=1
			svg "$theme" "$state" "$small" > "$work/icon.svg"
			rsvg-convert -w "$size" -h "$size" "$work/icon.svg" -o "$work/$size.png"
			pngs+=("$work/$size.png")
		done
		magick "${pngs[@]}" "$out/$theme-$state.ico"
	done
done
echo "wrote $(ls "$out" | wc -l) icons to $out/"
