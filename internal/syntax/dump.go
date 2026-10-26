package syntax

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/lyth-lang/lyth/internal/source"
)

// Dump renders a syntax tree in a stable, indented form for golden tests.
// Spans are printed as line:col-line:col.
func Dump(n any) string {
	var b strings.Builder
	dump(&b, reflect.ValueOf(n), 0, "")
	return b.String()
}

var spanType = reflect.TypeOf(source.Span{})

func dump(b *strings.Builder, v reflect.Value, indent int, label string) {
	pad := strings.Repeat("  ", indent)
	prefix := pad
	if label != "" {
		prefix += label + ": "
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		dump(b, v.Elem(), indent, label)
		return
	case reflect.Slice:
		if v.Len() == 0 {
			return
		}
		if v.Type().Elem().Kind() == reflect.Struct && v.Type().Elem() == reflect.TypeOf(Comment{}) {
			return
		}
		fmt.Fprintf(b, "%s[%d]\n", prefix, v.Len())
		for i := 0; i < v.Len(); i++ {
			dump(b, v.Index(i), indent+1, "")
		}
		return
	case reflect.Struct:
		if v.Type() == spanType {
			return
		}
		t := v.Type()
		var head strings.Builder
		head.WriteString(prefix + t.Name())
		// Inline scalar fields.
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fv := v.Field(i)
			if !f.IsExported() {
				continue
			}
			switch fv.Kind() {
			case reflect.String:
				if fv.String() != "" && f.Name != "Doc" {
					fmt.Fprintf(&head, " %s=%q", f.Name, fv.String())
				}
			case reflect.Bool:
				if fv.Bool() {
					fmt.Fprintf(&head, " %s", f.Name)
				}
			case reflect.Int:
				if fv.Int() != 0 {
					if k, ok := fv.Interface().(Kind); ok {
						fmt.Fprintf(&head, " %s=%s", f.Name, k)
					} else {
						fmt.Fprintf(&head, " %s=%d", f.Name, fv.Int())
					}
				}
			}
		}
		if sf, ok := t.FieldByName("S"); ok && sf.Type == spanType {
			s := v.FieldByName("S").Interface().(source.Span)
			if s.IsValid() {
				a, e := s.StartPos(), s.EndPos()
				fmt.Fprintf(&head, " @%d:%d-%d:%d", a.Line, a.Col, e.Line, e.Col)
			}
		}
		b.WriteString(head.String() + "\n")
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fv := v.Field(i)
			if !f.IsExported() || f.Name == "Src" {
				continue
			}
			switch fv.Kind() {
			case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Struct:
				dump(b, fv, indent+1, f.Name)
			}
		}
	default:
		fmt.Fprintf(b, "%s%v\n", prefix, v.Interface())
	}
}

// DumpShape renders a syntax tree like Dump, without positions: two files
// that differ only in layout have the same shape. Carriage returns are left
// out of the text, as Lyth leaves them out of raw strings, and trailing
// spaces of doc comment lines.
func DumpShape(n any) string {
	var b strings.Builder
	dumpShape(&b, reflect.ValueOf(n), 0, "")
	return b.String()
}

func dumpShape(b *strings.Builder, v reflect.Value, indent int, label string) {
	pad := strings.Repeat("  ", indent)
	prefix := pad
	if label != "" {
		prefix += label + ": "
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			dumpShape(b, v.Elem(), indent, label)
		}
	case reflect.Slice:
		if v.Len() == 0 || v.Type().Elem() == reflect.TypeOf(Comment{}) || v.Type().Elem() == spanType {
			return
		}
		fmt.Fprintf(b, "%s[%d]\n", prefix, v.Len())
		for i := 0; i < v.Len(); i++ {
			dumpShape(b, v.Index(i), indent+1, "")
		}
	case reflect.Struct:
		if v.Type() == spanType {
			return
		}
		t := v.Type()
		var head strings.Builder
		head.WriteString(prefix + t.Name())
		for i := 0; i < t.NumField(); i++ {
			f, fv := t.Field(i), v.Field(i)
			if !f.IsExported() {
				continue
			}
			switch fv.Kind() {
			case reflect.String:
				if s := fv.String(); s != "" {
					s = strings.ReplaceAll(s, "\r", "")
					if f.Name == "Doc" {
						lines := strings.Split(s, "\n")
						for i, l := range lines {
							lines[i] = strings.TrimRight(l, " \t")
						}
						s = strings.Join(lines, "\n")
					}
					fmt.Fprintf(&head, " %s=%q", f.Name, s)
				}
			case reflect.Bool:
				if fv.Bool() {
					fmt.Fprintf(&head, " %s", f.Name)
				}
			case reflect.Int:
				if fv.Int() != 0 {
					fmt.Fprintf(&head, " %s=%d", f.Name, fv.Int())
				}
			}
		}
		b.WriteString(head.String() + "\n")
		for i := 0; i < t.NumField(); i++ {
			f, fv := t.Field(i), v.Field(i)
			if !f.IsExported() || f.Name == "Src" {
				continue
			}
			switch fv.Kind() {
			case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Struct:
				dumpShape(b, fv, indent+1, f.Name)
			}
		}
	default:
		fmt.Fprintf(b, "%s%v\n", prefix, v.Interface())
	}
}
