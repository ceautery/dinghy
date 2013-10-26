package dinghy

/**
 *  NOTES
 *
 *  This is an attempt at a single .go file implementation of Markdown.pl,
 *  following the spirit of John Gruber's original program. In many cases, the
 *  original variable names and comments are used. In cases where regexen not
 *  supported by Go's re2 engine are used in Markdown.pl, these are rewritten
 *  either using effective Go idioms, or in multi-step regex approaches.
 *
 *  The intent of this is to be a plugin to my blogging engine, Dinghy, to be
 *  called infrequently with relatively small inputs, making optimization and
 *  efficiency secondary to functionality and readability (unless you don't live
 *  and breath regular expressions, in which case much of this will be wholly
 *  unreadable).
 * 
 *  This code, along with the Dinghy blog engine itself, is released under the
 *  Apache license (see the included "LICENSE" file), and you are welcome to
 *  fork the project and use it as you will.
 */

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const g_tab_width = 4
const escapedChars = `&'<>"*{}[]_\`

var tab_width = strconv.Itoa(g_tab_width)
var less_than_tab = strconv.Itoa(g_tab_width - 1)

var g_html_blocks = make(map[string]string)
var g_urls        = make(map[string]string)
var g_titles      = make(map[string]string)

var outdent_re *regexp.Regexp
var escaper    *strings.Replacer

func markdown (lead, content string) string {
	s := lead + content

	outdent_re =  regexp.MustCompile( `(?m)^ {1,` + tab_width + `}` )

	// Replacer for all markdown symbols, for use in code blocks
	escaper = strings.NewReplacer(`<`, `&lt;`, `>`, `&gt;`, `&`, `&amp;`, `'`, `&#39;`, `"`, `&#34;`,
		`*`, `&#42;`, `_`, `&#95;`, `{`, `&#123;`, `}`, `&#125;`, `[`, `&#91;`,  `]`, `&#93;`,  `\`, `&#92;`)

	// Standardize line endings:
	s = strings.Replace(s, "\r\n", "\n", -1)  // DOS to Unix
	s = strings.Replace(s, "\r",   "\n", -1)  // Mac to Unix
                  
	// Make sure "s" ends with a couple of newlines:
	s += "\n\n"   
                  
	// Convert all tabs to spaces.
	s = detab(s)  
                  
	// Strip any lines consisting only of spaces, so consecutive blank lines
	// can be matched with \n+
	var re = regexp.MustCompile(`(?m)^ +$`)
	s = re.ReplaceAllLiteralString(s, "")

	// Turn block-level HTML blocks into hash entries
	s = hashHTMLBlocks(s);

	// Strip link definitions, store in hashes.
	s = stripLinkDefinitions(s)
	return runBlockGamut(s)
}

func outdent(s string) string {
	return outdent_re.ReplaceAllLiteralString(s, "")
}

func runBlockGamut(s string) string {
	s = doHeaders(s)

	// Do Horizontal Rules:
	re := regexp.MustCompile( `(?m)^ {0,3}((\* ?){3,}|(- ?){3,}|(_ ?){3,}) *$` )
	s = re.ReplaceAllLiteralString(s, "\n<hr />\n")

	s = doCodeBlocks(s)
	s = runSpanGamut(s)
	s = doLists(s)
	s = doBlockQuotes(s)

	// We already ran _HashHTMLBlocks() before, in Markdown(), but that
	// was to escape raw HTML in the original Markdown source. This time,
	// we're escaping the markup we've just created, so that we don't wrap
	// <p> tags around block-level tags.
	s = hashHTMLBlocks(s)
	s = formParagraphs(s)

	return s
}

func formParagraphs(s string) string {
	s = strings.TrimSpace(s)
	re := regexp.MustCompile( `\n{2,}` )
	grafs := re.Split(s, -1)

	var buffer bytes.Buffer
	for _, p := range grafs {
		if g_html_blocks[p] == "" {
			buffer.WriteString("<p>")
			buffer.WriteString( strings.TrimSpace(p) )
			buffer.WriteString("</p>\n\n")
		} else {
			buffer.WriteString(g_html_blocks[p])
			buffer.WriteString("\n\n")
		}
	}
	
	return buffer.String()
}

func doBlockQuotes(s string) string {
	re := regexp.MustCompile( `\n+(> .+\n(.+\n)*\n*)+` )
	s = re.ReplaceAllStringFunc(s, func(m string) string {
		m = strings.TrimSpace(m)
		var buffer bytes.Buffer
		buffer.WriteString("\n\n<blockquote>\n")
		header := regexp.MustCompile( `(?m)^> ` )
		m = header.ReplaceAllLiteralString(m, "")
		buffer.WriteString( runBlockGamut(m) )
		buffer.WriteString( "\n</blockquote>\n" )
		return buffer.String()
	})
	return s
}

