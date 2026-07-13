// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runner

// stripANSI removes ANSI terminal escape sequences from b: CSI sequences
// (colors and cursor movement, "\x1b[31m"), OSC sequences (window titles and
// hyperlinks, terminated by BEL or ST), and the remaining ESC-prefixed forms
// (charset designations, "\x1b(B"). It covers what agent CLIs and the tools
// they shell out to actually emit; it is not a full terminal emulator.
func stripANSI(b []byte) []byte {
	const esc = 0x1b
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != esc {
			out = append(out, b[i])
			continue
		}
		i++ // consume the ESC
		if i >= len(b) {
			break
		}
		switch b[i] {
		case '[': // CSI: parameter/intermediate bytes, then one final in 0x40–0x7e
			for i++; i < len(b) && (b[i] < 0x40 || b[i] > 0x7e); i++ {
			}
		case ']': // OSC: runs until BEL or ST (ESC \)
			for i++; i < len(b); i++ {
				if b[i] == 0x07 {
					break
				}
				if b[i] == esc && i+1 < len(b) && b[i+1] == '\\' {
					i++
					break
				}
			}
		default: // other ESC sequence: intermediates in 0x20–0x2f, then one final
			for i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f {
				i++
			}
		}
	}
	return out
}
