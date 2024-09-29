/*
Copyright © 2024 Gareth Watts <gareth@omnipotent.net>
*/
package plsdo

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/gwatts/plsdo/pkg/ast"
	"github.com/gwatts/plsdo/pkg/gopls"
)

type matchEntry struct {
	Filename     string
	Line         int
	EncRecvType  string
	EncRecvName  string
	EncFuncName  string
	OrgSource    string
	PrettySource string
}

func (me matchEntry) fmtEnc() string {
	if me.EncRecvType != "" {
		if me.EncRecvName != "" {
			return fmt.Sprintf("(%s %s) %s(...)", me.EncRecvName, me.EncRecvType, me.EncFuncName)
		} else {
			return fmt.Sprintf("(%s) %s(...)", me.EncRecvType, me.EncFuncName)
		}
	}
	return fmt.Sprintf("%s(...)", me.EncFuncName)
}

// Matcher wraps ast and gopls to find matching functions and methods.
type Matcher struct {
	refs        []matchEntry
	pls         *gopls.GoplsClient
	DebugWriter io.Writer
}

// NewMatcher creates an initialized Matcher.
func NewMatcher() (*Matcher, error) {
	pls, err := gopls.NewGoplsClient(".")
	if err != nil {
		return nil, err
	}
	return &Matcher{
		pls: pls,
	}, nil
}

// Close closes the connection to the underlying gopls process.
func (m *Matcher) Close() {
	if m.pls != nil {
		m.pls.Close()
	}
	m.pls = nil
}

// PrettyPrint prints all matches to the supplied output.
// style is a Chroma style, or "none" for no coloring.
func (m *Matcher) PrettyPrint(w io.Writer, style string) {
	m.sort()
	lastFilename := ""
	lastEnc := ""

	for _, ref := range m.refs {
		if ref.Filename != lastFilename {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "+++ %s:%d\n", ref.Filename, ref.Line)
			lastFilename = ref.Filename
			lastEnc = ""
		}

		enc := ref.fmtEnc()
		if lastEnc != enc {
			fmt.Fprintln(w)
			fmt.Fprintln(w, enc)
			lastEnc = enc
		} else {
			fmt.Fprintln(w, "...")
		}

		formattedLines := strings.Split(ref.PrettySource, "\n")
		if style != "" && style != "none" {
			// Set up the lexer, formatter, and style for syntax highlighting
			lexer := lexers.Get("go")
			if lexer == nil {
				lexer = lexers.Fallback
			}

			style := styles.Get(style)
			if style == nil {
				style = styles.Fallback
			}

			formatter := formatters.TTY16m // For 24-bit color terminals

			// Tokenize the input code
			var buf strings.Builder
			iterator, err := lexer.Tokenise(nil, ref.PrettySource)
			if err == nil {
				err = formatter.Format(&buf, style, iterator)
			}
			if err == nil {
				formattedLines = strings.Split(buf.String(), "\n")
			}
		}

		// Print each line with the line number
		for i, line := range formattedLines {
			fmt.Fprintf(w, "%5d  %s\n", ref.Line+i, line)
		}
	}
}

// Json outputs matches in json format.
func (m *Matcher) Json(w io.Writer) error {
	m.sort()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	for _, me := range m.refs {
		if err := enc.Encode(me); err != nil {
			return err
		}
	}
	return nil
}

// FindFuncReferences scans the module in the current working directory for all
// references to the named functions or methods in a specific package, and adds any
// matches to the current match set.  It can be called multiple times to add additional
// matches across different packages.
func (m *Matcher) FindFuncReferences(pkgName string, patterns ...string) error {
	pwd, _ := filepath.Abs(".")

	ap := ast.NewASTProcessor()
	defs, err := ap.FindFuncDefinitions(pkgName, patterns...)
	if err != nil {
		return err
	}
	m.debug(func() {
		for _, def := range defs {
			m.debugPrintf("found %s -> %s at %s:%d:%d\n", def.Pkg, def.MethodName(), def.Filename, def.OffsetLine, def.OffsetCol)
		}
	})

	// for each matching definition, find refs to it
	for _, def := range defs {
		matches, err := m.pls.FindReferences(def.Filename, def.OffsetLine, def.OffsetCol)
		if err != nil {
			return err
		}
		for _, match := range matches {
			if !strings.HasPrefix(match.Filename, pwd) {
				continue
			}

			functionName, receiverType, receiverName, err := ap.GetEnclosingFunctionName(match.Filename, match.StartLine, match.StartCharacter)
			if err != nil {
				return err
			}

			src, err := ap.ExtractFullCall(match.Filename, match.StartLine, match.StartCharacter)
			if err != nil {
				return err
			}
			me := matchEntry{
				Filename:     match.Filename,
				Line:         match.StartLine,
				EncRecvType:  receiverType,
				EncRecvName:  receiverName,
				EncFuncName:  functionName,
				OrgSource:    src,
				PrettySource: ast.Format(src),
			}
			m.refs = append(m.refs, me)
		}
	}
	return nil
}

func (m *Matcher) sort() {
	slices.SortStableFunc(m.refs, func(a, b matchEntry) int {
		if v := strings.Compare(a.Filename, b.Filename); v != 0 {
			return v
		}
		return cmp.Compare(a.Line, b.Line)
	})
}

func (m *Matcher) debugPrintf(format string, a ...any) (n int, err error) {
	if m.DebugWriter != nil {
		return fmt.Fprintf(m.DebugWriter, "[debug] "+format, a...)
	}
	return 0, nil
}

func (m *Matcher) debug(f func()) {
	if m.DebugWriter != nil {
		f()
	}
}
