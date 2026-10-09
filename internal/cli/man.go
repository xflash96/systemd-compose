package cli

import (
	"slices"
	"strconv"
	"strings"
)

// Docs are the documents the binary carries, so help needs no man page
// installed (go install brings none): the manual's roff source and the
// key reference, docs/config.example.yaml.
type Docs struct{ Man, Keys string }

// docs is what Main was given.
var docs Docs

// manWidth is the width help renders the manual to.
const manWidth = 80

// renderMan renders roff source as man would at manWidth: the man(7)
// macros a manual like this one uses (sections, tagged paragraphs,
// insets), fonts dropped. TestRenderMan_SaysWhatGroffSays holds it to
// groff for this manual.
func renderMan(src string) string { return renderAt(src, 7) }

// renderAt is renderMan with body text, and tags, starting at column base.
func renderAt(src string, base int) string {
	r := &roff{base: base, indent: base, fill: true}
	for _, line := range strings.Split(src, "\n") {
		r.line(line)
	}
	r.flush()
	return strings.TrimLeft(r.out.String(), "\n")
}

// roff is the renderer's state.
type roff struct {
	out    strings.Builder
	words  []string // the line being filled
	first  string   // what starts the next output line: a short tag, padded
	indent int      // where body text starts
	base   int      // where a tag starts, and .PP returns
	insets []int    // the bases .RS saved
	tag    int      // > 0: the next text is a tag, its body at this indent
	fill   bool
	tight  bool // .PD 0: no blank line between paragraphs (stacked tags)
}

func (r *roff) line(l string) {
	if strings.HasPrefix(l, `.\"`) || strings.HasPrefix(l, `'\"`) {
		return
	}
	if !strings.HasPrefix(l, ".") {
		if l == "" {
			r.flush()
			r.out.WriteString("\n")
			return
		}
		r.text(unescape(l))
		return
	}
	name, rest, _ := strings.Cut(l[1:], " ")
	args := roffArgs(rest)
	switch name {
	case "PD":
		r.tight = len(args) > 0 && args[0] == "0"
	case "SH", "SS":
		r.flush()
		r.insets, r.base, r.indent, r.tag = nil, 7, 7, 0
		lead := ""
		if name == "SS" {
			lead = "   "
		}
		r.out.WriteString("\n" + lead + unescape(strings.Join(args, " ")) + "\n")
	case "PP", "LP", "P":
		r.paragraph()
		r.indent = r.base
	case "TP":
		r.paragraph()
		r.tag = r.base + width(args, 0, 7)
	case "IP":
		r.paragraph()
		r.tag = r.base + width(args, 1, 7)
		if len(args) > 0 {
			r.text(unescape(args[0]))
		} else {
			r.indent, r.tag = r.tag, 0
		}
	case "RS":
		r.flush()
		r.insets = append(r.insets, r.base)
		r.base = r.indent
	case "RE":
		r.flush()
		if n := len(r.insets); n > 0 {
			r.base, r.insets = r.insets[n-1], r.insets[:n-1]
		}
		r.indent = r.base
	case "br":
		r.flush()
	case "nf", "EX":
		r.flush()
		r.fill = false
	case "fi", "EE":
		r.flush()
		r.fill = true
	case "B", "I", "SM", "SB":
		r.text(unescape(strings.Join(args, " ")))
	case "BR", "RB", "IR", "RI", "BI", "IB":
		r.text(unescape(strings.Join(args, "")))
	}
}

// width is a macro's indent argument at index i, in ens, or def.
func width(args []string, i, def int) int {
	if len(args) > i {
		if n, err := strconv.Atoi(args[i]); err == nil {
			return n
		}
	}
	return def
}

// text takes a line of text: a tag after .TP or .IP, or words to fill.
func (r *roff) text(s string) {
	if r.tag > 0 {
		body := r.tag
		r.tag = 0
		pad := strings.Repeat(" ", r.base)
		if r.base+len([]rune(s)) < body-1 {
			r.first = pad + s + strings.Repeat(" ", body-r.base-len([]rune(s)))
		} else {
			r.out.WriteString(pad + s + "\n")
		}
		r.indent = body
		return
	}
	if !r.fill {
		r.emit(s)
		return
	}
	r.words = append(r.words, strings.Fields(s)...)
	for {
		n, used := 0, len([]rune(r.margin()))
		for n < len(r.words) && (n == 0 || used+1+len([]rune(r.words[n])) <= manWidth) {
			if n > 0 {
				used++
			}
			used += len([]rune(r.words[n]))
			n++
		}
		if n == len(r.words) {
			return // the rest waits for more words
		}
		r.emit(strings.Join(r.words[:n], " "))
		r.words = r.words[n:]
	}
}

// margin is what the next output line starts with: a short tag, padded,
// or the indent.
func (r *roff) margin() string {
	if r.first != "" {
		return r.first
	}
	return strings.Repeat(" ", r.indent)
}