func doCodeBlocks(s string) string {
	re := regexp.MustCompile( `\n*( {` + tab_width + `}.*\n+)+` )
	return re.ReplaceAllStringFunc(s, func(m string) string {
		var buffer bytes.Buffer
		buffer.WriteString("\n\n<pre><code>")
		// buffer.WriteString( strings.TrimSpace( encodeCode( outdent(m) ) ) )
		buffer.WriteString( strings.Trim( encodeCode( outdent(m) ), "\n" ) )
		buffer.WriteString("\n</code></pre>\n\n")
		m = blockToMD5( buffer.String() )
		g_html_blocks[m] = buffer.String()
		return m
	})
}

func encodeCode(s string) string {
	if strings.IndexAny(s, escapedChars) == -1 {
		return s
	}
     
	return escaper.Replace(s)
}

// Form HTML ordered (numbered) and unordered (bulleted) lists.
func doLists(s string) string {
	// marker := ` {0,` + less_than_tab + `}([*+-]|\d+\.)`
	marker := `([*+-]|\d+\.)`
	re := regexp.MustCompile( `(?m)^` + marker + ` .+\n(\S.*\n|\n*` + marker + `? .+\n)*` )
	return re.ReplaceAllStringFunc(s, processListItems)
}

// Receives a complete Markdown list from doLists(), returns HTML list
func processListItems(s string) string {
	var buffer bytes.Buffer
	ordered, _ := regexp.MatchString(`^\d`, s)
	if ordered {
		buffer.WriteString("<ol>\n")
	} else {
		buffer.WriteString("<ul>\n")
	}

	re := regexp.MustCompile( `(?m)^([*+-]|\d+\.)` )
	markers := re.FindAllStringSubmatchIndex(s, -1)
	for x := 0; x < len(markers); x++ {
		start := markers[x][1]
		var end int
		if x < len(markers) - 1 {
			end = markers[x+1][0]
		} else {
			end = len(s)
		}
		item := strings.TrimSpace( s[start:end] )

		if strings.Index(item, "\n") > -1 {
			item = runBlockGamut(outdent(item))
		}

		buffer.WriteString("<li>" + item + "</li>\n")
	}

	if ordered {
		buffer.WriteString("</ol>\n")
	} else {
		buffer.WriteString("</ul>\n")
	}

	return buffer.String()
}

func doHeaders(s string) string {
	// Setext-style headers:
	//	  Header 1
	//	  ========
	//  
	//	  Header 2
	//	  --------
	//
	re := regexp.MustCompile( `(?m)^(.+) *\n=+ *\n+` )
	s = re.ReplaceAllString(s, "<h1>$1</h1>\n\n")

	re = regexp.MustCompile( `(?m)^(.+) *\n-+ *\n+` )
	s = re.ReplaceAllString(s, "<h2>$1</h2>\n\n")
		
	// atx-style headers:
	//	# Header 1
	//	## Header 2
	//	## Header 2 with closing hashes ##
	//	...
	//	###### Header 6

	re = regexp.MustCompile( `(?m)^(\#{1,6}) *(.+?) *\#*\n+` )
	return re.ReplaceAllStringFunc(s, func(match string) string {
		m := re.FindStringSubmatchIndex(match)
		tag := "h" + strconv.Itoa(m[3]) + ">"
		return "<" + tag + match[m[4]:m[5]] + "</" + tag + "\n\n"
	})
}

func runSpanGamut(s string) string {
	s = doCodeSpans(s)

	// Process images, anchors, and autolinks
	s = doLinks(s)

	s = encodeAmpsAndAngles(s)

	s = doItalicsAndBold(s)

	// Do hard breaks:
	re := regexp.MustCompile( ` {2,}\n` )
	return re.ReplaceAllLiteralString(s, "<br />\n")
}

func doItalicsAndBold(s string) string {
	re := regexp.MustCompile( `(\*\*(.+?)\*\*|\b__(.+?)__\b)` )
	s = re.ReplaceAllString(s, "<strong>$2$3</strong>")

	re = regexp.MustCompile( `(\*(.+?)\*|\b_(.+?)_\b)` )
	s = re.ReplaceAllString(s, "<em>$2$3</em>")
	return s
}

