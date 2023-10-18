package bqb

import (
	"bufio"
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func dialectReplace(dialect Dialect, sql string, params []any) (string, error) {
	const (
		questionMark                = "?"
		doubleQuestionMarkDelimiter = "??"
		parameterPlaceholder        = paramPh
	)

	switch dialect {
	case RAW:
		raws := make([]string, len(params))
		for i, param := range params {
			p, err := paramToRaw(param)
			if err != nil {
				return "", err
			}
			raws[i] = p
		}

		return replaceWithScans(
			sql,
			scan{pattern: parameterPlaceholder, fn: func(i int) string { return raws[i] }},
		)
	case MYSQL, SQL:
		return replaceWithScans(
			sql,
			scan{pattern: parameterPlaceholder, fn: func(int) string { return questionMark }},
		)
	case PGSQL:
		return replaceWithScans(
			sql,
			scan{pattern: doubleQuestionMarkDelimiter, fn: func(int) string { return questionMark }},
			scan{pattern: parameterPlaceholder, fn: func(i int) string { return fmt.Sprintf("$%d", i+1) }},
		)
	default:
		// No replacement defined for dialect
		return sql, nil
	}
}

type replaceFn func(int) string

type scan struct {
	pattern string
	fn      replaceFn
}

// replaceWithScans applies the given set of scanning arguments and joins their
// errors together.
func replaceWithScans(in string, ss ...scan) (string, error) {
	errs := []error{}
	for _, s := range ss {
		out, err := scanReplace(in, s.pattern, s.fn)
		errs = append(errs, err)
		in = out
	}
	return in, errors.Join(errs...)
}

func scanReplace(stmt string, replace string, fn replaceFn) (string, error) {
	// Build a scanner that will iterate based on the replace token
	ph := []byte(replace)
	scanner := bufio.NewScanner(bytes.NewBufferString(stmt))
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		// Return nothing if at end of file and no data passed
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		switch i := bytes.Index(data, ph); {
		case i == 0:
			return len(ph), data[0:len(ph)], nil
		case i > 0:
			return i, data[0:i], nil

		}

		// If at end of file with data return the data
		if atEOF {
			return len(data), data, nil
		}

		return
	})

	i := 0

	// Scan replacing tokens with the value returned from delegate
	sb := strings.Builder{}
	for scanner.Scan() {
		switch txt := scanner.Text(); txt {
		case replace:
			// String builder will always return nil for an err so it is thrown
			// away.
			_, _ = sb.WriteString(fn(i))
			i++

		default:
			// String builder will always return nil for an err so it is thrown
			// away.
			_, _ = sb.WriteString(txt)
		}
	}

	return sb.String(), scanner.Err()
}

func convertArg(text string, arg any) (string, []any, []error) {
	var newArgs []any
	var errs []error

	switch v := arg.(type) {

	case Embedder:
		text = strings.Replace(text, "?", v.RawValue(), 1)

	case driver.Valuer:
		text = strings.Replace(text, "?", paramPh, 1)
		val, err := v.Value()
		if err != nil {
			errs = append(errs, err)
		} else {
			newArgs = append(newArgs, val)
		}
	case []int:
		newPh := []string{}
		for _, i := range v {
			newPh = append(newPh, paramPh)
			newArgs = append(newArgs, i)
		}
		text = strings.Replace(text, "?", strings.Join(newPh, ","), 1)

	case []*int:
		newPh := []string{}
		for _, i := range v {
			newPh = append(newPh, paramPh)
			newArgs = append(newArgs, i)
		}
		if len(newPh) > 0 {
			text = strings.Replace(text, "?", strings.Join(newPh, ","), 1)
		} else {
			text = strings.Replace(text, "?", paramPh, 1)
			newArgs = append(newArgs, nil)
		}

	case []string:
		newPh := []string{}
		for _, s := range v {
			newPh = append(newPh, paramPh)
			newArgs = append(newArgs, s)
		}
		text = strings.Replace(text, "?", strings.Join(newPh, ","), 1)

	case []*string:
		newPh := []string{}
		for _, s := range v {
			newPh = append(newPh, paramPh)
			newArgs = append(newArgs, s)
		}
		if len(newPh) > 0 {
			text = strings.Replace(text, "?", strings.Join(newPh, ","), 1)
		} else {
			text = strings.Replace(text, "?", paramPh, 1)
			newArgs = append(newArgs, nil)
		}

	case []any:
		newPh := []string{}
		for _, s := range v {
			newPh = append(newPh, paramPh)
			newArgs = append(newArgs, s)
		}
		text = strings.Replace(text, "?", strings.Join(newPh, ","), 1)

	case *Query:
		if v == nil {
			text = strings.Replace(text, "?", paramPh, 1)
			newArgs = append(newArgs, nil)
			return text, newArgs, errs
		}
		sql, params, err := v.toSql()
		text = strings.Replace(text, "?", sql, 1)
		if err != nil {
			errs = append(errs, err)
		} else {
			newArgs = append(newArgs, params...)
		}

	case JsonMap, JsonList:
		text = strings.Replace(text, "?", paramPh, 1)
		bytes, err := json.Marshal(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("cann jsonify struct: %v", err))
		} else {
			newArgs = append(newArgs, string(bytes))
		}

	case *JsonMap, *JsonList:
		text = strings.Replace(text, "?", paramPh, 1)
		bytes, err := json.Marshal(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("cann jsonify struct: %v", err))
		} else {
			newArgs = append(newArgs, string(bytes))
		}

	case Embedded:
		text = strings.Replace(text, "?", string(v), 1)

	default:
		text = strings.Replace(text, "?", paramPh, 1)
		newArgs = append(newArgs, v)
	}

	return text, newArgs, errs
}

func checkParamCounts(text, original string, args []any) error {
	extraCount := strings.Count(text, "?")
	if extraCount > 0 {
		return fmt.Errorf("extra ? in text: %v (%d args)", original, len(args))
	}

	paramCount := strings.Count(text, paramPh)
	if paramCount < len(args) {
		return fmt.Errorf("missing ? in text: %v (%d args)", original, len(args))
	}
	return nil
}

func makePart(text string, args ...any) QueryPart {
	tempPh := "XXX___XXX"
	originalText := text
	text = strings.ReplaceAll(text, "??", tempPh)

	var newArgs []any
	errs := make([]error, 0)

	for _, arg := range args {
		argText, fArgs, argErrs := convertArg(text, arg)
		if len(argErrs) > 0 {
			errs = append(errs, argErrs...)
		}
		newArgs = append(newArgs, fArgs...)
		text = argText
	}

	if err := checkParamCounts(text, originalText, newArgs); err != nil {
		errs = append(errs, err)
	}

	text = strings.ReplaceAll(text, tempPh, "??")

	return QueryPart{
		Text:   text,
		Params: newArgs,
		Errs:   errs,
	}
}

func paramToRaw(param any) (string, error) {
	switch p := param.(type) {
	case bool:
		return fmt.Sprintf("%v", p), nil
	case float32, float64, int, int8, int16, int32, int64,
		uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%v", p), nil
	case *int:
		if p == nil {
			return "NULL", nil
		}
		return fmt.Sprintf("%v", *p), nil
	case string:
		return fmt.Sprintf("'%v'", p), nil
	case *string:
		if p == nil {
			return "NULL", nil
		}
		return fmt.Sprintf("'%v'", *p), nil
	case nil:
		return "NULL", nil
	default:
		return "", fmt.Errorf("unsupported type for Raw query: %T", p)
	}
}
