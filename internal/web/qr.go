package web

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"rsc.io/qr"
)

// qrQuiet is the white border around a QR code, in modules. The QR spec
// asks for 4.
const qrQuiet = 4

// qrPNGScale is the size of one module in the PNG, in pixels.
const qrPNGScale = 8

// qrSVG renders text as an SVG QR code: one path of black modules on a
// white square, with a quiet zone.
func qrSVG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := c.Size + 2*qrQuiet
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := range c.Size {
		for x := range c.Size {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+qrQuiet, y+qrQuiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.Bytes(), nil
}

// qrPNG renders text as a PNG QR code with a quiet zone. rsc.io/qr's own
// Image leaves out the scale and the border, so it is drawn here.
func qrPNG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := (c.Size + 2*qrQuiet) * qrPNGScale
	img := image.NewGray(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			mx, my := x/qrPNGScale-qrQuiet, y/qrPNGScale-qrQuiet
			v := uint8(0xff)
			if c.Black(mx, my) {
				v = 0
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