// Sadly I couldn't implement this function with the fairly elegant regex from
// sub _DoCodeSpans in Markdown.pl, or even with a similar, two-step method. So
// replacing backticks with code tags as described on
// http://daringfireball.net/projects/markdown/syntax#code must be done a little
// more low-level in Go. For want of a backreference.
func doCodeSpans(s string) string {
	if strings.Index(s, "`") == -1 {
		return s
	}

	const (
		START int = iota
		REPLACE
	)

	re     := regexp.MustCompile( "`+" )
	loc    := re.FindAllStringIndex(s, -1)
	mode   := START
	slen   := 0
	idx    := 0
	var buffer bytes.Buffer

	for _, l := range loc {
		buffer.WriteString( s[idx:l[0]] )
		idx = l[1]

		switch mode {
		case START:
			buffer.WriteString("<code>")
			slen = l[1] - l[0]
			mode = REPLACE
		case REPLACE:
			rlen := l[1] - l[0]
			if rlen < slen {
				buffer.WriteString( strings.Repeat( `&#96;`, rlen ) )
			} else {
				buffer.WriteString("</code>")
				mode = START
			}
		}
	}

	buffer.WriteString( s[idx:] )
	return buffer.String()
}

func isImage(s string) bool {
	if s[0:1] == "!" {
		return true
	}
	return false
}

func getLink(img bool, addr, text, title string) string {
	// Check for mailto urls, and encode them
	u, err := url.Parse(addr)
	if err == nil && u.Scheme == "mailto" {
		addr = encodeEmailAddress(addr)
	}


	// Make all link URLs safe from later strong/em replace. This should be safe
	// since * and _ aren't allowed in domain names, and some basic testing
	// showed that web servers don't seem to mind query strings being URL
	// escaped. I imagine there are edge cases where I am dead-wrong about this,
	// and if I come across one (and please report one if you find it), I'll add
	// an md5 replace for links as well as blocks. Until then...
	addr = strings.Replace(addr, `_`, `%5F`, -1)
	addr = strings.Replace(addr, `*`, `%2A`, -1)

	if img {
		if title == "" {
			return `<img src="` + addr + `" alt="` + text + `" />`
		} else {
			return `<img src="` + addr + `" alt="` + text + `" title="` + title +`" />`
		}
	} else {
		if title == "" {
			return `<a href="` + addr + `">` + text + `</a>`
		} else {
			return `<a href="` + addr + `" title="` + title +`">` + text + `</a>`
		}
	}
}

func doLinks(s string) string {
	//
	// First, handle reference-style labeled links: [alt text][id]
	//
	re := regexp.MustCompile( `!?\[.*?\] ?(\n *)?\[.*?\]` )
	s = re.ReplaceAllStringFunc(s, func(m string) string {
		img   := isImage(m)
		r     := regexp.MustCompile( `[\[\]]` )
		ndx   := strings.Index(m, `[`) + 1
		parts := r.Split( m[ndx:], 4 )
		text  := encodeCode( parts[0] )
		id    := strings.ToLower(parts[2])
		if id == "" {
			id = strings.ToLower(parts[0])
		}
		if g_urls[id] == "" {
			return m
		}
		return getLink(img, g_urls[id], text, g_titles[id])
	})

	//
	// Next, handle inline links:  [alt text](url "optional title")
	//
	re = regexp.MustCompile( `!?\[.*?\]\([^'"]+?('.+?'|".+?")?\)` )
	s =  re.ReplaceAllStringFunc(s, func(m string) string {
		img   := isImage(m)
		r     := regexp.MustCompile( `[\[\]()'"]` )
		ndx   := strings.Index(m, `[`) + 1
		parts := r.Split( m[ndx:], 5 )
		text  := encodeCode( parts[0] )
		url   := strings.Trim(parts[2], `<> `)
		return getLink(img, url, text, parts[3])
	})

	// Handle auto-links
	re = regexp.MustCompile( `<[a-z]+:[^'">\s]+>` )
	return re.ReplaceAllStringFunc(s, func(m string) string {
		u, err := url.Parse(m[1:len(m) - 1])
		if err != nil {
			return m
		}
		var addr string
		if u.Scheme == "mailto" {
			addr = encodeEmailAddress(u.String())
		} else {
			addr = u.String()
		}
		return `<a href="` + addr + `">` + addr + `</a>`
	})
}

func encodeEmailAddress(s string) string {
	b := []byte(s)
	
	var buffer bytes.Buffer
	var r int
	for _,v := range b {
		if v == 64 || v == 58 { // always encode @ and :
			r = rand.Intn(2) + 1
		} else {
			r = rand.Intn(3)
		}
		switch r {
		case 0:
			buffer.WriteByte(v)
		case 1:
			buffer.WriteString( fmt.Sprintf("&#%d;", v) )
		case 2:
			buffer.WriteString( fmt.Sprintf("&#x%X;", v) )
		}
	}
	
	return buffer.String()
}

