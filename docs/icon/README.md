# App icon

`icon.svg` and `logo.svg` are the sources of `guest_access/icon.png` and
`guest_access/logo.png`. Regenerate them with:

```sh
rsvg-convert -w 128 -h 128 docs/icon/icon.svg -o guest_access/icon.png
cd docs/icon && rsvg-convert -w 250 -h 100 logo.svg -o ../../guest_access/logo.png
```

ImageMagick's built-in SVG renderer drops the gradient and strokes, so use
`rsvg-convert` (librsvg).
