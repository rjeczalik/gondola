// Package gen does code generation to automate tedious tasks.
//
// Although you can use this package and its subpackages directly, that's not
// recommended. Instead, you should create a genfile.yaml for your package and
// use the gondola gen command to perform the code generation.
package gen

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strconv"
	str "strings"

	"gnd.la/internal/gen/genutil"
	"gnd.la/internal/gen/json"
	"gnd.la/internal/gen/strings"
	"gnd.la/util/types"
	"gnd.la/util/yaml"
)

// Gen generates code according to the given config file. If the config file
// can't be found or it can't be parsed, an error will be returned. Previously
// autogenerated files will be overwritten, but trying to overwrite any files
// which were not autogenerated will also return an error. See the package
// documentation for the format of the config file.
func Gen(pkgName string, config string) error {
	if config == "" {
		pkg, err := genutil.NewPackage(pkgName)
		if err != nil {
			return err
		}
		config = filepath.Join(pkg.Dir(), "genfile.yaml")
	}
	data, err := ioutil.ReadFile(config)
	if err != nil {
		return fmt.Errorf("could not read %s: %s", config, err)
	}
	var opts map[string]interface{}
	if err := yaml.Unmarshal(data, &opts); err != nil {
		return fmt.Errorf("could not decode YAML: %s", err)
	}
	for k, v := range opts {
		switch k {
		case "json":
			opts, err := jsonOptions(v)
			if err != nil {
				return err
			}
			if err := json.Gen(pkgName, opts); err != nil {
				return err
			}
		case "strings":
			opts, err := stringsOptions(v)
			if err != nil {
				return err
			}
			if err := strings.Gen(pkgName, opts); err != nil {
				return err
			}
		case "template":
		}
	}
	return nil
}

func jsonOptions(val interface{}) (*json.Options, error) {
	m, ok := toMap(val)
	if !ok {
		return nil, fmt.Errorf("JSON options must be a map")
	}
	opts := &json.Options{}
	var err error
	for k, v := range m {
		switch k {
		case "marshal-json":
			opts.MarshalJSON, _ = types.IsTrue(v)
		case "buffer-size":
			if opts.BufferSize, err = types.ToInt(v); err != nil {
				return nil, err
			}
		case "max-buffer-size":
			if opts.MaxBufferSize, err = types.ToInt(v); err != nil {
				return nil, err
			}
		case "buffer-count":
			if opts.BufferCount, err = types.ToInt(v); err != nil {
				return nil, err
			}
		case "buffers-per-proc":
			if opts.BuffersPerProc, err = types.ToInt(v); err != nil {
				return nil, err
			}
		case "include":
			if val := types.ToString(v); val != "" {
				include, err := regexp.Compile(val)
				if err != nil {
					return nil, err
				}
				opts.Include = include
			}
		case "exclude":
			if val := types.ToString(v); val != "" {
				exclude, err := regexp.Compile(val)
				if err != nil {
					return nil, err
				}
				opts.Exclude = exclude
			}
		case "types":
			jsonTypes, ok := toMap(v)
			if !ok {
				return nil, fmt.Errorf("JSON %s must be a map", k)
			}
			for tn, t := range jsonTypes {
				typeFields, ok := toMap(t)
				if !ok {
					return nil, fmt.Errorf("JSON type options for %s must be a map", tn)
				}
				var jsonFields []*json.Field
				for key, node := range typeFields {
					field := &json.Field{
						Key: key,
					}
					switch value := node.(type) {
					case map[interface{}]interface{}:
						field.Name = types.ToString(value["name"])
						field.OmitEmpty, _ = types.IsTrue(value["omitempty"])
					case string:
						field.Name = types.ToString(value)
					default:
						return nil, fmt.Errorf("field/method value for %s must be string or map", key)
					}
					if field.Name == "" {
						field.Name = field.Key
					}
					jsonFields = append(jsonFields, field)
				}
				if opts.TypeFields == nil {
					opts.TypeFields = make(map[string][]*json.Field)
				}
				opts.TypeFields[tn] = jsonFields
			}
		}
	}
	return opts, nil
}

func stringsOptions(val interface{}) (*strings.Options, error) {
	m, ok := toMap(val)
	if !ok {
		return nil, fmt.Errorf("strings options must be a map, not %T", val)
	}
	opts := &strings.Options{}
	for k, v := range m {
		switch k {
		case "include":
			if val := types.ToString(v); val != "" {
				include, err := regexp.Compile(val)
				if err != nil {
					return nil, err
				}
				opts.Include = include
			}
		case "exclude":
			if val := types.ToString(v); val != "" {
				exclude, err := regexp.Compile(val)
				if err != nil {
					return nil, err
				}
				opts.Exclude = exclude
			}
		case "options":
			options, ok := toMap(v)
			if !ok {
				return nil, fmt.Errorf("options inside string options must be a map")
			}
			opts.TypeOptions = make(map[string]*strings.TypeOptions)
			for typeName, val := range options {
				valMap, ok := toMap(val)
				if !ok {
					return nil, fmt.Errorf("%s type options must be a map", typeName)
				}
				typeOptions := &strings.TypeOptions{}
				tr := types.ToString(valMap["transform"])
				switch tr {
				case "":
				case "uppercase":
					typeOptions.Transform = strings.ToUpper
				case "lowercase":
					typeOptions.Transform = strings.ToLower
				default:
					return nil, fmt.Errorf("invalid transform %q", tr)
				}
				if slice := valMap["slice"]; slice != nil {
					var err error
					if typeOptions.SliceBegin, typeOptions.SliceEnd, err = parseSlice(slice); err != nil {
						return nil, err
					}
				}
				opts.TypeOptions[typeName] = typeOptions
			}
		}
	}
	return opts, nil
}

func toMap(val interface{}) (map[string]interface{}, bool) {
	switch v := val.(type) {
	case nil:
		return nil, true
	case map[string]interface{}:
		return v, true
	case map[interface{}]interface{}:
		m := make(map[string]interface{})
		for k, v := range v {
			if s, ok := k.(string); ok {
				m[s] = v
			} else {
				return nil, false
			}
		}
		return m, true
	}
	return nil, false
}

func parseSlice(val interface{}) (int, int, error) {
	switch v := val.(type) {
	case string:
		return _parseSlice(v)
	case map[interface{}]interface{}:
		if len(v) == 1 {
			for k, v := range v {
				if v == nil {
					v = "0"
				}
				return _parseSlice(fmt.Sprintf("%v:%v", k, v))
			}
		}
	}
	return 0, 0, fmt.Errorf("invalid slice spec %v", val)
}

func _parseSlice(s string) (int, int, error) {
	idx := str.Index(s, ":")
	if idx < 0 {
		return 0, 0, fmt.Errorf("slice spec must contain :")
	}
	begin := s[:idx]
	end := s[idx+1:]
	b, err := strconv.Atoi(begin)
	if err != nil {
		return 0, 0, err
	}
	if end == "" {
		return b, 0, nil
	}
	e, err := strconv.Atoi(end)
	if err != nil {
		return 0, 0, err
	}
	return b, e, nil
}
