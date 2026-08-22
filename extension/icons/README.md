# Icons

The Caveman logo: the pixel-flame CaveMark, white on onyx (`#0a0a0a`).
`mark.svg` is the logo; `mark-tight.svg` is the same drawing with the pixel
gutters closed, because below ~32px the 1-unit gaps fall under a pixel and the
flame smears. Every PNG here is rendered from one of the two — don't hand-edit
the PNGs.

```sh
for s in 16 32;  do rsvg-convert -w $s -h $s mark-tight.svg -o icon$s.png; done
for s in 48 128; do rsvg-convert -w $s -h $s mark.svg -o icon$s.png; done
rsvg-convert -w 512 -h 512 mark.svg -o icon-master.png
rsvg-convert -w 128 -h 128 mark.svg -o ../store-assets/store-icon-128.png
```
