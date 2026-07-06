package anigate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func RenderPresetCommand(p Preset, raw map[string]any) ([]string, map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	specs := map[string]PresetArg{}
	for _, arg := range p.Args {
		specs[arg.Name] = arg
	}
	values := map[string]any{}
	for _, arg := range p.Args {
		value, ok := raw[arg.Name]
		if !ok {
			value = arg.Default
		}
		if value == nil {
			if arg.Required {
				return nil, nil, fmt.Errorf("missing required arg %q", arg.Name)
			}
			continue
		}
		normalized, err := normalizePresetArg(arg, value)
		if err != nil {
			return nil, nil, err
		}
		values[arg.Name] = normalized
	}
	for k := range raw {
		if _, ok := specs[k]; !ok {
			return nil, nil, fmt.Errorf("unknown preset arg %q", k)
		}
	}
	var command []string
	for _, token := range p.Command {
		name, exact := placeholderName(token)
		if !exact {
			rendered := token
			for key, value := range values {
				if list, ok := value.([]string); ok {
					if strings.Contains(rendered, "{"+key+"}") && len(list) != 1 {
						return nil, nil, fmt.Errorf("arg %q must be used as a full argv token", key)
					}
					if len(list) == 1 {
						rendered = strings.ReplaceAll(rendered, "{"+key+"}", list[0])
					}
					continue
				}
				rendered = strings.ReplaceAll(rendered, "{"+key+"}", fmt.Sprint(value))
			}
			if strings.Contains(rendered, "{") || strings.Contains(rendered, "}") {
				return nil, nil, fmt.Errorf("unresolved command placeholder in %q", token)
			}
			command = append(command, rendered)
			continue
		}
		value, ok := values[name]
		if !ok {
			return nil, nil, fmt.Errorf("missing value for command placeholder %q", name)
		}
		if list, ok := value.([]string); ok {
			command = append(command, list...)
		} else {
			command = append(command, fmt.Sprint(value))
		}
	}
	return command, values, nil
}

func normalizePresetArg(arg PresetArg, value any) (any, error) {
	typ := arg.Type
	if typ == "" {
		typ = "string"
	}
	switch typ {
	case "string":
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("arg %q must be a string", arg.Name)
		}
		if err := validatePresetString(arg, s); err != nil {
			return nil, err
		}
		return s, nil
	case "int":
		i, err := toInt64(value)
		if err != nil {
			return nil, fmt.Errorf("arg %q must be an integer", arg.Name)
		}
		if arg.Min != nil && i < *arg.Min {
			return nil, fmt.Errorf("arg %q must be >= %d", arg.Name, *arg.Min)
		}
		if arg.Max != nil && i > *arg.Max {
			return nil, fmt.Errorf("arg %q must be <= %d", arg.Name, *arg.Max)
		}
		return i, nil
	case "bool":
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("arg %q must be a boolean", arg.Name)
		}
		return b, nil
	case "string_array":
		raw, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("arg %q must be an array", arg.Name)
		}
		maxItems := arg.MaxItems
		if maxItems <= 0 {
			maxItems = 16
		}
		if len(raw) > maxItems {
			return nil, fmt.Errorf("arg %q has too many items", arg.Name)
		}
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("arg %q must contain only strings", arg.Name)
			}
			if err := validatePresetString(arg, s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("arg %q has unsupported type %q", arg.Name, arg.Type)
	}
}

func validatePresetString(arg PresetArg, s string) error {
	maxLen := arg.MaxLen
	if maxLen <= 0 {
		maxLen = 256
	}
	if len(s) > maxLen {
		return fmt.Errorf("arg %q is too long", arg.Name)
	}
	if len(arg.Enum) > 0 {
		for _, allowed := range arg.Enum {
			if s == allowed {
				return nil
			}
		}
		return fmt.Errorf("arg %q is not in enum", arg.Name)
	}
	// Reject option-injection by default: a value beginning with '-' would be
	// interpreted as a flag by the invoked binary. Presets that genuinely pass
	// flags must opt in with allow_leading_dash.
	if !arg.AllowLeadingDash && strings.HasPrefix(s, "-") {
		return fmt.Errorf("arg %q must not begin with '-' (set allow_leading_dash to pass flags)", arg.Name)
	}
	if arg.Pattern != "" {
		re, err := regexp.Compile(arg.Pattern)
		if err != nil {
			return fmt.Errorf("arg %q has invalid pattern", arg.Name)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("arg %q does not match pattern", arg.Name)
		}
	}
	return nil
}

func toInt64(value any) (int64, error) {
	switch v := value.(type) {
	case float64:
		i := int64(v)
		if float64(i) != v {
			return 0, fmt.Errorf("not an integer")
		}
		return i, nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("not an integer")
	}
}

func validateCommandPlaceholders(label string, command []string, names map[string]bool) error {
	for _, token := range command {
		for _, name := range findPlaceholders(token) {
			if name == "prompt" {
				continue
			}
			if !names[name] {
				return fmt.Errorf("%s uses unknown placeholder %q", label, name)
			}
		}
	}
	return nil
}

func findPlaceholders(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '{')
		if start < 0 {
			return out
		}
		end := strings.IndexByte(s[start+1:], '}')
		if end < 0 {
			return out
		}
		name := s[start+1 : start+1+end]
		if validName(name) {
			out = append(out, name)
		}
		s = s[start+1+end+1:]
	}
}

func placeholderName(token string) (string, bool) {
	if len(token) < 3 || token[0] != '{' || token[len(token)-1] != '}' {
		return "", false
	}
	name := token[1 : len(token)-1]
	if !validName(name) {
		return "", false
	}
	return name, true
}
