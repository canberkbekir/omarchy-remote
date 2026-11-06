// Package qrtext draws QR codes as text, for terminals and the bar widget.
package qrtext

import (
	"strings"

	"rsc.io/qr"
)

// Rows draws the link as rows of "1" (dark) and "0" (light) for the page.
func Rows(text string) []string {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	rows := make([]string, code.Size)
	for y := range code.Size {
		var row strings.Builder
		for x := range code.Size {
			if code.Black(x, y) {
				row.WriteByte('1')
			} else {
				row.WriteByte('0')
			}
		}
		rows[y] = row.String()
	}
	return rows
}
