package icarus

// decodeFields unwraps a Firestore REST document's typed-value "fields"
// object (each value wrapped as {"stringValue": ...} / {"mapValue": {...}} /
// etc.) into plain Go values. Only the value kinds this catalog's schema
// actually uses are handled; anything else decodes to nil rather than
// panicking, since an unrecognized field should be ignorable, not fatal.
func decodeFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = decodeValue(v)
	}
	return out
}

func decodeValue(v any) any {
	wrapped, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if s, ok := wrapped["stringValue"]; ok {
		return s
	}
	if b, ok := wrapped["booleanValue"]; ok {
		return b
	}
	if i, ok := wrapped["integerValue"]; ok {
		return i
	}
	if d, ok := wrapped["doubleValue"]; ok {
		return d
	}
	if m, ok := wrapped["mapValue"]; ok {
		mv, _ := m.(map[string]any)
		inner, _ := mv["fields"].(map[string]any)
		return decodeFields(inner)
	}
	if a, ok := wrapped["arrayValue"]; ok {
		av, _ := a.(map[string]any)
		values, _ := av["values"].([]any)
		out := make([]any, len(values))
		for i, item := range values {
			out[i] = decodeValue(item)
		}
		return out
	}
	if _, ok := wrapped["nullValue"]; ok {
		return nil
	}
	return nil
}
