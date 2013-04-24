package assets

import (
	"fmt"
	"strings"
)

type Options map[string]string

func (o Options) boolOpt(key string) bool {
	_, ok := o[key]
	return ok
}

func (o Options) debugSuffix(debug bool) string {
	if debug {
		return "?debug"
	}
	return "?!debug"
}

func (o Options) BoolOpt(key string, m Manager) bool {
	ok := o.boolOpt(key)
	// Check if the option is enabled for debug or !debug
	if !ok {
		ok = o.boolOpt(key + o.debugSuffix(m.Debug()))
	}
	return ok
}

func (o Options) StringOpt(key string, m Manager) string {
	val, ok := o[key+o.debugSuffix(m.Debug())]
	if !ok {
		val = o[key]
	}
	return val
}

func (o Options) Debug() bool {
	return o.boolOpt("debug")
}

func (o Options) NoDebug() bool {
	return o.boolOpt("!debug")
}

func (o Options) String() string {
	var values []string
	for k, v := range o {
		values = append(values, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(values, ",")
}