// emit writes a line at the margin, which uses up a tag.
func (r *roff) emit(s string) {
	r.out.WriteString(r.margin() + s + "\n")
	r.first = ""
}

func (r *roff) flush() {
	if len(r.words) > 0 {
		r.emit(strings.Join(r.words, " "))
		r.words = nil
	}
	if r.first != "" {
		r.out.WriteString(strings.TrimRight(r.first, " ") + "\n")
		r.first = ""
	}
}

func (r *roff) paragraph() {
	r.flush()
	if s := r.out.String(); s != "" && !r.tight && !strings.HasSuffix(s, "\n\n") {
		r.out.WriteString("\n")
	}
}

// roffArgs splits a macro's arguments: spaces separate them, double
// quotes group, and "" inside quotes is a quote.
func roffArgs(s string) []string {
	var args []string
	var a strings.Builder
	in, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '"' && i+1 < len(s) && s[i+1] == '"':
			a.WriteByte('"')
			i++
		case c == '"':
			quoted, in = !quoted, true
		case c == '\\' && i+1 < len(s):
			a.WriteByte(c)
			a.WriteByte(s[i+1])
			i++
			in = true
		case c == ' ' && !quoted:
			if in {
				args = append(args, a.String())
				a.Reset()
				in = false
			}
		default:
			a.WriteByte(c)
			in = true
		}
	}
	if in {
		args = append(args, a.String())
	}
	return args
}

// unescape turns roff's escapes into the characters they stand for, and
// drops font changes.
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'f':
			if i+1 < len(s) {
				i++
				if s[i] == '(' {
					i += 2
				}
			}
		case '(':
			if i+2 < len(s) {
				b.WriteString(special[s[i+1:i+3]])
				i += 2
			}
		case '-', 'e', '\\':
			b.WriteString(map[byte]string{'-': "-", 'e': `\`, '\\': `\`}[c])
		case '&', '%':
		case ' ', '~', '0':
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// special are the named characters the manual may use.
var special = map[string]string{"aq": "'", "dq": `"`, "em": "—", "en": "-", "lq": `"`, "rq": `"`, "bu": "•", "co": "©", "ga": "`", "ti": "~", "ha": "^", "rs": `\`}

// manEntries are the manual's entries for a verb, rendered under their
// section's name: the tagged paragraphs of COMMANDS whose tag names it.
// "" when it has none.
func manEntries(src, verb string) string {
	var out []string
	section, depth := "", 0
	lines := strings.Split(src, "\n")
	inCommands := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, ".SH "):
			inCommands = strings.TrimSpace(l[4:]) == "COMMANDS"
			continue
		case !inCommands:
			continue
		case strings.HasPrefix(l, ".SS "):
			section, depth = unescape(strings.Join(roffArgs(l[4:]), " ")), 0
			continue
		case l == ".RS" || strings.HasPrefix(l, ".RS "):
			depth++
		case l == ".RE":
			depth--
		}
		if l != ".TP" && !strings.HasPrefix(l, ".TP ") || depth != 0 || i+1 == len(lines) || !tagNames(lines[i+1], verb) {
			continue
		}
		entry := []string{".TP", lines[i+1]}
		d, body := 0, false
		for j := i + 2; j < len(lines); j++ {
			e := lines[j]
			next := e == ".TP" || strings.HasPrefix(e, ".TP ")
			if d == 0 && (next && body || strings.HasPrefix(e, ".SS ") || strings.HasPrefix(e, ".SH ")) {
				break
			}
			// tags stacked over one body (.PD 0, as run's and exec's): the
			// entry goes on through them to that body
			if lines[j-1] != ".TP" && !strings.HasPrefix(lines[j-1], ".TP ") && printsText(e) {
				body = true
			}
			switch {
			case e == ".RS" || strings.HasPrefix(e, ".RS "):
				d++
			case e == ".RE":
				d--
			}
			entry = append(entry, e)
		}
		out = append(out, strings.ToUpper(section)+":", strings.TrimRight(renderAt(strings.Join(entry, "\n"), 2), "\n"), "")
	}
	return strings.Join(out, "\n")
}

// printsText reports a line that prints: text, or a font macro's.
func printsText(l string) bool {
	if !strings.HasPrefix(l, ".") {
		return l != ""
	}
	name, _, _ := strings.Cut(l[1:], " ")
	return slices.Contains([]string{"B", "I", "BR", "RB", "IR", "RI", "BI", "IB", "SM", "SB"}, name)
}

// tagNames reports whether a .TP's tag line names verb: its first word,
// split at | and at commas.
func tagNames(tag, verb string) bool {
	if !strings.HasPrefix(tag, ".B") {
		return false // .I VERB: a stand-in, not a verb
	}
	text := strings.TrimSpace(renderMan(tag))
	first, _, _ := strings.Cut(text, " ")
	for _, w := range strings.FieldsFunc(first, func(r rune) bool { return r == '|' || r == ',' }) {
		if w == verb {
			return true
		}
	}
	return false
}
