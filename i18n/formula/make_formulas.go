// +build ignore

package main

import (
	"bytes"
	"fmt"
	"go/format"
	"gondola/i18n/formula"
	"html"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/scanner"
)

const (
	source = "http://docs.translatehouse.org/projects/localization-guide/en/latest/l10n/pluralforms.html"
)

func FetchFormulas() ([]string, error) {
	resp, err := http.Get(source)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	var formulas []string
	re := regexp.MustCompile("<td>(nplurals.*?)</td>")
	for _, v := range re.FindAllSubmatch(body, -1) {
		form := strings.TrimSpace(html.UnescapeString(string(v[1])))
		if idx := strings.Index(form, "<em>"); idx > 0 {
			form = form[:idx]
		}
		// fix some badly specified formulas
		if !strings.Contains(form, "plural=") {
			idx := strings.Index(form, ";")
			form = fmt.Sprintf("%s plural=%s", form[:idx+1], form[idx+1:])
		}
		form = strings.Replace(form, " or ", " || ", -1)
		form = strings.Replace(form, "= n", "=n", -1)
		fn, _, err := formula.Extract(form)
		if err != nil {
			return nil, err
		}
		if _, ok := set[fn]; !ok {
			set[fn] = struct{}{}
			formulas = append(formulas, form)
		}
	}
	sort.Strings(formulas)
	return formulas, nil
}

func FuncFromFormula(form string) (string, error) {
	f, _, err := formula.Extract(form)
	if err != nil {
		return "", err
	}
	var s scanner.Scanner
	s.Init(strings.NewReader(f))
	s.Error = func(s *scanner.Scanner, msg string) {
		err = fmt.Errorf("error parsing plural formula %s: %s", s.Pos(), msg)
	}
	s.Mode = scanner.ScanIdents | scanner.ScanInts
	s.Whitespace = 0
	tok := s.Scan()
	var code []string
	var buf bytes.Buffer
	for tok != scanner.EOF && err == nil {
		switch tok {
		case scanner.Ident, scanner.Int:
			buf.WriteString(s.TokenText())
		case '?':
			code = append(code, fmt.Sprintf("if %s {\n", buf.String()))
			buf.Reset()
		case ':':
			code = append(code, fmt.Sprintf("return %s\n}\n", buf.String()))
			buf.Reset()
		default:
			buf.WriteRune(tok)
		}
		tok = s.Scan()
	}
	if err != nil {
		return "", err
	}
	if len(code) == 0 && buf.Len() > 0 && buf.String() != "0" {
		code = append(code, fmt.Sprintf("if %s {\nreturn 1\n}\nreturn 0\n", buf.String()))
		buf.Reset()
	}
	if buf.Len() > 0 {
		code = append(code, fmt.Sprintf("\nreturn %s\n", buf.String()))
	}
	return strings.Join(code, ""), nil
}

func main() {
	formulas, err := FetchFormulas()
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	buf.WriteString("package formula\n\n")
	buf.WriteString("// THIS FILE IS AUTOGENERATED. DO NOT EDIT IT MANUALLY\n")
	buf.WriteString("// Use go run make_formulas.go to update it\n\n\n")
	buf.WriteString("var formulasTable = map[uint64]Formula{\n")
	for ii, v := range formulas {
		form, _, err := formula.Extract(v)
		if err != nil {
			panic(err)
		}
		p, err := formula.Compile(form)
		if err != nil {
			panic(err)
		}
		buf.WriteString(fmt.Sprintf("%d: formula%d,\n", p.Id(), ii))
	}
	buf.WriteString("}\n")
	for ii, v := range formulas {
		fn, err := FuncFromFormula(v)
		if err != nil {
			panic(err)
		}
		buf.WriteString("// " + v + "\n")
		buf.WriteString(fmt.Sprintf("func formula%d(n int) int {\n%s}\n", ii, fn))
	}
	res, err := format.Source(buf.Bytes())
	if err != nil {
		panic(err)
	}
	f, err := os.OpenFile("formulas.go", os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := io.Copy(f, bytes.NewReader(res)); err != nil {
		panic(err)
	}
}