func stripLinkDefinitions(s string) string {
	ws := ` *\n? *` // Whitespace between ID, link, and title can have an optional newline

	r := `(?m)` // Multi-line mode
	r += `^ {0,` + less_than_tab + `}`  // Start of line, up to tab-width - 1 spaces
	r += `\[(.+)\]:` + ws               // [id] + whitespace
	r += `<?(\S+?)>?` + ws              // address = non-spaces surrounded w/ optional angle brackets
	r += `(?:["'(](.+?)["')])? *$`        // 0 or 1 instances of "title" or (title)
	re := regexp.MustCompile(r)
	matches := re.FindAllStringSubmatch(s, -1)

	for _, m := range matches {
		id := strings.ToLower(m[1])
		g_urls[id] = encodeAmpsAndAngles(m[2])
		if m[3] != "" {
			g_titles[id] = encodeCode( m[3] )
		}
	}

	return re.ReplaceAllLiteralString(s, "")
}

// Smart processing for ampersands and angle brackets that need to be encoded.
func encodeAmpsAndAngles(s string) string {
	// Encode ampersands not part of an entity reference
	s = negLookAhead(s, `&`, `&#?[xX]?([0-9a-fA-F]+|\w+);`, `&amp;`)

	// Encode naked <'s
	return negLookAhead(s, `<`, `<[a-z/?\$!]`, `&lt;`)
}

func negLookAhead(s, char, neg, repl string) string {
	re := regexp.MustCompile(char + `[^` + char + `]*`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		// Does m match negative pattern?
		matches, _ := regexp.MatchString(neg, m)
		if matches {
			return m
		}
		return strings.Replace(m, char, repl, 1)
	})
}

func hashHTMLBlocks(s string) string {

	block_tags_b := "p|div|h[1-6]|blockquote|pre|table|dl|ol|ul|script|noscript|form|fieldset|iframe|math"
	block_tags_a := block_tags_b + "|ins|del"

	// Go doesn't support backreferences (e.g., \2), so tag matching must be done in two steps
	// Step 1: Identify HTML open tags at beginning of a line.
	re_a := regexp.MustCompile( `(?m)^<(` + block_tags_a + `)\b` )
	matches := re_a.FindAllStringSubmatch(s, -1)

	// Step 2: Iterate over matches, create a custom regex for each match
	for _, m := range matches {
		re := regexp.MustCompile( `(?m)^<(` + m[1] + `)\b(.*\n)*?</` + m[1] + `> *$` )
		s = re.ReplaceAllStringFunc(s, blockToMD5)
	}

	// Repeat for "liberal" match (closing tag doesn't need to be at start of line)
	re_b := regexp.MustCompile( `(?m)^<(` + block_tags_b + `)\b` )
	matches = re_b.FindAllStringSubmatch(s, -1)
	for _, m := range matches {
		re := regexp.MustCompile( `(?m)^<(` + m[1] + `)\b(.*\n)*?.*</` + m[1] + `> *$` )
		s = re.ReplaceAllStringFunc(s, blockToMD5)
	}

	// One-off prefix: Following blank line or start of text,
	// less than a tab's width of leading spaces
	oneOff := `(\n\n|\A\n?) {0,` + less_than_tab + `}`

	// One-off case for hr tags
	re := regexp.MustCompile( oneOff + `<hr\b([^<>])*?/?> *\n\n+` )
	s = re.ReplaceAllStringFunc(s, blockToMD5)

	// One-off case for HTML comments
	re = regexp.MustCompile( `(?s)` + oneOff + `(<!--.*?--\s*)+> *\n\n+` )
	return re.ReplaceAllStringFunc(s, blockToMD5)
}

func blockToMD5(match string) string {
	h := md5.New()
	io.WriteString(h, match)
	key := hex.EncodeToString(h.Sum(nil))
	g_html_blocks[key] = match
	return "\n\n" + key + "\n\n"
}

func detab(s string) string {
	var re = regexp.MustCompile(`.*?\t`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		// Not using a $1 group match, as ReplaceAllStringFunc doesn't use Expand templates,
		// so instead we'll manually lop off the tab before calculating the replace string
		match = match[:len(match)-1]

		var buffer bytes.Buffer
		buffer.WriteString(match)
		length := g_tab_width - len(match) % g_tab_width
		for n := 0; n < length; n++ {
			buffer.WriteString(" ")
		}
		return buffer.String()
	})
}
